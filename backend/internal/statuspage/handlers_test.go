package statuspage_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/api"
	"o1/internal/config"
	"o1/internal/db/sqlc"
	"o1/internal/statuspage"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

const handlerPassword = "correct horse battery staple"

type handlerEnv struct {
	t      *testing.T
	pool   *pgxpool.Pool
	cfg    config.Config
	wsSvc  *workspaces.Service
	svc    *statuspage.Service
	server *httptest.Server
	wsID   uuid.UUID
	slug   string
	owner  uuid.UUID
	admin  uuid.UUID
	member uuid.UUID
	viewer uuid.UUID
}

func newHandlerEnv(t *testing.T) *handlerEnv {
	t.Helper()
	pool, cleanup := testutil.NewPostgres(t)
	t.Cleanup(cleanup)
	cfg := testutil.TestConfig(t)
	wsSvc := workspaces.NewService(pool, sqlc.New(pool))
	svc := statuspage.NewService(pool, sqlc.New(pool), wsSvc)
	owner := testutil.CreateVerifiedUser(t, pool, "status-handler-owner@example.com", handlerPassword)
	ws, err := wsSvc.Create(context.Background(), owner, "Status API")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	env := &handlerEnv{t: t, pool: pool, cfg: cfg, wsSvc: wsSvc, svc: svc, wsID: ws.ID, slug: ws.Slug, owner: owner}
	env.admin = env.addMember("status-handler-admin@example.com", "admin")
	env.member = env.addMember("status-handler-member@example.com", "member")
	env.viewer = env.addMember("status-handler-viewer@example.com", "viewer")
	router := api.NewRouter(api.Dependencies{
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Pool:              pool,
		Config:            cfg,
		EmailSender:       testutil.NewFakeSender(),
		WorkspaceService:  wsSvc,
		StatusPageService: svc,
	})
	env.server = httptest.NewServer(router)
	t.Cleanup(env.server.Close)
	return env
}

func (e *handlerEnv) addMember(emailAddr, role string) uuid.UUID {
	e.t.Helper()
	id := testutil.CreateVerifiedUser(e.t, e.pool, emailAddr, handlerPassword)
	if err := sqlc.New(e.pool).UpsertWorkspaceMember(context.Background(), sqlc.UpsertWorkspaceMemberParams{
		WorkspaceID: e.wsID, UserID: id, Role: role,
	}); err != nil {
		e.t.Fatalf("add member: %v", err)
	}
	return id
}

func (e *handlerEnv) addMonitor(name string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO monitors (id, workspace_id, name, url, method, interval_seconds, timeout_seconds, created_by) VALUES ($1,$2,$3,'https://secret.example.com/path?token=abc','GET',60,10,$4)`, id, e.wsID, name, e.owner); err != nil {
		e.t.Fatalf("insert monitor: %v", err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO monitor_state (monitor_id) VALUES ($1)`, id); err != nil {
		e.t.Fatalf("insert monitor state: %v", err)
	}
	return id
}

func (e *handlerEnv) client(emailAddr string) *http.Client {
	e.t.Helper()
	token := testutil.SessionToken(e.t, e.pool, e.cfg, emailAddr, handlerPassword)
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatalf("cookie jar: %v", err)
	}
	base, err := url.Parse(e.server.URL)
	if err != nil {
		e.t.Fatalf("parse server url: %v", err)
	}
	jar.SetCookies(base, []*http.Cookie{{Name: "o1_session", Value: token}})
	return &http.Client{Jar: jar}
}

func (e *handlerEnv) do(c *http.Client, method, path string, body any) (*http.Response, []byte) {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		e.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatalf("do request: %v", err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		e.t.Fatalf("read body: %v", err)
	}
	return resp, data
}

func (e *handlerEnv) statusPath() string {
	return "/api/v1/workspaces/" + e.wsID.String() + "/status-page"
}

type problemJSON struct {
	Type   string            `json:"type"`
	Title  string            `json:"title"`
	Status int               `json:"status"`
	Code   string            `json:"code"`
	Detail string            `json:"detail"`
	Fields map[string]string `json:"fields"`
}

func decodeJSON[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode JSON %q: %v", string(data), err)
	}
	return v
}

func decodeProblem(t *testing.T, data []byte) problemJSON {
	t.Helper()
	return decodeJSON[problemJSON](t, data)
}

func TestStatusPageHandlers_Public(t *testing.T) {
	env := newHandlerEnv(t)
	monitorID := env.addMonitor("API")
	if _, err := env.svc.UpdateConfig(context.Background(), env.owner, env.wsID, statuspage.ConfigInput{
		Enabled: true, Title: "Acme status", Description: "Live availability", MonitorIDs: []uuid.UUID{monitorID},
	}); err != nil {
		t.Fatal(err)
	}

	resp, body := env.do(&http.Client{}, http.MethodGet, "/api/v1/public/status/"+env.slug, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Cache-Control"); got != "public, max-age=15, stale-while-revalidate=15" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var page statuspage.PublicPage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if page.Page.Title != "Acme status" || len(page.Monitors) != 1 || len(page.Monitors[0].Days) != 90 {
		t.Fatalf("page = %+v", page)
	}

	resp, body = env.do(&http.Client{}, http.MethodGet, "/api/v1/public/status/missing-slug", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("404 Cache-Control = %q", got)
	}
	if !strings.Contains(string(body), `"code":"not_found"`) {
		t.Fatalf("404 body = %s", body)
	}
}

func TestStatusPageHandlers_AdminCRUD(t *testing.T) {
	env := newHandlerEnv(t)
	monitorID := env.addMonitor("API")

	resp, _ := env.do(&http.Client{}, http.MethodGet, env.statusPath(), nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET = %d, want 401", resp.StatusCode)
	}

	owner := env.client("status-handler-owner@example.com")
	resp, body := env.do(owner, http.MethodGet, env.statusPath(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner GET = %d: %s", resp.StatusCode, body)
	}
	defaults := decodeJSON[statuspage.Config](t, body)
	if defaults.Enabled || defaults.Title != "Status API" || defaults.MonitorIDs == nil || len(defaults.MonitorIDs) != 0 {
		t.Fatalf("defaults = %+v", defaults)
	}

	save := statuspage.ConfigInput{
		Enabled: true, Title: "Acme status", Description: "Live availability", MonitorIDs: []uuid.UUID{monitorID},
	}
	resp, body = env.do(owner, http.MethodPut, env.statusPath(), save)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner PUT = %d: %s", resp.StatusCode, body)
	}
	saved := decodeJSON[statuspage.Config](t, body)
	if !saved.Enabled || saved.Title != "Acme status" || len(saved.MonitorIDs) != 1 || saved.MonitorIDs[0] != monitorID {
		t.Fatalf("saved = %+v", saved)
	}

	resp, body = env.do(owner, http.MethodGet, env.statusPath(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner GET after PUT = %d: %s", resp.StatusCode, body)
	}
	current := decodeJSON[statuspage.Config](t, body)
	if !current.Enabled || current.Title != "Acme status" || current.Description != "Live availability" {
		t.Fatalf("current = %+v", current)
	}

	admin := env.client("status-handler-admin@example.com")
	adminSave := statuspage.ConfigInput{
		Enabled: true, Title: "Admin status", MonitorIDs: []uuid.UUID{monitorID},
	}
	resp, body = env.do(admin, http.MethodPut, env.statusPath(), adminSave)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin PUT = %d: %s", resp.StatusCode, body)
	}

	for _, tc := range []struct {
		name   string
		client *http.Client
	}{
		{"member", env.client("status-handler-member@example.com")},
		{"viewer", env.client("status-handler-viewer@example.com")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := env.do(tc.client, http.MethodGet, env.statusPath(), nil)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s GET = %d, want 403", tc.name, resp.StatusCode)
			}
			resp, body := env.do(tc.client, http.MethodPut, env.statusPath(), save)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s PUT = %d: %s", tc.name, resp.StatusCode, body)
			}
			if problem := decodeProblem(t, body); problem.Code != "forbidden" {
				t.Fatalf("%s PUT code = %q", tc.name, problem.Code)
			}
		})
	}

	otherOwner := testutil.CreateVerifiedUser(t, env.pool, "status-handler-other@example.com", handlerPassword)
	other, err := env.wsSvc.Create(context.Background(), otherOwner, "Other Workspace")
	if err != nil {
		t.Fatalf("create other workspace: %v", err)
	}
	otherPath := "/api/v1/workspaces/" + other.ID.String() + "/status-page"
	resp, _ = env.do(owner, http.MethodGet, otherPath, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-workspace GET = %d, want 404", resp.StatusCode)
	}
	resp, _ = env.do(owner, http.MethodPut, otherPath, save)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-workspace PUT = %d, want 404", resp.StatusCode)
	}

	if _, err := env.pool.Exec(context.Background(), `UPDATE workspaces SET suspended_at = now() WHERE id = $1`, env.wsID); err != nil {
		t.Fatalf("suspend workspace: %v", err)
	}
	resp, body = env.do(owner, http.MethodPut, env.statusPath(), save)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("suspended PUT = %d: %s", resp.StatusCode, body)
	}
	if problem := decodeProblem(t, body); problem.Code != "workspace_suspended" {
		t.Fatalf("suspended code = %q", problem.Code)
	}
}

func TestStatusPageHandlers_Validation(t *testing.T) {
	env := newHandlerEnv(t)
	owner := env.client("status-handler-owner@example.com")
	monitorID := env.addMonitor("API")

	otherOwner := testutil.CreateVerifiedUser(t, env.pool, "status-handler-foreign@example.com", handlerPassword)
	other, err := env.wsSvc.Create(context.Background(), otherOwner, "Foreign Workspace")
	if err != nil {
		t.Fatalf("create other workspace: %v", err)
	}
	foreignMonitor := uuid.New()
	if _, err := env.pool.Exec(context.Background(), `INSERT INTO monitors (id, workspace_id, name, url, method, interval_seconds, timeout_seconds, created_by) VALUES ($1,$2,'Foreign','https://example.com','GET',60,10,$3)`, foreignMonitor, other.ID, otherOwner); err != nil {
		t.Fatalf("insert foreign monitor: %v", err)
	}

	cases := []struct {
		name  string
		in    statuspage.ConfigInput
		field string
	}{
		{
			name:  "bad title",
			in:    statuspage.ConfigInput{Enabled: true, Title: "  ", MonitorIDs: []uuid.UUID{monitorID}},
			field: "title",
		},
		{
			name:  "enabled with no monitors",
			in:    statuspage.ConfigInput{Enabled: true, Title: "Acme"},
			field: "monitor_ids",
		},
		{
			name:  "foreign monitor",
			in:    statuspage.ConfigInput{Enabled: true, Title: "Acme", MonitorIDs: []uuid.UUID{foreignMonitor}},
			field: "monitor_ids",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := env.do(owner, http.MethodPut, env.statusPath(), tc.in)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d: %s", resp.StatusCode, body)
			}
			problem := decodeProblem(t, body)
			if problem.Code != "validation_error" {
				t.Fatalf("code = %q", problem.Code)
			}
			if problem.Fields[tc.field] == "" {
				t.Fatalf("fields = %+v, want %q", problem.Fields, tc.field)
			}
		})
	}
}

func TestStatusPageHandlers_DisabledIs404(t *testing.T) {
	env := newHandlerEnv(t)
	owner := env.client("status-handler-owner@example.com")
	monitorID := env.addMonitor("API")

	resp, body := env.do(owner, http.MethodPut, env.statusPath(), statuspage.ConfigInput{
		Enabled: false, Title: "Acme status", MonitorIDs: []uuid.UUID{monitorID},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save disabled = %d: %s", resp.StatusCode, body)
	}

	resp, body = env.do(&http.Client{}, http.MethodGet, "/api/v1/public/status/"+env.slug, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled public = %d, want 404", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("disabled Cache-Control = %q", got)
	}
	if !strings.Contains(string(body), `"code":"not_found"`) {
		t.Fatalf("disabled body = %s", body)
	}

	resp, body = env.do(owner, http.MethodPut, env.statusPath(), statuspage.ConfigInput{
		Enabled: true, Title: "Acme status", MonitorIDs: []uuid.UUID{monitorID},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enable = %d: %s", resp.StatusCode, body)
	}

	resp, body = env.do(&http.Client{}, http.MethodGet, "/api/v1/public/status/"+env.slug, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enabled public = %d: %s", resp.StatusCode, body)
	}
}

func TestStatusPageHandlers_RateLimit(t *testing.T) {
	env := newHandlerEnv(t)
	monitorID := env.addMonitor("API")
	if _, err := env.svc.UpdateConfig(context.Background(), env.owner, env.wsID, statuspage.ConfigInput{
		Enabled: true, Title: "Acme status", MonitorIDs: []uuid.UUID{monitorID},
	}); err != nil {
		t.Fatal(err)
	}

	client := &http.Client{}
	for i := 0; i < 20; i++ {
		resp, body := env.do(client, http.MethodGet, "/api/v1/public/status/"+env.slug, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d = %d: %s", i+1, resp.StatusCode, body)
		}
	}
	last, lastBody := env.do(client, http.MethodGet, "/api/v1/public/status/"+env.slug, nil)
	if last.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("21st request = %d: %s", last.StatusCode, lastBody)
	}
	if got := decodeProblem(t, lastBody).Code; got != "rate_limited" {
		t.Fatalf("rate limit code = %q", got)
	}
}

func TestStatusPageHandlers_NoSecrets(t *testing.T) {
	env := newHandlerEnv(t)
	monitorID := env.addMonitor("API")
	if _, err := env.svc.UpdateConfig(context.Background(), env.owner, env.wsID, statuspage.ConfigInput{
		Enabled: true, Title: "Acme status", MonitorIDs: []uuid.UUID{monitorID},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE monitor_state SET status='down', last_error='token=abc secret failure' WHERE monitor_id=$1`, monitorID); err != nil {
		t.Fatalf("update monitor state: %v", err)
	}

	resp, body := env.do(&http.Client{}, http.MethodGet, "/api/v1/public/status/"+env.slug, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public = %d: %s", resp.StatusCode, body)
	}
	for _, leak := range []string{
		"secret.example.com", "token=", "acknowledged_by", env.wsID.String(),
	} {
		if strings.Contains(string(body), leak) {
			t.Fatalf("public body leaks %q: %s", leak, body)
		}
	}
}

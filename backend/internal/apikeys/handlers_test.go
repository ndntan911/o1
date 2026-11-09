package apikeys_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/api"
	"o1/internal/apikeys"
	"o1/internal/config"
	"o1/internal/db/sqlc"
	"o1/internal/incidents"
	"o1/internal/monitors"
	"o1/internal/probe"
	"o1/internal/secretbox"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

const handlerTestPassword = "correct horse battery staple"

var (
	keyPrefixPattern = regexp.MustCompile(`^o1_[a-z0-9]{12}$`)
	keySecretPattern = regexp.MustCompile(`^o1_[a-z0-9]{12}_[A-Za-z0-9_-]{43}$`)
)

// staticResolver returns a fixed address for every lookup so monitor
// validation never touches the network.
type staticResolver struct{ ip string }

func (r staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP(r.ip)}}, nil
}

type keyAPIEnv struct {
	t        *testing.T
	pool     *pgxpool.Pool
	cfg      config.Config
	server   *httptest.Server
	keys     *apikeys.Service
	wsA      uuid.UUID
	wsB      uuid.UUID
	ownerID  uuid.UUID
	adminID  uuid.UUID
	memberID uuid.UUID
	viewerID uuid.UUID
	ownerBID uuid.UUID
	seq      int
}

func newKeyAPIEnv(t *testing.T) *keyAPIEnv {
	t.Helper()
	pool, cleanup := testutil.NewPostgres(t)
	t.Cleanup(cleanup)

	cfg := testutil.TestConfig(t)
	cfg.MonitorCreateLimit = 100
	cfg.MonitorMinInterval = 60 * time.Second
	cfg.MonitorMaxInterval = 24 * time.Hour

	box, err := secretbox.New(cfg.EncryptionKey)
	if err != nil {
		t.Fatalf("secretbox: %v", err)
	}
	guard := probe.NewGuardWithResolver(probe.Policy{
		AllowedPorts:         map[int]struct{}{80: {}, 443: {}},
		MaxRedirects:         5,
		MaxResponseBody:      1 << 20,
		AllowPrivateNetworks: true,
	}, staticResolver{ip: "127.0.0.1"})

	wsSvc := workspaces.NewService(pool, sqlc.New(pool))
	keySvc := apikeys.NewService(pool, sqlc.New(pool), wsSvc)
	monitorSvc := monitors.NewService(pool, sqlc.New(pool), wsSvc, box, guard, cfg)
	incidentSvc := incidents.NewService(pool, sqlc.New(pool), wsSvc)

	router := api.NewRouter(api.Dependencies{
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Pool:             pool,
		Config:           cfg,
		EmailSender:      testutil.NewFakeSender(),
		WorkspaceService: wsSvc,
		MonitorService:   monitorSvc,
		IncidentService:  incidentSvc,
		APIKeyService:    keySvc,
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	env := &keyAPIEnv{
		t: t, pool: pool, cfg: cfg, server: server, keys: keySvc,
	}
	env.ownerID = env.addUser("keys-owner@example.com")
	env.adminID = env.addUser("keys-admin@example.com")
	env.memberID = env.addUser("keys-member@example.com")
	env.viewerID = env.addUser("keys-viewer@example.com")
	env.ownerBID = env.addUser("keys-owner-b@example.com")

	wsA, err := wsSvc.Create(context.Background(), env.ownerID, "Keys A")
	if err != nil {
		t.Fatalf("create workspace A: %v", err)
	}
	env.wsA = wsA.ID
	env.addMember(env.adminID, "admin")
	env.addMember(env.memberID, "member")
	env.addMember(env.viewerID, "viewer")

	wsB, err := wsSvc.Create(context.Background(), env.ownerBID, "Keys B")
	if err != nil {
		t.Fatalf("create workspace B: %v", err)
	}
	env.wsB = wsB.ID
	return env
}

func (e *keyAPIEnv) addUser(emailAddr string) uuid.UUID {
	e.t.Helper()
	return testutil.CreateVerifiedUser(e.t, e.pool, emailAddr, handlerTestPassword)
}

func (e *keyAPIEnv) addMember(userID uuid.UUID, role string) {
	e.t.Helper()
	if err := sqlc.New(e.pool).UpsertWorkspaceMember(context.Background(), sqlc.UpsertWorkspaceMemberParams{
		WorkspaceID: e.wsA,
		UserID:      userID,
		Role:        role,
	}); err != nil {
		e.t.Fatalf("add member: %v", err)
	}
}

// sessionClient returns a cookie-backed client for emailAddr.
func (e *keyAPIEnv) sessionClient(emailAddr string) *keyTestClient {
	e.t.Helper()
	token := testutil.SessionToken(e.t, e.pool, e.cfg, emailAddr, handlerTestPassword)
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatalf("cookie jar: %v", err)
	}
	baseURL, err := url.Parse(e.server.URL)
	if err != nil {
		e.t.Fatalf("parse server URL: %v", err)
	}
	jar.SetCookies(baseURL, []*http.Cookie{{Name: "o1_session", Value: token}})
	return &keyTestClient{t: e.t, base: e.server.URL, origin: e.cfg.AppBaseURL, client: &http.Client{Jar: jar}}
}

// keyClient returns a cookie-less client that authenticates with a Bearer key.
func (e *keyAPIEnv) keyClient(secret string) *keyTestClient {
	return &keyTestClient{t: e.t, base: e.server.URL, origin: e.cfg.AppBaseURL, client: &http.Client{}, bearer: secret}
}

// createKey creates a key in workspace A through the service.
func (e *keyAPIEnv) createKey(name, role string) apikeys.CreatedKey {
	e.t.Helper()
	created, err := e.keys.Create(context.Background(), e.ownerID, e.wsA, apikeys.CreateInput{Name: name, Role: role})
	if err != nil {
		e.t.Fatalf("create key %q: %v", name, err)
	}
	return created
}

func (e *keyAPIEnv) nextName(prefix string) string {
	e.seq++
	return fmt.Sprintf("%s-%d", prefix, e.seq)
}

func (e *keyAPIEnv) insertMonitor(workspaceID uuid.UUID, name string) uuid.UUID {
	e.t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		e.t.Fatalf("new monitor id: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO monitors
			(id, workspace_id, name, url, method, expected_status,
			 interval_seconds, timeout_seconds, confirmation_threshold,
			 recovery_threshold, paused, created_by)
		VALUES ($1, $2, $3, 'https://example.com/health', 'GET', '2xx', 60, 5, 1, 1, false, $4)`,
		id, workspaceID, name, e.ownerID); err != nil {
		e.t.Fatalf("insert monitor: %v", err)
	}
	return id
}

func (e *keyAPIEnv) insertAlertChannel(workspaceID uuid.UUID, name string) uuid.UUID {
	e.t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		e.t.Fatalf("new channel id: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO alert_channels (id, workspace_id, kind, name, config, enabled)
		VALUES ($1, $2, 'webhook', $3, '{}'::jsonb, true)`,
		id, workspaceID, name); err != nil {
		e.t.Fatalf("insert alert channel: %v", err)
	}
	return id
}

func (e *keyAPIEnv) insertIncident(workspaceID, monitorID uuid.UUID) uuid.UUID {
	e.t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		e.t.Fatalf("new incident id: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO incidents (id, workspace_id, monitor_id, status)
		VALUES ($1, $2, $3, 'open')`,
		id, workspaceID, monitorID); err != nil {
		e.t.Fatalf("insert incident: %v", err)
	}
	return id
}

func (e *keyAPIEnv) suspend(workspaceID uuid.UUID) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE workspaces SET suspended_at = now() WHERE id = $1`, workspaceID); err != nil {
		e.t.Fatalf("suspend workspace: %v", err)
	}
}

// keyTestClient is a minimal HTTP client with optional Bearer auth.
type keyTestClient struct {
	t      *testing.T
	base   string
	origin string
	client *http.Client
	bearer string
}

func (c *keyTestClient) do(method, path string, body any) (*http.Response, []byte) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("encode request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		req.Header.Set("Origin", c.origin)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			c.t.Fatalf("close response body: %v", err)
		}
	}()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("read response body: %v", err)
	}
	return resp, data
}

func decodeBody[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode JSON %q: %v", string(raw), err)
	}
	return v
}

func requireStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("%s %s = %d, want %d", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, want)
	}
}

type keyJSON struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Role       string     `json:"role"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

type createdKeyJSON struct {
	Key    keyJSON `json:"key"`
	Secret string  `json:"secret"`
}

type keyListJSON struct {
	Keys []keyJSON `json:"keys"`
}

type problemJSON struct {
	Code   string            `json:"code"`
	Detail string            `json:"detail"`
	Fields map[string]string `json:"fields"`
}

func validMonitorBody(i int) map[string]any {
	return map[string]any{
		"name":                   fmt.Sprintf("API %d", i),
		"url":                    "https://example.com/health",
		"method":                 "GET",
		"expected_status":        "2xx",
		"interval_seconds":       60,
		"timeout_seconds":        10,
		"confirmation_threshold": 2,
		"recovery_threshold":     2,
	}
}

func TestAPIKeyHandlers_CRUD(t *testing.T) {
	env := newKeyAPIEnv(t)
	owner := env.sessionClient("keys-owner@example.com")
	base := "/api/v1/workspaces/" + env.wsA.String() + "/api-keys"

	resp, raw := owner.do(http.MethodGet, base, nil)
	requireStatus(t, resp, http.StatusOK)
	if !strings.Contains(string(raw), `"keys":[]`) {
		t.Fatalf("empty list body = %s, want keys:[]", raw)
	}
	if list := decodeBody[keyListJSON](t, raw); len(list.Keys) != 0 {
		t.Fatalf("empty list has %d keys", len(list.Keys))
	}

	resp, raw = owner.do(http.MethodPost, base, map[string]string{"name": "ci", "role": "viewer"})
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[createdKeyJSON](t, raw)
	if !keyPrefixPattern.MatchString(created.Key.Prefix) {
		t.Fatalf("prefix = %q", created.Key.Prefix)
	}
	if !keySecretPattern.MatchString(created.Secret) {
		t.Fatalf("secret shape = %q", created.Secret)
	}
	if created.Key.Role != "viewer" || created.Key.CreatedBy != env.ownerID.String() {
		t.Fatalf("created key = %+v", created.Key)
	}

	resp, raw = owner.do(http.MethodGet, base, nil)
	requireStatus(t, resp, http.StatusOK)
	list := decodeBody[keyListJSON](t, raw)
	if len(list.Keys) != 1 || list.Keys[0].ID != created.Key.ID {
		t.Fatalf("list = %+v", list)
	}
	if strings.Contains(string(raw), `"secret"`) {
		t.Fatalf("list body contains a secret field: %s", raw)
	}

	resp, _ = owner.do(http.MethodDelete, base+"/"+created.Key.ID, nil)
	requireStatus(t, resp, http.StatusNoContent)
	resp, raw = owner.do(http.MethodGet, base, nil)
	requireStatus(t, resp, http.StatusOK)
	if list := decodeBody[keyListJSON](t, raw); len(list.Keys) != 0 {
		t.Fatalf("list after delete = %+v", list)
	}

	// Members and viewers cannot manage keys.
	extra := env.createKey("member-target", "viewer")
	member := env.sessionClient("keys-member@example.com")
	resp, _ = member.do(http.MethodGet, base, nil)
	requireStatus(t, resp, http.StatusForbidden)
	resp, _ = member.do(http.MethodPost, base, map[string]string{"name": "nope", "role": "viewer"})
	requireStatus(t, resp, http.StatusForbidden)
	resp, _ = member.do(http.MethodDelete, base+"/"+extra.Key.ID.String(), nil)
	requireStatus(t, resp, http.StatusForbidden)

	viewer := env.sessionClient("keys-viewer@example.com")
	resp, _ = viewer.do(http.MethodGet, base, nil)
	requireStatus(t, resp, http.StatusForbidden)
	resp, _ = viewer.do(http.MethodPost, base, map[string]string{"name": "nope", "role": "viewer"})
	requireStatus(t, resp, http.StatusForbidden)
}

func TestAPIKeyHandlers_SecretNeverReturnedAgain(t *testing.T) {
	env := newKeyAPIEnv(t)
	owner := env.sessionClient("keys-owner@example.com")
	base := "/api/v1/workspaces/" + env.wsA.String() + "/api-keys"

	resp, raw := owner.do(http.MethodPost, base, map[string]string{"name": "once", "role": "member"})
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[createdKeyJSON](t, raw)

	var storedHash []byte
	if err := env.pool.QueryRow(context.Background(),
		`SELECT token_hash FROM api_keys WHERE id = $1`, uuid.MustParse(created.Key.ID)).Scan(&storedHash); err != nil {
		t.Fatalf("load token hash: %v", err)
	}
	hashForms := [][]byte{
		storedHash,
		[]byte(base64.StdEncoding.EncodeToString(storedHash)),
		[]byte(base64.RawURLEncoding.EncodeToString(storedHash)),
		[]byte(hex.EncodeToString(storedHash)),
	}

	assertClean := func(name string, body []byte) {
		t.Helper()
		if bytes.Contains(body, []byte(created.Secret)) {
			t.Fatalf("%s leaks the secret: %s", name, body)
		}
		for _, form := range hashForms {
			if bytes.Contains(body, form) {
				t.Fatalf("%s leaks the token hash: %s", name, body)
			}
		}
	}

	resp, raw = owner.do(http.MethodGet, base, nil)
	requireStatus(t, resp, http.StatusOK)
	assertClean("list", raw)

	// A duplicate-name problem response must not echo key material.
	resp, raw = owner.do(http.MethodPost, base, map[string]string{"name": "once", "role": "member"})
	requireStatus(t, resp, http.StatusConflict)
	assertClean("duplicate problem", raw)

	// A malformed path id problem response must not echo key material either.
	resp, raw = owner.do(http.MethodDelete, base+"/not-a-uuid", nil)
	requireStatus(t, resp, http.StatusBadRequest)
	assertClean("malformed id problem", raw)
}

func TestAPIKeyHandlers_Validation(t *testing.T) {
	env := newKeyAPIEnv(t)
	owner := env.sessionClient("keys-owner@example.com")
	base := "/api/v1/workspaces/" + env.wsA.String() + "/api-keys"

	for name, body := range map[string]map[string]string{
		"blank name": {"name": "   ", "role": "viewer"},
		"long name":  {"name": strings.Repeat("a", 61), "role": "viewer"},
		"admin role": {"name": "ok", "role": "admin"},
	} {
		t.Run(name, func(t *testing.T) {
			resp, raw := owner.do(http.MethodPost, base, body)
			requireStatus(t, resp, http.StatusBadRequest)
			problem := decodeBody[problemJSON](t, raw)
			if problem.Code != "validation_error" || len(problem.Fields) == 0 {
				t.Fatalf("problem = %+v", problem)
			}
		})
	}

	resp, _ := owner.do(http.MethodPost, base, map[string]string{"name": "dup", "role": "viewer"})
	requireStatus(t, resp, http.StatusCreated)
	resp, raw := owner.do(http.MethodPost, base, map[string]string{"name": "dup", "role": "member"})
	requireStatus(t, resp, http.StatusConflict)
	if problem := decodeBody[problemJSON](t, raw); problem.Code != "api_key_name_taken" {
		t.Fatalf("code = %q, want api_key_name_taken", problem.Code)
	}

	// Fill the workspace to 20 active keys.
	for i := 1; i < 20; i++ {
		env.createKey(fmt.Sprintf("fill-%d", i), "viewer")
	}
	resp, raw = owner.do(http.MethodPost, base, map[string]string{"name": "key-21", "role": "viewer"})
	requireStatus(t, resp, http.StatusUnprocessableEntity)
	if problem := decodeBody[problemJSON](t, raw); problem.Code != "api_key_limit" {
		t.Fatalf("code = %q, want api_key_limit", problem.Code)
	}
}

func TestAPIKeyHandlers_SuspendedAndCrossWorkspace(t *testing.T) {
	env := newKeyAPIEnv(t)
	owner := env.sessionClient("keys-owner@example.com")
	base := "/api/v1/workspaces/" + env.wsA.String() + "/api-keys"

	toRevoke := env.createKey("to-revoke", "viewer")
	keyA := env.createKey("a-key", "viewer")
	other, err := env.keys.Create(context.Background(), env.ownerBID, env.wsB, apikeys.CreateInput{
		Name: "b-key", Role: "viewer",
	})
	if err != nil {
		t.Fatalf("create workspace B key: %v", err)
	}

	// A key from workspace B cannot be revoked through workspace A.
	resp, _ := owner.do(http.MethodDelete, base+"/"+other.Key.ID.String(), nil)
	requireStatus(t, resp, http.StatusNotFound)

	// Nor can a workspace A key read workspace B monitors.
	client := env.keyClient(keyA.Secret)
	resp, _ = client.do(http.MethodGet, "/api/v1/workspaces/"+env.wsB.String()+"/monitors", nil)
	requireStatus(t, resp, http.StatusNotFound)

	env.suspend(env.wsA)
	resp, raw := owner.do(http.MethodPost, base, map[string]string{"name": "blocked", "role": "viewer"})
	requireStatus(t, resp, http.StatusForbidden)
	if problem := decodeBody[problemJSON](t, raw); problem.Code != "workspace_suspended" {
		t.Fatalf("code = %q, want workspace_suspended", problem.Code)
	}
	resp, raw = owner.do(http.MethodDelete, base+"/"+toRevoke.Key.ID.String(), nil)
	requireStatus(t, resp, http.StatusForbidden)
	if problem := decodeBody[problemJSON](t, raw); problem.Code != "workspace_suspended" {
		t.Fatalf("code = %q, want workspace_suspended", problem.Code)
	}

	// Listing keeps working while suspended.
	resp, raw = owner.do(http.MethodGet, base, nil)
	requireStatus(t, resp, http.StatusOK)
	if list := decodeBody[keyListJSON](t, raw); len(list.Keys) != 2 {
		t.Fatalf("suspended list = %+v", list)
	}
}

func TestAPIKeyHandlers_AuthMatrix(t *testing.T) {
	env := newKeyAPIEnv(t)
	ctx := context.Background()

	viewerKey := env.createKey("viewer-key", "viewer")
	memberKey := env.createKey("member-key", "member")

	viewer := env.keyClient(viewerKey.Secret)
	member := env.keyClient(memberKey.Secret)

	monitorsPath := "/api/v1/workspaces/" + env.wsA.String() + "/monitors"

	resp, _ := viewer.do(http.MethodGet, monitorsPath, nil)
	requireStatus(t, resp, http.StatusOK)
	resp, raw := viewer.do(http.MethodPost, monitorsPath, validMonitorBody(1))
	requireStatus(t, resp, http.StatusForbidden)
	if problem := decodeBody[problemJSON](t, raw); problem.Code != "forbidden" {
		t.Fatalf("viewer POST code = %q, want forbidden", problem.Code)
	}

	resp, raw = member.do(http.MethodPost, monitorsPath, validMonitorBody(2))
	requireStatus(t, resp, http.StatusCreated)
	createdMonitor := decodeBody[struct {
		ID string `json:"id"`
	}](t, raw)
	if createdMonitor.ID == "" {
		t.Fatal("member-created monitor has no id")
	}

	switchMonitor := env.insertMonitor(env.wsA, "switch-monitor")
	channel := env.insertAlertChannel(env.wsA, "switch-channel")
	resp, _ = member.do(http.MethodPatch,
		monitorsPath+"/"+switchMonitor.String()+"/alert-channels/"+channel.String(),
		map[string]bool{"enabled": true})
	requireStatus(t, resp, http.StatusNoContent)

	incidentMonitor := env.insertMonitor(env.wsA, "incident-monitor")
	incident := env.insertIncident(env.wsA, incidentMonitor)
	resp, _ = member.do(http.MethodPost,
		"/api/v1/workspaces/"+env.wsA.String()+"/incidents/"+incident.String()+"/acknowledge", nil)
	requireStatus(t, resp, http.StatusNoContent)

	// Session-only routes ignore the Bearer header and answer 401.
	sessionOnly := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/v1/workspaces/" + env.wsA.String() + "/alert-channels", nil},
		{http.MethodPut, "/api/v1/workspaces/" + env.wsA.String() + "/status-page", map[string]any{"enabled": true, "monitors": []string{}}},
		{http.MethodGet, "/api/v1/workspaces/" + env.wsA.String() + "/members", nil},
		{http.MethodGet, "/api/v1/admin/workspaces", nil},
	}
	for _, tc := range sessionOnly {
		resp, _ := member.do(tc.method, tc.path, tc.body)
		requireStatus(t, resp, http.StatusUnauthorized)
	}

	// A workspace A key cannot touch workspace B.
	bMonitors := "/api/v1/workspaces/" + env.wsB.String() + "/monitors"
	resp, _ = member.do(http.MethodGet, bMonitors, nil)
	requireStatus(t, resp, http.StatusNotFound)
	resp, _ = member.do(http.MethodPost, bMonitors, validMonitorBody(3))
	requireStatus(t, resp, http.StatusNotFound)

	// Revoked and malformed keys are unauthenticated.
	revoked := env.createKey("revoked-key", "viewer")
	if err := env.keys.Revoke(ctx, env.ownerID, env.wsA, revoked.Key.ID); err != nil {
		t.Fatalf("revoke key: %v", err)
	}
	resp, _ = env.keyClient(revoked.Secret).do(http.MethodGet, monitorsPath, nil)
	requireStatus(t, resp, http.StatusUnauthorized)
	resp, _ = env.keyClient("not-a-valid-key").do(http.MethodGet, monitorsPath, nil)
	requireStatus(t, resp, http.StatusUnauthorized)

	// A valid session cookie without an Authorization header still works.
	owner := env.sessionClient("keys-owner@example.com")
	resp, _ = owner.do(http.MethodGet, monitorsPath, nil)
	requireStatus(t, resp, http.StatusOK)

	// Per-key limiter: the burst passes, then the key is rate limited while a
	// second key remains independent.
	limitedKey := env.createKey("limited-key", "viewer")
	limited := env.keyClient(limitedKey.Secret)
	allowed := 0
	limited429 := false
	for i := 0; i < 200; i++ {
		resp, _ := limited.do(http.MethodGet, monitorsPath, nil)
		if resp.StatusCode == http.StatusTooManyRequests {
			limited429 = true
			break
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("limited key request %d = %d, want 200", i, resp.StatusCode)
		}
		allowed++
	}
	if !limited429 {
		t.Fatal("per-key limiter never returned 429")
	}
	if allowed < 60 {
		t.Fatalf("limiter rejected after %d requests, want at least the 60-request burst", allowed)
	}

	secondKey := env.createKey("second-key", "viewer")
	resp, _ = env.keyClient(secondKey.Secret).do(http.MethodGet, monitorsPath, nil)
	requireStatus(t, resp, http.StatusOK)
}

func TestAPIKeyHandlers_AuditAttribution(t *testing.T) {
	env := newKeyAPIEnv(t)
	memberKey := env.createKey("audit-key", "member")
	member := env.keyClient(memberKey.Secret)

	monitorsPath := "/api/v1/workspaces/" + env.wsA.String() + "/monitors"
	resp, _ := member.do(http.MethodPost, monitorsPath, validMonitorBody(1))
	requireStatus(t, resp, http.StatusCreated)

	var actorUser uuid.UUID
	var actorAPIKey *uuid.UUID
	if err := env.pool.QueryRow(context.Background(),
		`SELECT actor_user_id, actor_api_key_id
		   FROM security_events
		  WHERE workspace_id = $1 AND kind = 'monitor_created'
		  ORDER BY created_at DESC, id DESC
		  LIMIT 1`,
		env.wsA).Scan(&actorUser, &actorAPIKey); err != nil {
		t.Fatalf("load monitor_created event: %v", err)
	}
	if actorAPIKey == nil || *actorAPIKey != memberKey.Key.ID {
		t.Fatalf("actor_api_key_id = %v, want %s", actorAPIKey, memberKey.Key.ID)
	}
	if actorUser != env.ownerID {
		t.Fatalf("actor_user_id = %s, want key creator %s", actorUser, env.ownerID)
	}
}

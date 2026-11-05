package api_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/api"
	"o1/internal/config"
	"o1/internal/db/sqlc"
	"o1/internal/monitors"
	"o1/internal/probe"
	"o1/internal/secretbox"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

// staticResolver returns a fixed address for every lookup.
type staticResolver struct{ ip string }

func (r staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP(r.ip)}}, nil
}

type monitorJSON struct {
	ID             string            `json:"id"`
	WorkspaceID    string            `json:"workspace_id"`
	Name           string            `json:"name"`
	URL            string            `json:"url"`
	Method         string            `json:"method"`
	Headers        map[string]string `json:"headers"`
	HasBody        bool              `json:"has_body"`
	ExpectedStatus string            `json:"expected_status"`
	Paused         bool              `json:"paused"`
	Status         string            `json:"status"`
	Summary24h     *struct {
		Checks int64 `json:"checks"`
	} `json:"summary_24h"`
}

type monitorListJSON struct {
	Monitors   []monitorJSON `json:"monitors"`
	NextCursor string        `json:"next_cursor"`
}

type problemJSON struct {
	Code   string            `json:"code"`
	Fields map[string]string `json:"fields"`
}

type monitorAPIEnv struct {
	t      *testing.T
	pool   *pgxpool.Pool
	cfg    config.Config
	router http.Handler
	server *httptest.Server
}

func newMonitorAPIEnv(t *testing.T, createLimit int) *monitorAPIEnv {
	t.Helper()
	pool, cleanup := testutil.NewPostgres(t)
	t.Cleanup(cleanup)

	cfg := testutil.TestConfig(t)
	cfg.MonitorCreateLimit = createLimit
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
	monitorSvc := monitors.NewService(pool, sqlc.New(pool), wsSvc, box, guard, cfg)

	router := api.NewRouter(api.Dependencies{
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Pool:             pool,
		Config:           cfg,
		EmailSender:      testutil.NewFakeSender(),
		WorkspaceService: wsSvc,
		MonitorService:   monitorSvc,
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &monitorAPIEnv{t: t, pool: pool, cfg: cfg, router: router, server: server}
}

func (e *monitorAPIEnv) login(emailAddr string) *apiClient {
	e.t.Helper()
	password := "correct horse battery staple"
	testutil.CreateVerifiedUser(e.t, e.pool, emailAddr, password)
	token := testutil.SessionToken(e.t, e.pool, e.cfg, emailAddr, password)

	c := newAPIClient(e.t, e.server.URL, e.cfg.AppBaseURL)
	baseURL, err := url.Parse(e.server.URL)
	if err != nil {
		e.t.Fatalf("parse server URL: %v", err)
	}
	c.client.Jar.SetCookies(baseURL, []*http.Cookie{{Name: "o1_session", Value: token}})
	return c
}

func (c *apiClient) createWorkspace(t *testing.T, name string) string {
	t.Helper()
	resp, body := c.do(http.MethodPost, "/api/v1/workspaces", map[string]string{"name": name})
	requireStatus(t, resp, http.StatusCreated)
	ws := decodeJSON[struct {
		ID string `json:"id"`
	}](t, body)
	return ws.ID
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

func TestAPI_MonitorCRUDAndTenantIsolation(t *testing.T) {
	env := newMonitorAPIEnv(t, 10)
	alice := env.login("alice@example.com")
	bob := env.login("bob@example.com")
	ws := alice.createWorkspace(t, "Alice")

	body := validMonitorBody(0)
	body["headers"] = map[string]string{"Authorization": "Bearer super-secret"}
	resp, raw := alice.do(http.MethodPost, "/api/v1/workspaces/"+ws+"/monitors", body)
	requireStatus(t, resp, http.StatusCreated)
	created := decodeJSON[monitorJSON](t, raw)
	if created.Status != "unknown" || created.Headers["Authorization"] != "***" {
		t.Fatalf("created = %+v", created)
	}
	if created.Summary24h != nil {
		t.Fatalf("new monitor summary = %+v, want nil", created.Summary24h)
	}
	if created.ExpectedStatus != "200-299" {
		t.Fatalf("expected_status = %q, want normalized 200-299", created.ExpectedStatus)
	}

	// Another tenant cannot see or mutate it.
	resp, _ = bob.do(http.MethodGet, "/api/v1/workspaces/"+ws+"/monitors/"+created.ID, nil)
	requireStatus(t, resp, http.StatusNotFound)
	resp, _ = bob.do(http.MethodDelete, "/api/v1/workspaces/"+ws+"/monitors/"+created.ID, nil)
	requireStatus(t, resp, http.StatusNotFound)

	// Pause and resume are idempotent.
	for i := 0; i < 2; i++ {
		resp, raw = alice.do(http.MethodPost, "/api/v1/workspaces/"+ws+"/monitors/"+created.ID+"/pause", nil)
		requireStatus(t, resp, http.StatusOK)
		if got := decodeJSON[monitorJSON](t, raw); !got.Paused || got.Status != "paused" {
			t.Fatalf("pause %d = %+v", i, got)
		}
	}
	resp, raw = alice.do(http.MethodPost, "/api/v1/workspaces/"+ws+"/monitors/"+created.ID+"/resume", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeJSON[monitorJSON](t, raw); got.Paused || got.Status == "paused" {
		t.Fatalf("resume = %+v", got)
	}

	// List is tenant-scoped and masks headers.
	resp, raw = alice.do(http.MethodGet, "/api/v1/workspaces/"+ws+"/monitors", nil)
	requireStatus(t, resp, http.StatusOK)
	list := decodeJSON[monitorListJSON](t, raw)
	if len(list.Monitors) != 1 || list.Monitors[0].ID != created.ID {
		t.Fatalf("list = %+v", list)
	}
	if list.Monitors[0].Headers["Authorization"] != "***" {
		t.Fatalf("list headers = %+v", list.Monitors[0].Headers)
	}

	// PATCH replaces headers and keeps the monitor readable.
	resp, raw = alice.do(http.MethodPatch, "/api/v1/workspaces/"+ws+"/monitors/"+created.ID, map[string]any{
		"name":    "Renamed",
		"headers": map[string]string{"X-Api-Key": "secret"},
	})
	requireStatus(t, resp, http.StatusOK)
	updated := decodeJSON[monitorJSON](t, raw)
	if updated.Name != "Renamed" || updated.Headers["X-Api-Key"] != "***" || len(updated.Headers) != 1 {
		t.Fatalf("updated = %+v", updated)
	}

	// DELETE answers 204 and the monitor is gone.
	resp, _ = alice.do(http.MethodDelete, "/api/v1/workspaces/"+ws+"/monitors/"+created.ID, nil)
	requireStatus(t, resp, http.StatusNoContent)
	resp, _ = alice.do(http.MethodGet, "/api/v1/workspaces/"+ws+"/monitors/"+created.ID, nil)
	requireStatus(t, resp, http.StatusNotFound)
}

func TestAPI_MonitorValidationQuotaRateLimitAndViewer(t *testing.T) {
	env := newMonitorAPIEnv(t, 3)
	alice := env.login("alice@example.com")
	viewerEmail := "viewer@example.com"
	ws := alice.createWorkspace(t, "Quota")

	// Invalid URL produces field-level validation errors.
	resp, raw := alice.do(http.MethodPost, "/api/v1/workspaces/"+ws+"/monitors", map[string]any{
		"name":             "bad",
		"url":              "ftp://example.com/",
		"method":           "GET",
		"interval_seconds": 60,
		"timeout_seconds":  10,
	})
	requireStatus(t, resp, http.StatusBadRequest)
	p := decodeJSON[problemJSON](t, raw)
	if p.Code != "validation_error" || p.Fields["url"] == "" {
		t.Fatalf("validation problem = %+v", p)
	}

	// The third create hits the rate limit; the first three succeed.
	for i := 0; i < 3; i++ {
		resp, _ = alice.do(http.MethodPost, "/api/v1/workspaces/"+ws+"/monitors", validMonitorBody(i))
		requireStatus(t, resp, http.StatusCreated)
	}
	resp, _ = alice.do(http.MethodPost, "/api/v1/workspaces/"+ws+"/monitors", validMonitorBody(3))
	requireStatus(t, resp, http.StatusTooManyRequests)
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Fatal("429 response missing Retry-After")
	}

	// A viewer can read but not mutate.
	viewer := env.login(viewerEmail)
	resp, _ = alice.do(http.MethodPost, "/api/v1/workspaces/"+ws+"/members", map[string]string{
		"email": viewerEmail,
		"role":  "viewer",
	})
	requireStatus(t, resp, http.StatusNoContent)
	resp, _ = viewer.do(http.MethodGet, "/api/v1/workspaces/"+ws+"/monitors", nil)
	requireStatus(t, resp, http.StatusOK)
	resp, _ = viewer.do(http.MethodPost, "/api/v1/workspaces/"+ws+"/monitors", validMonitorBody(4))
	requireStatus(t, resp, http.StatusForbidden)
}

func TestAPI_MonitorQuota(t *testing.T) {
	env := newMonitorAPIEnv(t, 10)
	alice := env.login("alice@example.com")
	ws := alice.createWorkspace(t, "Full")

	for i := 0; i < 5; i++ {
		resp, _ := alice.do(http.MethodPost, "/api/v1/workspaces/"+ws+"/monitors", validMonitorBody(i))
		requireStatus(t, resp, http.StatusCreated)
	}
	resp, raw := alice.do(http.MethodPost, "/api/v1/workspaces/"+ws+"/monitors", validMonitorBody(5))
	requireStatus(t, resp, http.StatusConflict)
	if p := decodeJSON[problemJSON](t, raw); p.Code != "quota_exceeded" {
		t.Fatalf("problem = %+v", p)
	}
}

func (e *monitorAPIEnv) insertResult(t *testing.T, monitorID, workspaceID uuid.UUID, checkedAt time.Time) {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new uuid: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO probe_results
			(id, monitor_id, workspace_id, scheduled_for, checked_at, status, status_code, latency_ms)
		VALUES ($1, $2, $3, $4, $4, 'up', 200, 120)`,
		id, monitorID, workspaceID, checkedAt); err != nil {
		t.Fatalf("insert result: %v", err)
	}
}

func TestAPI_MonitorResultsAndSummary(t *testing.T) {
	env := newMonitorAPIEnv(t, 10)
	alice := env.login("alice@example.com")
	bob := env.login("bob@example.com")
	ws := alice.createWorkspace(t, "Results")
	wsID := uuid.MustParse(ws)

	resp, raw := alice.do(http.MethodPost, "/api/v1/workspaces/"+ws+"/monitors", validMonitorBody(0))
	requireStatus(t, resp, http.StatusCreated)
	created := decodeJSON[monitorJSON](t, raw)
	monitorID := uuid.MustParse(created.ID)

	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < 3; i++ {
		env.insertResult(t, monitorID, wsID, now.Add(-time.Duration(i)*time.Minute))
	}

	resp, raw = alice.do(http.MethodGet, "/api/v1/workspaces/"+ws+"/monitors/"+created.ID+"/results?window=24h&limit=2", nil)
	requireStatus(t, resp, http.StatusOK)
	first := decodeJSON[struct {
		Results []struct {
			ID        string `json:"id"`
			CheckedAt string `json:"checked_at"`
		} `json:"results"`
		NextCursor string `json:"next_cursor"`
	}](t, raw)
	if len(first.Results) != 2 || first.NextCursor == "" {
		t.Fatalf("results page 1 = %+v", first)
	}
	resp, raw = alice.do(http.MethodGet, "/api/v1/workspaces/"+ws+"/monitors/"+created.ID+"/results?window=24h&limit=2&cursor="+url.QueryEscape(first.NextCursor), nil)
	requireStatus(t, resp, http.StatusOK)
	second := decodeJSON[struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
		NextCursor string `json:"next_cursor"`
	}](t, raw)
	if len(second.Results) != 1 || second.NextCursor != "" {
		t.Fatalf("results page 2 = %+v", second)
	}

	resp, raw = alice.do(http.MethodGet, "/api/v1/workspaces/"+ws+"/monitors/"+created.ID+"/summary", nil)
	requireStatus(t, resp, http.StatusOK)
	summary := decodeJSON[struct {
		Window string  `json:"window"`
		Checks int64   `json:"checks"`
		Up     int64   `json:"up"`
		Uptime float64 `json:"uptime"`
	}](t, raw)
	if summary.Window != "24h" || summary.Checks != 3 || summary.Up != 3 || summary.Uptime != 1 {
		t.Fatalf("summary = %+v", summary)
	}

	resp, raw = alice.do(http.MethodGet, "/api/v1/workspaces/"+ws+"/monitors/"+created.ID+"/results?window=30d", nil)
	requireStatus(t, resp, http.StatusBadRequest)
	if p := decodeJSON[problemJSON](t, raw); p.Fields["window"] == "" {
		t.Fatalf("window problem = %+v", p)
	}

	resp, _ = bob.do(http.MethodGet, "/api/v1/workspaces/"+ws+"/monitors/"+created.ID+"/summary", nil)
	requireStatus(t, resp, http.StatusNotFound)
}

func TestAPI_MonitorListCursor(t *testing.T) {
	env := newMonitorAPIEnv(t, 10)
	alice := env.login("alice@example.com")
	ws := alice.createWorkspace(t, "Pages")

	for i := 0; i < 3; i++ {
		resp, _ := alice.do(http.MethodPost, "/api/v1/workspaces/"+ws+"/monitors", validMonitorBody(i))
		requireStatus(t, resp, http.StatusCreated)
	}
	resp, raw := alice.do(http.MethodGet, "/api/v1/workspaces/"+ws+"/monitors?limit=2", nil)
	requireStatus(t, resp, http.StatusOK)
	first := decodeJSON[monitorListJSON](t, raw)
	if len(first.Monitors) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	resp, raw = alice.do(http.MethodGet, "/api/v1/workspaces/"+ws+"/monitors?limit=2&cursor="+url.QueryEscape(first.NextCursor), nil)
	requireStatus(t, resp, http.StatusOK)
	second := decodeJSON[monitorListJSON](t, raw)
	if len(second.Monitors) != 1 || second.NextCursor != "" {
		t.Fatalf("second page = %+v", second)
	}
	if second.Monitors[0].ID == first.Monitors[0].ID || second.Monitors[0].ID == first.Monitors[1].ID {
		t.Fatalf("pages overlap: %+v %+v", first, second)
	}
}

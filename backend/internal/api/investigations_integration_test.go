package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"o1/internal/api"
	"o1/internal/config"
	"o1/internal/testutil"
)

// investigationAPIEnv wires the real API router against a disposable database.
// When enabled is false the router must leave the investigation service nil, as
// the API process does when O1_COPILOT_ENABLED is off.
type investigationAPIEnv struct {
	t      *testing.T
	pool   *pgxpool.Pool
	cfg    config.Config
	router http.Handler
	server *httptest.Server
}

func newInvestigationAPIEnv(t *testing.T, enabled bool, perIncidentPerHour int) *investigationAPIEnv {
	t.Helper()
	pool, cleanup := testutil.NewPostgres(t)
	t.Cleanup(cleanup)

	cfg := testutil.TestConfig(t)
	cfg.AIProvider = "stub"
	cfg.AIModel = "stub"
	cfg.CopilotEnabled = enabled
	// A distinct copilot model proves the router passes deps.Config.CopilotModel
	// through to the stored row rather than reusing AIModel.
	cfg.CopilotModel = "copilot-test-model"
	cfg.CopilotTimeout = 90 * time.Second
	cfg.CopilotCallTimeout = 30 * time.Second
	cfg.CopilotMaxModelCalls = 5
	cfg.CopilotMaxToolCalls = 6
	cfg.CopilotMaxResultBytes = 8192
	cfg.CopilotMaxOutputTokens = 1200
	cfg.CopilotMaxInputTokens = 40000
	cfg.CopilotPerIncidentPerHour = perIncidentPerHour
	cfg.CopilotPerWorkspacePerDay = 20

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: logger})
	if err != nil {
		t.Fatalf("new river client: %v", err)
	}

	router := api.NewRouter(api.Dependencies{
		Logger:        logger,
		Pool:          pool,
		Config:        cfg,
		EmailSender:   testutil.NewFakeSender(),
		RiverInserter: riverClient,
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &investigationAPIEnv{t: t, pool: pool, cfg: cfg, router: router, server: server}
}

// login creates a verified user and returns a cookie-authenticated client.
func (e *investigationAPIEnv) login(emailAddr string) *apiClient {
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

// createIncident creates a monitor through the API and an incident directly in
// the database, returning the incident id.
func (e *investigationAPIEnv) createIncident(owner *apiClient, workspaceID string) uuid.UUID {
	e.t.Helper()
	resp, raw := owner.do(http.MethodPost, "/api/v1/workspaces/"+workspaceID+"/monitors", validMonitorBody(0))
	requireStatus(e.t, resp, http.StatusCreated)
	monitor := decodeJSON[monitorJSON](e.t, raw)

	var incidentID uuid.UUID
	if err := e.pool.QueryRow(context.Background(),
		`INSERT INTO incidents (id, workspace_id, monitor_id, status)
		 VALUES (gen_random_uuid(), $1, $2, 'open') RETURNING id`,
		workspaceID, monitor.ID,
	).Scan(&incidentID); err != nil {
		e.t.Fatalf("seed incident: %v", err)
	}
	return incidentID
}

// postInvestigation issues POST .../investigations, spacing calls out so the
// per-IP start limiter (6/s, burst 2) does not turn an intended assertion into
// a 429.
func (e *investigationAPIEnv) postInvestigation(c *apiClient, workspaceID string, incidentID uuid.UUID) (*http.Response, []byte) {
	e.t.Helper()
	time.Sleep(250 * time.Millisecond)
	return c.do(http.MethodPost,
		"/api/v1/workspaces/"+workspaceID+"/incidents/"+incidentID.String()+"/investigations", nil)
}

func (e *investigationAPIEnv) latestPath(workspaceID string, incidentID uuid.UUID) string {
	return "/api/v1/workspaces/" + workspaceID + "/incidents/" + incidentID.String() + "/investigations/latest"
}

// doBearer issues a request authenticated by an API key rather than a cookie.
func (e *investigationAPIEnv) doBearer(token, path string) (*http.Response, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.server.URL+path, nil)
	if err != nil {
		e.t.Fatalf("create bearer request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("GET %s with bearer key: %v", path, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("read bearer response: %v", err)
	}
	return resp, data
}

func TestInvestigations_MemberStartsViewerReads(t *testing.T) {
	env := newInvestigationAPIEnv(t, true, 3)
	owner := env.login("owner@example.com")
	viewerEmail := "viewer@example.com"
	viewer := env.login(viewerEmail)
	other := env.login("other@example.com")

	workspaceID := owner.createWorkspace(t, "Investigations")
	resp, _ := owner.do(http.MethodPost, "/api/v1/workspaces/"+workspaceID+"/members",
		map[string]string{"email": viewerEmail, "role": "viewer"})
	requireStatus(t, resp, http.StatusNoContent)

	incidentID := env.createIncident(owner, workspaceID)
	otherWorkspaceID := other.createWorkspace(t, "Other")
	otherIncidentID := env.createIncident(other, otherWorkspaceID)

	// A member starts a run: 202 with the queued public row.
	resp, raw := env.postInvestigation(owner, workspaceID, incidentID)
	requireStatus(t, resp, http.StatusAccepted)
	started := decodeJSON[struct {
		ID         string  `json:"id"`
		IncidentID string  `json:"incident_id"`
		Status     string  `json:"status"`
		Model      string  `json:"model"`
		Attempts   int     `json:"attempts"`
		ErrorCode  *string `json:"error_code"`
	}](t, raw)
	if started.ID == "" || started.IncidentID != incidentID.String() {
		t.Fatalf("started = %+v", started)
	}
	if started.Status != "queued" || started.Model != "copilot-test-model" {
		t.Fatalf("started status/model = %q/%q, want queued/copilot-test-model", started.Status, started.Model)
	}

	// The row and its River job are committed together, and the stored model
	// is the configured copilot model, not the AI model used for insights.
	var rowStatus, rowModel string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT status, model FROM incident_investigations WHERE id = $1`, started.ID,
	).Scan(&rowStatus, &rowModel); err != nil {
		t.Fatalf("load investigation row: %v", err)
	}
	if rowStatus != "queued" {
		t.Fatalf("stored status = %q, want queued", rowStatus)
	}
	if rowModel != "copilot-test-model" {
		t.Fatalf("stored model = %q, want copilot-test-model", rowModel)
	}
	var queue string
	var maxAttempts int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT queue, max_attempts FROM river_job
		 WHERE kind = 'investigation' AND args->>'investigation_id' = $1`, started.ID,
	).Scan(&queue, &maxAttempts); err != nil {
		t.Fatalf("load river job: %v", err)
	}
	if queue != "ai" || maxAttempts != 3 {
		t.Fatalf("river job queue/max_attempts = %q/%d, want ai/3", queue, maxAttempts)
	}

	// A second start while the first is active is a 409 carrying the active row.
	resp, raw = env.postInvestigation(owner, workspaceID, incidentID)
	requireStatus(t, resp, http.StatusConflict)
	active := decodeJSON[struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}](t, raw)
	if active.ID != started.ID || active.Status != "queued" {
		t.Fatalf("active row = %+v, want %s/queued", active, started.ID)
	}

	// Viewers can read but not start.
	resp, _ = env.postInvestigation(viewer, workspaceID, incidentID)
	requireStatus(t, resp, http.StatusForbidden)

	// An incident from another workspace is not found through this path.
	resp, _ = env.postInvestigation(owner, workspaceID, otherIncidentID)
	requireStatus(t, resp, http.StatusNotFound)

	// Complete the run directly so the read surface has a result and a trace
	// that includes tool calls and model calls, which must not be exposed.
	result := `{"sufficient":true,"assessment":"the API is down","hypotheses":[]}`
	trace := `{"steps":[{"index":0,"tool":"o1_get_monitor","args_summary":"{}","status":"ok","duration_ms":3}],` +
		`"calls":[{"id":"call-secret","name":"o1_get_monitor","result":{"password":"hunter2"}}],` +
		`"model_calls":[{"index":0,"input_tokens":11,"output_tokens":4}]}`
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE incident_investigations
		 SET status='ok', result=$2::jsonb, trace=$3::jsonb, tool_calls=1, completed_at=now()
		 WHERE id=$1`, started.ID, result, trace,
	); err != nil {
		t.Fatalf("complete investigation: %v", err)
	}

	resp, raw = viewer.do(http.MethodGet, env.latestPath(workspaceID, incidentID), nil)
	requireStatus(t, resp, http.StatusOK)
	viewerBody := decodeJSON[map[string]any](t, raw)
	allowed := map[string]struct{}{
		"id": {}, "incident_id": {}, "status": {}, "model": {}, "result": {},
		"trace": {}, "attempts": {}, "tool_calls": {}, "input_tokens": {},
		"output_tokens": {}, "error_code": {}, "created_at": {}, "completed_at": {},
	}
	for key := range viewerBody {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("response exposes unexpected field %q: %v", key, viewerBody)
		}
	}
	if len(viewerBody) != len(allowed) {
		t.Fatalf("response fields = %v, want exactly %d", viewerBody, len(allowed))
	}
	if viewerBody["id"] != started.ID || viewerBody["status"] != "ok" {
		t.Fatalf("viewer latest = %v", viewerBody)
	}
	traceBody, ok := viewerBody["trace"].(map[string]any)
	if !ok {
		t.Fatalf("trace = %#v, want an object", viewerBody["trace"])
	}
	if _, ok := traceBody["calls"]; ok {
		t.Fatalf("trace exposes raw calls: %v", traceBody)
	}
	if _, ok := traceBody["model_calls"]; ok {
		t.Fatalf("trace exposes raw model calls: %v", traceBody)
	}
	steps, ok := traceBody["steps"].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("trace steps = %#v, want one step", traceBody["steps"])
	}
	resultBody, ok := viewerBody["result"].(map[string]any)
	if !ok || resultBody["assessment"] != "the API is down" {
		t.Fatalf("result = %#v", viewerBody["result"])
	}

	// The by-id read accepts the same viewer.
	resp, raw = viewer.do(http.MethodGet,
		"/api/v1/workspaces/"+workspaceID+"/incidents/"+incidentID.String()+"/investigations/"+started.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeJSON[struct {
		ID string `json:"id"`
	}](t, raw); got.ID != started.ID {
		t.Fatalf("get by id = %+v", got)
	}

	// A viewer key scoped to workspace A cannot read workspace B through the
	// path: the service authorizes the key's workspace before the lookup.
	resp, raw = owner.do(http.MethodPost, "/api/v1/workspaces/"+workspaceID+"/api-keys",
		map[string]string{"name": "viewer-key", "role": "viewer"})
	requireStatus(t, resp, http.StatusCreated)
	key := decodeJSON[struct {
		Secret string `json:"secret"`
	}](t, raw)
	resp, _ = env.doBearer(key.Secret, env.latestPath(otherWorkspaceID, otherIncidentID))
	requireStatus(t, resp, http.StatusNotFound)
}

func TestInvestigations_Limit(t *testing.T) {
	// A configured cap of one proves the router passes
	// CopilotPerIncidentPerHour through instead of relying on the default.
	env := newInvestigationAPIEnv(t, true, 1)
	owner := env.login("limit@example.com")
	workspaceID := owner.createWorkspace(t, "Limit")
	incidentID := env.createIncident(owner, workspaceID)

	// One completed run in the last hour exhausts the configured cap.
	if _, err := env.pool.Exec(context.Background(),
		`INSERT INTO incident_investigations
		   (id, workspace_id, incident_id, status, model, prompt_version, created_at, completed_at)
		 VALUES (gen_random_uuid(), $1, $2, 'ok', 'stub', 'test', now(), now())`,
		workspaceID, incidentID,
	); err != nil {
		t.Fatalf("seed completed investigation: %v", err)
	}

	resp, _ := env.postInvestigation(owner, workspaceID, incidentID)
	requireStatus(t, resp, http.StatusTooManyRequests)
}

func TestInvestigations_Disabled(t *testing.T) {
	env := newInvestigationAPIEnv(t, false, 3)
	owner := env.login("disabled@example.com")
	workspaceID := owner.createWorkspace(t, "Disabled")
	incidentID := env.createIncident(owner, workspaceID)

	resp, _ := env.postInvestigation(owner, workspaceID, incidentID)
	requireStatus(t, resp, http.StatusNotImplemented)

	resp, _ = owner.do(http.MethodGet, env.latestPath(workspaceID, incidentID), nil)
	requireStatus(t, resp, http.StatusNotImplemented)

	resp, _ = owner.do(http.MethodGet,
		"/api/v1/workspaces/"+workspaceID+"/incidents/"+incidentID.String()+"/investigations/"+uuid.NewString(), nil)
	requireStatus(t, resp, http.StatusNotImplemented)
}

package mcpserver_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"o1/internal/auth"
	"o1/internal/copilot/tools"
	"o1/internal/db/sqlc"
	"o1/internal/incidents"
	"o1/internal/mcpserver"
	"o1/internal/monitors"
	"o1/internal/observability"
	"o1/internal/probe"
	"o1/internal/secretbox"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

const mcpTestPassword = "correct horse battery staple"

// Recognizable secrets that must never appear in an MCP tool result.
const (
	monitorURLToken = "super-secret-monitor-url-token"
	channelName     = "Ops Email Secret"
	recipient       = "hidden@example.com"
	deliveryError   = "smtp connect failed for hidden@example.com"
)

type mcpEnv struct {
	t *testing.T

	server  *httptest.Server
	pool    *pgxpool.Pool
	metrics *observability.Metrics

	memberSession *mcp.ClientSession
	viewerSession *mcp.ClientSession

	memberRaw   string
	memberKeyID uuid.UUID
	viewerKeyID uuid.UUID

	monitorID  uuid.UUID
	incidentID uuid.UUID
	otherID    uuid.UUID
}

func newMCPEnv(t *testing.T) *mcpEnv {
	t.Helper()
	pool, cleanup := testutil.NewPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	cfg := testutil.TestConfig(t)
	q := sqlc.New(pool)
	wsSvc := workspaces.NewService(pool, q)
	box, err := secretbox.New(cfg.EncryptionKey)
	if err != nil {
		t.Fatalf("secretbox: %v", err)
	}
	monSvc := monitors.NewService(
		pool, q, wsSvc, box,
		probe.NewGuard(probe.PolicyFromConfig(cfg)),
		cfg,
	)
	incSvc := incidents.NewService(pool, q, wsSvc)
	authSvc := auth.NewService(pool, q, testutil.NewFakeSender(), cfg)
	metrics := observability.NewMetrics("o1-api", "test")

	owner := testutil.CreateVerifiedUser(t, pool, "mcp-owner@example.com", mcpTestPassword)
	ws, err := wsSvc.Create(ctx, owner, "MCP workspace")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	otherOwner := testutil.CreateVerifiedUser(t, pool, "mcp-other-owner@example.com", mcpTestPassword)
	otherWS, err := wsSvc.Create(ctx, otherOwner, "Other MCP workspace")
	if err != nil {
		t.Fatalf("create other workspace: %v", err)
	}

	env := &mcpEnv{t: t, pool: pool, metrics: metrics}
	env.memberRaw, env.memberKeyID = createKey(t, q, ctx, ws.ID, owner, "aaaaaaaaaaaa", "member")
	_, env.viewerKeyID = createKey(t, q, ctx, ws.ID, owner, "bbbbbbbbbbbb", "viewer")
	viewerRaw := "o1_bbbbbbbbbbbb_" + strings.Repeat("A", 43)

	env.monitorID = insertMonitor(t, pool, ws.ID, owner)
	env.incidentID = insertIncident(t, pool, ws.ID, env.monitorID)
	insertAlertDelivery(t, pool, ws.ID, env.incidentID)

	otherMonitor := insertMonitor(t, pool, otherWS.ID, otherOwner)
	env.otherID = insertIncident(t, pool, otherWS.ID, otherMonitor)

	registry := tools.NewRegistry(incSvc, monSvc, time.Now)
	env.server = httptest.NewServer(mcpserver.New(mcpserver.Options{
		Auth:           authSvc,
		Registry:       registry,
		Queries:        q,
		AllowedOrigins: cfg.AllowedOrigins,
		Version:        "test",
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:        metrics,
	}).Handler())
	t.Cleanup(env.server.Close)

	env.memberSession = connect(t, env.server.URL, env.memberRaw)
	env.viewerSession = connect(t, env.server.URL, viewerRaw)
	return env
}

// createKey seeds an API key row with a known raw key and returns both.
func createKey(t *testing.T, q *sqlc.Queries, ctx context.Context, workspaceID, owner uuid.UUID, prefix, role string) (string, uuid.UUID) {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new key id: %v", err)
	}
	raw := "o1_" + prefix + "_" + strings.Repeat("A", 43)
	if _, err := q.CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{
		ID:          id,
		WorkspaceID: workspaceID,
		Name:        role + " key",
		Prefix:      prefix,
		TokenHash:   auth.HashToken(raw),
		Role:        role,
		CreatedBy:   owner,
	}); err != nil {
		t.Fatalf("create %s key: %v", role, err)
	}
	return raw, id
}

func insertMonitor(t *testing.T, pool *pgxpool.Pool, workspaceID, owner uuid.UUID) uuid.UUID {
	t.Helper()
	id := newID(t)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO monitors
			(id, workspace_id, name, url, method, expected_status, interval_seconds,
			 timeout_seconds, confirmation_threshold, recovery_threshold, paused, created_by)
		VALUES ($1, $2, 'API health', $3, 'GET', '200-299', 60, 5, 1, 1, false, $4)`,
		id, workspaceID, "https://internal.example.test/health?token="+monitorURLToken, owner,
	); err != nil {
		t.Fatalf("insert monitor: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO monitor_state (monitor_id) VALUES ($1)`, id); err != nil {
		t.Fatalf("insert monitor state: %v", err)
	}
	return id
}

func insertIncident(t *testing.T, pool *pgxpool.Pool, workspaceID, monitorID uuid.UUID) uuid.UUID {
	t.Helper()
	id := newID(t)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO incidents (id, workspace_id, monitor_id, status, started_at)
		VALUES ($1, $2, $3, 'open', now())`,
		id, workspaceID, monitorID,
	); err != nil {
		t.Fatalf("insert incident: %v", err)
	}
	return id
}

// insertAlertDelivery seeds a channel and one failed delivery whose recipient,
// channel name, and error text all contain recognizable secrets.
func insertAlertDelivery(t *testing.T, pool *pgxpool.Pool, workspaceID, incidentID uuid.UUID) {
	t.Helper()
	channelID := newID(t)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO alert_channels (id, workspace_id, kind, name, config)
		VALUES ($1, $2, 'email', $3, $4)`,
		channelID, workspaceID, channelName,
		[]byte(`{"recipients":["`+recipient+`"]}`),
	); err != nil {
		t.Fatalf("insert alert channel: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO alert_deliveries
			(id, incident_id, channel_id, event, sequence, recipient, status, attempts, last_error)
		VALUES ($1, $2, $3, 'opened', 0, $4, 'failed', 1, $5)`,
		newID(t), incidentID, channelID, recipient, deliveryError,
	); err != nil {
		t.Fatalf("insert alert delivery: %v", err)
	}
}

func newID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}
	return id
}

func connect(t *testing.T, endpoint, raw string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-conformance", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           bearerClient(raw),
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func bearerClient(raw string) *http.Client {
	return &http.Client{Transport: &bearerTransport{token: raw}}
}

type bearerTransport struct {
	token string
}

func (b *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(clone)
}

func toolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok {
			t.Fatalf("content = %T, want *mcp.TextContent", content)
		}
		b.WriteString(text.Text)
	}
	return b.String()
}

func TestMCP_MemberToolsAndViewerTools(t *testing.T) {
	env := newMCPEnv(t)

	memberTools := toolNames(t, env.memberSession)
	if len(memberTools) != 6 {
		t.Fatalf("member tools = %v, want 6", memberTools)
	}
	if !contains(memberTools, "o1_get_alert_deliveries") {
		t.Fatalf("member tools = %v, missing o1_get_alert_deliveries", memberTools)
	}

	viewerTools := toolNames(t, env.viewerSession)
	if len(viewerTools) != 5 {
		t.Fatalf("viewer tools = %v, want 5", viewerTools)
	}
	if contains(viewerTools, "o1_get_alert_deliveries") {
		t.Fatalf("viewer tools = %v, must not contain o1_get_alert_deliveries", viewerTools)
	}

	// A viewer that invokes the delivery tool directly gets an error and no data.
	result, err := env.viewerSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "o1_get_alert_deliveries",
		Arguments: map[string]any{"incident_id": env.incidentID.String()},
	})
	if err == nil && result != nil && !result.IsError {
		t.Fatalf("viewer direct delivery call succeeded: %s", resultText(t, result))
	}
}

func TestMCP_RejectsUnauthenticatedAndCrossTenant(t *testing.T) {
	env := newMCPEnv(t)
	ctx := context.Background()

	noAuth := postMCP(t, env.server.URL, "", "")
	if noAuth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no auth status = %d, want 401", noAuth.StatusCode)
	}
	if got := noAuth.Header.Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("no auth WWW-Authenticate = %q, want Bearer", got)
	}

	bad := postMCP(t, env.server.URL, "o1_cccccccccccc_"+strings.Repeat("B", 43), "")
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad key status = %d, want 401", bad.StatusCode)
	}

	disallowed := postMCP(t, env.server.URL, env.memberRaw, "https://evil.example")
	if disallowed.StatusCode != http.StatusForbidden {
		t.Fatalf("bad origin status = %d, want 403", disallowed.StatusCode)
	}

	// Another workspace's incident is a bounded not_found tool error, never data.
	result, err := env.memberSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "o1_get_incident",
		Arguments: map[string]any{"incident_id": env.otherID.String()},
	})
	if err != nil {
		t.Fatalf("cross-tenant call transport error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("cross-tenant result = %s, want IsError", resultText(t, result))
	}
	text := resultText(t, result)
	if !strings.Contains(text, `"error_code":"not_found"`) {
		t.Fatalf("cross-tenant result = %s, want not_found", text)
	}
	if strings.Contains(text, env.otherID.String()) {
		t.Fatalf("cross-tenant result leaks id: %s", text)
	}
}

func TestMCP_AuditsAndRedacts(t *testing.T) {
	env := newMCPEnv(t)
	ctx := context.Background()

	listResult, err := env.memberSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "o1_list_monitors",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("list monitors: %v", err)
	}
	listText := resultText(t, listResult)
	if listResult.IsError {
		t.Fatalf("list monitors error: %s", listText)
	}
	if strings.Contains(listText, monitorURLToken) || strings.Contains(listText, `"url"`) {
		t.Fatalf("monitor list leaked the URL: %s", listText)
	}

	getResult, err := env.memberSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "o1_get_monitor",
		Arguments: map[string]any{"monitor_id": env.monitorID.String()},
	})
	if err != nil {
		t.Fatalf("get monitor: %v", err)
	}
	getText := resultText(t, getResult)
	if getResult.IsError {
		t.Fatalf("get monitor error: %s", getText)
	}
	if strings.Contains(getText, monitorURLToken) {
		t.Fatalf("monitor detail leaked the URL: %s", getText)
	}

	deliveries, err := env.memberSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "o1_get_alert_deliveries",
		Arguments: map[string]any{"incident_id": env.incidentID.String()},
	})
	if err != nil {
		t.Fatalf("get alert deliveries: %v", err)
	}
	deliveryText := resultText(t, deliveries)
	if deliveries.IsError {
		t.Fatalf("get alert deliveries error: %s", deliveryText)
	}
	for _, forbidden := range []string{recipient, channelName, deliveryError, "recipient", "channel_name", "last_error"} {
		if strings.Contains(deliveryText, forbidden) {
			t.Fatalf("delivery result leaked %q: %s", forbidden, deliveryText)
		}
	}
	if !strings.Contains(deliveryText, `"channel_kind"`) {
		t.Fatalf("delivery result missing channel_kind: %s", deliveryText)
	}

	// One tools/call writes a bounded audit event tied to the key, not the secret.
	var kind string
	var detail []byte
	if err := env.pool.QueryRow(ctx,
		`SELECT kind, detail FROM security_events WHERE actor_api_key_id = $1 AND kind = 'mcp.tool_called' ORDER BY created_at DESC LIMIT 1`,
		env.memberKeyID,
	).Scan(&kind, &detail); err != nil {
		t.Fatalf("load audit event: %v", err)
	}
	if kind != "mcp.tool_called" {
		t.Fatalf("audit kind = %q", kind)
	}
	if !strings.Contains(string(detail), `"o1_get_alert_deliveries"`) {
		t.Fatalf("audit detail = %s, want tool name", detail)
	}
	for _, forbidden := range []string{"o1_aaaaaaaaaaaa_", strings.Repeat("A", 43), recipient} {
		if strings.Contains(string(detail), forbidden) {
			t.Fatalf("audit detail leaked %q: %s", forbidden, detail)
		}
	}

	// The metrics surface records the call with bounded tool/status labels.
	rec := httptest.NewRecorder()
	env.metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `o1_mcp_tool_calls_total{status="ok",tool="o1_get_alert_deliveries"} 1`) {
		t.Fatalf("metrics missing MCP tool counter:\n%s", body)
	}
}

func postMCP(t *testing.T, endpoint, token, origin string) *http.Response {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

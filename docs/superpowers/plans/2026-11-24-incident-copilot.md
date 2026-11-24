# Incident Copilot and MCP Server Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a read-only incident investigator to o1 and expose the same six read-only tools to external AI clients through an authenticated MCP server.

**Architecture:** One tool layer (`internal/copilot/tools`) reads incidents, monitors, checks, and delivery outcomes through existing services. The internal agent calls it in-process; an MCP adapter (`internal/mcpserver`) exposes it over stateless Streamable HTTP. Investigations run as River jobs; the API returns a stored row and the UI polls it. Every claim in a final answer must resolve to a recorded tool result.

**Tech Stack:** Go 1.26.11, PostgreSQL 16, River v0.51.0, sqlc 1.31, the official MCP Go SDK v1.7.0 (`github.com/modelcontextprotocol/go-sdk`), React 19, TanStack Query, Vitest, MSW, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-24-incident-copilot-design.md`

## Global Constraints

- Both features are off by default: `O1_MCP_ENABLED=false`, `O1_COPILOT_ENABLED=false`. With both off, no route, job, UI panel, or model call changes behaviour.
- Copilot caps: 5 model calls, 6 tool calls, 90s overall, 30s per provider call, 8 KB per tool result, 1,200 output tokens per call, 40,000 input tokens per investigation, 3 investigations per incident per hour, 20 per workspace per day.
- MCP caps: 60 tool calls per minute per key, burst 10.
- Roles: viewer sessions and viewer API keys get five tools (no `o1_get_alert_deliveries`); member/admin get six. Starting an investigation is a session action for member or above. API keys never start investigations.
- Workspace always comes from the session or API key. No tool accepts `workspace_id`. Cross-tenant IDs return `not_found`.
- No write tools. No outbound HTTP from the agent except the existing model provider call.
- Tools never expose monitor URL, headers, body, query, recipients, channel names, user emails, sessions, channel secrets, raw target errors, or delivery error text.
- MCP protocol versions: `2026-07-28` and `2025-11-25`, stateless Streamable HTTP, JSON responses. Authentication is a static `Authorization: Bearer o1_<prefix>_<secret>` API key; OAuth discovery is unsupported.
- Every task ends with a passing test run and a commit. Conventional commit messages.

## Review Focus

These are the failure modes most likely to make the feature unsafe or untrustworthy. Each has a test in the task named.

1. **Cross-tenant IDs in a tool call.** A monitor or incident UUID from another workspace must return `not_found`, never data. Tested in Tasks 4, 5, and 11.
2. **A fabricated or failed citation.** A hypothesis citing a missing tool call, a failed tool call, a bad JSON Pointer, or a mismatched value must be dropped and marked unverified. Tested in Tasks 6 and 7.
3. **Truncation breaking a citation.** A result over 8 KB must be structurally truncated so every stored JSON Pointer still resolves. Tested in Tasks 5 and 6.
4. **Retry and stale state.** A transient provider error must keep the row `running` until the final River attempt; a stale row must become `failed`/`stale`; a completed row must never be completed twice. Tested in Tasks 1, 8, and 9.
5. **Viewer role boundaries.** A viewer key must not see delivery outcomes or start an investigation, while still reading the five viewer-safe tools and a stored result. Tested in Tasks 4, 5, 10, and 11.

---

## File Structure

**Backend, new**
- `backend/internal/db/migrations/00013_incident_investigations.sql` — table, indexes, partial unique active row.
- `backend/internal/db/migrations/00014_investigation_events.sql` — widen the `incident_events.kind` check with `investigation_completed` (the original check was inline).
- `backend/internal/db/queries/investigations.sql` — investigation CRUD and caps.
- `backend/internal/copilot/types.go` — statuses, result, evidence, trace, investigation.
- `backend/internal/copilot/store.go` — Postgres store and generated-query mapping.
- `backend/internal/copilot/seed.go` — redacted seed built on the shared evidence projections, plus its hash.
- `backend/internal/copilot/prompt.go` — prompt version and system/user prompt text.
- `backend/internal/copilot/parse.go` — final-answer JSON parsing and limits.
- `backend/internal/copilot/verify.go` — JSON Pointer resolution and citation validation.
- `backend/internal/copilot/runner.go` — bounded tool-calling loop.
- `backend/internal/copilot/service.go` — start, run, retry, complete, timeline event.
- `backend/internal/copilot/handlers.go` — HTTP handlers and public response mapping.
- `backend/internal/evidence/projections.go` — the one allowlisted projection set shared by insights, the seed, and tools, plus failure-class derivation.
- `backend/internal/copilot/tools/definitions.go` — six tool specs and JSON schemas.
- `backend/internal/copilot/tools/registry.go` — role checks, argument validation, dispatch, truncation.
- `backend/internal/mcpserver/server.go` — SDK server and per-key tool registration.
- `backend/internal/mcpserver/auth.go` — bearer-key middleware, Origin check, rate limit.
- `backend/internal/worker/investigation_worker.go` — River worker wrapper.
- `backend/internal/jobs/investigation.go` — job payload.
- `backend/cmd/copilot-eval/main.go` — opt-in live-provider evaluation.
- `backend/internal/copilot/testdata/eval/*.json` — evaluation fixtures.

**Backend, modified**
- `backend/internal/db/sqlc/*` — regenerated.
- `backend/internal/ai/errors.go`, `ai/tools.go`, `ai/openai_tools.go`, `ai/stub_tools.go` — tool-calling transport; `ai/openai.go` error type.
- `backend/internal/insights/payload.go` — built on the shared evidence projections.
- `backend/internal/auth/session.go` — exported `ContextWithPrincipal`.
- `backend/internal/incidents/deliveries.go` — redacted delivery outcomes for members.
- `backend/internal/incidents/service.go` — timeline event helper.
- `backend/internal/api/router.go` — dependencies, routes, `/mcp`.
- `backend/internal/config/config.go` — switches and caps.
- `backend/cmd/api/main.go` — insert-only River client and MCP handler.
- `backend/cmd/worker/main.go` — copilot service, investigation worker, `ai` queue.
- `backend/internal/scheduler/scheduler.go` — stale-row sweep.
- `backend/go.mod`, `go.sum` — MCP SDK.

**Frontend, new**
- `web/src/features/incidents/IncidentInvestigationPanel.tsx` — the panel.
- `web/src/features/incidents/investigationHooks.ts` — latest query, polling, start mutation.
- `web/src/features/api-keys/MCPCard.tsx` — endpoint and client setup card.

**Frontend, modified**
- `web/src/lib/api/types.ts`, `web/src/lib/query/keys.ts`, `web/src/features/incidents/api.ts`, `web/src/features/incidents/IncidentDetailScreen.tsx`, `web/src/features/api-keys/APIKeysScreen.tsx`, `web/src/lib/roles.ts`.

**Docs and operations**
- `docs/mcp.md`, `docs/ai/copilot.md`, `docs/ai/copilot-eval.md`, `docs/security/threat-model.md`, `README.md`.
- `compose.yaml`, `web/Caddyfile`, `.github/workflows/ci.yml`, `Makefile`, `web/tests/e2e/smoke.spec.ts`.

---

### Task 1: Investigation schema, queries, and Postgres store

**Files:**
- Create: `backend/internal/db/migrations/00013_incident_investigations.sql`
- Create: `backend/internal/db/queries/investigations.sql`
- Regenerate: `backend/internal/db/sqlc/models.go`, `backend/internal/db/sqlc/investigations.sql.go`
- Create: `backend/internal/copilot/types.go`
- Create: `backend/internal/copilot/store.go`
- Test: `backend/internal/copilot/store_test.go`

**Interfaces:**
- Consumes: `sqlc.Queries`, `pgxpool.Pool`, `testutil.NewPostgres`.
- Produces:
  - `type Status string` with `StatusQueued`, `StatusRunning`, `StatusOK`, `StatusInsufficient`, `StatusIncomplete`, `StatusFailed`.
  - `type Evidence struct { ToolCallID string; Pointer string; Value json.RawMessage; Note string }`
  - `type Hypothesis struct { Rank int; Cause string; Confidence string; Evidence []Evidence; FalsifiedBy string; Unverified bool }`
  - `type Result struct { Sufficient bool; Assessment string; Hypotheses []Hypothesis; NextChecks []string }`
  - `type Step struct { Index int; Tool string; ArgsSummary string; Status string; DurationMS int }`
  - `type ToolCallRecord struct { ID string; Name string; ArgsSummary string; ArgsHash string; Result json.RawMessage; IsError bool; DurationMS int }`
  - `type ModelCallRecord struct { Index int; FinishReason string; InputTokens, OutputTokens, DurationMS int }`
  - `type Trace struct { Steps []Step; Calls []ToolCallRecord; ModelCalls []ModelCallRecord }`
  - `type Investigation struct { ID, WorkspaceID, IncidentID uuid.UUID; Status Status; Model, PromptVersion, SeedHash string; Result *Result; Trace Trace; Attempts, ToolCalls, InputTokens, OutputTokens, DurationMS int; ErrorCode string; CreatedBy *uuid.UUID; CreatedAt time.Time; CompletedAt *time.Time }`
  - `type Store struct`; `func NewPostgresStore(pool *pgxpool.Pool) *Store`.
  - `func (s *Store) Create(ctx context.Context, inv Investigation) (Investigation, error)` — begins a transaction and calls `CreateTx`.
  - `func (s *Store) CreateTx(ctx context.Context, tx pgx.Tx, inv Investigation) (Investigation, error)` — the atomic path Task 10 uses to insert the row and the River job in one transaction.
  - `func (s *Store) Get(ctx context.Context, workspaceID, incidentID, id uuid.UUID) (Investigation, error)`
  - `func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (Investigation, error)`
  - `func (s *Store) Latest(ctx context.Context, workspaceID, incidentID uuid.UUID) (*Investigation, error)`
  - `func (s *Store) MarkRunning(ctx context.Context, id uuid.UUID, attempt int, seedHash string) error`
  - `func (s *Store) SaveProgress(ctx context.Context, id uuid.UUID, trace Trace, attempts, toolCalls, inputTokens, outputTokens int) error`
  - `func (s *Store) Complete(ctx context.Context, inv Investigation) error`
  - `func (s *Store) ReapStale(ctx context.Context, incidentID uuid.UUID, before time.Time) (int64, error)`
  - `func (s *Store) ReapAllStale(ctx context.Context, before time.Time) (int64, error)`
  - `func (s *Store) CountForIncidentSince(ctx context.Context, incidentID uuid.UUID, since time.Time) (int64, error)`
  - `func (s *Store) CountForWorkspaceSince(ctx context.Context, workspaceID uuid.UUID, since time.Time) (int64, error)`
  - Errors `ErrNotFound`, `ErrActive`, `ErrNotActive` — the last one is returned by `MarkRunning`, `SaveProgress`, and `Complete` when the row is no longer `queued`/`running` (for example after the stale reaper won the race).

- [ ] **Step 1: Write the migration and queries**

Create `00013_incident_investigations.sql` exactly as the spec's Storage section, plus `seed_hash text NOT NULL DEFAULT ''`. Add these named queries in `investigations.sql`:

```sql
-- name: CreateInvestigation :one
INSERT INTO incident_investigations (
  id, workspace_id, incident_id, status, model, prompt_version, seed_hash,
  result, trace, attempts, tool_calls, input_tokens, output_tokens,
  duration_ms, error_code, created_by, created_at, completed_at
) VALUES (
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18
) RETURNING *;

-- name: GetInvestigation :one
SELECT * FROM incident_investigations
WHERE id=$1 AND workspace_id=$2 AND incident_id=$3;

-- name: GetInvestigationByID :one
SELECT * FROM incident_investigations WHERE id=$1;

-- name: GetLatestInvestigation :one
SELECT * FROM incident_investigations
WHERE workspace_id=$1 AND incident_id=$2
ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: MarkInvestigationRunning :execrows
UPDATE incident_investigations
SET status='running', attempts=$2, seed_hash=$3, completed_at=NULL
WHERE id=$1 AND status IN ('queued','running');

-- name: SaveInvestigationProgress :execrows
UPDATE incident_investigations
SET trace=$2, attempts=$3, tool_calls=$4, input_tokens=$5, output_tokens=$6
WHERE id=$1 AND status='running';

-- name: CompleteInvestigation :execrows
UPDATE incident_investigations
SET status=$2, result=$3, trace=$4, duration_ms=$5, error_code=$6,
    input_tokens=$7, output_tokens=$8, tool_calls=$9, completed_at=now()
WHERE id=$1 AND status IN ('queued','running');

-- name: ReapStaleInvestigations :execrows
UPDATE incident_investigations
SET status='failed', error_code='stale', completed_at=now()
WHERE incident_id=$1 AND status IN ('queued','running') AND created_at < $2;

-- name: ReapAllStaleInvestigations :execrows
UPDATE incident_investigations
SET status='failed', error_code='stale', completed_at=now()
WHERE status IN ('queued','running') AND created_at < $1;

-- name: CountInvestigationsForIncidentSince :one
SELECT count(*) FROM incident_investigations
WHERE incident_id=$1 AND created_at >= $2;

-- name: CountInvestigationsForWorkspaceSince :one
SELECT count(*) FROM incident_investigations
WHERE workspace_id=$1 AND created_at >= $2;
```

- [ ] **Step 2: Generate sqlc and inspect the generated names**

Run: `cd backend && sqlc generate`
Expected: `sqlc.IncidentInvestigation`, `sqlc.CreateInvestigationParams`, and the row structs used below compile.

- [ ] **Step 3: Write the failing store test**

```go
func TestPostgresStore_CreateAndLatest(t *testing.T) {
    pool, _ := testutil.NewPostgres(t)
    ws, inc := seedWorkspaceIncident(t, pool)
    s := NewPostgresStore(pool)
    created, err := s.Create(context.Background(), Investigation{
        ID: uuid.New(), WorkspaceID: ws, IncidentID: inc,
        Status: StatusQueued, Model: "stub", PromptVersion: "test",
        CreatedAt: time.Now().UTC(),
    })
    if err != nil { t.Fatal(err) }
    got, err := s.Latest(context.Background(), ws, inc)
    if err != nil || got == nil || got.ID != created.ID { t.Fatalf("latest = %+v, %v", got, err) }
}

func TestPostgresStore_OneActiveAndReapStale(t *testing.T) {
    // Create a queued row, expect a second Create to return ErrActive.
    // Create an old running row directly, call ReapStale with now+6m,
    // expect it to become failed/stale; a fresh row stays running.
}

func TestPostgresStore_GetIsWorkspaceScoped(t *testing.T) {
    // Create in ws A; Get from ws B returns ErrNotFound.
}

func TestPostgresStore_StaleRowCannotBeRevived(t *testing.T) {
    // Create a running row with created_at 10 minutes ago; ReapStale -> failed/stale.
    // MarkRunning, SaveProgress, and Complete each return ErrNotActive.
    // Reload and assert status=failed, error_code=stale, completed_at non-null.
}

func TestPostgresStore_CreateTxRollsBack(t *testing.T) {
    // Begin a transaction, CreateTx, rollback; Latest returns nil.
    // Then Create succeeds and Latest returns the row.
}
```

Define `seedWorkspaceIncident(t, pool)` in the same test file: insert a user, workspace, monitor, monitor_state, and incident with `gen_random_uuid()` where the schema allows; return their ids. Use raw SQL like `backend/internal/worker/alert_worker_test.go` does.

- [ ] **Step 4: Run the test to verify it fails**

Run: `cd backend && go test ./internal/copilot -run TestPostgresStore -v`
Expected: FAIL — `NewPostgresStore` undefined.

- [ ] **Step 5: Implement types and the store**

Map each generated row to `Investigation` with `json.Unmarshal` of `trace` and `result`; marshal on write. `CreateTx` runs `CreateInvestigation` on the caller's transaction and maps PostgreSQL unique violation `incident_investigations_one_active_idx` to `ErrActive`; `Create` begins a transaction, calls `CreateTx`, and commits. `MarkRunning`, `SaveProgress`, and `Complete` use the `:execrows` queries and return `ErrNotActive` when zero rows match, so a stale row cannot be revived by its old worker. `Latest` returns `nil, nil` on `pgx.ErrNoRows`; `Get` returns `ErrNotFound`. The roundtrip test includes `Trace.ModelCalls` and `ToolCallRecord.ArgsHash`.

- [ ] **Step 6: Run the test to verify it passes**

Run: `cd backend && go test ./internal/copilot -run TestPostgresStore -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/db backend/internal/copilot
git commit -m "feat: store incident investigations"
```

---

### Task 2: Tool-calling model transport

**Files:**
- Create: `backend/internal/ai/tools.go`
- Create: `backend/internal/ai/errors.go`
- Create: `backend/internal/ai/openai_tools.go`
- Create: `backend/internal/ai/stub_tools.go`
- Modify: `backend/internal/ai/openai.go`
- Test: `backend/internal/ai/tools_test.go`

**Interfaces:**
- Produces:
  - `type ToolDefinition struct { Name string; Description string; Parameters json.RawMessage }`
  - `type ChatRole string`; `ChatRoleSystem`, `ChatRoleUser`, `ChatRoleAssistant`, `ChatRoleTool`.
  - `type ChatMessage struct { Role ChatRole; Content string; ToolCallID string; ToolCalls []ToolCall }`
  - `type ToolCall struct { ID string; Name string; Arguments json.RawMessage }`
  - `type ChatRequest struct { Model string; Messages []ChatMessage; Tools []ToolDefinition; MaxTokens int }`
  - `type ChatResponse struct { Text string; ToolCalls []ToolCall; Model string; InputTokens, OutputTokens int; FinishReason string }`
  - `type ToolProvider interface { Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) }`
  - `type HTTPError struct { StatusCode int; Body string }` with `Error() string`.
  - `func IsTransient(err error) bool` — true for network errors, 408, 425, 429, 5xx, and `context.DeadlineExceeded`; false for other 4xx and `context.Canceled`.
  - `(*OpenAIClient).Chat(ctx, ChatRequest) (ChatResponse, error)`.
  - `type Stub struct { Reply string; Err error; ChatScript []ChatResponse; ... }`; `(*Stub).Chat` returns the next script entry. With no script it runs the deterministic demo trajectory: call `o1_get_recent_checks` for `seed.monitor.id`, then cite the first `down` check's `/data/checks/{i}/status`; with no down check it returns an insufficient answer.

- [ ] **Step 1: Write the failing transport tests**

```go
func TestOpenAIChat_EncodesToolsAndParsesToolCalls(t *testing.T) {
    var body map[string]any
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        json.NewDecoder(r.Body).Decode(&body)
        io.WriteString(w, `{"model":"m","choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"o1_get_incident","arguments":"{\"incident_id\":\"i\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`)
    }))
    c := &OpenAIClient{HTTP: srv.Client(), BaseURL: srv.URL, APIKey: "k"}
    res, err := c.Chat(context.Background(), ChatRequest{
        Model: "m", MaxTokens: 100,
        Messages: []ChatMessage{{Role: ChatRoleUser, Content: "hi"}},
        Tools: []ToolDefinition{{Name: "o1_get_incident", Parameters: json.RawMessage(`{"type":"object"}`)}},
    })
    if err != nil || len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "call_1" { t.Fatalf("%+v %v", res, err) }
    // Assert body["tools"].([]any)[0] has type "function" and tool_choice "auto".
}

func TestIsTransient(t *testing.T) {
    cases := []struct{ err error; want bool }{
        {&HTTPError{StatusCode: 500}, true},
        {&HTTPError{StatusCode: 429}, true},
        {&HTTPError{StatusCode: 400}, false},
        {context.DeadlineExceeded, true},
        {context.Canceled, false},
    }
    // assert each
}

func TestStubChat_DefaultDemoTrajectory(t *testing.T) {
    stub := &Stub{}
    first, err := stub.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{
        {Role: ChatRoleUser, Content: `{"seed":{"monitor":{"id":"monitor-1"}},"instruction":"investigate"}`},
    }})
    if err != nil || len(first.ToolCalls) != 1 || first.ToolCalls[0].Name != "o1_get_recent_checks" {
        t.Fatalf("first = %+v, %v", first, err)
    }
    second, err := stub.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{
        {Role: ChatRoleAssistant, ToolCalls: first.ToolCalls},
        {Role: ChatRoleTool, ToolCallID: first.ToolCalls[0].ID, Content: `{"data":{"checks":[{"status":"up"},{"status":"down"}]},"meta":{}}`},
    }})
    if err != nil || !strings.Contains(second.Text, `"/data/checks/1/status"`) || !strings.Contains(second.Text, `"down"`) {
        t.Fatalf("second = %+v, %v", second, err)
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/ai -run 'TestOpenAIChat|TestIsTransient|TestStubChat' -v`
Expected: FAIL — `Chat` undefined.

- [ ] **Step 3: Implement the types, OpenAI `Chat`, and error classification**

`Chat` sends `tools:[{type:"function",function:{name,description,parameters}}]`, `tool_choice:"auto"`, and messages with `tool_calls` / `tool_call_id`. It does not set `response_format` for tool-calling turns. Parse `choices[0].message.tool_calls[].function.arguments` as raw JSON. Return `*HTTPError` for non-2xx; keep `Complete`'s existing error text unchanged.

- [ ] **Step 4: Implement the stub script and demo trajectory**

`Stub.Chat` records the request like `Complete` does and returns the next `ChatScript` entry. With no script: parse the last user message as `{"seed":{"monitor":{"id":"..."}}}`; if there is no tool message yet, return one tool call `o1_get_recent_checks` with `{"monitor_id":"<id>","hours":168}` and ID `stub-checks-1`. If a tool message exists, parse its envelope, find the first check with `status=="down"`, and return a final JSON answer citing that check's `/data/checks/{i}/status` with value `"down"` and the assistant tool-call ID. If there is no down check or no monitor id, return `{"sufficient":false,"assessment":"No failing check was found in the last 24 hours.","hypotheses":[]}`.

- [ ] **Step 5: Run the tests**

Run: `cd backend && go test ./internal/ai -v`
Expected: PASS, including the existing `ai_test.go`.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/ai
git commit -m "feat: add tool-calling model transport"
```

---

### Task 3: Shared evidence projections and failure classes

**Files:**
- Create: `backend/internal/evidence/projections.go`
- Modify: `backend/internal/insights/payload.go`
- Test: `backend/internal/evidence/projections_test.go`
- Test: `backend/internal/insights/insights_test.go` — keep the existing leak tests passing.

**Interfaces:**
- Produces:
  - `type MonitorView struct { ID uuid.UUID; Name, Method, ExpectedStatus string; IntervalSeconds, TimeoutSeconds, ConfirmationThreshold, RecoveryThreshold int; Paused bool; Status string; ActiveIncidentID *uuid.UUID; LastCheckAt *time.Time }`
  - `type IncidentView struct { ID, MonitorID uuid.UUID; MonitorName, Status string; StartedAt time.Time; ResolvedAt *time.Time; DurationMinutes, ReminderCount int }`
  - `type SeedEventView struct { Kind string; At time.Time }` — no id and no free text.
  - `type EventView struct { ID uuid.UUID; Kind string; At time.Time; Detail map[string]string }` — used by `o1_get_incident` and insights; `Detail` keeps only the allowlisted keys.
  - `type CheckView struct { CheckedAt time.Time; Status string; StatusCode *int; LatencyMS int; FailureClass string }`
  - `type ChecksView struct { WindowHours int; Checks []CheckView; Count, Failures int; P95LatencyMS float64; FailureClasses map[string]int }`
  - `type DeliveryView struct { ChannelKind, Event, Status string; Attempts int; SentAt *time.Time; CreatedAt time.Time }`
  - `func FailureClass(raw *string) string` — bounded mapping from the probe error prefix.
  - `func Monitor(m monitors.Monitor) MonitorView`
  - `func Incident(d incidents.IncidentDetail) IncidentView`
  - `func SeedTimeline(d incidents.IncidentDetail) []SeedEventView`
  - `func Timeline(d incidents.IncidentDetail) []EventView` — allowlist detail keys `previous_status`, `new_status`, `previous`, `next`, `from`, `to`, `reason`, `status`, `sequence`; values are strings, truncated to 80 chars.
  - `func Checks(results []monitors.ProbeResult, summary monitors.Summary, hours int) ChecksView`
  - `func Delivery(d incidents.DeliveryOutcome) DeliveryView`

- [ ] **Step 1: Write the failing redaction tests**

```go
func TestProjections_DoNotLeakSecrets(t *testing.T) {
    m := monitors.Monitor{Name: "api", URL: "https://user:pass@example.com/health?token=secret", Headers: map[string]string{"Authorization": "Bearer secret"}}
    inc := incidents.IncidentDetail{Incident: incidents.Incident{MonitorName: "api"}}
    checks := Checks([]monitors.ProbeResult{{Status: "down", Error: strPtr("timeout: dial tcp 10.0.0.1:443")}}, monitors.Summary{Checks: 1, Down: 1}, 24)
    blob, _ := json.Marshal([]any{Monitor(m), Incident(inc), SeedTimeline(inc), Timeline(inc), checks})
    for _, secret := range []string{"https://", "Authorization", "Bearer", "user:pass", "token=secret", "10.0.0.1"} {
        if strings.Contains(string(blob), secret) { t.Fatalf("leaked %q: %s", secret, blob) }
    }
}

func TestFailureClass(t *testing.T) {
    cases := map[string]string{
        "timeout: dial tcp": "timeout", "dns": "dns", "ssrf_blocked: private ip": "ssrf_blocked",
        "probe failed": "unknown", "": "unknown",
    }
    // assert each
}

func TestChecks_UsesFullWindowSummary(t *testing.T) {
    // Pass one result but a summary with Checks=10, Down=4, P95=1200.
    // Expect ChecksView.Count=10, Failures=4, P95=1200; FailureClasses from the one result.
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/evidence -run 'TestProjections|TestFailureClass' -v`
Expected: FAIL — package/functions undefined.

- [ ] **Step 3: Implement the projections**

Map only the fields named in the spec's tool table. `FailureClass` lowercases, splits at the first `:`, and accepts `timeout`, `dns`, `tls`, `connection`, `redirect`, `status`, `ssrf_blocked`; everything else is `unknown`. `Checks` takes `Count`, `Failures`, and `P95LatencyMS` from `summary` (the full requested window) and computes `FailureClasses` from the returned result slice; it never copies `ProbeResult.Error`. `Timeline` drops detail keys outside the allowlist. The allowlist includes the real `previous_status`/`new_status` keys so an opened/resolved event does not project to an empty detail.

- [ ] **Step 4: Move insights onto the shared projections**

Replace the field-by-field copies in `insights/payload.go` with calls to `evidence.Monitor`, `evidence.Incident`, `evidence.Timeline`, and `evidence.Delivery`; map into the existing `insights` wire structs so the response shape and event IDs do not change. The allowlist may drop unknown detail keys; update the insights test to assert that an unknown key is absent rather than raw detail being preserved.

- [ ] **Step 5: Run both test packages**

Run: `cd backend && go test ./internal/evidence ./internal/insights -v`
Expected: PASS, including the existing leak test.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/evidence backend/internal/insights
git commit -m "feat: share the evidence projection boundary"
```

---

### Task 4: Redacted delivery outcomes for members

**Files:**
- Modify: `backend/internal/db/queries/incidents.sql`
- Regenerate: `backend/internal/db/sqlc/incidents.sql.go`
- Create: `backend/internal/incidents/deliveries.go`
- Test: `backend/internal/incidents/deliveries_test.go`

**Interfaces:**
- Produces:
  - `func (s *Service) DeliveryOutcomes(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) ([]DeliveryOutcome, error)` — requires `RoleMember`; cross-workspace is `ErrNotFound`.
  - `DeliveryOutcome` already exists from Task 3 (introduced so `evidence.Delivery` could compile); do not redeclare it. If its fields differ from `{ChannelKind, Event, Status, Attempts, SentAt, CreatedAt}`, extend it here and keep the evidence caller compiling.

- [ ] **Step 1: Add the query and failing tests**

Add to `incidents.sql`:

```sql
-- name: ListIncidentDeliveryOutcomes :many
SELECT c.kind AS channel_kind, d.event, d.status, d.attempts, d.sent_at, d.created_at
FROM alert_deliveries d
JOIN alert_channels c ON c.id = d.channel_id
WHERE d.incident_id=$1
ORDER BY d.created_at, d.id;
```

Test:

```go
func TestDeliveryOutcomes_MemberOnlyAndRedacted(t *testing.T) {
    // viewer -> errors.Is(err, workspaces.ErrForbidden)
    // member -> rows; marshal rows and assert no recipient, channel name, or last_error
    // other workspace -> ErrNotFound
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd backend && go test ./internal/incidents -run TestDeliveryOutcomes -v`
Expected: FAIL — `DeliveryOutcomes` undefined.

- [ ] **Step 3: Implement the service method**

Authorize `RoleMember`, verify the incident belongs to the workspace with `GetIncident`, call `ListIncidentDeliveryOutcomes`, map only the safe fields.

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd backend && go test ./internal/incidents -run TestDeliveryOutcomes -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/db backend/internal/incidents
git commit -m "feat: read redacted delivery outcomes for members"
```

---

### Task 5: The six-tool registry

**Files:**
- Create: `backend/internal/copilot/tools/definitions.go`
- Create: `backend/internal/copilot/tools/registry.go`
- Modify: `backend/internal/auth/session.go` — add `ContextWithPrincipal` so tests and later tasks use the real principal context, not a test seam.
- Test: `backend/internal/copilot/tools/registry_test.go`

**Interfaces:**
- Consumes: `incidents.Service`, `monitors.Service`, `workspaces.Authorize`, `auth.PrincipalFrom`, `internal/evidence`, Task 4 `DeliveryOutcomes`.
- Produces:
  - `type Spec struct { Name, Description string; InputSchema json.RawMessage; MinRole workspaces.Role }`
  - `func Specs() []Spec` — the six tools in spec order.
  - `type Meta struct { AsOf time.Time; Count, Returned, Total int; Truncated bool }`
  - `type Result struct { Data any; Meta Meta; IsError bool; ErrorCode string }`
  - `type IncidentSource interface { Get(ctx, actorID, workspaceID, incidentID uuid.UUID) (incidents.IncidentDetail, error); List(ctx, actorID, workspaceID uuid.UUID, filter incidents.IncidentFilter, cursor *httpx.PageCursor, limit int) ([]incidents.Incident, error); DeliveryOutcomes(ctx, actorID, workspaceID, incidentID uuid.UUID) ([]incidents.DeliveryOutcome, error) }`
  - `type MonitorSource interface { Get(ctx, actorID, workspaceID, monitorID uuid.UUID) (monitors.Monitor, error); List(ctx, actorID, workspaceID uuid.UUID, cursor *monitors.Cursor, limit int) ([]monitors.Monitor, error); Results(ctx, actorID, workspaceID, monitorID uuid.UUID, window time.Duration, cursor *monitors.Cursor, limit int) ([]monitors.ProbeResult, error); Summary(ctx, actorID, workspaceID, monitorID uuid.UUID, window time.Duration) (monitors.Summary, error) }`
  - `type Registry struct`; `func NewRegistry(incidents IncidentSource, monitors MonitorSource, now func() time.Time) *Registry`.
  - `func (r *Registry) Specs() []Spec`
  - `func (r *Registry) Call(ctx context.Context, workspaceID uuid.UUID, name string, args json.RawMessage) (Result, error)`
  - `func auth.ContextWithPrincipal(ctx context.Context, p auth.Principal) context.Context` — reuses the existing `principalKey{}` key; the registry tests and Task 8 both use it.

- [ ] **Step 1: Write the failing registry tests**

```go
func TestSpecs_AreSixAndValidJSON(t *testing.T) {
    specs := Specs()
    if len(specs) != 6 { t.Fatalf("specs = %d", len(specs)) }
    for _, s := range specs {
        var schema map[string]any
        if err := json.Unmarshal(s.InputSchema, &schema); err != nil { t.Fatalf("%s: %v", s.Name, err) }
        if s.Name == "o1_get_alert_deliveries" && s.MinRole != workspaces.RoleMember {
            t.Fatal("deliveries must be member-only")
        }
    }
}

func TestCall_RejectsUnknownAndCrossTenant(t *testing.T) {
    // Unknown tool name -> ErrUnknownTool.
    // Fake incident source returns incidents.ErrNotFound for a cross-workspace id;
    // Call -> Result{IsError:true, ErrorCode:"not_found"}.
    // Viewer context calling o1_get_alert_deliveries -> ErrorCode "forbidden".
}

func TestCall_TruncatesStructurally(t *testing.T) {
    // Build 100 checks; Call o1_get_recent_checks; marshal Data;
    // assert len <= 8192, Meta.Truncated true, Meta.Total 100, and a JSON Pointer
    // into Data/checks/0 still resolves.
}

func TestCall_ArgumentMappingAndSuccess(t *testing.T) {
    // Fake sources capture their arguments. Assert:
    // o1_list_incidents passes filter.Status, filter.MonitorID, and the limit.
    // o1_get_monitor returns the projected monitor.
    // o1_get_alert_deliveries passes the incident id and returns member rows.
    // o1_get_recent_checks passes the 6h window and the result limit.
}

func TestCall_RejectsWorkspaceID(t *testing.T) {
    // o1_get_incident with {"incident_id":"...","workspace_id":"..."}
    // -> Result{IsError:true, ErrorCode:"invalid_argument"}.
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/copilot/tools -run 'TestSpecs|TestCall' -v`
Expected: FAIL — `Specs`, `NewRegistry` undefined.

- [ ] **Step 3: Implement definitions and dispatch**

Hand-write the six JSON schemas with `type:"object"`, `additionalProperties:false`, and required ID fields. `Call` looks up the spec, parses args with `json.Decoder.DisallowUnknownFields`, then dispatches. The tests build the actor context with the real `auth.ContextWithPrincipal`, not a test-only seam. Do not add a separate authorizer: the source methods already enforce viewer for monitor/incident/check reads and member for `DeliveryOutcomes`; `Spec.MinRole` is the MCP tool-list filter, and a viewer that calls the delivery tool directly still gets `forbidden` from the service. For `o1_list_monitors`, fill `MonitorView.ActiveIncidentID` by listing the workspace's active incidents once and mapping by monitor id; do not add a new monitor-service method. Map `workspaces.ErrForbidden` to `forbidden`, `incidents.ErrNotFound`/`monitors.ErrNotFound` to `not_found`, bad UUID/limit to `invalid_argument`. Never return a raw service error to the model; return `Result{IsError:true, ErrorCode:"internal"}` and log at the caller.

- [ ] **Step 4: Implement truncation**

Build the typed view, marshal it, and if the envelope exceeds 8,192 bytes, halve the largest array (`checks`, `incidents`, `events`, or `deliveries`), set `Meta.Truncated`, and re-marshal until it fits. `Meta.Count` and aggregate values stay computed from the full window; `Meta.Returned` is the array length. The wrapper `{data, meta}` is never cut.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/copilot/tools -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/copilot/tools
git commit -m "feat: add the read-only copilot tool registry"
```

---

### Task 6: Seed, prompt, answer parsing, and citation verification

**Files:**
- Create: `backend/internal/copilot/seed.go`
- Create: `backend/internal/copilot/prompt.go`
- Create: `backend/internal/copilot/parse.go`
- Create: `backend/internal/copilot/verify.go`
- Test: `backend/internal/copilot/seed_test.go`, `parse_test.go`, `verify_test.go`

**Interfaces:**
- Consumes: `incidents.Service`, `monitors.Service`, `internal/evidence`, Task 1 `Result`/`Trace`.
- Produces:
  - `type Seed struct { Incident evidence.IncidentView; Monitor evidence.MonitorView; Timeline []evidence.SeedEventView; Deliveries []evidence.DeliveryView }`
  - `type IncidentSeedSource interface { Get(ctx, actorID, workspaceID, incidentID uuid.UUID) (incidents.IncidentDetail, error); DeliveryOutcomes(...) ([]incidents.DeliveryOutcome, error) }`
  - `type MonitorSeedSource interface { Get(ctx, actorID, workspaceID, monitorID uuid.UUID) (monitors.Monitor, error) }`
  - `func BuildSeed(ctx context.Context, incidents IncidentSeedSource, monitors MonitorSeedSource, actorID, workspaceID, incidentID uuid.UUID, now time.Time) (Seed, error)`
  - `func HashSeed(seed Seed, promptVersion string) string`
  - `const PromptVersion = "2026-11-24.1"`
  - `const SystemPrompt = "..."`
  - `func BuildUserMessage(seed Seed) (string, error)`
  - `func Parse(raw string) (Result, error)`
  - `func Verify(result Result, trace Trace) (Result, int)` — returns the sanitized result and the number of hypotheses with at least one verified evidence item.

- [ ] **Step 1: Write the failing seed and parse tests**

```go
func TestBuildSeed_HasNoEventIDsOrSecrets(t *testing.T) {
    // Fake sources return a monitor with URL/headers, an incident with events
    // whose detail contains "secret", and a delivery with a recipient.
    seed, err := BuildSeed(...)
    blob, _ := json.Marshal(seed)
    for _, s := range []string{"https://", "Authorization", "secret", "recipient@example.com"} {
        if strings.Contains(string(blob), s) { t.Fatalf("leaked %q", s) }
    }
    if strings.Contains(string(blob), "event_id") { t.Fatal("seed must not carry event ids") }
}

func TestParse_AcceptsFencedJSONAndCapsHypotheses(t *testing.T) {
    // Six hypotheses -> three; ranks 1..3; fenced JSON accepted; prose rejected.
}
```

- [ ] **Step 2: Write the failing verification tests**

```go
func TestVerify_CitationRules(t *testing.T) {
    trace := Trace{Calls: []ToolCallRecord{{ID: "call_1", Result: json.RawMessage(`{"data":{"checks":[{"failure_class":"timeout"}]},"meta":{}}`), IsError: false}}}
    cases := []struct{ name, ptr string; value string; wantVerified bool }{
        {"valid", "/data/checks/0/failure_class", `"timeout"`, true},
        {"bad pointer", "/data/checks/9/failure_class", `"timeout"`, false},
        {"wrong value", "/data/checks/0/failure_class", `"dns"`, false},
    }
    // unknown tool_call_id and a failed call also produce zero verified.
}

func TestVerify_AllUnverifiedIsInsufficient(t *testing.T) {
    // Result with one unverified hypothesis -> Verify returns 0.
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/copilot -run 'TestBuildSeed|TestParse|TestVerify' -v`
Expected: FAIL — functions undefined.

- [ ] **Step 4: Implement seed and prompt**

`BuildSeed` loads the incident, monitor, and delivery outcomes with the actor's context. If delivery outcomes return `ErrForbidden` or `ErrNotFound`, use an empty slice; the rest still works. The seed timeline uses `evidence.SeedTimeline` (kind + timestamp only). `BuildUserMessage` returns one JSON object: `{"seed": <seed>, "instruction": "Investigate this incident. Use the provided tools. Cite only tool results by tool_call_id."}` — the stub parses the same shape. `HashSeed` is SHA-256 of `{version, seed}` hex-encoded.

- [ ] **Step 5: Implement parse and verify**

Parse accepts a raw JSON object or a fenced ```json block. Enforce: at most 3 hypotheses, renumber ranks, confidence in `high|medium|low` else `low`, at most 5 evidence items per hypothesis, strings trimmed and capped (assessment 1200, cause 300, note 300, falsified 300, next_checks 3). A missing `sufficient` is malformed and returns an error; an explicit `sufficient:false` clears hypotheses. `Verify` resolves RFC 6901 pointers with `~0`/`~1` unescaping, compares decoded JSON values with `reflect.DeepEqual`, drops evidence for unknown or failed calls, marks hypotheses with no surviving evidence `Unverified`, and returns the verified count. After verification set `Sufficient = verifiedCount > 0`; when it is false, clear hypotheses so the stored shape can never say `sufficient:false` with a list of causes. A partially verified result keeps its hypotheses, with the unverified ones badged.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/copilot -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/copilot
git commit -m "feat: seed and verify copilot citations"
```

---

### Task 7: Bounded agent runner

**Files:**
- Create: `backend/internal/copilot/runner.go`
- Test: `backend/internal/copilot/runner_test.go`

**Interfaces:**
- Consumes: `ai.ToolProvider`, `tools.Registry`, Task 6 seed/parse/verify.
- Produces:
  - `type ToolCaller interface { Call(ctx context.Context, workspaceID uuid.UUID, name string, args json.RawMessage) (tools.Result, error) }`
  - `type Limits struct { MaxModelCalls, MaxToolCalls, MaxResultBytes, MaxOutputTokens, MaxInputTokens int; Timeout, CallTimeout time.Duration }`
  - `func DefaultLimits() Limits`
  - `type Outcome struct { Status Status; Result *Result; Trace Trace; InputTokens, OutputTokens, DurationMS int; ErrorCode string; Err error }`
  - `type Runner struct`; `func NewRunner(provider ai.ToolProvider, caller ToolCaller, model string, limits Limits, now func() time.Time) *Runner`.
  - `func (r *Runner) Run(ctx context.Context, workspaceID uuid.UUID, seed Seed) Outcome`.

- [ ] **Step 1: Write the failing runner tests**

```go
func TestRunner_CallsToolThenReturnsVerifiedAnswer(t *testing.T) {
    provider := &scriptedProvider{script: []ai.ChatResponse{
        {ToolCalls: []ai.ToolCall{{ID: "call_1", Name: "o1_get_recent_checks", Arguments: json.RawMessage(`{"monitor_id":"m"}`)}}},
        {Text: `{"sufficient":true,"assessment":"timeouts","hypotheses":[{"rank":1,"cause":"timeout","confidence":"high","evidence":[{"tool_call_id":"call_1","pointer":"/data/checks/0/failure_class","value":"timeout"}]}]}`},
    }}
    caller := fakeCaller{result: tools.Result{Data: map[string]any{"checks": []any{map[string]any{"failure_class": "timeout"}}}}}
    out := NewRunner(provider, caller, "m", DefaultLimits(), time.Now).Run(context.Background(), uuid.New(), Seed{})
    if out.Status != StatusOK || out.Result == nil || len(out.Trace.Steps) != 1 { t.Fatalf("%+v", out) }
}

func TestRunner_StopsAtCaps(t *testing.T) {
    // Provider always returns a tool call; expect StatusIncomplete and 6 steps.
}

func TestRunner_TransientProviderErrorIsReturned(t *testing.T) {
    // provider returns &ai.HTTPError{StatusCode:500}; expect out.Err != nil,
    // out.Status empty, and no result.
}

func TestRunner_PermanentProviderErrorFails(t *testing.T) {
    // provider returns &ai.HTTPError{StatusCode:400}; expect StatusFailed,
    // ErrorCode "provider_rejected", Err nil, Result nil.
}

func TestRunner_RecordsToolCallThatHitsDeadline(t *testing.T) {
    // A caller that blocks until the overall deadline; expect StatusIncomplete,
    // ErrorCode "limit_exceeded", and exactly one error Step/ToolCallRecord.
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/copilot -run TestRunner -v`
Expected: FAIL — `Runner` undefined.

- [ ] **Step 3: Implement the loop**

Start with `ChatRoleSystem` = `SystemPrompt`, `ChatRoleUser` = `BuildUserMessage(seed)`. Each iteration calls `provider.Chat` with the six tool definitions from `tools.Specs()`; record one `ModelCallRecord` per call (finish reason, input/output tokens, duration). If tool calls are present, execute sequentially (each with `CallTimeout`), append a `tool` message with the envelope JSON, record a `Step` and `ToolCallRecord` (including the SHA-256 hex `ArgsHash` of the raw arguments), and stop with `incomplete/limit_exceeded` at 6 calls. A Go error from the caller becomes an `is_error` tool result with code `internal`; it does not abort the loop. If the overall deadline expires during a tool call, still record that call as an error step before returning `incomplete`; the trace must show every call that actually ran. If text is present, parse it; on parse error return `failed/invalid_output`. Verify; `StatusOK` when verified count > 0, else `StatusInsufficient`. Track token totals and a 90s overall context; hitting it returns `incomplete/limit_exceeded`. A transient provider error returns `Outcome{Err: err}`; a permanent one returns `failed/provider_rejected`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/copilot -run TestRunner -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/copilot
git commit -m "feat: add the bounded copilot agent runner"
```

---

### Task 8: Investigation service, job, and worker

**Files:**
- Create: `backend/internal/jobs/investigation.go`
- Create: `backend/internal/copilot/service.go`
- Modify: `backend/internal/copilot/store.go` — add `CompleteTx`.
- Create: `backend/internal/db/migrations/00014_investigation_events.sql`
- Create: `backend/internal/worker/investigation_worker.go`
- Modify: `backend/internal/incidents/service.go` — add `RecordInvestigationEventTx` and keep the authorized wrapper.
- Modify: `backend/internal/observability/metrics.go` — add `ObserveInvestigation`.
- Modify: `backend/cmd/worker/main.go`
- Test: `backend/internal/worker/investigation_worker_test.go`

**Interfaces:**
- Consumes: Task 1 store, Task 5 registry, Task 7 runner, `incidents.Service`, `monitors.Service`.
- Produces:
  - `func (s *incidents.Service) RecordInvestigationEvent(ctx context.Context, workspaceID, incidentID, investigationID uuid.UUID, status, model string, toolCalls, hypotheses int) error` — requires `RoleMember`, appends one `investigation_completed` event with a bounded detail object.
  - `func (s *incidents.Service) RecordInvestigationEventTx(ctx context.Context, tx pgx.Tx, workspaceID, incidentID, investigationID uuid.UUID, status, model string, toolCalls, hypotheses int) error` — the same insert on the caller's transaction; the caller has already authorized the actor.
  - `func (s *Store) CompleteTx(ctx context.Context, tx pgx.Tx, inv Investigation) error` — the active-only terminal update on the caller's transaction; `Complete` keeps its wrapper behavior.
  - `func (m *observability.Metrics) ObserveInvestigation(status string, d time.Duration, toolCalls, inputTokens, outputTokens int)` — counter plus duration/token histograms; nil receiver is safe.
  - `type InvestigationArgs struct { InvestigationID uuid.UUID }`; `func (InvestigationArgs) Kind() string { return "investigation" }`.
  - `type JobInserter interface { InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) }`
  - `type Options struct { Store *Store; Incidents *incidents.Service; Monitors *monitors.Service; Workspaces *workspaces.Service; Provider ai.ToolProvider; Inserter JobInserter; Pool *pgxpool.Pool; Queries *sqlc.Queries; Model string; Limits Limits; Now func() time.Time; Logger *slog.Logger; Metrics *observability.Metrics }`
  - `func NewService(opts Options) *Service`; `func (s *Service) Enabled() bool`.
  - `func (s *Service) Run(ctx context.Context, investigationID uuid.UUID, attempt, maxAttempts int) error`.
  - `func NewInvestigationWorker(svc *Service, logger *slog.Logger, metrics *observability.Metrics) *InvestigationWorker`; `func (*InvestigationWorker) Timeout(*river.Job[jobs.InvestigationArgs]) time.Duration { return 2 * time.Minute }`.

- [ ] **Step 1: Write the failing worker integration test**

```go
func TestInvestigationWorker_SuccessAndRetry(t *testing.T) {
    pool, _ := testutil.NewPostgres(t)
    ws, inc, monitor := seedInvestigationFixture(t, pool) // user, workspace, monitor, incident, one down check
    store := copilot.NewPostgresStore(pool)
    inv, _ := store.Create(ctx, copilot.Investigation{... Status: copilot.StatusQueued ...})
    svc := copilot.NewService(copilot.Options{
        Store: store, Incidents: incidents.NewService(pool, sqlc.New(pool), workspaces.NewService(pool, sqlc.New(pool))),
        Monitors: monitors.NewService(...), Workspaces: workspaces.NewService(pool, sqlc.New(pool)),
        Provider: &ai.Stub{ChatScript: ...},
        Pool: pool, Queries: sqlc.New(pool), Model: "stub", Limits: copilot.DefaultLimits(),
    })
    w := NewInvestigationWorker(svc, slog.Default(), nil)
    if err := w.Work(ctx, &river.Job[jobs.InvestigationArgs]{Args: jobs.InvestigationArgs{InvestigationID: inv.ID}, Attempt: 1, MaxAttempts: 3}); err != nil { t.Fatal(err) }
    got, _ := store.GetByID(ctx, inv.ID)
    if got.Status != copilot.StatusOK { t.Fatalf("status = %s", got.Status) }
    // Assert an incident_events row of kind investigation_completed exists.
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd backend && go test ./internal/worker -run TestInvestigationWorker -v`
Expected: FAIL — `copilot.NewService`, worker undefined.

- [ ] **Step 3: Implement `Service.Run`**

`NewService` builds the tool registry with `tools.NewRegistry(opts.Incidents, opts.Monitors, opts.Now)` and the runner with `NewRunner(opts.Provider, registry, opts.Model, opts.Limits, opts.Now)`; it stores `opts.Workspaces`, `opts.Queries`, and `opts.Pool` for `Start`. `auth.ContextWithPrincipal` is available from Task 5. Load the row; terminal statuses are no-ops. Build the actor context with `auth.ContextWithPrincipal(ctx, auth.Principal{UserID: *inv.CreatedBy})`; if `CreatedBy` is nil or no longer a member, complete `failed/actor_removed`. Build the seed; on error complete `failed/internal`. `MarkRunning` with `attempt` and `HashSeed`. Run the runner. On `Outcome.Err` transient and `attempt < maxAttempts`, `SaveProgress` and return the error for River; otherwise complete `failed/provider_unavailable` on the final attempt. On `StatusOK` or `StatusInsufficient`, open one transaction, call `Store.CompleteTx` and `RecordInvestigationEventTx` in it, and commit; the terminal row and its timeline event are atomic, so a failed event insert rolls the status back for a River retry. On other statuses, use the same transaction for `CompleteTx` with no event. Write the `investigation_completed` audit row after commit (best-effort) with the model, status, tool calls, hypotheses, token counts, and error code. Record `ObserveInvestigation(status, duration, toolCalls, inputTokens, outputTokens)` when non-nil. If `MarkRunning`, `SaveProgress`, or `CompleteTx` returns `ErrNotActive`, the stale reaper or another worker won the race: roll back, return nil, and write no timeline event or stale-code overwrite.

- [ ] **Step 4: Wire the worker**

Build the provider once with `ai.New`; type-assert `ai.ToolProvider`; if nil, leave the service nil and log that the copilot is off. Build the service with `incidents.NewService`, `monitors.NewService`, and `workspaces.NewService`. Add `investigationWorker := worker.NewInvestigationWorker(investigationSvc, rt.Logger, obs.Metrics)`; register it; add `"ai": {MaxWorkers: 2}` to the River queue map.

- [ ] **Step 5: Run the tests**

Run: `cd backend && go test ./internal/worker -run TestInvestigationWorker -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/jobs backend/internal/copilot backend/internal/worker backend/internal/auth backend/internal/incidents backend/cmd/worker
git commit -m "feat: run investigations in the worker"
```

---

### Task 9: Stale-row sweep in the scheduler

**Files:**
- Modify: `backend/internal/scheduler/scheduler.go`
- Test: `backend/internal/scheduler/scheduler_test.go`
- Regenerate: `backend/internal/db/sqlc/investigations.sql.go` if Task 1's query names changed.

**Interfaces:**
- Consumes: `sqlc.ReapAllStaleInvestigations`.
- Produces: `func (s *Scheduler) reapStaleInvestigations(ctx context.Context) (int64, error)`.

- [ ] **Step 1: Write the failing sweep test**

```go
func TestScheduler_ReapsStaleInvestigations(t *testing.T) {
    pool, _ := testutil.NewPostgres(t)
    // Insert one running row created 10 minutes ago and one fresh running row.
    s := New(pool, sqlc.New(pool), nil, config.Config{SchedulerTick: time.Second}, slog.Default(), nil, nil)
    n, err := s.reapStaleInvestigations(context.Background())
    if err != nil || n != 1 { t.Fatalf("n = %d, %v", n, err) }
    // Assert old is failed/stale and fresh is still running.
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd backend && go test ./internal/scheduler -run TestScheduler_ReapsStale -v`
Expected: FAIL — method undefined.

- [ ] **Step 3: Implement and call it**

`reapStaleInvestigations` calls `q.ReapAllStaleInvestigations(ctx, s.now().Add(-5*time.Minute))`. Call it from the scheduler's run loop before each probe tick and log the count when non-zero. Do not change `Tick`'s return value.

- [ ] **Step 4: Run the tests**

Run: `cd backend && go test ./internal/scheduler -run TestScheduler_ReapsStale -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/scheduler backend/internal/db/sqlc
git commit -m "feat: reap stale investigations"
```

---

### Task 10: API handlers, router, config, and API wiring

**Files:**
- Create: `backend/internal/copilot/handlers.go`
- Modify: `backend/internal/copilot/service.go` — add `Start`, `Latest`, `Get`.
- Modify: `backend/internal/api/router.go`
- Modify: `backend/internal/config/config.go`
- Modify: `backend/cmd/api/main.go`
- Test: `backend/internal/api/investigations_integration_test.go`, `backend/internal/config/config_test.go`

**Interfaces:**
- Consumes: Task 8 service, Task 1 store, `auth.RequireSession`, `auth.RequireAuth`, `httpx.KeyedRateLimit`.
- Produces:
  - `func (s *Service) Start(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) (Investigation, error)` — member+, 202 semantics, `ErrActive`, `ErrLimit`, `ErrNotFound`; a viewer gets `workspaces.ErrForbidden`.
  - `func (s *Service) Latest(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) (*Investigation, error)`; `func (s *Service) Get(ctx context.Context, actorID, workspaceID, incidentID, id uuid.UUID) (Investigation, error)` — both authorize `RoleViewer` first, so a key scoped to workspace A cannot read workspace B through the path.
  - `func StartHandler(svc *Service) http.HandlerFunc`, `LatestHandler`, `GetHandler`.
  - `api.Dependencies` gains `InvestigationService *copilot.Service`, `MCPHandler http.Handler`, `RiverInserter copilot.JobInserter`.
  - Config fields: `MCPEnabled, CopilotEnabled bool`; `CopilotModel string`; `CopilotTimeout, CopilotCallTimeout time.Duration`; `CopilotMaxModelCalls, CopilotMaxToolCalls, CopilotMaxResultBytes, CopilotMaxOutputTokens, CopilotMaxInputTokens, CopilotPerIncidentPerHour, CopilotPerWorkspacePerDay int`.

- [ ] **Step 1: Write the failing API tests**

```go
func TestInvestigations_MemberStartsViewerReads(t *testing.T) {
    // Member session POST -> 202; row status queued; a river_job row exists.
    // Viewer session GET latest -> 200 after a completion row; POST -> 403.
    // Second POST while queued -> 409.
    // Cross-workspace incident -> 404.
    // A viewer key scoped to workspace A GETs workspace B -> 404.
}

func TestInvestigations_Disabled(t *testing.T) {
    // Router with nil InvestigationService: GET latest and POST -> 501.
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd backend && go test ./internal/api -run TestInvestigations -v`
Expected: FAIL — routes absent.

- [ ] **Step 3: Implement `Start` atomically**

Call `s.workspaces.Authorize(ctx, actorID, workspaceID, workspaces.RoleMember)` first (viewer -> `ErrForbidden`), then `s.incidents.Get(ctx, actorID, workspaceID, incidentID)` to verify the incident and return `ErrNotFound` for a cross-workspace id. Open a transaction; `qtx := s.queries.WithTx(tx)`; call `qtx.ReapStaleInvestigations(ctx, incidentID, now-5m)`; count with `qtx.CountInvestigationsForIncidentSince` and `qtx.CountInvestigationsForWorkspaceSince`, return `ErrLimit` at 3/20; insert the investigation row with `s.store.CreateTx(ctx, tx, inv)` (which maps the partial unique violation to `ErrActive`) and `jobs.InvestigationArgs` with `InsertTx` and `river.InsertOpts{Queue: "ai", MaxAttempts: 3}`; commit. Write `investigation_started` audit best-effort.

- [ ] **Step 4: Implement handlers and routes**

`StartHandler` requires a session (member check inside `Start`), returns `202` with the public row; `ErrActive` -> 409 with the active row; `ErrLimit` -> 429; `ErrNotFound` -> 404; `workspaces.ErrForbidden` -> 403; disabled -> 501. `LatestHandler` and `GetHandler` accept session or API key and call the viewer-authorized Service methods, so a key scoped to another workspace returns 404 rather than data; `ErrForbidden` -> 403, `ErrNotFound` -> 404, disabled -> 501. Add routes exactly:

```go
mux.Handle("POST /api/v1/workspaces/{workspaceID}/incidents/{incidentID}/investigations",
    requireSession(investigationLimiter(http.HandlerFunc(copilot.StartHandler(investigationSvc)))))
mux.Handle("GET /api/v1/workspaces/{workspaceID}/incidents/{incidentID}/investigations/latest",
    withKeyAuth(http.HandlerFunc(copilot.LatestHandler(investigationSvc))))
mux.Handle("GET /api/v1/workspaces/{workspaceID}/incidents/{incidentID}/investigations/{investigationID}",
    withKeyAuth(http.HandlerFunc(copilot.GetHandler(investigationSvc))))
```

`investigationLimiter` is `httpx.RateLimit(6, 2, true)`.

- [ ] **Step 5: Add config and wire the API**

Parse the env names from Global Constraints with defaults. Validate: timeouts 1s–2m, caps > 0, 1,200 output max 4,000, 8 KB result bytes 1 KB–64 KB. `CopilotModel` defaults to `AIModel`. In `cmd/api/main.go`, create `river.NewClient(riverpgxv5.New(rt.Pool), &river.Config{Logger: rt.Logger})` and pass it as `RiverInserter`; it is not started because it only inserts, like the scheduler. In the router, build the provider once; build the copilot service when `CopilotEnabled && provider != nil && RiverInserter != nil`, passing `IncidentService`, `MonitorService`, `WorkspaceService`, `Pool`, and `Queries`; otherwise leave it nil. Leave `/mcp` unregistered in this task; Task 11 adds the handler and route.

- [ ] **Step 6: Run the tests**

Run: `cd backend && go test ./internal/api -run TestInvestigations -v && go test ./internal/config -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/copilot backend/internal/api backend/internal/config backend/cmd/api
git commit -m "feat: expose investigation endpoints"
```

---

### Task 11: MCP server, bearer auth, conformance tests, and docs

**Files:**
- Create: `backend/internal/mcpserver/server.go`
- Create: `backend/internal/mcpserver/auth.go`
- Modify: `backend/go.mod`, `backend/go.sum` — add `github.com/modelcontextprotocol/go-sdk v1.7.0`.
- Modify: `backend/internal/observability/metrics.go` — add `ObserveMCPTool`.
- Create: `docs/mcp.md`
- Test: `backend/internal/mcpserver/server_test.go`
- Modify: `backend/internal/api/router.go` — pass the real handler.

**Interfaces:**
- Consumes: Task 5 `tools.Registry`, `auth.Service.AuthenticateAPIKey`, `httpx.KeyedRateLimit`, `config.Config`.
- Produces:
  - `type Options struct { Auth *auth.Service; Registry *tools.Registry; Queries *sqlc.Queries; AllowedOrigins []string; Version string; Logger *slog.Logger; Metrics *observability.Metrics }`
  - `func New(opts Options) *Server`; `func (s *Server) Handler() http.Handler`.
  - `func (m *observability.Metrics) ObserveMCPTool(tool, status string, d time.Duration)` — counter plus duration histogram; nil receiver is safe.

- [ ] **Step 1: Add the SDK and write the failing conformance test**

Run: `cd backend && go get github.com/modelcontextprotocol/go-sdk@v1.7.0`

Test setup: `testutil.NewPostgres(t)`, seed a workspace and two API-key rows (viewer/member) with known raw keys and `auth.HashToken`, start `httptest.NewServer(mcpserver.New(...).Handler())`, connect with `mcp.NewClient` + `mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: clientWithBearer(raw)}`.

```go
func TestMCP_MemberToolsAndViewerTools(t *testing.T) {
    // Member key: ListTools returns 6 names including o1_get_alert_deliveries.
    // Viewer key: 5 names, no o1_get_alert_deliveries.
}

func TestMCP_RejectsUnauthenticatedAndCrossTenant(t *testing.T) {
    // No header -> 401; bad key -> 401; disallowed Origin -> 403.
    // Call o1_get_incident with another workspace's id -> IsError, not_found.
}

func TestMCP_AuditsAndRedacts(t *testing.T) {
    // After one tools/call, security_events has mcp.tool_called with key id and tool.
    // The result text contains no monitor URL, header, recipient, or raw error.
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/mcpserver -v`
Expected: FAIL — package undefined.

- [ ] **Step 3: Implement the server and auth**

`Handler` requires `Authorization: Bearer` (no cookie fallback), authenticates with `AuthenticateAPIKey`, checks Origin, and applies `httpx.KeyedRateLimit(auth.APIKeyRateKey, 1, 10)` (60/min). On auth failure return 401 with `WWW-Authenticate: Bearer`; log only the parsed prefix and key id, never the secret, and write no audit row with key material. On bad Origin return 403. Build a stateless SDK server per authenticated key with `SupportedProtocolVersions: []string{"2026-07-28", "2025-11-25"}`, `Stateless: true`, `JSONResponse: true`, and an `Instructions` string saying the surface is read-only, workspace-scoped, and that IDs come from `o1_list_monitors` / `o1_list_incidents`. Register only specs where `key.Role` parses and `AtLeast(spec.MinRole)`, using the spec's `InputSchema` so the registry and MCP cannot drift. The tool handler puts the key and principal into the context, calls `Registry.Call` with `key.WorkspaceID`, writes `mcp.tool_called` best-effort, calls `ObserveMCPTool(tool, status, duration)`, and returns `TextContent` with the result JSON; `IsError` results set `CallToolResult.IsError`. In `api/router.go`, when `MCPEnabled` is true and the auth/registry dependencies exist, build `mcpserver.New(...).Handler()` and register `mux.Handle("POST /mcp", handler)`; when false, register a handler that returns 501 with `httpx.WriteProblem`.

- [ ] **Step 4: Write `docs/mcp.md`**

Document `POST /mcp`, the bearer key (viewer = five tools, member = six), the six tools, workspace scoping, Origin behavior, the unsupported OAuth flow, and one generic client config with `Authorization: Bearer o1_...`.

- [ ] **Step 5: Run the tests**

Run: `cd backend && go test ./internal/mcpserver -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/go.mod backend/go.sum backend/internal/mcpserver backend/internal/api backend/internal/observability docs/mcp.md
git commit -m "feat: add the o1 MCP server"
```

---

### Task 12: Investigation panel, API client, and hooks

**Files:**
- Modify: `web/src/lib/api/types.ts`
- Modify: `web/src/lib/query/keys.ts`
- Modify: `web/src/features/incidents/api.ts`
- Create: `web/src/features/incidents/investigationHooks.ts`
- Create: `web/src/features/incidents/IncidentInvestigationPanel.tsx`
- Modify: `web/src/features/incidents/IncidentDetailScreen.tsx`
- Modify: `web/src/lib/roles.ts`
- Test: `web/src/features/incidents/IncidentInvestigationPanel.test.tsx`

**Interfaces:**
- Consumes: existing `apiRequest`, `renderWithProviders`, MSW server.
- Produces:
  - Types `InvestigationStatus`, `InvestigationEvidence`, `InvestigationHypothesis`, `InvestigationResult`, `InvestigationStep`, `IncidentInvestigation`.
  - `queryKeys.incidentInvestigationLatest(workspaceId, incidentId)`, `queryKeys.incidentInvestigation(workspaceId, incidentId, id)`.
  - `getIncidentInvestigationLatest`, `getIncidentInvestigation`, `startIncidentInvestigation`.
  - `useIncidentInvestigation`, `useStartIncidentInvestigation`.
  - `canInvestigateIncidents(role: Role): boolean` — `roleAtLeast(role, 'member')`.
  - `IncidentInvestigationPanel({ workspaceId, incidentId, canStart }: {...})`.

- [ ] **Step 1: Write the failing panel tests**

```tsx
test('offers to investigate and shows the stored result', async () => {
    server.use(
        http.get(`${base}/latest`, () => new HttpResponse(null, { status: 404 })),
        http.post(base, () => HttpResponse.json(investigation({ status: 'running', result: null }))),
        http.get(`${base}/latest`, () => HttpResponse.json(investigation({ status: 'ok' }))),
    );
    renderWithProviders(<IncidentInvestigationPanel workspaceId={workspaceId} incidentId={incidentId} canStart />);
    await userEvent.click(await screen.findByRole('button', { name: /investigate/i }));
    expect(await screen.findByText(/timeouts above the limit/i)).toBeInTheDocument();
    expect(screen.getByText(/unverified/i)).toBeInTheDocument();
});

test('hides the action from viewers but still shows a stored result', async () => {
    // GET latest returns ok; canStart={false}; expect no Investigate button.
});

test('shows a retry after a failed run and hides when disabled', async () => {
    // failed -> Retry; latest 501 -> nothing rendered.
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && npm test -- --run IncidentInvestigationPanel`
Expected: FAIL — module missing.

- [ ] **Step 3: Add types, API functions, keys, and hooks**

`useIncidentInvestigation` uses `retry:false`, `staleTime:0`, and `refetchInterval: (query) => ['queued','running'].includes(query.state.data?.status ?? '') ? 2000 : false`. `useStartIncidentInvestigation` posts and seeds the latest query data on success; on 409 it also seeds the returned active row.

- [ ] **Step 4: Implement the panel and mount it**

Render the panel under `IncidentInsightPanel` in `IncidentDetailScreen` with `canStart={canInvestigateIncidents(workspace.role)}`. The panel: 404 + no start -> null; 501 -> null; queued/running -> `role="status"` with elapsed/attempts and the step list; ok/insufficient -> assessment, up to 3 hypotheses with confidence, evidence chips (pointer/value), an `Unverified` badge on a hypothesis that lost all its citations, next checks, model/time; if every hypothesis was unverified the status is insufficient and the panel shows the refusal rather than a cause; failed/incomplete -> error text and Retry. Use `aria-live="polite"` for the running state.

- [ ] **Step 5: Run the tests**

Run: `cd web && npm test -- --run IncidentInvestigationPanel`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src/lib web/src/features/incidents
git commit -m "feat: show incident investigations"
```

---

### Task 13: MCP setup card on the API keys screen

**Files:**
- Create: `web/src/features/api-keys/MCPCard.tsx`
- Modify: `web/src/features/api-keys/APIKeysScreen.tsx`
- Test: `web/src/features/api-keys/MCPCard.test.tsx`

**Interfaces:**
- Produces: `MCPCard({ endpoint }: { endpoint: string })` — a static card with the endpoint, a "viewer key reads five tools; member key adds deliveries" note, and a link to `/docs/mcp.md` (or the deployed docs URL).
- Consumes: `window.location.origin` for the endpoint.

- [ ] **Step 1: Write the failing test**

```tsx
test('shows the MCP endpoint and the key-role rule', () => {
    renderWithProviders(<MCPCard endpoint="https://o1.example.com/mcp" />);
    expect(screen.getByText('https://o1.example.com/mcp')).toBeInTheDocument();
    expect(screen.getByText(/viewer key/i)).toBeInTheDocument();
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npm test -- --run MCPCard`
Expected: FAIL — module missing.

- [ ] **Step 3: Implement and mount the card**

Render `<MCPCard endpoint={`${window.location.origin}/mcp`} />` below the key table in `APIKeysContent`. Keep it read-only; no API call.

- [ ] **Step 4: Run the tests**

Run: `cd web && npm test -- --run MCPCard APIKeysScreen`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/features/api-keys
git commit -m "feat: add the MCP setup card"
```

---

### Task 14: Compose, Caddy, CI, e2e, and README

**Files:**
- Modify: `compose.yaml`
- Modify: `web/Caddyfile`
- Modify: `.github/workflows/ci.yml`
- Modify: `web/tests/e2e/smoke.spec.ts`
- Modify: `README.md`
- Modify: `docs/security/threat-model.md`

**Interfaces:**
- Consumes: Tasks 10–13.
- Produces: env plumbing, `/mcp` routing, one Playwright investigation path, docs.

- [ ] **Step 1: Add the env and route**

In `compose.yaml` under `x-backend-env`, add `O1_MCP_ENABLED: ${O1_MCP_ENABLED:-false}`, `O1_COPILOT_ENABLED: ${O1_COPILOT_ENABLED:-false}`, `O1_COPILOT_MODEL: ${O1_COPILOT_MODEL:-}`, `O1_COPILOT_TIMEOUT: ${O1_COPILOT_TIMEOUT:-90s}`, `O1_COPILOT_CALL_TIMEOUT: ${O1_COPILOT_CALL_TIMEOUT:-30s}`. In `web/Caddyfile`, add `handle /mcp { reverse_proxy api:8080 { header_up X-Forwarded-For {remote_host} } }` before the SPA fallback.

- [ ] **Step 2: Add the e2e path**

In the CI `e2e` job, start the stack with:

```yaml
- run: O1_MCP_ENABLED=true O1_COPILOT_ENABLED=true O1_AI_PROVIDER=stub docker compose up -d --build
```

In `smoke.spec.ts`, after the incident timeline is acknowledged, click `Investigate`, wait for the stub assessment text, assert a hypothesis is visible, and assert the panel does not contain `https://example.com` or the monitor URL. After creating the member key, assert `POST /mcp` without auth returns 401 and with `Authorization: Bearer ${memberSecret}` returns 200 for a `tools/list` request; keep the request to one raw JSON-RPC call with the required headers.

- [ ] **Step 3: Update the threat model and README**

Add a short "Incident copilot and MCP" section to `docs/security/threat-model.md` covering the model boundary, the read-only tool boundary, tenant scope, and the static bearer key tradeoff. Add a README section linking `docs/ai/copilot.md` and `docs/mcp.md`; Task 15 adds the evaluation link.

- [ ] **Step 4: Run the frontend and e2e checks**

Run: `cd web && npm run typecheck && npm test -- --run`
Expected: PASS. Then run the Compose stack with the env above and `npx playwright test` when Docker is available.

- [ ] **Step 5: Commit**

```bash
git add compose.yaml web/Caddyfile .github/workflows/ci.yml web/tests/e2e/smoke.spec.ts README.md docs/security/threat-model.md
git commit -m "chore: wire the copilot and MCP into the stack"
```

---

### Task 15: Live evaluation harness and docs

**Files:**
- Create: `backend/cmd/copilot-eval/main.go`
- Create: `backend/internal/copilot/eval.go`
- Create: `backend/internal/copilot/testdata/eval/*.json`
- Create: `docs/ai/copilot.md`
- Create: `docs/ai/copilot-eval.md`
- Modify: `Makefile`
- Test: `backend/internal/copilot/eval_test.go`

**Interfaces:**
- Consumes: Task 7 `Runner`, `ai.New`, fixture JSON.
- Produces:
  - `type Fixture struct { Name string; Seed Seed; ToolResults map[string]json.RawMessage; Want []string }`
  - `func LoadFixtures(dir string) ([]Fixture, error)`
  - `func CheckFixture(f Fixture, out Outcome) error` — asserts the named properties (`calls_checks`, `cites_failure_class`, `insufficient`, `no_unverified`, `within_limits`, `refuses_injection`).
  - `func RunLive(ctx context.Context, provider ai.ToolProvider, fixtures []Fixture, model string) ([]EvalResult, error)`.
  - `make copilot-eval` runs `cd backend && go run ./cmd/copilot-eval -out ../docs/ai/copilot-eval.md`.

- [ ] **Step 1: Write the fixture loader test**

```go
func TestLoadFixtures(t *testing.T) {
    fs, err := LoadFixtures("testdata/eval")
    if err != nil || len(fs) != 10 { t.Fatalf("%d, %v", len(fs), err) }
    for _, f := range fs { if f.Name == "" || len(f.Want) == 0 { t.Fatalf("bad fixture %+v", f) } }
}
```

Create the ten fixtures from the spec's Evaluation section: timeout, DNS, one success in failures, no checks, tool error, invented tool_call_id, wrong pointer value, loop limit, injection, oversized result.

- [ ] **Step 2: Run it to verify it fails**

Run: `cd backend && go test ./internal/copilot -run TestLoadFixtures -v`
Expected: FAIL — fixtures/loader missing.

- [ ] **Step 3: Implement the fixture checker and CLI**

The CLI requires `O1_AI_PROVIDER=openai` (or another `ToolProvider`); with `off` it exits 2 with a message. For each fixture, build a `Runner` with a `fixtureCaller` that returns `ToolResults[name]`, run, check properties, and write a markdown table with fixture, pass/fail, tool calls, tokens, and unsupported citations. The unit test uses `ai.Stub{ChatScript: ...}` and asserts the checker itself.

- [ ] **Step 4: Write the docs**

`docs/ai/copilot.md`: what it does, enable/config, what the model sees, caps, failure states, evaluation. `docs/ai/copilot-eval.md`: the recorded live run table with a note that the stub-only results are not evidence of model quality.

- [ ] **Step 5: Add the Make target and run the unit test**

```make
copilot-eval:
	cd $(BACKEND) && go run ./cmd/copilot-eval -out ../docs/ai/copilot-eval.md
```

Run: `cd backend && go test ./internal/copilot -run TestLoadFixtures -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/cmd/copilot-eval backend/internal/copilot docs/ai Makefile
git commit -m "feat: add the copilot evaluation harness"
```

---

## Self-Review

**Spec coverage:** storage, tools, agent loop, verification, MCP, auth, roles, API, UI, caps, failure mapping, evaluation, docs, and operations each have a task. RAG, Slack summaries, auto-run, SSE, OAuth, and NATS are intentionally absent per the spec's deferred table.

**Type consistency:** `Investigation`, `Result`, `Trace`, `Step`, `ToolCallRecord`, `tools.Result`, `Seed`, `Outcome`, `Limits`, and `Service` are defined once and reused. Task 8 defines `Service` and `Run`; Task 10 adds `Start`/`Latest`/`Get` and uses them; Task 12 uses the API response shape from Task 10. Task 5 and Task 6 use `evidence` types from Task 3.

**Review focus:** each of the five risks has a named test in the owning task. The injection fixture is in Task 15 and the verification logic in Task 6.

**Proportion:** the plan names files, interfaces, assertions, and exact values; bodies are left to the implementer except where the spec pins a contract.

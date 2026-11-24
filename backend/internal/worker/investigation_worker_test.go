package worker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"o1/internal/ai"
	"o1/internal/copilot"
	"o1/internal/db/sqlc"
	"o1/internal/incidents"
	"o1/internal/jobs"
	"o1/internal/monitors"
	"o1/internal/observability"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

// investigationFixture is one tenant with an open incident and one failing
// check, which is the smallest real context the copilot can investigate.
type investigationFixture struct {
	userID      uuid.UUID
	workspaceID uuid.UUID
	monitorID   uuid.UUID
	incidentID  uuid.UUID
}

// seedInvestigationFixture inserts a verified user, a workspace they own, a
// monitor, its state row, an open incident, and one down check.
func seedInvestigationFixture(t *testing.T, pool *pgxpool.Pool) investigationFixture {
	t.Helper()
	ctx := context.Background()
	userID := testutil.CreateVerifiedUser(t, pool, "copilot-"+uuid.NewString()+"@example.com", "correct horse battery staple")

	var workspaceID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspaces (id, name, slug, created_by)
		 VALUES (gen_random_uuid(), 'Investigation', $1, $2) RETURNING id`,
		"investigation-"+uuid.NewString(), userID,
	).Scan(&workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`,
		workspaceID, userID,
	); err != nil {
		t.Fatalf("seed workspace member: %v", err)
	}

	var monitorID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO monitors (id, workspace_id, name, url, method, interval_seconds, timeout_seconds, created_by)
		 VALUES (gen_random_uuid(), $1, 'API', 'https://example.com/health', 'GET', 60, 10, $2) RETURNING id`,
		workspaceID, userID,
	).Scan(&monitorID); err != nil {
		t.Fatalf("seed monitor: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO monitor_state (monitor_id) VALUES ($1)`, monitorID); err != nil {
		t.Fatalf("seed monitor state: %v", err)
	}

	var incidentID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO incidents (id, workspace_id, monitor_id, status, started_at)
		 VALUES (gen_random_uuid(), $1, $2, 'open', $3) RETURNING id`,
		workspaceID, monitorID, time.Now().UTC().Add(-5*time.Minute),
	).Scan(&incidentID); err != nil {
		t.Fatalf("seed incident: %v", err)
	}

	checkedAt := time.Now().UTC().Add(-time.Minute)
	if _, err := pool.Exec(ctx,
		`INSERT INTO probe_results
		   (id, monitor_id, workspace_id, scheduled_for, checked_at, status, status_code, latency_ms, error)
		 VALUES ($1, $2, $3, $4, $4, 'down', 503, 120, 'upstream unavailable')`,
		uuid.New(), monitorID, workspaceID, checkedAt,
	); err != nil {
		t.Fatalf("seed down check: %v", err)
	}

	return investigationFixture{
		userID:      userID,
		workspaceID: workspaceID,
		monitorID:   monitorID,
		incidentID:  incidentID,
	}
}

// newInvestigationService wires the real tool registry and runner over the
// fixture database, using a scripted stub as the model.
func newInvestigationService(t *testing.T, pool *pgxpool.Pool, provider ai.ToolProvider, metrics *observability.Metrics) *copilot.Service {
	t.Helper()
	q := sqlc.New(pool)
	wsSvc := workspaces.NewService(pool, q)
	return copilot.NewService(copilot.Options{
		Store:      copilot.NewPostgresStore(pool),
		Incidents:  incidents.NewService(pool, q, wsSvc),
		Monitors:   monitors.NewService(pool, q, wsSvc, nil, nil, testutil.TestConfig(t)),
		Workspaces: wsSvc,
		Provider:   provider,
		Pool:       pool,
		Queries:    q,
		Model:      "stub",
		Limits:     copilot.DefaultLimits(),
		Now:        time.Now,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:    metrics,
	})
}

func newInvestigationWorkerLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func investigationJob(id uuid.UUID, attempt, maxAttempts int) *river.Job[jobs.InvestigationArgs] {
	return &river.Job[jobs.InvestigationArgs]{
		JobRow: &rivertype.JobRow{Attempt: attempt, MaxAttempts: maxAttempts},
		Args:   jobs.InvestigationArgs{InvestigationID: id},
	}
}

func createQueuedInvestigation(t *testing.T, pool *pgxpool.Pool, fixture investigationFixture, createdBy *uuid.UUID) copilot.Investigation {
	t.Helper()
	store := copilot.NewPostgresStore(pool)
	created, err := store.Create(context.Background(), copilot.Investigation{
		WorkspaceID:   fixture.workspaceID,
		IncidentID:    fixture.incidentID,
		Status:        copilot.StatusQueued,
		Model:         "stub",
		PromptVersion: copilot.PromptVersion,
		CreatedBy:     createdBy,
	})
	if err != nil {
		t.Fatalf("create investigation: %v", err)
	}
	return created
}

type investigationEventRow struct {
	kind   string
	detail map[string]any
}

func investigationEvents(t *testing.T, pool *pgxpool.Pool, incidentID uuid.UUID) []investigationEventRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT kind, detail FROM incident_events WHERE incident_id = $1 ORDER BY created_at`, incidentID)
	if err != nil {
		t.Fatalf("list incident events: %v", err)
	}
	defer rows.Close()

	var out []investigationEventRow
	for rows.Next() {
		var row investigationEventRow
		var raw []byte
		if err := rows.Scan(&row.kind, &raw); err != nil {
			t.Fatalf("scan incident event: %v", err)
		}
		if err := json.Unmarshal(raw, &row.detail); err != nil {
			t.Fatalf("decode incident event detail: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate incident events: %v", err)
	}
	return out
}

func investigationAuditDetail(t *testing.T, pool *pgxpool.Pool, workspaceID uuid.UUID, kind string) map[string]any {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT detail FROM security_events WHERE workspace_id = $1 AND kind = $2 ORDER BY created_at DESC LIMIT 1`,
		workspaceID, kind,
	).Scan(&raw); err != nil {
		t.Fatalf("load %s audit event: %v", kind, err)
	}
	var detail map[string]any
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("decode %s audit detail: %v", kind, err)
	}
	return detail
}

func TestInvestigationWorker_SuccessAndRetry(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	fixture := seedInvestigationFixture(t, pool)
	createdBy := fixture.userID
	inv := createQueuedInvestigation(t, pool, fixture, &createdBy)

	checkArgs, err := json.Marshal(map[string]any{
		"monitor_id": fixture.monitorID.String(),
		"hours":      24,
	})
	if err != nil {
		t.Fatalf("encode tool arguments: %v", err)
	}
	provider := &ai.Stub{ChatScript: []ai.ChatResponse{
		{
			ToolCalls: []ai.ToolCall{{
				ID:        "call_1",
				Name:      "o1_get_recent_checks",
				Arguments: checkArgs,
			}},
			Model:        "stub",
			InputTokens:  100,
			OutputTokens: 10,
			FinishReason: "tool_calls",
		},
		{
			Text:         `{"sufficient":true,"assessment":"The monitor returned a failing check in the last 24 hours.","hypotheses":[{"rank":1,"cause":"The monitor is receiving 503 responses.","confidence":"high","evidence":[{"tool_call_id":"call_1","pointer":"/data/checks/0/status","value":"down","note":"the newest check failed"}]}],"next_checks":["Check the upstream service."]}`,
			Model:        "stub",
			InputTokens:  140,
			OutputTokens: 60,
			FinishReason: "stop",
		},
	}}

	metrics := observability.NewMetrics("o1-worker", "test")
	svc := newInvestigationService(t, pool, provider, metrics)
	w := NewInvestigationWorker(svc, newInvestigationWorkerLogger(), metrics, 0)
	if err := w.Work(ctx, investigationJob(inv.ID, 1, 3)); err != nil {
		t.Fatalf("Work: %v", err)
	}

	store := copilot.NewPostgresStore(pool)
	got, err := store.GetByID(ctx, inv.ID)
	if err != nil {
		t.Fatalf("load investigation: %v", err)
	}
	if got.Status != copilot.StatusOK {
		t.Fatalf("status = %s, want ok", got.Status)
	}
	if got.Result == nil || !got.Result.Sufficient || len(got.Result.Hypotheses) != 1 {
		t.Fatalf("result = %+v, want one sufficient hypothesis", got.Result)
	}
	if got.ErrorCode != "" {
		t.Fatalf("error code = %q, want empty", got.ErrorCode)
	}
	if got.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", got.Attempts)
	}
	if got.SeedHash == "" {
		t.Fatal("seed hash is empty")
	}
	if got.CompletedAt == nil {
		t.Fatal("completed at is nil")
	}
	if got.ToolCalls != 1 {
		t.Fatalf("tool calls = %d, want 1", got.ToolCalls)
	}
	if got.InputTokens != 240 || got.OutputTokens != 70 {
		t.Fatalf("tokens = %d/%d, want 240/70", got.InputTokens, got.OutputTokens)
	}
	if len(got.Trace.Calls) != 1 || got.Trace.Calls[0].Name != "o1_get_recent_checks" {
		t.Fatalf("trace calls = %+v, want one o1_get_recent_checks call", got.Trace.Calls)
	}
	if len(got.Trace.ModelCalls) != 2 {
		t.Fatalf("model calls = %+v, want 2", got.Trace.ModelCalls)
	}
	if provider.ChatCalls() != 2 {
		t.Fatalf("provider chat calls = %d, want 2", provider.ChatCalls())
	}

	events := investigationEvents(t, pool, fixture.incidentID)
	if len(events) != 1 {
		t.Fatalf("incident events = %+v, want exactly one", events)
	}
	if events[0].kind != "investigation_completed" {
		t.Fatalf("event kind = %q, want investigation_completed", events[0].kind)
	}
	if events[0].detail["investigation_id"] != inv.ID.String() {
		t.Fatalf("investigation_id = %v, want %s", events[0].detail["investigation_id"], inv.ID)
	}
	if events[0].detail["status"] != "ok" || events[0].detail["model"] != "stub" {
		t.Fatalf("event detail = %+v, want status ok and model stub", events[0].detail)
	}
	if events[0].detail["tool_calls"] != float64(1) || events[0].detail["hypotheses"] != float64(1) {
		t.Fatalf("event counts = %+v, want 1 tool call and 1 hypothesis", events[0].detail)
	}

	audit := investigationAuditDetail(t, pool, fixture.workspaceID, "investigation_completed")
	if audit["status"] != "ok" || audit["model"] != "stub" {
		t.Fatalf("audit detail = %+v, want status ok and model stub", audit)
	}
	if audit["input_tokens"] != float64(240) || audit["output_tokens"] != float64(70) {
		t.Fatalf("audit tokens = %+v, want 240/70", audit)
	}
	if _, ok := audit["error_code"]; ok {
		t.Fatalf("audit error code = %v, want absent", audit["error_code"])
	}

	body := gatherWorkerMetrics(t, metrics)
	for _, want := range []string{
		`o1_investigation_runs_total{status="ok"} 1`,
		`o1_investigation_duration_seconds_count{status="ok"} 1`,
		`o1_investigation_tool_calls_count{status="ok"} 1`,
		`o1_investigation_input_tokens_count{status="ok"} 1`,
		`o1_investigation_output_tokens_count{status="ok"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("investigation metrics missing %q:\n%s", want, body)
		}
	}

	// A terminal row is a no-op: a replay must not revive it, call the model,
	// or append a second timeline event.
	if err := w.Work(ctx, investigationJob(inv.ID, 2, 3)); err != nil {
		t.Fatalf("replay Work: %v", err)
	}
	replayed, err := store.GetByID(ctx, inv.ID)
	if err != nil {
		t.Fatalf("reload investigation: %v", err)
	}
	if replayed.Status != copilot.StatusOK || replayed.Attempts != 1 {
		t.Fatalf("replayed row = %+v, want unchanged ok/1", replayed)
	}
	if provider.ChatCalls() != 2 {
		t.Fatalf("replay called the provider %d times, want 2 total", provider.ChatCalls())
	}
	if events := investigationEvents(t, pool, fixture.incidentID); len(events) != 1 {
		t.Fatalf("replay incident events = %+v, want still one", events)
	}
}

func TestInvestigationWorker_TransientFailureRetriesThenFails(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	fixture := seedInvestigationFixture(t, pool)
	createdBy := fixture.userID
	inv := createQueuedInvestigation(t, pool, fixture, &createdBy)

	provider := &ai.Stub{Err: &ai.HTTPError{StatusCode: 503, Body: "provider unavailable"}}
	svc := newInvestigationService(t, pool, provider, nil)
	w := NewInvestigationWorker(svc, newInvestigationWorkerLogger(), nil, 0)

	err := w.Work(ctx, investigationJob(inv.ID, 1, 3))
	if err == nil {
		t.Fatal("Work = nil, want a retryable provider error")
	}

	store := copilot.NewPostgresStore(pool)
	running, err := store.GetByID(ctx, inv.ID)
	if err != nil {
		t.Fatalf("load running investigation: %v", err)
	}
	if running.Status != copilot.StatusRunning {
		t.Fatalf("status = %s, want running", running.Status)
	}
	if running.ErrorCode != "" {
		t.Fatalf("error code = %q, want empty on a retryable attempt", running.ErrorCode)
	}
	if running.Attempts != 1 || len(running.Trace.ModelCalls) != 1 {
		t.Fatalf("running row = %+v, want attempts 1 and one saved model call", running)
	}
	if running.CompletedAt != nil {
		t.Fatal("retryable attempt completed the row")
	}
	if events := investigationEvents(t, pool, fixture.incidentID); len(events) != 0 {
		t.Fatalf("retryable attempt wrote events: %+v", events)
	}

	// The last attempt gives up without another retry and records the failure.
	if err := w.Work(ctx, investigationJob(inv.ID, 3, 3)); err != nil {
		t.Fatalf("final attempt Work: %v", err)
	}
	failed, err := store.GetByID(ctx, inv.ID)
	if err != nil {
		t.Fatalf("load failed investigation: %v", err)
	}
	if failed.Status != copilot.StatusFailed || failed.ErrorCode != "provider_unavailable" {
		t.Fatalf("failed row = %+v, want failed/provider_unavailable", failed)
	}
	if failed.CompletedAt == nil || failed.Attempts != 3 {
		t.Fatalf("failed row = %+v, want completed at attempt 3", failed)
	}
	if events := investigationEvents(t, pool, fixture.incidentID); len(events) != 0 {
		t.Fatalf("failed run wrote a timeline event: %+v", events)
	}

	audit := investigationAuditDetail(t, pool, fixture.workspaceID, "investigation_completed")
	if audit["status"] != "failed" || audit["error_code"] != "provider_unavailable" {
		t.Fatalf("audit detail = %+v, want failed/provider_unavailable", audit)
	}
}

// TestInvestigationWorker_EventInsertRollsBackAndRetries proves the terminal
// row and its timeline event commit together. A blocked event insert must leave
// the row running so River can retry instead of losing the event forever.
func TestInvestigationWorker_EventInsertRollsBackAndRetries(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	fixture := seedInvestigationFixture(t, pool)
	createdBy := fixture.userID
	inv := createQueuedInvestigation(t, pool, fixture, &createdBy)

	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION block_investigation_event_fn() RETURNS trigger AS $$
		BEGIN
			RAISE EXCEPTION 'blocked investigation event';
		END;
		$$ LANGUAGE plpgsql;`); err != nil {
		t.Fatalf("create event blocking function: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TRIGGER block_investigation_event_trg
			BEFORE INSERT ON incident_events
			FOR EACH ROW WHEN (NEW.kind = 'investigation_completed')
			EXECUTE FUNCTION block_investigation_event_fn();`); err != nil {
		t.Fatalf("install event blocking trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS block_investigation_event_trg ON incident_events`)
		_, _ = pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS block_investigation_event_fn()`)
	})

	// The stub's deterministic demo trajectory can run again on the retry.
	svc := newInvestigationService(t, pool, &ai.Stub{}, nil)
	w := NewInvestigationWorker(svc, newInvestigationWorkerLogger(), nil, 0)

	err := w.Work(ctx, investigationJob(inv.ID, 1, 3))
	if err == nil {
		t.Fatal("Work = nil, want the blocked event to fail the transaction")
	}

	store := copilot.NewPostgresStore(pool)
	afterFail, err := store.GetByID(ctx, inv.ID)
	if err != nil {
		t.Fatalf("load investigation after blocked event: %v", err)
	}
	if afterFail.Status != copilot.StatusRunning {
		t.Fatalf("status = %s, want running (completion must roll back)", afterFail.Status)
	}
	if afterFail.CompletedAt != nil || afterFail.ErrorCode != "" {
		t.Fatalf("row = %+v, want an active row with no terminal code", afterFail)
	}
	if events := investigationEvents(t, pool, fixture.incidentID); len(events) != 0 {
		t.Fatalf("blocked event still wrote a timeline row: %+v", events)
	}

	if _, err := pool.Exec(ctx,
		`DROP TRIGGER IF EXISTS block_investigation_event_trg ON incident_events`); err != nil {
		t.Fatalf("remove event blocking trigger: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`DROP FUNCTION IF EXISTS block_investigation_event_fn()`); err != nil {
		t.Fatalf("remove event blocking function: %v", err)
	}
	if err := w.Work(ctx, investigationJob(inv.ID, 2, 3)); err != nil {
		t.Fatalf("retry Work: %v", err)
	}

	got, err := store.GetByID(ctx, inv.ID)
	if err != nil {
		t.Fatalf("load investigation after retry: %v", err)
	}
	if got.Status != copilot.StatusOK {
		t.Fatalf("status = %s, want ok after retry", got.Status)
	}
	if got.CompletedAt == nil || got.Attempts != 2 {
		t.Fatalf("row = %+v, want completed at attempt 2", got)
	}
	events := investigationEvents(t, pool, fixture.incidentID)
	if len(events) != 1 || events[0].kind != "investigation_completed" {
		t.Fatalf("events = %+v, want exactly one investigation_completed", events)
	}
}

func TestInvestigationWorker_NonMemberFailsAsActorRemoved(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	fixture := seedInvestigationFixture(t, pool)
	outsider := testutil.CreateVerifiedUser(t, pool, "outsider-"+uuid.NewString()+"@example.com", "correct horse battery staple")
	inv := createQueuedInvestigation(t, pool, fixture, &outsider)

	provider := &ai.Stub{}
	svc := newInvestigationService(t, pool, provider, nil)
	w := NewInvestigationWorker(svc, newInvestigationWorkerLogger(), nil, 0)
	if err := w.Work(ctx, investigationJob(inv.ID, 1, 3)); err != nil {
		t.Fatalf("Work: %v", err)
	}

	got, err := copilot.NewPostgresStore(pool).GetByID(ctx, inv.ID)
	if err != nil {
		t.Fatalf("load investigation: %v", err)
	}
	if got.Status != copilot.StatusFailed || got.ErrorCode != "actor_removed" {
		t.Fatalf("row = %+v, want failed/actor_removed", got)
	}
	if provider.ChatCalls() != 0 {
		t.Fatalf("provider called %d times for a non-member", provider.ChatCalls())
	}
	if events := investigationEvents(t, pool, fixture.incidentID); len(events) != 0 {
		t.Fatalf("actor_removed wrote a timeline event: %+v", events)
	}
}

func TestInvestigationWorker_NilCreatorFailsAsActorRemoved(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	fixture := seedInvestigationFixture(t, pool)
	inv := createQueuedInvestigation(t, pool, fixture, nil)

	svc := newInvestigationService(t, pool, &ai.Stub{}, nil)
	w := NewInvestigationWorker(svc, newInvestigationWorkerLogger(), nil, 0)
	if err := w.Work(ctx, investigationJob(inv.ID, 1, 3)); err != nil {
		t.Fatalf("Work: %v", err)
	}

	got, err := copilot.NewPostgresStore(pool).GetByID(ctx, inv.ID)
	if err != nil {
		t.Fatalf("load investigation: %v", err)
	}
	if got.Status != copilot.StatusFailed || got.ErrorCode != "actor_removed" {
		t.Fatalf("row = %+v, want failed/actor_removed", got)
	}
}

func TestInvestigationWorker_NilServiceIsNoOp(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	fixture := seedInvestigationFixture(t, pool)
	createdBy := fixture.userID
	inv := createQueuedInvestigation(t, pool, fixture, &createdBy)

	w := NewInvestigationWorker(nil, newInvestigationWorkerLogger(), nil, 0)
	if err := w.Work(context.Background(), investigationJob(inv.ID, 1, 3)); err != nil {
		t.Fatalf("Work with a nil service: %v", err)
	}
	got, err := copilot.NewPostgresStore(pool).GetByID(context.Background(), inv.ID)
	if err != nil {
		t.Fatalf("load investigation: %v", err)
	}
	if got.Status != copilot.StatusQueued {
		t.Fatalf("status = %s, want queued", got.Status)
	}
}

func TestInvestigationWorker_MissingRowIsNoOp(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	svc := newInvestigationService(t, pool, &ai.Stub{}, nil)
	w := NewInvestigationWorker(svc, newInvestigationWorkerLogger(), nil, 0)
	if err := w.Work(context.Background(), investigationJob(uuid.New(), 1, 3)); err != nil {
		t.Fatalf("Work: %v", err)
	}
}

// TestInvestigationWorker_Timeout covers the two-minute default and a
// configured budget. The worker binary passes CopilotTimeout plus a margin.
func TestInvestigationWorker_Timeout(t *testing.T) {
	if got := NewInvestigationWorker(nil, nil, nil, 0).Timeout(nil); got != 2*time.Minute {
		t.Fatalf("default timeout = %s, want 2m", got)
	}
	if got := NewInvestigationWorker(nil, nil, nil, 3*time.Minute).Timeout(nil); got != 3*time.Minute {
		t.Fatalf("configured timeout = %s, want 3m", got)
	}
}

// TestInvestigationWorker_ReapedRowIsNotRevived covers the stale reaper winning
// before the job starts. MarkRunning/SaveProgress/Complete all return
// ErrNotActive for the same terminal row; Run must leave it alone.
func TestInvestigationWorker_ReapedRowIsNotRevived(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	fixture := seedInvestigationFixture(t, pool)
	createdBy := fixture.userID
	inv := createQueuedInvestigation(t, pool, fixture, &createdBy)

	provider := &ai.Stub{Err: &ai.HTTPError{StatusCode: 503, Body: "provider unavailable"}}
	svc := newInvestigationService(t, pool, provider, nil)

	if _, err := copilot.NewPostgresStore(pool).ReapStale(ctx, fixture.incidentID, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("reap stale investigation: %v", err)
	}
	if err := svc.Run(ctx, inv.ID, 1, 3); err != nil {
		t.Fatalf("Run on a reaped row: %v", err)
	}
	got, err := copilot.NewPostgresStore(pool).GetByID(ctx, inv.ID)
	if err != nil {
		t.Fatalf("load investigation: %v", err)
	}
	if got.Status != copilot.StatusFailed || got.ErrorCode != "stale" {
		t.Fatalf("row = %+v, want failed/stale", got)
	}
	if provider.ChatCalls() != 0 {
		t.Fatalf("provider called %d times for a reaped row", provider.ChatCalls())
	}
	if events := investigationEvents(t, pool, fixture.incidentID); len(events) != 0 {
		t.Fatalf("reaped row wrote a timeline event: %+v", events)
	}
}

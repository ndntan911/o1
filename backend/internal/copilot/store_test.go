package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/testutil"
)

// seedWorkspaceIncident inserts a user, workspace, monitor, monitor_state, and
// incident, returning the workspace and incident ids. Each call gets its own
// rows, so a test can seed two independent tenants.
func seedWorkspaceIncident(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()
	userID := seedUser(t, pool)

	var workspaceID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspaces (id, name, slug, created_by)
		 VALUES (gen_random_uuid(), 'Copilot', $1, $2) RETURNING id`,
		"copilot-"+suffix, userID,
	).Scan(&workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	var monitorID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO monitors (id, workspace_id, name, url, method, interval_seconds, timeout_seconds, created_by)
		 VALUES (gen_random_uuid(), $1, 'API', 'https://example.com', 'GET', 60, 10, $2) RETURNING id`,
		workspaceID, userID,
	).Scan(&monitorID); err != nil {
		t.Fatalf("seed monitor: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO monitor_state (monitor_id) VALUES ($1)`, monitorID,
	); err != nil {
		t.Fatalf("seed monitor state: %v", err)
	}

	var incidentID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO incidents (id, workspace_id, monitor_id, status)
		 VALUES (gen_random_uuid(), $1, $2, 'open') RETURNING id`,
		workspaceID, monitorID,
	).Scan(&incidentID); err != nil {
		t.Fatalf("seed incident: %v", err)
	}

	return workspaceID, incidentID
}

// seedUser inserts a verified user so tests can satisfy the created_by foreign
// key. The caller gets one user per workspace fixture.
func seedUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var userID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (id, email, password_hash, email_verified_at)
		 VALUES (gen_random_uuid(), $1, 'x', now()) RETURNING id`,
		"copilot-"+uuid.NewString()+"@example.com",
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return userID
}

// insertInvestigationRaw writes a row directly so a test can control
// created_at and status without going through Store.
func insertInvestigationRaw(t *testing.T, pool *pgxpool.Pool, workspaceID, incidentID uuid.UUID, status Status, createdAt time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO incident_investigations
		   (id, workspace_id, incident_id, status, model, prompt_version, created_at)
		 VALUES ($1, $2, $3, $4, 'stub', 'test', $5)`,
		id, workspaceID, incidentID, status, createdAt,
	); err != nil {
		t.Fatalf("insert raw investigation: %v", err)
	}
	return id
}

// assertTrace compares traces semantically: jsonb rewrites whitespace inside
// raw JSON values, so byte equality is too strict.
func assertTrace(t *testing.T, got, want Trace) {
	t.Helper()
	if !reflect.DeepEqual(got.Steps, want.Steps) {
		t.Fatalf("steps = %+v, want %+v", got.Steps, want.Steps)
	}
	if len(got.Calls) != len(want.Calls) {
		t.Fatalf("calls = %+v, want %+v", got.Calls, want.Calls)
	}
	for index := range want.Calls {
		g, w := got.Calls[index], want.Calls[index]
		if g.ID != w.ID || g.Name != w.Name || g.ArgsSummary != w.ArgsSummary ||
			g.ArgsHash != w.ArgsHash || g.IsError != w.IsError || g.DurationMS != w.DurationMS ||
			!jsonEqual(g.Result, w.Result) {
			t.Fatalf("call %d = %+v, want %+v", index, g, w)
		}
	}
	if !reflect.DeepEqual(got.ModelCalls, want.ModelCalls) {
		t.Fatalf("model calls = %+v, want %+v", got.ModelCalls, want.ModelCalls)
	}
}

func jsonEqual(got, want json.RawMessage) bool {
	if len(got) == 0 || len(want) == 0 {
		return len(got) == len(want)
	}
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		return false
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		return false
	}
	return reflect.DeepEqual(gotValue, wantValue)
}

func TestPostgresStore_CreateAndLatest(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ws, inc := seedWorkspaceIncident(t, pool)
	s := NewPostgresStore(pool)

	created, err := s.Create(context.Background(), Investigation{
		ID: uuid.New(), WorkspaceID: ws, IncidentID: inc,
		Status: StatusQueued, Model: "stub", PromptVersion: "test",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.Latest(context.Background(), ws, inc)
	if err != nil || got == nil || got.ID != created.ID {
		t.Fatalf("latest = %+v, %v", got, err)
	}
	if got.Status != StatusQueued {
		t.Fatalf("status = %q, want %q", got.Status, StatusQueued)
	}
	if got.SeedHash != "" {
		t.Fatalf("seed hash = %q, want empty default", got.SeedHash)
	}
	if got.Result != nil {
		t.Fatalf("result = %+v, want nil", got.Result)
	}

	// No row yet for another incident: Latest must report absence, not error.
	otherWS, otherIncident := seedWorkspaceIncident(t, pool)
	none, err := s.Latest(context.Background(), otherWS, otherIncident)
	if err != nil || none != nil {
		t.Fatalf("latest missing = %+v, %v; want nil, nil", none, err)
	}
}

func TestPostgresStore_OneActiveAndReapStale(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	ws, inc := seedWorkspaceIncident(t, pool)
	s := NewPostgresStore(pool)

	queued, err := s.Create(ctx, Investigation{
		ID: uuid.New(), WorkspaceID: ws, IncidentID: inc,
		Status: StatusQueued, Model: "stub", PromptVersion: "test",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Create(ctx, Investigation{
		ID: uuid.New(), WorkspaceID: ws, IncidentID: inc,
		Status: StatusQueued, Model: "stub", PromptVersion: "test",
		CreatedAt: time.Now().UTC(),
	}); !errors.Is(err, ErrActive) {
		t.Fatalf("second active create err = %v, want ErrActive", err)
	}

	oldWS, oldInc := seedWorkspaceIncident(t, pool)
	oldID := insertInvestigationRaw(t, pool, oldWS, oldInc, StatusRunning, time.Now().Add(-10*time.Minute))

	freshWS, freshInc := seedWorkspaceIncident(t, pool)
	freshID := insertInvestigationRaw(t, pool, freshWS, freshInc, StatusRunning, time.Now())

	reaped, err := s.ReapStale(ctx, oldInc, time.Now().Add(-5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if reaped != 1 {
		t.Fatalf("reaped = %d, want 1", reaped)
	}

	old, err := s.GetByID(ctx, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != StatusFailed || old.ErrorCode != "stale" || old.CompletedAt == nil {
		t.Fatalf("old row = %+v, want failed/stale/completed", old)
	}

	fresh, err := s.GetByID(ctx, freshID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != StatusRunning || fresh.CompletedAt != nil {
		t.Fatalf("fresh row = %+v, want still running", fresh)
	}

	// The queued row is newer than the cutoff, so it is untouched too.
	stillQueued, err := s.GetByID(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillQueued.Status != StatusQueued {
		t.Fatalf("queued row status = %q, want queued", stillQueued.Status)
	}

	// A second sweep finds nothing.
	reaped, err = s.ReapStale(ctx, oldInc, time.Now().Add(-5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if reaped != 0 {
		t.Fatalf("second reap = %d, want 0", reaped)
	}

	// ReapAllStale is not scoped to one incident; it takes every active row
	// older than the cutoff, which is now in the future relative to the rows.
	reaped, err = s.ReapAllStale(ctx, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if reaped != 2 {
		t.Fatalf("reap all = %d, want 2 (running and queued rows)", reaped)
	}
	fresh, err = s.GetByID(ctx, freshID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != StatusFailed || fresh.ErrorCode != "stale" {
		t.Fatalf("fresh row after reap all = %+v, want failed/stale", fresh)
	}
	stillQueued, err = s.GetByID(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillQueued.Status != StatusFailed || stillQueued.ErrorCode != "stale" {
		t.Fatalf("queued row after reap all = %+v, want failed/stale", stillQueued)
	}
}

func TestPostgresStore_GetIsWorkspaceScoped(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	wsA, incA := seedWorkspaceIncident(t, pool)
	wsB, incB := seedWorkspaceIncident(t, pool)
	s := NewPostgresStore(pool)

	created, err := s.Create(ctx, Investigation{
		ID: uuid.New(), WorkspaceID: wsA, IncidentID: incA,
		Status: StatusQueued, Model: "stub", PromptVersion: "test",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(ctx, wsA, incA, created.ID)
	if err != nil || got.ID != created.ID {
		t.Fatalf("get own workspace = %+v, %v", got, err)
	}

	if _, err := s.Get(ctx, wsB, incB, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace get err = %v, want ErrNotFound", err)
	}

	// GetByID is the worker's unscoped lookup.
	if got, err := s.GetByID(ctx, created.ID); err != nil || got.ID != created.ID {
		t.Fatalf("get by id = %+v, %v", got, err)
	}
	if _, err := s.GetByID(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get by unknown id err = %v, want ErrNotFound", err)
	}
}

func TestPostgresStore_RoundTrip(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	ws, inc := seedWorkspaceIncident(t, pool)
	s := NewPostgresStore(pool)

	creator := seedUser(t, pool)
	trace := Trace{
		Steps: []Step{{Index: 0, Tool: "get_monitor", ArgsSummary: "{}", Status: "ok", DurationMS: 12}},
		Calls: []ToolCallRecord{{
			ID: "call-1", Name: "get_monitor", ArgsSummary: "{}", ArgsHash: "sha256:args-1",
			Result: json.RawMessage(`{"status":"up"}`), DurationMS: 12,
		}},
		ModelCalls: []ModelCallRecord{{
			Index: 0, FinishReason: "tool_calls", InputTokens: 10, OutputTokens: 5, DurationMS: 40,
		}},
	}

	created, err := s.Create(ctx, Investigation{
		ID: uuid.New(), WorkspaceID: ws, IncidentID: inc,
		Status: StatusQueued, Model: "stub", PromptVersion: "v1", SeedHash: "seed-1",
		Result: &Result{
			Sufficient: true, Assessment: "api is up",
			Hypotheses: []Hypothesis{{
				Rank: 1, Cause: "none", Confidence: "high",
				Evidence: []Evidence{{ToolCallID: "call-1", Pointer: "/status", Value: json.RawMessage(`"up"`), Note: "up"}},
			}},
			NextChecks: []string{"watch"},
		},
		Trace: trace, Attempts: 1, ToolCalls: 1, InputTokens: 10, OutputTokens: 5,
		DurationMS: 100, CreatedBy: &creator, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.CreatedBy == nil || *created.CreatedBy != creator {
		t.Fatalf("created by = %v, want %v", created.CreatedBy, creator)
	}
	assertTrace(t, created.Trace, trace)
	if created.Result == nil || created.Result.Assessment != "api is up" {
		t.Fatalf("created result = %+v", created.Result)
	}
	if created.CompletedAt != nil {
		t.Fatalf("created completed at = %v, want nil", created.CompletedAt)
	}

	if err := s.MarkRunning(ctx, created.ID, 2, "seed-2"); err != nil {
		t.Fatal(err)
	}
	running, err := s.Get(ctx, ws, inc, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if running.Status != StatusRunning || running.Attempts != 2 || running.SeedHash != "seed-2" {
		t.Fatalf("running row = %+v", running)
	}

	progressTrace := Trace{
		Steps: []Step{{Index: 1, Tool: "list_checks", ArgsSummary: `{"limit":5}`, Status: "ok", DurationMS: 30}},
		Calls: []ToolCallRecord{{
			ID: "call-2", Name: "list_checks", ArgsSummary: `{"limit":5}`, ArgsHash: "sha256:args-2",
			Result: json.RawMessage(`[]`), DurationMS: 30,
		}},
		ModelCalls: []ModelCallRecord{{
			Index: 1, FinishReason: "tool_calls", InputTokens: 20, OutputTokens: 10, DurationMS: 50,
		}},
	}
	if err := s.SaveProgress(ctx, created.ID, progressTrace, 2, 3, 20, 10); err != nil {
		t.Fatal(err)
	}
	progressed, err := s.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertTrace(t, progressed.Trace, progressTrace)
	if progressed.Attempts != 2 || progressed.ToolCalls != 3 || progressed.InputTokens != 20 || progressed.OutputTokens != 10 {
		t.Fatalf("progress counters = %+v", progressed)
	}

	final := Result{
		Sufficient: false, Assessment: "still investigating",
		Hypotheses: []Hypothesis{},
		NextChecks: []string{"check db"},
	}
	completedAt := time.Now().UTC()
	if err := s.Complete(ctx, Investigation{
		ID: created.ID, Status: StatusIncomplete, Result: &final,
		Trace: progressTrace, DurationMS: 1234,
		InputTokens: 30, OutputTokens: 15, ToolCalls: 3,
		CompletedAt: &completedAt,
	}); err != nil {
		t.Fatal(err)
	}
	done, err := s.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != StatusIncomplete || done.DurationMS != 1234 {
		t.Fatalf("completed row = %+v", done)
	}
	if done.ErrorCode != "" {
		t.Fatalf("completed error code = %q, want empty", done.ErrorCode)
	}
	if done.CompletedAt == nil {
		t.Fatal("completed at = nil, want a timestamp")
	}
	if done.Result == nil || done.Result.Assessment != "still investigating" {
		t.Fatalf("completed result = %+v", done.Result)
	}
	if !reflect.DeepEqual(done.Result.NextChecks, []string{"check db"}) {
		t.Fatalf("completed next checks = %v", done.Result.NextChecks)
	}
	if done.InputTokens != 30 || done.OutputTokens != 15 || done.ToolCalls != 3 {
		t.Fatalf("completed counters = %+v", done)
	}
}

func TestPostgresStore_CompleteFailed(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	ws, inc := seedWorkspaceIncident(t, pool)
	s := NewPostgresStore(pool)

	created, err := s.Create(ctx, Investigation{
		ID: uuid.New(), WorkspaceID: ws, IncidentID: inc,
		Status: StatusRunning, Model: "stub", PromptVersion: "test",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Complete(ctx, Investigation{
		ID: created.ID, Status: StatusFailed, ErrorCode: "provider_error",
		Trace: Trace{}, DurationMS: 900,
	}); err != nil {
		t.Fatal(err)
	}
	failed, err := s.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != StatusFailed || failed.ErrorCode != "provider_error" || failed.Result != nil {
		t.Fatalf("failed row = %+v", failed)
	}
}

func TestPostgresStore_StaleRowCannotBeRevived(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	ws, inc := seedWorkspaceIncident(t, pool)
	s := NewPostgresStore(pool)

	id := insertInvestigationRaw(t, pool, ws, inc, StatusRunning, time.Now().Add(-10*time.Minute))
	reaped, err := s.ReapStale(ctx, inc, time.Now().Add(-5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if reaped != 1 {
		t.Fatalf("reaped = %d, want 1", reaped)
	}

	if err := s.MarkRunning(ctx, id, 2, "seed-2"); !errors.Is(err, ErrNotActive) {
		t.Fatalf("mark running stale row err = %v, want ErrNotActive", err)
	}
	if err := s.SaveProgress(ctx, id, Trace{}, 2, 0, 0, 0); !errors.Is(err, ErrNotActive) {
		t.Fatalf("save progress stale row err = %v, want ErrNotActive", err)
	}
	if err := s.Complete(ctx, Investigation{ID: id, Status: StatusOK}); !errors.Is(err, ErrNotActive) {
		t.Fatalf("complete stale row err = %v, want ErrNotActive", err)
	}

	got, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusFailed || got.ErrorCode != "stale" || got.CompletedAt == nil {
		t.Fatalf("stale row after late writes = %+v, want failed/stale/completed", got)
	}
}

func TestPostgresStore_CreateTxRollsBack(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	ws, inc := seedWorkspaceIncident(t, pool)
	s := NewPostgresStore(pool)

	inv := Investigation{
		ID: uuid.New(), WorkspaceID: ws, IncidentID: inc,
		Status: StatusQueued, Model: "stub", PromptVersion: "test",
		CreatedAt: time.Now().UTC(),
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTx(ctx, tx, inv); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	none, err := s.Latest(ctx, ws, inc)
	if err != nil || none != nil {
		t.Fatalf("latest after rollback = %+v, %v; want nil, nil", none, err)
	}

	created, err := s.Create(ctx, inv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Latest(ctx, ws, inc)
	if err != nil || got == nil || got.ID != created.ID {
		t.Fatalf("latest after create = %+v, %v", got, err)
	}
}

func TestPostgresStore_Counts(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	wsA, incA := seedWorkspaceIncident(t, pool)
	_, incB := seedWorkspaceIncident(t, pool)
	wsB, incC := seedWorkspaceIncident(t, pool)
	s := NewPostgresStore(pool)

	for _, row := range []struct {
		ws, inc uuid.UUID
	}{
		{wsA, incA},
		{wsA, incB},
		{wsB, incC},
	} {
		if _, err := s.Create(ctx, Investigation{
			ID: uuid.New(), WorkspaceID: row.ws, IncidentID: row.inc,
			Status: StatusOK, Model: "stub", PromptVersion: "test",
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	count, err := s.CountForIncidentSince(ctx, incA, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("incident count = %d, want 1", count)
	}

	count, err = s.CountForWorkspaceSince(ctx, wsA, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("workspace count = %d, want 2", count)
	}

	count, err = s.CountForWorkspaceSince(ctx, wsB, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("other workspace count = %d, want 1", count)
	}

	count, err = s.CountForIncidentSince(ctx, incA, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("future count = %d, want 0", count)
	}
}

package admin_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/admin"
	"o1/internal/db/sqlc"
	"o1/internal/monitors"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

type testEnv struct {
	t       *testing.T
	pool    *pgxpool.Pool
	svc     *admin.Service
	ownerID uuid.UUID
	adminID uuid.UUID
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	pool, cleanup := testutil.NewPostgres(t)
	t.Cleanup(cleanup)
	ownerID := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	adminID := testutil.CreateVerifiedUser(t, pool, "admin@example.com", "correct horse battery staple")
	if _, err := pool.Exec(context.Background(), "UPDATE users SET is_admin = true WHERE id = $1", adminID); err != nil {
		t.Fatalf("promote admin: %v", err)
	}
	return &testEnv{
		t:       t,
		pool:    pool,
		svc:     admin.NewService(pool, sqlc.New(pool)),
		ownerID: ownerID,
		adminID: adminID,
	}
}

func (e *testEnv) createWorkspace(t *testing.T, name string) workspaces.Workspace {
	t.Helper()
	ws, err := workspaces.NewService(e.pool, sqlc.New(e.pool)).Create(context.Background(), e.ownerID, name)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	return ws
}

func (e *testEnv) createMonitor(t *testing.T, workspaceID uuid.UUID, rawURL string) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new monitor id: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO monitors
			(id, workspace_id, name, url, method, expected_status,
			 interval_seconds, timeout_seconds, confirmation_threshold,
			 recovery_threshold, created_by)
		VALUES ($1, $2, 'admin monitor', $3, 'GET', '2xx', 60, 10, 2, 2, $4)`,
		id, workspaceID, rawURL, e.ownerID); err != nil {
		t.Fatalf("insert monitor: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO monitor_state (monitor_id) VALUES ($1)`, id); err != nil {
		t.Fatalf("insert monitor state: %v", err)
	}
	return id
}

func TestSuspendAndUnsuspendAreIdempotentAndAudited(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "Suspend Me")
	env.createMonitor(t, ws.ID, "https://example.com/health")

	for i := 0; i < 2; i++ {
		if err := env.svc.Suspend(context.Background(), env.adminID, ws.ID, "abuse"); err != nil {
			t.Fatalf("suspend %d: %v", i, err)
		}
	}
	got, err := env.svc.GetWorkspace(context.Background(), ws.ID)
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if got.SuspendedAt == nil || got.SuspendedReason == nil || *got.SuspendedReason != "abuse" {
		t.Fatalf("workspace = %+v", got)
	}

	events, err := env.svc.ListSecurityEvents(context.Background(), "workspace_suspended", &ws.ID, nil, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("suspended events = %d, want 2", len(events))
	}
	if events[0].ActorUserID == nil || *events[0].ActorUserID != env.adminID {
		t.Fatalf("event actor = %v", events[0].ActorUserID)
	}

	if err := env.svc.Unsuspend(context.Background(), env.adminID, ws.ID); err != nil {
		t.Fatalf("unsuspend: %v", err)
	}
	got, err = env.svc.GetWorkspace(context.Background(), ws.ID)
	if err != nil {
		t.Fatalf("get workspace after unsuspend: %v", err)
	}
	if got.SuspendedAt != nil || got.SuspendedReason != nil {
		t.Fatalf("workspace after unsuspend = %+v", got)
	}
	events, err = env.svc.ListSecurityEvents(context.Background(), "workspace_unsuspended", &ws.ID, nil, 10)
	if err != nil {
		t.Fatalf("list unsuspend events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("unsuspended events = %d, want 1", len(events))
	}
}

func TestSuspendRejectsInvalidInputAndMissingWorkspace(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "Validation")

	for _, reason := range []string{"", "   ", string(make([]byte, 501))} {
		if err := env.svc.Suspend(context.Background(), env.adminID, ws.ID, reason); !errors.Is(err, admin.ErrValidation) {
			t.Errorf("reason length %d: err = %v, want ErrValidation", len(reason), err)
		}
	}
	if err := env.svc.Suspend(context.Background(), env.adminID, uuid.New(), "abuse"); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("unknown workspace err = %v, want ErrNotFound", err)
	}
	if err := env.svc.Unsuspend(context.Background(), env.adminID, uuid.New()); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("unknown workspace unsuspend err = %v, want ErrNotFound", err)
	}
}

func TestListWorkspacesReturnsCountsAndFiltersSuspension(t *testing.T) {
	env := newTestEnv(t)
	first := env.createWorkspace(t, "First")
	second := env.createWorkspace(t, "Second")
	env.createMonitor(t, first.ID, "https://example.com/one")
	env.createMonitor(t, first.ID, "https://example.com/two")
	if err := env.svc.Suspend(context.Background(), env.adminID, second.ID, "abuse"); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	all, err := env.svc.ListWorkspaces(context.Background(), "econd", nil, nil, 10)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 1 || all[0].ID != second.ID || all[0].MemberCount != 1 || all[0].SuspendedAt == nil {
		t.Fatalf("filtered workspaces = %+v", all)
	}

	suspended := true
	suspendedOnly, err := env.svc.ListWorkspaces(context.Background(), "", &suspended, nil, 10)
	if err != nil {
		t.Fatalf("list suspended: %v", err)
	}
	if len(suspendedOnly) != 1 || suspendedOnly[0].ID != second.ID {
		t.Fatalf("suspended only = %+v", suspendedOnly)
	}

	page1, err := env.svc.ListWorkspaces(context.Background(), "", nil, nil, 1)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1) != 1 {
		t.Fatalf("page 1 = %+v", page1)
	}
	page2, err := env.svc.ListWorkspaces(context.Background(), "", nil,
		&monitors.Cursor{Time: page1[0].CreatedAt, ID: page1[0].ID}, 1)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].ID == page1[0].ID {
		t.Fatalf("page 2 = %+v", page2)
	}
	if page2[0].ID != first.ID && page2[0].ID != second.ID {
		t.Fatalf("unexpected page 2 row %+v", page2[0])
	}
}

func TestListMonitorsRedactsURLsAndFiltersWorkspace(t *testing.T) {
	env := newTestEnv(t)
	first := env.createWorkspace(t, "First")
	second := env.createWorkspace(t, "Second")
	wantID := env.createMonitor(t, first.ID, "https://example.com/health?token=super-secret#fragment")
	env.createMonitor(t, second.ID, "https://api.example.com/health")

	list, err := env.svc.ListMonitors(context.Background(), &first.ID, "", nil, 10)
	if err != nil {
		t.Fatalf("list monitors: %v", err)
	}
	if len(list) != 1 || list[0].ID != wantID {
		t.Fatalf("filtered monitors = %+v", list)
	}
	if list[0].URL != "https://example.com/health" {
		t.Fatalf("url = %q, want query and fragment stripped", list[0].URL)
	}
	if list[0].WorkspaceName != "First" {
		t.Fatalf("workspace name = %q", list[0].WorkspaceName)
	}

	all, err := env.svc.ListMonitors(context.Background(), nil, "", nil, 10)
	if err != nil {
		t.Fatalf("list all monitors: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all monitors = %+v", all)
	}
}

func TestListSecurityEventsOrdersNewestFirst(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "Events")
	if err := env.svc.Suspend(context.Background(), env.adminID, ws.ID, "first"); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := env.svc.Suspend(context.Background(), env.adminID, ws.ID, "second"); err != nil {
		t.Fatalf("second suspend: %v", err)
	}

	events, err := env.svc.ListSecurityEvents(context.Background(), "workspace_suspended", &ws.ID, nil, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 2 || !events[0].CreatedAt.After(events[1].CreatedAt) {
		t.Fatalf("events = %+v", events)
	}
}

// TestSuspendWaitsForProbeLock holds the probe path's FOR KEY SHARE lock on a
// workspace, then suspends it. A plain UPDATE (FOR NO KEY UPDATE) is compatible
// with KEY SHARE and would commit while the probe is still in flight, so
// Suspend must take FOR UPDATE first and block until the probe releases.
func TestSuspendWaitsForProbeLock(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "Suspend Race")

	assertWaitsForProbeLock(t, env, ws.ID, func(ctx context.Context) error {
		return env.svc.Suspend(ctx, env.adminID, ws.ID, "abuse")
	})

	got, err := env.svc.GetWorkspace(context.Background(), ws.ID)
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if got.SuspendedAt == nil {
		t.Fatal("workspace was not suspended")
	}
}

// TestUnsuspendWaitsForProbeLock covers the same serialization for clearing a
// suspension.
func TestUnsuspendWaitsForProbeLock(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "Unsuspend Race")
	if err := env.svc.Suspend(context.Background(), env.adminID, ws.ID, "abuse"); err != nil {
		t.Fatalf("suspend before test: %v", err)
	}

	assertWaitsForProbeLock(t, env, ws.ID, func(ctx context.Context) error {
		return env.svc.Unsuspend(ctx, env.adminID, ws.ID)
	})

	got, err := env.svc.GetWorkspace(context.Background(), ws.ID)
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if got.SuspendedAt != nil {
		t.Fatal("workspace is still suspended")
	}
}

// assertWaitsForProbeLock takes FOR KEY SHARE on the workspace, runs call
// concurrently, and verifies call stays blocked until the lock is released.
func assertWaitsForProbeLock(t *testing.T, env *testEnv, workspaceID uuid.UUID, call func(context.Context) error) {
	t.Helper()
	ctx := context.Background()
	tx, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin probe tx: %v", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	if _, err := tx.Exec(ctx, `SELECT id FROM workspaces WHERE id = $1 FOR KEY SHARE`, workspaceID); err != nil {
		t.Fatalf("lock workspace for probe: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- call(context.Background())
	}()

	waitForWorkspaceRowLockWait(t, env.pool)

	select {
	case err := <-done:
		t.Fatalf("call finished while the probe lock was held: %v", err)
	default:
	}

	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("release probe lock: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("call after lock release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("call did not finish after the probe lock was released")
	}
}

func waitForWorkspaceRowLockWait(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND pid <> pg_backend_pid()`).Scan(&waiting); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("suspension did not block on the workspace row lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

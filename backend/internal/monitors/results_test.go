package monitors_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"o1/internal/monitors"
)

func (e *testEnv) createMonitor(t *testing.T) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ws := e.createWorkspace(t, "results")
	created, err := e.svc.Create(context.Background(), e.ownerID, ws, validInput(0))
	if err != nil {
		t.Fatalf("create monitor: %v", err)
	}
	return ws, created.ID
}

func (e *testEnv) insertResult(t *testing.T, monitorID, workspaceID uuid.UUID, checkedAt time.Time, status string, statusCode int, latency int) {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new uuid: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO probe_results
			(id, monitor_id, workspace_id, scheduled_for, checked_at, status, status_code, latency_ms)
		VALUES ($1, $2, $3, $4, $4, $5, $6, $7)`,
		id, monitorID, workspaceID, checkedAt, status, statusCode, latency); err != nil {
		t.Fatalf("insert result: %v", err)
	}
}

func TestSummary_ExcludesNoDataAndComputesP95(t *testing.T) {
	env := newTestEnv(t)
	ws, id := env.createMonitor(t)
	now := time.Now().UTC()
	for i, latency := range []int{100, 200, 300, 400} {
		env.insertResult(t, id, ws, now.Add(-time.Duration(i)*time.Minute), "up", 200, latency)
	}
	env.insertResult(t, id, ws, now.Add(-10*time.Minute), "down", 500, 50)

	sum, err := env.svc.Summary(context.Background(), env.ownerID, ws, id, 24*time.Hour)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum.Window != "24h" || sum.Checks != 5 || sum.Up != 4 || sum.Down != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if sum.Uptime == nil || math.Abs(*sum.Uptime-0.8) > 1e-9 {
		t.Fatalf("uptime = %v", sum.Uptime)
	}
	if sum.P95LatencyMS <= 300 || sum.P95LatencyMS > 400 {
		t.Fatalf("p95 = %v", sum.P95LatencyMS)
	}

	otherWS, other := env.createMonitor(t)
	empty, err := env.svc.Summary(context.Background(), env.ownerID, otherWS, other, 24*time.Hour)
	if err != nil || empty.Checks != 0 || empty.Uptime != nil {
		t.Fatalf("empty summary = %+v, %v", empty, err)
	}
}

func TestResults_PaginatesNewestFirstAndIsTenantScoped(t *testing.T) {
	env := newTestEnv(t)
	ws, id := env.createMonitor(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < 3; i++ {
		env.insertResult(t, id, ws, now.Add(-time.Duration(i)*time.Minute), "up", 200, 100+i)
	}

	page, err := env.svc.Results(context.Background(), env.ownerID, ws, id, 24*time.Hour, nil, 2)
	if err != nil {
		t.Fatalf("Results page 1: %v", err)
	}
	if len(page) != 2 || !page[0].CheckedAt.After(page[1].CheckedAt) {
		t.Fatalf("page 1 = %+v", page)
	}
	last := page[len(page)-1]
	next, err := env.svc.Results(context.Background(), env.ownerID, ws, id, 24*time.Hour,
		&monitors.Cursor{Time: last.CheckedAt, ID: last.ID}, 2)
	if err != nil {
		t.Fatalf("Results page 2: %v", err)
	}
	if len(next) != 1 || !next[0].CheckedAt.Before(last.CheckedAt) {
		t.Fatalf("page 2 = %+v", next)
	}

	outsider := env.createUser(t, "outsider@example.com")
	if _, err := env.svc.Results(context.Background(), outsider, ws, id, 24*time.Hour, nil, 10); !errors.Is(err, monitors.ErrNotFound) {
		t.Fatalf("outsider results err = %v, want ErrNotFound", err)
	}
	if _, err := env.svc.Summary(context.Background(), outsider, ws, id, 24*time.Hour); !errors.Is(err, monitors.ErrNotFound) {
		t.Fatalf("outsider summary err = %v, want ErrNotFound", err)
	}
}

func TestResultsAndSummary_MissingOrCrossWorkspaceMonitorIsNotFound(t *testing.T) {
	env := newTestEnv(t)
	ws, id := env.createMonitor(t)
	otherWS, otherID := env.createMonitor(t)
	now := time.Now().UTC()
	env.insertResult(t, id, ws, now, "up", 200, 100)

	if _, err := env.svc.Results(context.Background(), env.ownerID, ws, uuid.New(), 24*time.Hour, nil, 10); !errors.Is(err, monitors.ErrNotFound) {
		t.Fatalf("missing monitor results err = %v, want ErrNotFound", err)
	}
	if _, err := env.svc.Summary(context.Background(), env.ownerID, ws, uuid.New(), 24*time.Hour); !errors.Is(err, monitors.ErrNotFound) {
		t.Fatalf("missing monitor summary err = %v, want ErrNotFound", err)
	}
	if _, err := env.svc.Results(context.Background(), env.ownerID, ws, otherID, 24*time.Hour, nil, 10); !errors.Is(err, monitors.ErrNotFound) {
		t.Fatalf("cross-workspace results err = %v, want ErrNotFound", err)
	}
	if _, err := env.svc.Summary(context.Background(), env.ownerID, ws, otherID, 24*time.Hour); !errors.Is(err, monitors.ErrNotFound) {
		t.Fatalf("cross-workspace summary err = %v, want ErrNotFound", err)
	}
	if _, err := env.svc.Results(context.Background(), env.ownerID, otherWS, otherID, 24*time.Hour, nil, 10); err != nil {
		t.Fatalf("own results err = %v", err)
	}
}

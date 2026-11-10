package observability_test

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/observability"
	"o1/internal/testutil"
)

func insertRiverJob(t *testing.T, pool *pgxpool.Pool, queue, state string, createdAt time.Time, finalized bool) {
	t.Helper()
	var finalizedAt any
	if finalized {
		finalizedAt = time.Now().UTC()
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO river_job (kind, args, queue, state, created_at, finalized_at)
		VALUES ('test','{}',$1,$2,$3,$4)`,
		queue, state, createdAt, finalizedAt); err != nil {
		t.Fatalf("insert river job: %v", err)
	}
}

func TestQueueCollector_CountsAndOldestRunnable(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, row := range []struct {
		queue, state string
		age          time.Duration
		finalized    bool
	}{
		{"probe", "available", 2 * time.Minute, false},
		{"probe", "running", time.Minute, false},
		{"probe", "discarded", 24 * time.Hour, true},
		{"alert", "completed", time.Hour, true},
	} {
		insertRiverJob(t, pool, row.queue, row.state, now.Add(-row.age), row.finalized)
	}

	m := observability.NewMetrics("o1-worker", "test")
	c := observability.NewQueueCollector(pool, m)
	if err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	body := gatherText(t, m.Registry())
	if !strings.Contains(body, `o1_queue_jobs{queue="probe",state="available"} 1`) {
		t.Fatalf("missing queue gauge:\n%s", body)
	}
	if !strings.Contains(body, `o1_queue_jobs{queue="probe",state="discarded"} 1`) {
		t.Fatalf("missing discarded gauge:\n%s", body)
	}
	if strings.Contains(body, `state="completed"`) {
		t.Fatal("terminal job counted")
	}
	// Oldest age ignores discarded: ~2 minutes, not 24 hours.
	line := regexp.MustCompile(`o1_queue_oldest_job_age_seconds\{queue="probe"\} ([0-9.e+-]+)`).
		FindStringSubmatch(body)
	if line == nil {
		t.Fatalf("missing oldest age:\n%s", body)
	}
	age, err := strconv.ParseFloat(line[1], 64)
	if err != nil {
		t.Fatalf("parse oldest age: %v", err)
	}
	if age < 60 || age > 300 {
		t.Fatalf("oldest age = %fs, want about 120s", age)
	}
}

func TestQueueCollector_ClearsLabelsThatDisappear(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()

	insertRiverJob(t, pool, "probe", "available", time.Now().UTC(), false)
	m := observability.NewMetrics("o1-worker", "test")
	c := observability.NewQueueCollector(pool, m)
	if err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE river_job SET state = 'completed', finalized_at = now()`); err != nil {
		t.Fatalf("complete job: %v", err)
	}
	if err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	body := gatherText(t, m.Registry())
	if !strings.Contains(body, `o1_queue_jobs{queue="probe",state="available"} 0`) {
		t.Fatalf("stale queue gauge was not reset:\n%s", body)
	}
}

func TestIncidentCollector_CountsUnresolved(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()

	ownerID := testutil.CreateVerifiedUser(t, pool, "collector@example.com", "correct horse battery staple")
	var workspaceID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspaces (id, name, slug, created_by)
		 VALUES ($1, 'Collector', $2, $3) RETURNING id`,
		uuid.New(), "collector-"+uuid.NewString(), ownerID).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	newMonitor := func(t *testing.T) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO monitors
				(id, workspace_id, name, url, method, expected_status,
				 interval_seconds, timeout_seconds, confirmation_threshold,
				 recovery_threshold, created_by)
			VALUES ($1, $2, 'collector monitor', 'https://example.com', 'GET', '200-299',
			 60, 5, 2, 2, $3)
			RETURNING id`,
			uuid.New(), workspaceID, ownerID).Scan(&id); err != nil {
			t.Fatalf("insert monitor: %v", err)
		}
		return id
	}
	insertIncident := func(t *testing.T, monitorID uuid.UUID, status string, resolved bool) {
		t.Helper()
		var acknowledgedAt, resolvedAt *time.Time
		now := time.Now().UTC()
		if status == "acknowledged" {
			acknowledgedAt = &now
		}
		if resolved {
			resolvedAt = &now
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO incidents
				(id, workspace_id, monitor_id, status, started_at, acknowledged_at, resolved_at)
			VALUES ($1, $2, $3, $4, now(), $5, $6)`,
			uuid.New(), workspaceID, monitorID, status, acknowledgedAt, resolvedAt); err != nil {
			t.Fatalf("insert incident: %v", err)
		}
	}

	monitorA := newMonitor(t)
	monitorB := newMonitor(t)
	insertIncident(t, monitorA, "open", false)
	insertIncident(t, monitorB, "acknowledged", false)
	insertIncident(t, monitorB, "resolved", true)

	m := observability.NewMetrics("o1-worker", "test")
	c := observability.NewIncidentCollector(pool, m)
	if err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	body := gatherText(t, m.Registry())
	if !strings.Contains(body, "o1_incidents_open 2") {
		t.Fatalf("missing incident gauge:\n%s", body)
	}
}

func TestPoolCollector_ExposesStates(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping pool: %v", err)
	}

	m := observability.NewMetrics("o1-worker", "test")
	m.Register(observability.NewPoolCollector(pool))
	body := gatherText(t, m.Registry())
	for _, want := range []string{
		`o1_db_pool_connections{state="acquired"}`,
		`o1_db_pool_connections{state="idle"}`,
		`o1_db_pool_connections{state="total"}`,
		`o1_db_pool_connections{state="max"}`,
		"o1_db_pool_acquires_total",
		"o1_db_pool_acquire_duration_seconds_total",
		"o1_db_pool_empty_acquires_total",
		"o1_db_pool_canceled_acquires_total",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pool metrics missing %s:\n%s", want, body)
		}
	}
}

func TestCollector_RefreshFailureKeepsLastValue(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	insertRiverJob(t, pool, "probe", "available", time.Now().UTC(), false)

	m := observability.NewMetrics("o1-worker", "test")
	c := observability.NewQueueCollector(pool, m)
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Refresh(ctx); err == nil {
		t.Fatal("Refresh succeeded with a canceled context")
	}
	body := gatherText(t, m.Registry())
	if !strings.Contains(body, `o1_queue_jobs{queue="probe",state="available"} 1`) {
		t.Fatalf("failed refresh changed the gauge:\n%s", body)
	}
}

func TestQueueCollector_StartStopsOnCancel(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	insertRiverJob(t, pool, "probe", "available", time.Now().UTC(), false)

	m := observability.NewMetrics("o1-worker", "test")
	c := observability.NewQueueCollector(pool, m)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Start(ctx)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(gatherText(t, m.Registry()), `o1_queue_jobs{queue="probe",state="available"} 1`) {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("Start did not refresh before the deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after cancellation")
	}
}

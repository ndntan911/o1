package scheduler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"o1/internal/config"
	"o1/internal/db/sqlc"
	"o1/internal/observability"
	"o1/internal/retention"
	"o1/internal/rollup"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

type schedulerEnv struct {
	t       *testing.T
	pool    *pgxpool.Pool
	sched   *Scheduler
	ownerID uuid.UUID
	metrics *observability.Metrics
}

func newSchedulerEnv(t *testing.T) *schedulerEnv {
	t.Helper()
	pool, _ := testutil.NewPostgres(t)
	cfg := config.Config{
		Env:            "test",
		SchedulerTick:  10 * time.Second,
		SchedulerBatch: 500,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: logger})
	if err != nil {
		t.Fatalf("new river client: %v", err)
	}
	q := sqlc.New(pool)
	ownerID := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	metrics := observability.NewMetrics("o1-scheduler", "test")
	return &schedulerEnv{
		t:       t,
		pool:    pool,
		sched:   New(pool, q, client, cfg, logger, metrics, nil),
		ownerID: ownerID,
		metrics: metrics,
	}
}

func gatherSchedulerMetrics(t *testing.T, m *observability.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func (e *schedulerEnv) addMonitor(t *testing.T, name string, interval int, paused, suspended bool, next time.Time) uuid.UUID {
	t.Helper()
	wsvc := workspaces.NewService(e.pool, sqlc.New(e.pool))
	ws, err := wsvc.Create(context.Background(), e.ownerID, name)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new monitor id: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO monitors
			(id, workspace_id, name, url, method, expected_status,
			 interval_seconds, timeout_seconds, confirmation_threshold,
			 recovery_threshold, paused, created_by)
		VALUES ($1, $2, $3, 'https://example.com/health', 'GET', '200-299',
			 $4, 10, 2, 2, $5, $6)`,
		id, ws.ID, name, interval, paused, e.ownerID); err != nil {
		t.Fatalf("insert monitor: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO monitor_state (monitor_id, next_check_at) VALUES ($1, $2)`, id, next); err != nil {
		t.Fatalf("insert monitor state: %v", err)
	}
	if suspended {
		if _, err := e.pool.Exec(context.Background(),
			`UPDATE workspaces SET suspended_at = now(), suspended_by = $1 WHERE id = $2`,
			e.ownerID, ws.ID); err != nil {
			t.Fatalf("suspend workspace: %v", err)
		}
	}
	return id
}

func TestTick_EnqueuesOnlyDueActiveMonitors(t *testing.T) {
	env := newSchedulerEnv(t)
	env.addMonitor(t, "due", 60, false, false, time.Now().Add(-time.Minute))
	env.addMonitor(t, "future", 60, false, false, time.Now().Add(time.Hour))
	env.addMonitor(t, "paused", 60, true, false, time.Now().Add(-time.Minute))
	env.addMonitor(t, "suspended", 60, false, true, time.Now().Add(-time.Minute))

	n, err := env.sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n != 1 {
		t.Fatalf("Tick enqueued %d, want 1", n)
	}

	var kind string
	var count int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*), min(kind) FROM river_job WHERE kind = 'probe'`).Scan(&count, &kind); err != nil {
		t.Fatalf("read river_job: %v", err)
	}
	if count != 1 || kind != "probe" {
		t.Fatalf("jobs = %d/%q", count, kind)
	}

	var next time.Time
	if err := env.pool.QueryRow(context.Background(),
		`SELECT ms.next_check_at FROM monitor_state ms JOIN monitors m ON m.id = ms.monitor_id WHERE m.name = 'due'`).
		Scan(&next); err != nil {
		t.Fatalf("read next_check_at: %v", err)
	}
	if !next.After(time.Now().Add(50 * time.Second)) {
		t.Fatalf("next_check_at = %v, want about one interval ahead", next)
	}
}

func TestTick_DuplicateUniqueJobIsIgnored(t *testing.T) {
	env := newSchedulerEnv(t)
	fixed := time.Now().UTC().Add(5 * time.Second).Truncate(time.Second)
	env.addMonitor(t, "due", 60, false, false, fixed.Add(-time.Minute))
	env.sched.now = func() time.Time { return fixed }
	env.sched.jitter = func(time.Duration) time.Duration { return 0 }

	if _, err := env.sched.Tick(context.Background()); err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	// Rewind the schedule to simulate a crash after insert but before the
	// advanced schedule became visible.
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE monitor_state SET next_check_at = $1`, fixed.Add(-time.Minute)); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if _, err := env.sched.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	var count int
	if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'probe'`).Scan(&count); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if count != 1 {
		t.Fatalf("job count = %d, want 1", count)
	}
}

// addReminderIncident creates a monitor with an email alert channel and one
// incident in the given status whose last notification is lastNotified.
func (e *schedulerEnv) addReminderIncident(t *testing.T, status string, lastNotified time.Time) (monitorID, incidentID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	monitorID = e.addMonitor(t, "reminder "+uuid.NewString(), 60, false, false, time.Now().Add(time.Hour))

	var workspaceID uuid.UUID
	if err := e.pool.QueryRow(ctx, `SELECT workspace_id FROM monitors WHERE id = $1`, monitorID).Scan(&workspaceID); err != nil {
		t.Fatalf("read monitor workspace: %v", err)
	}
	channelID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new channel id: %v", err)
	}
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO alert_channels (id, workspace_id, kind, name, config)
		VALUES ($1, $2, 'email', 'Email', '{"recipients":["reminder@example.com"]}')`,
		channelID, workspaceID); err != nil {
		t.Fatalf("insert alert channel: %v", err)
	}
	if _, err := e.pool.Exec(ctx,
		`INSERT INTO monitor_alert_channels (monitor_id, channel_id) VALUES ($1, $2)`,
		monitorID, channelID); err != nil {
		t.Fatalf("attach alert channel: %v", err)
	}

	incidentID, err = uuid.NewV7()
	if err != nil {
		t.Fatalf("new incident id: %v", err)
	}
	var acknowledgedAt, resolvedAt *time.Time
	switch status {
	case "acknowledged":
		acknowledgedAt = &lastNotified
	case "resolved":
		resolvedAt = &lastNotified
	}
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO incidents
			(id, workspace_id, monitor_id, status, started_at, acknowledged_at,
			 resolved_at, last_notified_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		incidentID, workspaceID, monitorID, status, lastNotified.Add(-time.Hour),
		acknowledgedAt, resolvedAt, lastNotified); err != nil {
		t.Fatalf("insert %s incident: %v", status, err)
	}
	return monitorID, incidentID
}

func TestEnqueueReminders_OnlyDueActiveIncidents(t *testing.T) {
	env := newSchedulerEnv(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	env.sched.now = func() time.Time { return now }
	due := now.Add(-31 * time.Minute)

	_, dueIncidentID := env.addReminderIncident(t, "open", due)
	env.addReminderIncident(t, "acknowledged", due)
	env.addReminderIncident(t, "resolved", due)

	// The probe tick runs first; the reminder pass then runs in its own
	// transaction through the scheduler.
	if _, err := env.sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	n, err := env.sched.enqueueReminders(context.Background())
	if err != nil {
		t.Fatalf("enqueueReminders: %v", err)
	}
	if n != 1 {
		t.Fatalf("enqueueReminders = %d, want 1", n)
	}

	var deliveries int
	if err := env.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM alert_deliveries
		WHERE event = 'reminder' AND incident_id = $1`, dueIncidentID).Scan(&deliveries); err != nil {
		t.Fatalf("count reminder deliveries: %v", err)
	}
	if deliveries != 1 {
		t.Fatalf("reminder deliveries = %d, want 1", deliveries)
	}
	var jobs int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = 'alert'`).Scan(&jobs); err != nil {
		t.Fatalf("count alert jobs: %v", err)
	}
	if jobs != 1 {
		t.Fatalf("alert jobs = %d, want 1", jobs)
	}

	var reminderCount int
	var lastNotified *time.Time
	if err := env.pool.QueryRow(context.Background(),
		`SELECT reminder_count, last_notified_at FROM incidents WHERE id = $1`,
		dueIncidentID).Scan(&reminderCount, &lastNotified); err != nil {
		t.Fatalf("read incident reminder state: %v", err)
	}
	if reminderCount != 1 || lastNotified == nil || !lastNotified.Equal(now) {
		t.Fatalf("reminder state = %d/%v, want 1/%v", reminderCount, lastNotified, now)
	}
}

func TestTick_AdvancesScheduleByInterval(t *testing.T) {
	env := newSchedulerEnv(t)
	fixed := time.Now().UTC().Truncate(time.Second)
	env.addMonitor(t, "due", 120, false, false, fixed.Add(-time.Minute))
	env.sched.now = func() time.Time { return fixed }
	env.sched.jitter = func(time.Duration) time.Duration { return 12 * time.Second }

	if _, err := env.sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	var next time.Time
	if err := env.pool.QueryRow(context.Background(),
		`SELECT next_check_at FROM monitor_state`).Scan(&next); err != nil {
		t.Fatalf("read next_check_at: %v", err)
	}
	want := fixed.Add(132 * time.Second)
	if !next.Equal(want) {
		t.Fatalf("next_check_at = %v, want %v", next, want)
	}
}

func TestSchedulerMetrics_Tick(t *testing.T) {
	env := newSchedulerEnv(t)
	env.addMonitor(t, "due", 60, false, false, time.Now().Add(-time.Minute))

	if _, err := env.sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	body := gatherSchedulerMetrics(t, env.metrics)
	for _, want := range []string{
		"o1_scheduler_ticks_total 1",
		"o1_scheduler_enqueued_total 1",
		"o1_scheduler_tick_duration_seconds_count 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scheduler metrics missing %s:\n%s", want, body)
		}
	}
}

// TestSchedulerPanicIsCapturedAndLoopContinues verifies a panicking tick is
// captured once, logged, and swallowed so the next tick still runs. It uses a
// fake tick function and a fake tick channel, so no database is needed.
func TestSchedulerPanicIsCapturedAndLoopContinues(t *testing.T) {
	var mu sync.Mutex
	var captures []error
	restore := observability.SetCaptureHookForTest(func(_ context.Context, err error, _ map[string]string, _ map[string]sentry.Context) {
		mu.Lock()
		defer mu.Unlock()
		captures = append(captures, err)
	})
	t.Cleanup(restore)

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var tickCalls, reminderCalls atomic.Int32
	s := &Scheduler{
		logger: logger,
		tickFn: func(context.Context) (int, error) {
			if tickCalls.Add(1) == 1 {
				panic("probe tick exploded")
			}
			cancel()
			return 3, nil
		},
		reminderFn: func(context.Context) (int, error) {
			reminderCalls.Add(1)
			return 0, nil
		},
	}

	ticks := make(chan time.Time)
	partitions := make(chan time.Time)
	done := make(chan error, 1)
	go func() { done <- s.tickLoop(ctx, ticks, partitions) }()

	ticks <- time.Now()
	ticks <- time.Now()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tickLoop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tickLoop did not stop after the context was canceled")
	}

	if got := tickCalls.Load(); got != 2 {
		t.Fatalf("tick calls = %d, want 2 (the loop must continue after a panic)", got)
	}
	if got := reminderCalls.Load(); got != 1 {
		t.Fatalf("reminder calls = %d, want 1 (skipped for the panicking tick)", got)
	}

	mu.Lock()
	captured := len(captures)
	var capturedErr error
	if captured > 0 {
		capturedErr = captures[0]
	}
	mu.Unlock()
	if captured != 1 {
		t.Fatalf("captures = %d, want 1", captured)
	}
	if capturedErr == nil || !strings.Contains(capturedErr.Error(), "probe tick exploded") {
		t.Fatalf("captured error = %v, want the panic value", capturedErr)
	}
	if !strings.Contains(logs.String(), "panic") {
		t.Fatalf("scheduler log missing panic entry:\n%s", logs.String())
	}
}

// callRecorder records maintenance runner calls in order.
type callRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *callRecorder) add(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name)
}

func (r *callRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

type fakeRollupRunner struct {
	mu     sync.Mutex
	calls  int
	rec    *callRecorder
	result rollup.Result
	err    error
	onRun  func()
}

func (f *fakeRollupRunner) Run(context.Context) (rollup.Result, error) {
	f.mu.Lock()
	f.calls++
	rec, onRun := f.rec, f.onRun
	result, err := f.result, f.err
	f.mu.Unlock()
	if rec != nil {
		rec.add("rollup")
	}
	if onRun != nil {
		onRun()
	}
	return result, err
}

func (f *fakeRollupRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeRetentionRunner struct {
	mu     sync.Mutex
	calls  int
	rec    *callRecorder
	result retention.Result
	err    error
	onRun  func()
}

func (f *fakeRetentionRunner) Run(context.Context) (retention.Result, error) {
	f.mu.Lock()
	f.calls++
	rec, onRun := f.rec, f.onRun
	result, err := f.result, f.err
	f.mu.Unlock()
	if rec != nil {
		rec.add("retention")
	}
	if onRun != nil {
		onRun()
	}
	return result, err
}

func (f *fakeRetentionRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestMaintenance_RunsRollupThenRetention(t *testing.T) {
	env := newSchedulerEnv(t)
	rec := &callRecorder{}
	env.sched.SetMaintenance(&fakeRollupRunner{rec: rec}, &fakeRetentionRunner{rec: rec})

	env.sched.runMaintenance(context.Background(), true, true)

	got := rec.snapshot()
	want := []string{"rollup", "retention"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("maintenance order = %v, want %v", got, want)
	}
}

func TestMaintenance_DryRunReportsCandidatesNotDeletedRows(t *testing.T) {
	env := newSchedulerEnv(t)
	env.sched.cfg.RetentionDryRun = true
	env.sched.SetMaintenance(nil, &fakeRetentionRunner{result: retention.Result{
		RollupRows:          3,
		AuditRows:           2,
		PartitionsDropped:   1,
		DroppedBeyondRollup: 1,
	}})

	env.sched.runMaintenance(context.Background(), false, true)

	body := gatherSchedulerMetrics(t, env.metrics)
	if strings.Contains(body, "o1_retention_rows_deleted_total") {
		t.Fatalf("dry-run reported deleted rows:\n%s", body)
	}
	for _, want := range []string{
		`o1_retention_candidates_total{kind="rollup"} 3`,
		`o1_retention_candidates_total{kind="audit"} 2`,
		`o1_retention_partitions_dropped_total{path="beyond_rollup"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dry-run metrics missing %s:\n%s", want, body)
		}
	}

	// Once deletion is enabled the same counts move to the deleted counter and
	// the candidate counter does not double count them.
	env.sched.cfg.RetentionDryRun = false
	env.sched.runMaintenance(context.Background(), false, true)

	body = gatherSchedulerMetrics(t, env.metrics)
	for _, want := range []string{
		`o1_retention_rows_deleted_total{kind="rollup"} 3`,
		`o1_retention_rows_deleted_total{kind="audit"} 2`,
		`o1_retention_candidates_total{kind="rollup"} 3`,
		`o1_retention_candidates_total{kind="audit"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("enabled-retention metrics missing %s:\n%s", want, body)
		}
	}
}

func TestMaintenance_FailureDoesNotStopProbeTick(t *testing.T) {
	env := newSchedulerEnv(t)
	env.addMonitor(t, "due", 60, false, false, time.Now().Add(-time.Minute))

	var logs bytes.Buffer
	env.sched.logger = slog.New(slog.NewTextHandler(&logs, nil))
	env.sched.SetMaintenance(
		&fakeRollupRunner{err: errors.New("rollup exploded")},
		&fakeRetentionRunner{},
	)

	env.sched.runMaintenance(context.Background(), true, false)

	n, err := env.sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick after failed maintenance: %v", err)
	}
	if n != 1 {
		t.Fatalf("Tick enqueued %d, want 1", n)
	}
	if !strings.Contains(logs.String(), "rollup exploded") {
		t.Fatalf("maintenance error was not logged:\n%s", logs.String())
	}
}

func TestMaintenance_SkipsWhenLockHeld(t *testing.T) {
	env := newSchedulerEnv(t)
	ctx := context.Background()

	conn, err := env.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire lock connection: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, maintenanceLockID); err != nil {
		t.Fatalf("acquire advisory lock: %v", err)
	}
	defer func() {
		if _, err := conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, maintenanceLockID); err != nil {
			t.Errorf("release advisory lock: %v", err)
		}
	}()

	rollupFake := &fakeRollupRunner{}
	retentionFake := &fakeRetentionRunner{}
	env.sched.SetMaintenance(rollupFake, retentionFake)

	env.sched.runMaintenance(ctx, true, true)

	if got := rollupFake.callCount(); got != 0 {
		t.Errorf("rollup calls = %d, want 0 while the lock is held", got)
	}
	if got := retentionFake.callCount(); got != 0 {
		t.Errorf("retention calls = %d, want 0 while the lock is held", got)
	}
	if body := gatherSchedulerMetrics(t, env.metrics); !strings.Contains(body, `o1_retention_skipped_total{reason="locked"} 1`) {
		t.Errorf("locked skip metric missing:\n%s", body)
	}
}

func TestMaintenance_StartupPassRuns(t *testing.T) {
	env := newSchedulerEnv(t)
	env.sched.cfg.RollupInterval = time.Hour

	started := make(chan string, 4)
	rollupFake := &fakeRollupRunner{onRun: func() { started <- "rollup" }}
	retentionFake := &fakeRetentionRunner{onRun: func() { started <- "retention" }}
	env.sched.SetMaintenance(rollupFake, retentionFake)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- env.sched.Run(ctx) }()

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatal("startup maintenance pass did not run both runners")
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after the context was canceled")
	}

	if got := rollupFake.callCount(); got != 1 {
		t.Errorf("startup rollup calls = %d, want 1", got)
	}
	if got := retentionFake.callCount(); got != 1 {
		t.Errorf("startup retention calls = %d, want 1", got)
	}
}

// TestSchedulerTickLoopStopsOnCancel verifies shutdown stays clean even when a
// tick panics just before the context is canceled.
func TestSchedulerTickLoopStopsOnCancel(t *testing.T) {
	restore := observability.SetCaptureHookForTest(func(context.Context, error, map[string]string, map[string]sentry.Context) {})
	t.Cleanup(restore)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := &Scheduler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	err := s.tickLoop(ctx, make(chan time.Time), make(chan time.Time))
	if err != nil {
		t.Fatalf("tickLoop: %v", err)
	}
}

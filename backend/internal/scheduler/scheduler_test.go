package scheduler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"o1/internal/config"
	"o1/internal/db/sqlc"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

type schedulerEnv struct {
	t       *testing.T
	pool    *pgxpool.Pool
	sched   *Scheduler
	ownerID uuid.UUID
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
	return &schedulerEnv{
		t:       t,
		pool:    pool,
		sched:   New(pool, q, client, cfg, logger),
		ownerID: ownerID,
	}
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

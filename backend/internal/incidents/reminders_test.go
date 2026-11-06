package incidents_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/db/sqlc"
	"o1/internal/incidents"
	"o1/internal/testutil"
)

type reminderEnv struct {
	pool        *pgxpool.Pool
	tx          pgx.Tx
	q           *sqlc.Queries
	inserter    *fakeInserter
	workspaceID uuid.UUID
	monitorID   uuid.UUID
	channelID   uuid.UUID
}

func newReminderEnv(t *testing.T) *reminderEnv {
	t.Helper()
	pool, _ := testutil.NewPostgres(t)
	workspaceID, monitorID, channelID := createLifecycleFixtures(t, pool)

	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
	})

	return &reminderEnv{
		pool:        pool,
		tx:          tx,
		q:           sqlc.New(pool).WithTx(tx),
		inserter:    &fakeInserter{},
		workspaceID: workspaceID,
		monitorID:   monitorID,
		channelID:   channelID,
	}
}

// addIncident inserts an incident on the env's default monitor with the given
// status and notification state.
func (e *reminderEnv) addIncident(t *testing.T, status string, lastNotifiedAt time.Time, reminderCount int) uuid.UUID {
	t.Helper()
	return e.addIncidentForMonitor(t, e.monitorID, status, lastNotifiedAt, reminderCount)
}

// addIncidentForMonitor inserts an incident on monitorID.
func (e *reminderEnv) addIncidentForMonitor(t *testing.T, monitorID uuid.UUID, status string, lastNotifiedAt time.Time, reminderCount int) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new incident id: %v", err)
	}

	var acknowledgedAt, resolvedAt *time.Time
	switch status {
	case "acknowledged":
		acknowledgedAt = &lastNotifiedAt
	case "resolved":
		resolvedAt = &lastNotifiedAt
	}

	if _, err := e.tx.Exec(context.Background(), `
		INSERT INTO incidents
			(id, workspace_id, monitor_id, status, started_at, acknowledged_at,
			 resolved_at, last_notified_at, reminder_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		id, e.workspaceID, monitorID, status, lastNotifiedAt.Add(-time.Hour),
		acknowledgedAt, resolvedAt, lastNotifiedAt, reminderCount); err != nil {
		t.Fatalf("insert %s incident: %v", status, err)
	}
	return id
}

// addUndeliverableIncident creates a monitor without any alert channel and an
// open incident on it. Such an incident can never produce a delivery.
func (e *reminderEnv) addUndeliverableIncident(t *testing.T, lastNotifiedAt time.Time) uuid.UUID {
	t.Helper()
	monitorID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new monitor id: %v", err)
	}
	if _, err := e.tx.Exec(context.Background(), `
		INSERT INTO monitors
			(id, workspace_id, name, url, method, expected_status,
			 interval_seconds, timeout_seconds, confirmation_threshold,
			 recovery_threshold, paused, created_by)
		SELECT $1, w.id, $2, 'https://example.com/health', 'GET', '200-299',
			 60, 5, 1, 1, false, w.created_by
		FROM workspaces w WHERE w.id = $3`,
		monitorID, "undeliverable "+monitorID.String(), e.workspaceID); err != nil {
		t.Fatalf("insert undeliverable monitor: %v", err)
	}
	return e.addIncidentForMonitor(t, monitorID, "open", lastNotifiedAt, 0)
}

func (e *reminderEnv) enqueue(t *testing.T, now time.Time) int {
	t.Helper()
	n, err := incidents.EnqueueReminders(context.Background(), e.tx, e.q, e.inserter, now)
	if err != nil {
		t.Fatalf("EnqueueReminders: %v", err)
	}
	return n
}

func (e *reminderEnv) reminderState(t *testing.T, incidentID uuid.UUID) (int, *time.Time) {
	t.Helper()
	var count int
	var lastNotified *time.Time
	if err := e.tx.QueryRow(context.Background(),
		`SELECT reminder_count, last_notified_at FROM incidents WHERE id = $1`,
		incidentID).Scan(&count, &lastNotified); err != nil {
		t.Fatalf("read reminder state: %v", err)
	}
	return count, lastNotified
}

func TestEnqueueReminders(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	due := now.Add(-31 * time.Minute)
	ctx := context.Background()

	t.Run("due incident creates reminder deliveries and jobs", func(t *testing.T) {
		env := newReminderEnv(t)
		incidentID := env.addIncident(t, "open", due, 0)

		if n := env.enqueue(t, now); n != 1 {
			t.Fatalf("EnqueueReminders = %d, want 1", n)
		}

		deliveries, err := env.q.ListIncidentDeliveries(ctx, incidentID)
		if err != nil {
			t.Fatalf("list deliveries: %v", err)
		}
		if len(deliveries) != 2 {
			t.Fatalf("deliveries = %d, want 2", len(deliveries))
		}
		recipients := make(map[string]bool, len(deliveries))
		for _, delivery := range deliveries {
			if delivery.Event != "reminder" || delivery.Sequence != 1 || delivery.Status != "pending" {
				t.Fatalf("delivery event/sequence/status = %q/%d/%q, want reminder/1/pending",
					delivery.Event, delivery.Sequence, delivery.Status)
			}
			recipients[delivery.Recipient] = true
		}
		if !recipients["one@example.com"] || !recipients["two@example.com"] {
			t.Fatalf("recipients = %v, want one@example.com and two@example.com", recipients)
		}
		assertAlertJobs(t, env.inserter, deliveries)

		count, lastNotified := env.reminderState(t, incidentID)
		if count != 1 {
			t.Fatalf("reminder_count = %d, want 1", count)
		}
		if lastNotified == nil || !lastNotified.Equal(now) {
			t.Fatalf("last_notified_at = %v, want %v", lastNotified, now)
		}
	})

	t.Run("acknowledged incident creates nothing", func(t *testing.T) {
		env := newReminderEnv(t)
		incidentID := env.addIncident(t, "acknowledged", due, 0)

		if n := env.enqueue(t, now); n != 0 {
			t.Fatalf("EnqueueReminders = %d, want 0", n)
		}
		if got := countRows(t, env.tx, `SELECT count(*) FROM alert_deliveries WHERE incident_id = $1`, incidentID); got != 0 {
			t.Fatalf("deliveries = %d, want 0", got)
		}
		if len(env.inserter.jobs) != 0 {
			t.Fatalf("jobs = %d, want 0", len(env.inserter.jobs))
		}
		if count, lastNotified := env.reminderState(t, incidentID); count != 0 || !lastNotified.Equal(due) {
			t.Fatalf("reminder state = %d/%v, want 0/%v", count, lastNotified, due)
		}
	})

	t.Run("resolved incident creates nothing", func(t *testing.T) {
		env := newReminderEnv(t)
		incidentID := env.addIncident(t, "resolved", due, 0)

		if n := env.enqueue(t, now); n != 0 {
			t.Fatalf("EnqueueReminders = %d, want 0", n)
		}
		if got := countRows(t, env.tx, `SELECT count(*) FROM alert_deliveries WHERE incident_id = $1`, incidentID); got != 0 {
			t.Fatalf("deliveries = %d, want 0", got)
		}
		if count, lastNotified := env.reminderState(t, incidentID); count != 0 || !lastNotified.Equal(due) {
			t.Fatalf("reminder state = %d/%v, want 0/%v", count, lastNotified, due)
		}
	})

	t.Run("paused monitor creates nothing", func(t *testing.T) {
		env := newReminderEnv(t)
		incidentID := env.addIncident(t, "open", due, 0)
		if _, err := env.tx.Exec(ctx, `UPDATE monitors SET paused = true WHERE id = $1`, env.monitorID); err != nil {
			t.Fatalf("pause monitor: %v", err)
		}

		if n := env.enqueue(t, now); n != 0 {
			t.Fatalf("EnqueueReminders = %d, want 0", n)
		}
		if got := countRows(t, env.tx, `SELECT count(*) FROM alert_deliveries WHERE incident_id = $1`, incidentID); got != 0 {
			t.Fatalf("deliveries = %d, want 0", got)
		}
	})

	t.Run("suspended workspace creates nothing", func(t *testing.T) {
		env := newReminderEnv(t)
		incidentID := env.addIncident(t, "open", due, 0)
		if _, err := env.tx.Exec(ctx,
			`UPDATE workspaces SET suspended_at = now() WHERE id = $1`, env.workspaceID); err != nil {
			t.Fatalf("suspend workspace: %v", err)
		}

		if n := env.enqueue(t, now); n != 0 {
			t.Fatalf("EnqueueReminders = %d, want 0", n)
		}
		if got := countRows(t, env.tx, `SELECT count(*) FROM alert_deliveries WHERE incident_id = $1`, incidentID); got != 0 {
			t.Fatalf("deliveries = %d, want 0", got)
		}
	})

	t.Run("no enabled channel advances last_notified_at but not reminder_count", func(t *testing.T) {
		env := newReminderEnv(t)
		incidentID := env.addIncident(t, "open", due, 0)
		if _, err := env.tx.Exec(ctx,
			`DELETE FROM monitor_alert_channels WHERE monitor_id = $1`, env.monitorID); err != nil {
			t.Fatalf("detach alert channel: %v", err)
		}

		if n := env.enqueue(t, now); n != 0 {
			t.Fatalf("EnqueueReminders = %d, want 0", n)
		}
		if got := countRows(t, env.tx, `SELECT count(*) FROM alert_deliveries WHERE incident_id = $1`, incidentID); got != 0 {
			t.Fatalf("deliveries = %d, want 0", got)
		}
		if len(env.inserter.jobs) != 0 {
			t.Fatalf("jobs = %d, want 0", len(env.inserter.jobs))
		}
		count, lastNotified := env.reminderState(t, incidentID)
		if count != 0 {
			t.Fatalf("reminder_count = %d, want 0", count)
		}
		if lastNotified == nil || !lastNotified.Equal(now) {
			t.Fatalf("last_notified_at = %v, want %v (rotated out of the batch)", lastNotified, now)
		}
	})

	t.Run("repeated call does not duplicate the sequence", func(t *testing.T) {
		env := newReminderEnv(t)
		incidentID := env.addIncident(t, "open", due, 0)

		if n := env.enqueue(t, now); n != 1 {
			t.Fatalf("first EnqueueReminders = %d, want 1", n)
		}
		if n := env.enqueue(t, now); n != 0 {
			t.Fatalf("second EnqueueReminders = %d, want 0", n)
		}
		if got := countRows(t, env.tx, `SELECT count(*) FROM alert_deliveries WHERE incident_id = $1`, incidentID); got != 2 {
			t.Fatalf("deliveries = %d, want 2", got)
		}
		if len(env.inserter.jobs) != 2 {
			t.Fatalf("jobs = %d, want 2", len(env.inserter.jobs))
		}
		if count, _ := env.reminderState(t, incidentID); count != 1 {
			t.Fatalf("reminder_count = %d, want 1", count)
		}
	})

	t.Run("max reminders reached creates nothing", func(t *testing.T) {
		env := newReminderEnv(t)
		incidentID := env.addIncident(t, "open", due, 10)

		if n := env.enqueue(t, now); n != 0 {
			t.Fatalf("EnqueueReminders = %d, want 0", n)
		}
		if got := countRows(t, env.tx, `SELECT count(*) FROM alert_deliveries WHERE incident_id = $1`, incidentID); got != 0 {
			t.Fatalf("deliveries = %d, want 0", got)
		}
		if count, lastNotified := env.reminderState(t, incidentID); count != 10 || !lastNotified.Equal(due) {
			t.Fatalf("reminder state = %d/%v, want 10/%v", count, lastNotified, due)
		}
	})

	t.Run("malformed channel config is skipped", func(t *testing.T) {
		env := newReminderEnv(t)
		incidentID := env.addIncident(t, "open", due, 0)
		if _, err := env.tx.Exec(ctx,
			`UPDATE alert_channels SET config = $1 WHERE id = $2`,
			[]byte(`{"recipients":"not-a-list"}`), env.channelID); err != nil {
			t.Fatalf("corrupt channel config: %v", err)
		}

		if n := env.enqueue(t, now); n != 0 {
			t.Fatalf("EnqueueReminders = %d, want 0", n)
		}
		if got := countRows(t, env.tx, `SELECT count(*) FROM alert_deliveries WHERE incident_id = $1`, incidentID); got != 0 {
			t.Fatalf("deliveries = %d, want 0", got)
		}
		count, lastNotified := env.reminderState(t, incidentID)
		if count != 0 {
			t.Fatalf("reminder_count = %d, want 0", count)
		}
		if lastNotified == nil || !lastNotified.Equal(now) {
			t.Fatalf("last_notified_at = %v, want %v (rotated out of the batch)", lastNotified, now)
		}
	})
}

func TestEnqueueRemindersUndeliverableIncidentsDoNotStarve(t *testing.T) {
	env := newReminderEnv(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	ctx := context.Background()

	// Fill the first batch past capacity with undeliverable incidents that are
	// older than the deliverable one. Before the touch fix, these stayed at the
	// front of the ORDER BY last_notified_at batch forever.
	const reminderBatchSize = 100
	oldest := now.Add(-2 * time.Hour)
	for i := 0; i < reminderBatchSize+1; i++ {
		env.addUndeliverableIncident(t, oldest.Add(time.Duration(i)*time.Second))
	}
	deliverableID := env.addIncident(t, "open", now.Add(-31*time.Minute), 0)

	// First tick processes only undeliverable incidents; the deliverable one is
	// still behind them.
	if n := env.enqueue(t, now); n != 0 {
		t.Fatalf("first EnqueueReminders = %d, want 0", n)
	}
	if got := countRows(t, env.tx, `SELECT count(*) FROM alert_deliveries WHERE incident_id = $1`, deliverableID); got != 0 {
		t.Fatalf("deliverable deliveries after first tick = %d, want 0", got)
	}

	// The touched incidents rotated behind. The remaining undeliverable and the
	// deliverable incident are now in the same batch, so neither starves.
	if n := env.enqueue(t, now); n != 1 {
		t.Fatalf("second EnqueueReminders = %d, want 1", n)
	}
	deliveries, err := env.q.ListIncidentDeliveries(ctx, deliverableID)
	if err != nil {
		t.Fatalf("list deliverable deliveries: %v", err)
	}
	if len(deliveries) != 2 {
		t.Fatalf("deliverable deliveries = %d, want 2", len(deliveries))
	}
	if count, lastNotified := env.reminderState(t, deliverableID); count != 1 || lastNotified == nil || !lastNotified.Equal(now) {
		t.Fatalf("deliverable reminder state = %d/%v, want 1/%v", count, lastNotified, now)
	}
}

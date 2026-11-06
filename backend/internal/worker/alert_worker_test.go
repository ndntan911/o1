package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"o1/internal/db/sqlc"
	"o1/internal/email"
	"o1/internal/jobs"
	"o1/internal/testutil"
)

type alertTestSender struct {
	mu   sync.Mutex
	msgs []email.Message
	err  error
}

func (s *alertTestSender) Send(_ context.Context, msg email.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.msgs = append(s.msgs, msg)
	return nil
}

func (s *alertTestSender) messages() []email.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]email.Message(nil), s.msgs...)
}

type alertFixture struct {
	incidentID uuid.UUID
	deliveryID uuid.UUID
}

func newAlertWorkerForTest(env *workerEnv, sender email.Sender) *AlertWorker {
	return NewAlertWorker(env.pool, sqlc.New(env.pool), sender, testutil.TestConfig(env.t), env.logger())
}

func (e *workerEnv) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func createAlertFixture(t *testing.T, env *workerEnv, monitorID, workspaceID, channelID uuid.UUID, event string, sequence int, createdAt time.Time) alertFixture {
	t.Helper()
	ctx := context.Background()
	incidentID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new incident id: %v", err)
	}
	startedAt := time.Now().UTC().Add(-10 * time.Minute)
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO incidents (id, workspace_id, monitor_id, status, started_at, last_notified_at)
		VALUES ($1, $2, $3, 'open', $4, $4)`,
		incidentID, workspaceID, monitorID, startedAt); err != nil {
		t.Fatalf("insert incident: %v", err)
	}
	deliveryID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new delivery id: %v", err)
	}
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO alert_deliveries
			(id, incident_id, channel_id, event, sequence, recipient, created_at)
		VALUES ($1, $2, $3, $4, $5, 'one@example.com', $6)`,
		deliveryID, incidentID, channelID, event, sequence, createdAt); err != nil {
		t.Fatalf("insert delivery: %v", err)
	}
	return alertFixture{incidentID: incidentID, deliveryID: deliveryID}
}

func alertJob(deliveryID uuid.UUID, attempt, maxAttempts int) *river.Job[jobs.AlertArgs] {
	return &river.Job[jobs.AlertArgs]{
		JobRow: &rivertype.JobRow{Attempt: attempt, MaxAttempts: maxAttempts},
		Args:   jobs.AlertArgs{DeliveryID: deliveryID},
	}
}

type deliveryState struct {
	status    string
	attempts  int
	lastError *string
	sentAt    *time.Time
}

func readDeliveryState(t *testing.T, env *workerEnv, id uuid.UUID) deliveryState {
	t.Helper()
	var state deliveryState
	if err := env.pool.QueryRow(context.Background(), `
		SELECT status, attempts, last_error, sent_at FROM alert_deliveries WHERE id = $1`, id).
		Scan(&state.status, &state.attempts, &state.lastError, &state.sentAt); err != nil {
		t.Fatalf("read delivery: %v", err)
	}
	return state
}

func TestAlertWorker_SendsOpenedDelivery(t *testing.T) {
	env := newWorkerEnv(t)
	monitorID, workspaceID := env.createMonitor(t, "https://example.com/health", 1, 1, false, false)
	channelID := env.attachEmailChannel(t, monitorID, workspaceID, "one@example.com")
	fixture := createAlertFixture(t, env, monitorID, workspaceID, channelID, "opened", 0, time.Now().UTC())

	sender := &alertTestSender{}
	worker := newAlertWorkerForTest(env, sender)
	if err := worker.Work(context.Background(), alertJob(fixture.deliveryID, 1, 5)); err != nil {
		t.Fatalf("Work: %v", err)
	}

	msgs := sender.messages()
	if len(msgs) != 1 {
		t.Fatalf("sent %d messages, want 1", len(msgs))
	}
	if msgs[0].To != "one@example.com" {
		t.Fatalf("To = %q, want one@example.com", msgs[0].To)
	}
	if !strings.Contains(msgs[0].Subject, "worker monitor") {
		t.Fatalf("subject = %q, want monitor name", msgs[0].Subject)
	}
	state := readDeliveryState(t, env, fixture.deliveryID)
	if state.status != "sent" || state.attempts != 1 || state.lastError != nil || state.sentAt == nil {
		t.Fatalf("delivery = %+v, want sent/1/no error/sent_at", state)
	}

	// A replay of an already-sent delivery must not send again.
	if err := worker.Work(context.Background(), alertJob(fixture.deliveryID, 2, 5)); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(sender.messages()) != 1 {
		t.Fatal("replay sent a second email")
	}
}

func TestAlertWorker_RedactsMonitorURLQueryAndFragment(t *testing.T) {
	env := newWorkerEnv(t)
	monitorID, workspaceID := env.createMonitor(
		t, "https://example.com/health?token=secret#fragment", 1, 1, false, false)
	channelID := env.attachEmailChannel(t, monitorID, workspaceID, "one@example.com")
	fixture := createAlertFixture(t, env, monitorID, workspaceID, channelID, "opened", 0, time.Now().UTC())

	sender := &alertTestSender{}
	worker := newAlertWorkerForTest(env, sender)
	if err := worker.Work(context.Background(), alertJob(fixture.deliveryID, 1, 5)); err != nil {
		t.Fatalf("Work: %v", err)
	}

	msgs := sender.messages()
	if len(msgs) != 1 {
		t.Fatalf("sent %d messages, want 1", len(msgs))
	}
	for _, body := range []struct {
		name string
		text string
	}{
		{"Text", msgs[0].Text},
		{"HTML", msgs[0].HTML},
	} {
		if strings.Contains(body.text, "token=secret") {
			t.Errorf("%s body leaked the query string: %q", body.name, body.text)
		}
		if strings.Contains(body.text, "fragment") || strings.Contains(body.text, "#") {
			t.Errorf("%s body leaked the URL fragment: %q", body.name, body.text)
		}
		if !strings.Contains(body.text, "https://example.com/health") {
			t.Errorf("%s body missing redacted URL: %q", body.name, body.text)
		}
	}
}

func TestAlertWorker_FailureRetriesThenMarksFailed(t *testing.T) {
	env := newWorkerEnv(t)
	monitorID, workspaceID := env.createMonitor(t, "https://example.com/health", 1, 1, false, false)
	channelID := env.attachEmailChannel(t, monitorID, workspaceID, "one@example.com")
	fixture := createAlertFixture(t, env, monitorID, workspaceID, channelID, "opened", 0, time.Now().UTC())

	sender := &alertTestSender{err: errors.New("smtp down")}
	worker := newAlertWorkerForTest(env, sender)

	err := worker.Work(context.Background(), alertJob(fixture.deliveryID, 1, 5))
	if err == nil || !strings.Contains(err.Error(), "smtp down") {
		t.Fatalf("Work error = %v, want retryable send error", err)
	}
	state := readDeliveryState(t, env, fixture.deliveryID)
	if state.status != "pending" || state.attempts != 1 || state.lastError == nil ||
		!strings.Contains(*state.lastError, "smtp down") {
		t.Fatalf("delivery after first failure = %+v, want pending/1/last_error", state)
	}

	// The final attempt marks the delivery failed and returns nil so River does
	// not schedule another retry.
	if err := worker.Work(context.Background(), alertJob(fixture.deliveryID, 5, 5)); err != nil {
		t.Fatalf("final attempt: %v", err)
	}
	state = readDeliveryState(t, env, fixture.deliveryID)
	if state.status != "failed" || state.attempts != 2 || state.lastError == nil {
		t.Fatalf("delivery after final failure = %+v, want failed/2/last_error", state)
	}
}

func TestAlertWorker_TerminalDeliveriesAreNoOps(t *testing.T) {
	for _, status := range []string{"sent", "failed", "skipped"} {
		t.Run(status, func(t *testing.T) {
			env := newWorkerEnv(t)
			monitorID, workspaceID := env.createMonitor(t, "https://example.com/health", 1, 1, false, false)
			channelID := env.attachEmailChannel(t, monitorID, workspaceID, "one@example.com")
			fixture := createAlertFixture(t, env, monitorID, workspaceID, channelID, "opened", 0, time.Now().UTC())
			if _, err := env.pool.Exec(context.Background(),
				`UPDATE alert_deliveries SET status = $2, attempts = 3 WHERE id = $1`,
				fixture.deliveryID, status); err != nil {
				t.Fatalf("set terminal status: %v", err)
			}

			sender := &alertTestSender{}
			worker := newAlertWorkerForTest(env, sender)
			if err := worker.Work(context.Background(), alertJob(fixture.deliveryID, 2, 5)); err != nil {
				t.Fatalf("Work: %v", err)
			}
			if len(sender.messages()) != 0 {
				t.Fatalf("%s delivery sent an email", status)
			}
			state := readDeliveryState(t, env, fixture.deliveryID)
			if state.status != status || state.attempts != 3 {
				t.Fatalf("delivery = %+v, want unchanged %s/3", state, status)
			}
		})
	}
}

func TestAlertWorker_MissingDeliveryIsNoOp(t *testing.T) {
	env := newWorkerEnv(t)
	sender := &alertTestSender{}
	worker := newAlertWorkerForTest(env, sender)
	if err := worker.Work(context.Background(), alertJob(uuid.New(), 1, 5)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if len(sender.messages()) != 0 {
		t.Fatal("missing delivery sent an email")
	}
}

func TestAlertWorker_SkipsStaleReminders(t *testing.T) {
	env := newWorkerEnv(t)
	ctx := context.Background()

	cases := []struct {
		name      string
		createdAt time.Time
		mutate    func(t *testing.T, monitorID, workspaceID, incidentID uuid.UUID)
	}{
		{
			name:      "acknowledged",
			createdAt: time.Now().UTC(),
			mutate: func(t *testing.T, _, _, incidentID uuid.UUID) {
				if _, err := env.pool.Exec(ctx,
					`UPDATE incidents SET status = 'acknowledged', acknowledged_at = now() WHERE id = $1`,
					incidentID); err != nil {
					t.Fatalf("acknowledge incident: %v", err)
				}
			},
		},
		{
			name:      "resolved",
			createdAt: time.Now().UTC(),
			mutate: func(t *testing.T, _, _, incidentID uuid.UUID) {
				if _, err := env.pool.Exec(ctx,
					`UPDATE incidents SET status = 'resolved', resolved_at = now() WHERE id = $1`,
					incidentID); err != nil {
					t.Fatalf("resolve incident: %v", err)
				}
			},
		},
		{
			name:      "monitor paused",
			createdAt: time.Now().UTC(),
			mutate: func(t *testing.T, monitorID, _, _ uuid.UUID) {
				if _, err := env.pool.Exec(ctx, `UPDATE monitors SET paused = true WHERE id = $1`, monitorID); err != nil {
					t.Fatalf("pause monitor: %v", err)
				}
			},
		},
		{
			name:      "workspace suspended",
			createdAt: time.Now().UTC(),
			mutate: func(t *testing.T, _, workspaceID, _ uuid.UUID) {
				if _, err := env.pool.Exec(ctx, `UPDATE workspaces SET suspended_at = now() WHERE id = $1`, workspaceID); err != nil {
					t.Fatalf("suspend workspace: %v", err)
				}
			},
		},
		{
			name:      "older than 60 minutes",
			createdAt: time.Now().UTC().Add(-61 * time.Minute),
			mutate:    func(*testing.T, uuid.UUID, uuid.UUID, uuid.UUID) {},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			monitorID, workspaceID := env.createMonitor(t, "https://example.com/health", 1, 1, false, false)
			channelID := env.attachEmailChannel(t, monitorID, workspaceID, "one@example.com")
			fixture := createAlertFixture(t, env, monitorID, workspaceID, channelID, "reminder", 1, tc.createdAt)
			tc.mutate(t, monitorID, workspaceID, fixture.incidentID)

			sender := &alertTestSender{}
			worker := newAlertWorkerForTest(env, sender)
			if err := worker.Work(ctx, alertJob(fixture.deliveryID, 1, 5)); err != nil {
				t.Fatalf("Work: %v", err)
			}
			if len(sender.messages()) != 0 {
				t.Fatal("stale reminder was sent")
			}
			state := readDeliveryState(t, env, fixture.deliveryID)
			if state.status != "skipped" || state.lastError == nil {
				t.Fatalf("delivery = %+v, want skipped with a reason", state)
			}
		})
	}
}

// TestAlertJobInsertWithRealWorkerConfig is the Task 3 regression test for the
// real worker configuration: the "alert" kind is registered, the client does
// not skip unknown job checks, and an AlertArgs insert succeeds.
func TestAlertJobInsertWithRealWorkerConfig(t *testing.T) {
	env := newWorkerEnv(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	alertWorker := NewAlertWorker(env.pool, sqlc.New(env.pool), &alertTestSender{}, testutil.TestConfig(t), logger)

	workers := river.NewWorkers()
	river.AddWorker(workers, env.worker)
	river.AddWorker(workers, alertWorker)

	client, err := river.NewClient[pgx.Tx](riverpgxv5.New(env.pool), &river.Config{
		Logger:  logger,
		Workers: workers,
		Queues: map[string]river.QueueConfig{
			"probe": {MaxWorkers: 1},
			"alert": {MaxWorkers: 4},
		},
	})
	if err != nil {
		t.Fatalf("create river client: %v", err)
	}

	ctx := context.Background()
	tx, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	deliveryID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new delivery id: %v", err)
	}
	if _, err := client.InsertTx(ctx, tx, jobs.AlertArgs{DeliveryID: deliveryID}, &river.InsertOpts{
		MaxAttempts: 5,
		Queue:       "alert",
	}); err != nil {
		t.Fatalf("insert alert job: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var count int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM river_job WHERE kind = 'alert' AND args->>'delivery_id' = $1`,
		deliveryID.String()).Scan(&count); err != nil {
		t.Fatalf("count alert jobs: %v", err)
	}
	if count != 1 {
		t.Fatalf("alert jobs = %d, want 1", count)
	}
}

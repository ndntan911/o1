package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"o1/internal/alerts"
	"o1/internal/db/sqlc"
	"o1/internal/email"
	"o1/internal/jobs"
	"o1/internal/probe"
	"o1/internal/secretbox"
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

func alertTestBox(t *testing.T) *secretbox.Box {
	t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("new secretbox: %v", err)
	}
	return box
}

type testResolver struct{}

func (testResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
}

func guardForURL(t *testing.T, rawURL string) *probe.Guard {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return probe.NewGuardWithResolver(probe.Policy{
		AllowedPorts:         map[int]struct{}{port: {}},
		AllowPrivateNetworks: true,
	}, testResolver{})
}

func newAlertWorkerWith(env *workerEnv, sender email.Sender, box *secretbox.Box, notifier alerts.NotificationSender) *AlertWorker {
	return NewAlertWorker(env.pool, sqlc.New(env.pool), sender, box, notifier,
		testutil.TestConfig(env.t), env.logger())
}

func newAlertWorkerForTest(env *workerEnv, sender email.Sender) *AlertWorker {
	return newAlertWorkerWith(env, sender, alertTestBox(env.t), alerts.NewNotifier(probe.NewGuard(probe.Policy{}), ""))
}

func (e *workerEnv) attachChannel(t *testing.T, monitorID, workspaceID uuid.UUID, kind, name string, config any) uuid.UUID {
	t.Helper()
	channelID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new channel id: %v", err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("encode channel config: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO alert_channels (id, workspace_id, kind, name, config)
		VALUES ($1, $2, $3, $4, $5)`,
		channelID, workspaceID, kind, name, encoded); err != nil {
		t.Fatalf("insert %s channel: %v", kind, err)
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO monitor_alert_channels (monitor_id, channel_id)
		VALUES ($1, $2)`, monitorID, channelID); err != nil {
		t.Fatalf("attach %s channel: %v", kind, err)
	}
	return channelID
}

func (e *workerEnv) attachWebhookChannel(t *testing.T, box *secretbox.Box, monitorID, workspaceID uuid.UUID, rawURL, secret string) uuid.UUID {
	t.Helper()
	urlEnc, err := box.Seal([]byte(rawURL))
	if err != nil {
		t.Fatalf("seal webhook url: %v", err)
	}
	var secretEnc string
	if secret != "" {
		secretEnc, err = box.Seal([]byte(secret))
		if err != nil {
			t.Fatalf("seal signing secret: %v", err)
		}
	}
	return e.attachChannel(t, monitorID, workspaceID, "webhook", "Webhook", alerts.WebhookConfig{
		URLEnc: urlEnc, URLPreview: "https://example.com/…", SigningSecretEnc: secretEnc,
	})
}

func (e *workerEnv) attachTelegramChannel(t *testing.T, box *secretbox.Box, monitorID, workspaceID uuid.UUID, token, chatID string) uuid.UUID {
	t.Helper()
	tokenEnc, err := box.Seal([]byte(token))
	if err != nil {
		t.Fatalf("seal bot token: %v", err)
	}
	return e.attachChannel(t, monitorID, workspaceID, "telegram", "Telegram", alerts.TelegramConfig{
		BotTokenEnc: tokenEnc, ChatID: chatID,
	})
}

func (e *workerEnv) attachSlackChannel(t *testing.T, box *secretbox.Box, monitorID, workspaceID uuid.UUID, rawURL string) uuid.UUID {
	t.Helper()
	urlEnc, err := box.Seal([]byte(rawURL))
	if err != nil {
		t.Fatalf("seal slack url: %v", err)
	}
	return e.attachChannel(t, monitorID, workspaceID, "slack", "Slack", alerts.SlackConfig{
		URLEnc: urlEnc, URLPreview: "https://hooks.slack.com/…",
	})
}

type recordingDeliveryNotifier struct {
	mu      sync.Mutex
	targets []alerts.Target
	events  []alerts.Event
}

func (n *recordingDeliveryNotifier) Send(_ context.Context, target alerts.Target, event alerts.Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.targets = append(n.targets, target)
	n.events = append(n.events, event)
	return nil
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
	alertWorker := NewAlertWorker(env.pool, sqlc.New(env.pool), &alertTestSender{}, alertTestBox(t),
		alerts.NewNotifier(nil, ""), testutil.TestConfig(t), logger)

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

func TestAlertWorker_SendsWebhook(t *testing.T) {
	env := newWorkerEnv(t)
	monitorID, workspaceID := env.createMonitor(t, "https://example.com/health", 1, 1, false, false)
	var gotHeaders http.Header
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	box := alertTestBox(t)
	channelID := env.attachWebhookChannel(t, box, monitorID, workspaceID, srv.URL, "0123456789abcdef")
	fixture := createAlertFixture(t, env, monitorID, workspaceID, channelID, "opened", 0, time.Now().UTC())
	worker := newAlertWorkerWith(env, &alertTestSender{}, box, alerts.NewNotifier(guardForURL(t, srv.URL), ""))
	if err := worker.Work(context.Background(), alertJob(fixture.deliveryID, 1, 5)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	state := readDeliveryState(t, env, fixture.deliveryID)
	if state.status != "sent" || state.attempts != 1 {
		t.Fatalf("delivery = %+v, want sent/1", state)
	}
	if gotHeaders.Get("X-O1-Event") != "opened" {
		t.Fatalf("X-O1-Event = %q", gotHeaders.Get("X-O1-Event"))
	}
	if gotHeaders.Get("X-O1-Signature") == "" {
		t.Fatal("signed webhook missing signature")
	}
	var payload map[string]any
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["event"] != "opened" {
		t.Fatalf("event = %v", payload["event"])
	}
}

func TestAlertWorker_WebhookPermanentFailure(t *testing.T) {
	env := newWorkerEnv(t)
	monitorID, workspaceID := env.createMonitor(t, "https://example.com/health", 1, 1, false, false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	box := alertTestBox(t)
	channelID := env.attachWebhookChannel(t, box, monitorID, workspaceID, srv.URL, "")
	fixture := createAlertFixture(t, env, monitorID, workspaceID, channelID, "opened", 0, time.Now().UTC())
	worker := newAlertWorkerWith(env, &alertTestSender{}, box, alerts.NewNotifier(guardForURL(t, srv.URL), ""))

	if err := worker.Work(context.Background(), alertJob(fixture.deliveryID, 1, 5)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	state := readDeliveryState(t, env, fixture.deliveryID)
	if state.status != "failed" || state.attempts != 1 {
		t.Fatalf("delivery = %+v, want failed/1", state)
	}
	if state.lastError == nil || *state.lastError != "webhook returned HTTP 400" {
		t.Fatalf("last_error = %v", state.lastError)
	}
}

func TestAlertWorker_WebhookTransientFailureRetries(t *testing.T) {
	env := newWorkerEnv(t)
	monitorID, workspaceID := env.createMonitor(t, "https://example.com/health", 1, 1, false, false)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	box := alertTestBox(t)
	channelID := env.attachWebhookChannel(t, box, monitorID, workspaceID, srv.URL, "")
	fixture := createAlertFixture(t, env, monitorID, workspaceID, channelID, "opened", 0, time.Now().UTC())
	worker := newAlertWorkerWith(env, &alertTestSender{}, box, alerts.NewNotifier(guardForURL(t, srv.URL), ""))

	err := worker.Work(context.Background(), alertJob(fixture.deliveryID, 1, 5))
	if err == nil {
		t.Fatal("Work = nil, want retryable error")
	}
	state := readDeliveryState(t, env, fixture.deliveryID)
	if state.status != "pending" || state.attempts != 1 {
		t.Fatalf("delivery after first attempt = %+v, want pending/1", state)
	}
	if state.lastError == nil || *state.lastError != "webhook returned HTTP 500" {
		t.Fatalf("last_error = %v", state.lastError)
	}
	if err := worker.Work(context.Background(), alertJob(fixture.deliveryID, 2, 5)); err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	state = readDeliveryState(t, env, fixture.deliveryID)
	if state.status != "sent" || state.attempts != 2 || state.sentAt == nil {
		t.Fatalf("delivery after retry = %+v, want sent/2", state)
	}
}

func TestAlertWorker_DeletedChannelSkipped(t *testing.T) {
	env := newWorkerEnv(t)
	monitorID, workspaceID := env.createMonitor(t, "https://example.com/health", 1, 1, false, false)
	box := alertTestBox(t)
	channelID := env.attachWebhookChannel(t, box, monitorID, workspaceID, "https://example.com/hook", "")
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE alert_channels SET deleted_at = now() WHERE id = $1`, channelID); err != nil {
		t.Fatalf("soft delete channel: %v", err)
	}
	fixture := createAlertFixture(t, env, monitorID, workspaceID, channelID, "opened", 0, time.Now().UTC())
	worker := newAlertWorkerWith(env, &alertTestSender{}, box, alerts.NewNotifier(nil, ""))

	if err := worker.Work(context.Background(), alertJob(fixture.deliveryID, 1, 5)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	state := readDeliveryState(t, env, fixture.deliveryID)
	if state.status != "skipped" || state.lastError == nil || *state.lastError != "channel deleted" {
		t.Fatalf("delivery = %+v, want skipped/channel deleted", state)
	}
}

func TestAlertWorker_SanitizesSecretInLastError(t *testing.T) {
	env := newWorkerEnv(t)
	monitorID, workspaceID := env.createMonitor(t, "https://example.com/health", 1, 1, false, false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	box := alertTestBox(t)
	destination := srv.URL + "/hook?token=supersecret"
	channelID := env.attachWebhookChannel(t, box, monitorID, workspaceID, destination, "")
	fixture := createAlertFixture(t, env, monitorID, workspaceID, channelID, "opened", 0, time.Now().UTC())
	worker := newAlertWorkerWith(env, &alertTestSender{}, box, alerts.NewNotifier(guardForURL(t, srv.URL), ""))

	if err := worker.Work(context.Background(), alertJob(fixture.deliveryID, 5, 5)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	state := readDeliveryState(t, env, fixture.deliveryID)
	if state.lastError == nil || *state.lastError != "webhook returned HTTP 500" {
		t.Fatalf("last_error = %v", state.lastError)
	}
	if strings.Contains(*state.lastError, "supersecret") || strings.Contains(*state.lastError, srv.URL) {
		t.Fatalf("last_error leaks destination: %q", *state.lastError)
	}
}

func TestAlertWorker_TelegramDispatch(t *testing.T) {
	env := newWorkerEnv(t)
	monitorID, workspaceID := env.createMonitor(t, "https://example.com/health", 1, 1, false, false)
	var gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	box := alertTestBox(t)
	channelID := env.attachTelegramChannel(t, box, monitorID, workspaceID, "123:abc", "42")
	fixture := createAlertFixture(t, env, monitorID, workspaceID, channelID, "opened", 0, time.Now().UTC())
	worker := newAlertWorkerWith(env, &alertTestSender{}, box, alerts.NewNotifier(nil, srv.URL))

	if err := worker.Work(context.Background(), alertJob(fixture.deliveryID, 1, 5)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if gotPath != "/bot123:abc/sendMessage" {
		t.Fatalf("path = %q", gotPath)
	}
	var payload struct {
		ChatID string `json:"chat_id"`
	}
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("decode telegram body: %v", err)
	}
	if payload.ChatID != "42" {
		t.Fatalf("chat_id = %q", payload.ChatID)
	}
	if state := readDeliveryState(t, env, fixture.deliveryID); state.status != "sent" {
		t.Fatalf("delivery = %+v, want sent", state)
	}
}

func TestAlertWorker_SlackDispatch(t *testing.T) {
	env := newWorkerEnv(t)
	monitorID, workspaceID := env.createMonitor(t, "https://example.com/health", 1, 1, false, false)
	box := alertTestBox(t)
	channelID := env.attachSlackChannel(t, box, monitorID, workspaceID, "https://hooks.slack.com/services/T/B/X")
	fixture := createAlertFixture(t, env, monitorID, workspaceID, channelID, "opened", 0, time.Now().UTC())
	notifier := &recordingDeliveryNotifier{}
	worker := newAlertWorkerWith(env, &alertTestSender{}, box, notifier)

	if err := worker.Work(context.Background(), alertJob(fixture.deliveryID, 1, 5)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if len(notifier.targets) != 1 || notifier.targets[0].Kind != alerts.KindSlack {
		t.Fatalf("targets = %+v", notifier.targets)
	}
	if len(notifier.events) != 1 || notifier.events[0].Kind != "opened" {
		t.Fatalf("events = %+v", notifier.events)
	}
	if notifier.events[0].Link == "" {
		t.Fatal("event link is empty")
	}
	if state := readDeliveryState(t, env, fixture.deliveryID); state.status != "sent" {
		t.Fatalf("delivery = %+v, want sent", state)
	}
}

package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/db/sqlc"
	"o1/internal/secretbox"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

const (
	serviceTestPassword = "correct horse battery staple"
	webhookURL          = "https://example.com/o1/hook"
	slackURL            = "https://hooks.slack.com/services/T000/B000/XXXX"
	telegramToken       = "123456:ABC_def-1"
	telegramChatID      = "-1001234567890"
)

type recordingNotifier struct {
	mu      sync.Mutex
	targets []Target
	events  []Event
	err     error
}

func (n *recordingNotifier) Send(_ context.Context, target Target, event Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.err != nil {
		return n.err
	}
	n.targets = append(n.targets, target)
	n.events = append(n.events, event)
	return nil
}

type serviceEnv struct {
	t      *testing.T
	pool   *pgxpool.Pool
	ws     *workspaces.Service
	svc    *Service
	box    *secretbox.Box
	notify *recordingNotifier
	sender *testutil.FakeSender
	wsID   uuid.UUID
	owner  uuid.UUID
	admin  uuid.UUID
	member uuid.UUID
	viewer uuid.UUID
}

func newServiceEnv(t *testing.T) *serviceEnv {
	t.Helper()
	pool, cleanup := testutil.NewPostgres(t)
	t.Cleanup(cleanup)

	cfg := testutil.TestConfig(t)
	wsSvc := workspaces.NewService(pool, sqlc.New(pool))
	box, err := secretbox.New(cfg.EncryptionKey)
	if err != nil {
		t.Fatalf("new secretbox: %v", err)
	}
	notify := &recordingNotifier{}
	sender := testutil.NewFakeSender()
	svc := NewService(pool, sqlc.New(pool), wsSvc, box, testGuard("93.184.216.34"), notify, sender, cfg.AppBaseURL)

	owner := testutil.CreateVerifiedUser(t, pool, "alerts-owner@example.com", serviceTestPassword)
	ws, err := wsSvc.Create(context.Background(), owner, "Alerts")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	env := &serviceEnv{
		t: t, pool: pool, ws: wsSvc, svc: svc, box: box, notify: notify, sender: sender,
		wsID: ws.ID, owner: owner,
	}
	env.admin = env.addMember("alerts-admin@example.com", "admin")
	env.member = env.addMember("alerts-member@example.com", "member")
	env.viewer = env.addMember("alerts-viewer@example.com", "viewer")
	return env
}

func (e *serviceEnv) addMember(emailAddr, role string) uuid.UUID {
	e.t.Helper()
	id := testutil.CreateVerifiedUser(e.t, e.pool, emailAddr, serviceTestPassword)
	if err := sqlc.New(e.pool).UpsertWorkspaceMember(context.Background(), sqlc.UpsertWorkspaceMemberParams{
		WorkspaceID: e.wsID,
		UserID:      id,
		Role:        role,
	}); err != nil {
		e.t.Fatalf("add member: %v", err)
	}
	return id
}

func (e *serviceEnv) insertMonitor(name string) uuid.UUID {
	e.t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		e.t.Fatalf("new monitor id: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO monitors
			(id, workspace_id, name, url, method, expected_status,
			 interval_seconds, timeout_seconds, confirmation_threshold,
			 recovery_threshold, paused, created_by)
		VALUES ($1, $2, $3, 'https://example.com/health', 'GET', '200-299', 60, 5, 1, 1, false, $4)`,
		id, e.wsID, name, e.owner); err != nil {
		e.t.Fatalf("insert monitor: %v", err)
	}
	return id
}

func (e *serviceEnv) createWebhook(name string) Channel {
	e.t.Helper()
	secret := "0123456789abcdef"
	channel, err := e.svc.Create(context.Background(), e.owner, e.wsID, CreateInput{
		Kind: KindWebhook, Name: name, URL: webhookURL, SigningSecret: secret,
	})
	if err != nil {
		e.t.Fatalf("create webhook channel: %v", err)
	}
	return channel
}

func (e *serviceEnv) openChannel(channelID uuid.UUID) Target {
	e.t.Helper()
	row, err := e.svc.q.GetAlertChannelByID(context.Background(), sqlc.GetAlertChannelByIDParams{
		ID: channelID, WorkspaceID: e.wsID,
	})
	if err != nil {
		e.t.Fatalf("load channel: %v", err)
	}
	target, err := OpenTarget(e.box, row.Kind, row.Config)
	if err != nil {
		e.t.Fatalf("open target: %v", err)
	}
	return target
}

func TestService_CreateChannelPerKind(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()

	webhook, err := env.svc.Create(ctx, env.owner, env.wsID, CreateInput{
		Kind: KindWebhook, Name: "Deploy hook", URL: webhookURL, SigningSecret: "0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("create webhook: %v", err)
	}
	if webhook.Kind != KindWebhook || webhook.Name != "Deploy hook" || !webhook.Enabled {
		t.Fatalf("webhook = %+v", webhook)
	}
	if webhook.URLPreview != "https://example.com/…" {
		t.Fatalf("webhook preview = %q", webhook.URLPreview)
	}
	if !webhook.SigningSecretSet {
		t.Fatal("webhook signing_secret_set = false")
	}

	enabled := false
	slack, err := env.svc.Create(ctx, env.owner, env.wsID, CreateInput{
		Kind: KindSlack, Name: "Slack #ops", URL: slackURL, Enabled: &enabled,
	})
	if err != nil {
		t.Fatalf("create slack: %v", err)
	}
	if slack.Kind != KindSlack || slack.Enabled {
		t.Fatalf("slack = %+v", slack)
	}
	if slack.URLPreview != "https://hooks.slack.com/…" {
		t.Fatalf("slack preview = %q", slack.URLPreview)
	}

	telegram, err := env.svc.Create(ctx, env.owner, env.wsID, CreateInput{
		Kind: KindTelegram, Name: "Telegram on-call", BotToken: telegramToken, ChatID: telegramChatID,
	})
	if err != nil {
		t.Fatalf("create telegram: %v", err)
	}
	if telegram.Kind != KindTelegram || telegram.ChatID != telegramChatID || !telegram.BotTokenSet {
		t.Fatalf("telegram = %+v", telegram)
	}

	encoded, err := json.Marshal([]Channel{webhook, slack, telegram})
	if err != nil {
		t.Fatalf("marshal channels: %v", err)
	}
	for _, secret := range []string{
		webhookURL, "example.com/o1", slackURL, "T000/B000/XXXX", telegramToken, "ABC_def-1",
	} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("channel JSON leaks %q: %s", secret, encoded)
		}
	}
}

func TestService_CreateRejectsEmailAndInvalidInput(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()

	cases := []struct {
		name  string
		in    CreateInput
		email bool
		field string
	}{
		{"email", CreateInput{Kind: KindEmail, Name: "Email"}, true, ""},
		{"unknown kind", CreateInput{Kind: "pagerduty", Name: "Pager"}, false, "kind"},
		{"bad name", CreateInput{Kind: KindWebhook, Name: " ", URL: webhookURL}, false, "name"},
		{"bad url", CreateInput{Kind: KindWebhook, Name: "Hook", URL: "http://127.0.0.1/hook"}, false, "url"},
		{"short secret", CreateInput{Kind: KindWebhook, Name: "Hook", URL: webhookURL, SigningSecret: "short"}, false, "signing_secret"},
		{"slack host", CreateInput{Kind: KindSlack, Name: "Slack", URL: "https://example.com/hook"}, false, "url"},
		{"telegram token", CreateInput{Kind: KindTelegram, Name: "TG", BotToken: "nope", ChatID: "1"}, false, "bot_token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.svc.Create(ctx, env.owner, env.wsID, tc.in)
			if tc.email {
				if !errors.Is(err, ErrEmailChannel) {
					t.Fatalf("Create = %v, want ErrEmailChannel", err)
				}
				return
			}
			var validation *ValidationError
			if !errors.As(err, &validation) || validation.Fields[tc.field] == "" {
				t.Fatalf("Create = %v, want field %q", err, tc.field)
			}
		})
	}
}

func TestService_CreateAttachesEveryMonitor(t *testing.T) {
	env := newServiceEnv(t)
	first := env.insertMonitor("API")
	second := env.insertMonitor("Web")

	env.createWebhook("Deploy hook")

	var count int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM monitor_alert_channels mac
		 JOIN alert_channels c ON c.id = mac.channel_id
		 WHERE c.workspace_id = $1 AND c.kind = 'webhook' AND mac.enabled = true`,
		env.wsID).Scan(&count); err != nil {
		t.Fatalf("count attachments: %v", err)
	}
	if count != 2 {
		t.Fatalf("attachments = %d, want 2", count)
	}
	for _, monitorID := range []uuid.UUID{first, second} {
		var enabled bool
		if err := env.pool.QueryRow(context.Background(),
			`SELECT mac.enabled FROM monitor_alert_channels mac
			 JOIN alert_channels c ON c.id = mac.channel_id
			 WHERE mac.monitor_id = $1 AND c.kind = 'webhook' AND c.name = 'Deploy hook'`,
			monitorID).Scan(&enabled); err != nil {
			t.Fatalf("load attachment for %s: %v", monitorID, err)
		}
		if !enabled {
			t.Fatalf("monitor %s attachment disabled", monitorID)
		}
	}
}

func TestService_ListOnlyActiveAndAdmin(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()

	channel := env.createWebhook("Deploy hook")
	if err := env.svc.Delete(ctx, env.owner, env.wsID, channel.ID); err != nil {
		t.Fatalf("delete channel: %v", err)
	}
	if _, err := env.svc.q.UpsertEmailAlertChannel(ctx, sqlc.UpsertEmailAlertChannelParams{
		ID: uuid.New(), WorkspaceID: env.wsID,
		Config: []byte(`{"recipients":["ops@example.com"]}`), Enabled: true,
	}); err != nil {
		t.Fatalf("upsert email channel: %v", err)
	}

	list, err := env.svc.List(ctx, env.admin, env.wsID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Kind != KindEmail {
		t.Fatalf("list = %+v, want only email", list)
	}
	if len(list[0].Recipients) != 1 || list[0].Recipients[0] != "ops@example.com" {
		t.Fatalf("email recipients = %+v", list[0].Recipients)
	}
	if _, err := env.svc.List(ctx, env.member, env.wsID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member List = %v, want ErrForbidden", err)
	}
}

func TestService_UpdateRotationAndKeep(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()
	channel := env.createWebhook("Deploy hook")
	before, err := env.svc.q.GetAlertChannelByID(ctx, sqlc.GetAlertChannelByIDParams{ID: channel.ID, WorkspaceID: env.wsID})
	if err != nil {
		t.Fatalf("load before: %v", err)
	}
	if got := env.openChannel(channel.ID); got.URL != webhookURL || got.SigningSecret != "0123456789abcdef" {
		t.Fatalf("target before = %+v", got)
	}

	disabled := false
	rotated := "https://example.org/rotated"
	updated, err := env.svc.Update(ctx, env.owner, env.wsID, channel.ID, UpdateInput{
		Name: ptrString("Deploy hook renamed"), Enabled: &disabled, URL: &rotated,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "Deploy hook renamed" || updated.Enabled {
		t.Fatalf("updated = %+v", updated)
	}
	if updated.URLPreview != "https://example.org/…" {
		t.Fatalf("preview = %q", updated.URLPreview)
	}
	target := env.openChannel(channel.ID)
	if target.URL != rotated {
		t.Fatalf("rotated url = %q", target.URL)
	}
	if target.SigningSecret != "0123456789abcdef" {
		t.Fatalf("omitted secret = %q, want kept", target.SigningSecret)
	}

	newSecret := "fedcba9876543210"
	if _, err := env.svc.Update(ctx, env.owner, env.wsID, channel.ID, UpdateInput{SigningSecret: &newSecret}); err != nil {
		t.Fatalf("replace secret: %v", err)
	}
	if got := env.openChannel(channel.ID); got.SigningSecret != newSecret {
		t.Fatalf("replaced secret = %q", got.SigningSecret)
	}
	if _, err := env.svc.Update(ctx, env.owner, env.wsID, channel.ID, UpdateInput{ClearSigningSecret: true}); err != nil {
		t.Fatalf("clear secret: %v", err)
	}
	if got := env.openChannel(channel.ID); got.SigningSecret != "" {
		t.Fatalf("cleared secret = %q", got.SigningSecret)
	}

	after, err := env.svc.q.GetAlertChannelByID(ctx, sqlc.GetAlertChannelByIDParams{ID: channel.ID, WorkspaceID: env.wsID})
	if err != nil {
		t.Fatalf("load after: %v", err)
	}
	if string(before.Config) == string(after.Config) {
		t.Fatal("config ciphertext did not change after rotation")
	}
}

func TestService_UpdateRejectsEmail(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()
	emailRow, err := env.svc.q.UpsertEmailAlertChannel(ctx, sqlc.UpsertEmailAlertChannelParams{
		ID: uuid.New(), WorkspaceID: env.wsID,
		Config: []byte(`{"recipients":["ops@example.com"]}`), Enabled: true,
	})
	if err != nil {
		t.Fatalf("upsert email: %v", err)
	}
	name := "Other"
	if _, err := env.svc.Update(ctx, env.owner, env.wsID, emailRow.ID, UpdateInput{Name: &name}); !errors.Is(err, ErrEmailChannel) {
		t.Fatalf("Update email = %v, want ErrEmailChannel", err)
	}
}

func TestService_DeleteRejectsEmail(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()
	emailRow, err := env.svc.q.UpsertEmailAlertChannel(ctx, sqlc.UpsertEmailAlertChannelParams{
		ID: uuid.New(), WorkspaceID: env.wsID,
		Config: []byte(`{"recipients":["ops@example.com"]}`), Enabled: true,
	})
	if err != nil {
		t.Fatalf("upsert email: %v", err)
	}
	if err := env.svc.Delete(ctx, env.owner, env.wsID, emailRow.ID); !errors.Is(err, ErrEmailChannel) {
		t.Fatalf("Delete email = %v, want ErrEmailChannel", err)
	}
}

func TestService_DeleteSoftDeletesAndReusesName(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()
	channel := env.createWebhook("Deploy hook")

	monitorID := env.insertMonitor("API")
	incidentID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new incident id: %v", err)
	}
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO incidents (id, workspace_id, monitor_id, status, started_at, last_notified_at)
		VALUES ($1, $2, $3, 'open', now(), now())`, incidentID, env.wsID, monitorID); err != nil {
		t.Fatalf("insert incident: %v", err)
	}
	deliveryID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new delivery id: %v", err)
	}
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO alert_deliveries (id, incident_id, channel_id, event, sequence, recipient, status, attempts, sent_at)
		VALUES ($1, $2, $3::uuid, 'opened', 0, $3::text, 'sent', 1, now())`, deliveryID, incidentID, channel.ID); err != nil {
		t.Fatalf("insert delivery: %v", err)
	}

	if err := env.svc.Delete(ctx, env.owner, env.wsID, channel.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := env.svc.q.GetAlertChannelByID(ctx, sqlc.GetAlertChannelByIDParams{ID: channel.ID, WorkspaceID: env.wsID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("Get deleted channel = %v, want ErrNoRows", err)
	}
	deliveries, err := env.svc.q.ListIncidentDeliveries(ctx, incidentID)
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	if len(deliveries) != 1 || deliveries[0].ChannelName != "Deploy hook" {
		t.Fatalf("deliveries = %+v", deliveries)
	}
	if _, err := env.svc.Create(ctx, env.owner, env.wsID, CreateInput{
		Kind: KindWebhook, Name: "Deploy hook", URL: "https://example.org/new",
	}); err != nil {
		t.Fatalf("reuse deleted name: %v", err)
	}
}

func TestService_NameDuplicateAndLimits(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()

	env.createWebhook("Deploy hook")
	if _, err := env.svc.Create(ctx, env.owner, env.wsID, CreateInput{
		Kind: KindWebhook, Name: "Deploy hook", URL: "https://example.org/other",
	}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name = %v, want ErrNameTaken", err)
	}

	for i := 0; i < 10; i++ {
		if i == 0 {
			continue // the duplicate-name attempt above already created one webhook
		}
		if _, err := env.svc.Create(ctx, env.owner, env.wsID, CreateInput{
			Kind: KindWebhook, Name: "Hook " + string(rune('A'+i)), URL: "https://example.org/hook",
		}); err != nil {
			t.Fatalf("create webhook %d: %v", i, err)
		}
	}
	if _, err := env.svc.Create(ctx, env.owner, env.wsID, CreateInput{
		Kind: KindWebhook, Name: "Hook overflow", URL: "https://example.org/hook",
	}); !errors.Is(err, ErrLimit) {
		t.Fatalf("per-kind limit = %v, want ErrLimit", err)
	}

	for i := 0; i < 10; i++ {
		if _, err := env.svc.Create(ctx, env.owner, env.wsID, CreateInput{
			Kind: KindSlack, Name: "Slack " + string(rune('A'+i)), URL: slackURL,
		}); err != nil {
			t.Fatalf("create slack %d: %v", i, err)
		}
	}
	for i := 0; i < 10; i++ {
		if _, err := env.svc.Create(ctx, env.owner, env.wsID, CreateInput{
			Kind: KindTelegram, Name: "Telegram " + string(rune('A'+i)),
			BotToken: telegramToken, ChatID: telegramChatID,
		}); err != nil {
			t.Fatalf("create telegram %d: %v", i, err)
		}
	}
	if _, err := env.svc.Create(ctx, env.owner, env.wsID, CreateInput{
		Kind: KindSlack, Name: "Slack overflow", URL: slackURL,
	}); !errors.Is(err, ErrLimit) {
		t.Fatalf("total limit = %v, want ErrLimit", err)
	}
}

func TestService_CrossWorkspaceAndSuspended(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()

	otherOwner := testutil.CreateVerifiedUser(t, env.pool, "other-owner@example.com", serviceTestPassword)
	other, err := env.ws.Create(ctx, otherOwner, "Other")
	if err != nil {
		t.Fatalf("create other workspace: %v", err)
	}
	foreign, err := env.svc.Create(ctx, otherOwner, other.ID, CreateInput{
		Kind: KindWebhook, Name: "Foreign", URL: webhookURL,
	})
	if err != nil {
		t.Fatalf("create foreign channel: %v", err)
	}
	otherWorkspaceGuard := foreign.ID
	name := "Renamed"
	if _, err := env.svc.Update(ctx, env.owner, env.wsID, otherWorkspaceGuard, UpdateInput{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace Update = %v, want ErrNotFound", err)
	}
	if err := env.svc.Delete(ctx, env.owner, env.wsID, otherWorkspaceGuard); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace Delete = %v, want ErrNotFound", err)
	}

	local := env.createWebhook("Local")
	if _, err := env.pool.Exec(ctx, `UPDATE workspaces SET suspended_at = now() WHERE id = $1`, env.wsID); err != nil {
		t.Fatalf("suspend workspace: %v", err)
	}
	if _, err := env.svc.Create(ctx, env.owner, env.wsID, CreateInput{
		Kind: KindWebhook, Name: "Another", URL: webhookURL,
	}); !errors.Is(err, ErrSuspended) {
		t.Fatalf("suspended Create = %v, want ErrSuspended", err)
	}
	if _, err := env.svc.Update(ctx, env.owner, env.wsID, local.ID, UpdateInput{Name: &name}); !errors.Is(err, ErrSuspended) {
		t.Fatalf("suspended Update = %v, want ErrSuspended", err)
	}
	if err := env.svc.Delete(ctx, env.owner, env.wsID, local.ID); !errors.Is(err, ErrSuspended) {
		t.Fatalf("suspended Delete = %v, want ErrSuspended", err)
	}
}

func TestService_ListMonitorChannels(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()
	monitorID := env.insertMonitor("API")
	webhook := env.createWebhook("Deploy hook")
	telegram, err := env.svc.Create(ctx, env.owner, env.wsID, CreateInput{
		Kind: KindTelegram, Name: "Telegram", BotToken: telegramToken, ChatID: telegramChatID,
		Enabled: ptrBool(false),
	})
	if err != nil {
		t.Fatalf("create telegram: %v", err)
	}

	list, err := env.svc.ListMonitorChannels(ctx, env.member, env.wsID, monitorID)
	if err != nil {
		t.Fatalf("list monitor channels: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("monitor channels = %+v, want 2", list)
	}
	byID := map[uuid.UUID]MonitorChannel{}
	for _, channel := range list {
		byID[channel.ID] = channel
	}
	if got := byID[webhook.ID]; !got.Enabled || !got.ChannelEnabled {
		t.Fatalf("webhook switch = %+v", got)
	}
	if got := byID[telegram.ID]; !got.Enabled || got.ChannelEnabled {
		t.Fatalf("telegram switch = %+v (globally disabled)", got)
	}

	if _, err := env.svc.ListMonitorChannels(ctx, env.viewer, env.wsID, monitorID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer list = %v, want ErrForbidden", err)
	}
	if _, err := env.svc.ListMonitorChannels(ctx, env.member, env.wsID, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown monitor = %v, want ErrNotFound", err)
	}
}

func TestService_SetMonitorChannel(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()
	monitorID := env.insertMonitor("API")
	first := env.createWebhook("First")
	second := env.createWebhook("Second")

	if err := env.svc.SetMonitorChannel(ctx, env.member, env.wsID, monitorID, first.ID, false); err != nil {
		t.Fatalf("set monitor channel: %v", err)
	}
	list, err := env.svc.ListMonitorChannels(ctx, env.member, env.wsID, monitorID)
	if err != nil {
		t.Fatalf("list monitor channels: %v", err)
	}
	for _, channel := range list {
		switch channel.ID {
		case first.ID:
			if channel.Enabled {
				t.Fatal("first channel still enabled")
			}
		case second.ID:
			if !channel.Enabled {
				t.Fatal("second channel changed")
			}
		}
	}

	if err := env.svc.SetMonitorChannel(ctx, env.member, env.wsID, monitorID, uuid.New(), true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown channel = %v, want ErrNotFound", err)
	}
	if err := env.svc.SetMonitorChannel(ctx, env.member, env.wsID, uuid.New(), first.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown monitor = %v, want ErrNotFound", err)
	}
	if err := env.svc.SetMonitorChannel(ctx, env.viewer, env.wsID, monitorID, first.ID, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer set = %v, want ErrForbidden", err)
	}

	otherOwner := testutil.CreateVerifiedUser(t, env.pool, "switch-other@example.com", serviceTestPassword)
	other, err := env.ws.Create(ctx, otherOwner, "Other switch")
	if err != nil {
		t.Fatalf("create other workspace: %v", err)
	}
	foreign, err := env.svc.Create(ctx, otherOwner, other.ID, CreateInput{
		Kind: KindWebhook, Name: "Foreign", URL: webhookURL,
	})
	if err != nil {
		t.Fatalf("create foreign channel: %v", err)
	}
	if err := env.svc.SetMonitorChannel(ctx, env.member, env.wsID, monitorID, foreign.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace channel = %v, want ErrNotFound", err)
	}

	if _, err := env.pool.Exec(ctx, `UPDATE workspaces SET suspended_at = now() WHERE id = $1`, env.wsID); err != nil {
		t.Fatalf("suspend workspace: %v", err)
	}
	if err := env.svc.SetMonitorChannel(ctx, env.member, env.wsID, monitorID, first.ID, true); !errors.Is(err, ErrSuspended) {
		t.Fatalf("suspended set = %v, want ErrSuspended", err)
	}
}

func TestService_SendTestEmail(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()
	if _, err := env.svc.q.UpsertEmailAlertChannel(ctx, sqlc.UpsertEmailAlertChannelParams{
		ID: uuid.New(), WorkspaceID: env.wsID,
		Config: []byte(`{"recipients":["ops@example.com","oncall@example.com"]}`), Enabled: true,
	}); err != nil {
		t.Fatalf("upsert email: %v", err)
	}
	emailChannel, err := env.svc.q.GetEmailAlertChannel(ctx, env.wsID)
	if err != nil {
		t.Fatalf("get email channel: %v", err)
	}

	if err := env.svc.SendTest(ctx, env.owner, env.wsID, emailChannel.ID); err != nil {
		t.Fatalf("send test email: %v", err)
	}
	messages := env.sender.Messages()
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(messages))
	}
	for _, msg := range messages {
		if !strings.Contains(msg.Subject, "Alerts") {
			t.Fatalf("subject = %q, want workspace name", msg.Subject)
		}
		if !strings.Contains(msg.Text, "Alerts") {
			t.Fatalf("text = %q, want workspace name", msg.Text)
		}
	}
	var deliveries int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM alert_deliveries`).Scan(&deliveries); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if deliveries != 0 {
		t.Fatalf("deliveries = %d, want 0", deliveries)
	}
}

func TestService_SendTestNotifier(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()
	webhook := env.createWebhook("Deploy hook")
	slack, err := env.svc.Create(ctx, env.owner, env.wsID, CreateInput{
		Kind: KindSlack, Name: "Slack", URL: slackURL,
	})
	if err != nil {
		t.Fatalf("create slack: %v", err)
	}
	telegram, err := env.svc.Create(ctx, env.owner, env.wsID, CreateInput{
		Kind: KindTelegram, Name: "Telegram", BotToken: telegramToken, ChatID: telegramChatID,
	})
	if err != nil {
		t.Fatalf("create telegram: %v", err)
	}

	for _, channel := range []Channel{webhook, slack, telegram} {
		if err := env.svc.SendTest(ctx, env.owner, env.wsID, channel.ID); err != nil {
			t.Fatalf("send test %s: %v", channel.Kind, err)
		}
	}
	if len(env.notify.events) != 3 {
		t.Fatalf("notifier events = %d, want 3", len(env.notify.events))
	}
	for i, event := range env.notify.events {
		if event.Kind != "test" || event.WorkspaceName != "Alerts" || event.WorkspaceSlug != "alerts" {
			t.Fatalf("event %d = %+v", i, event)
		}
		if event.Link != "http://localhost:5173" {
			t.Fatalf("event link = %q", event.Link)
		}
	}
	if env.notify.targets[0].Kind != KindWebhook || env.notify.targets[1].Kind != KindSlack || env.notify.targets[2].Kind != KindTelegram {
		t.Fatalf("targets = %+v", env.notify.targets)
	}
	if !strings.Contains(string(slackPayload(env.notify.events[1])), "Test notification from o1 for") {
		t.Fatal("slack test payload missing test line")
	}
	if !strings.Contains(string(telegramPayload(env.notify.events[2], telegramChatID)), "Test notification from o1 for") {
		t.Fatal("telegram test payload missing test line")
	}

	var deliveries int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM alert_deliveries`).Scan(&deliveries); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if deliveries != 0 {
		t.Fatalf("deliveries = %d, want 0", deliveries)
	}
}

func TestService_SendTestDisabledComplete(t *testing.T) {
	env := newServiceEnv(t)
	channel, err := env.svc.Create(context.Background(), env.owner, env.wsID, CreateInput{
		Kind: KindWebhook, Name: "Disabled hook", URL: webhookURL, Enabled: ptrBool(false),
	})
	if err != nil {
		t.Fatalf("create disabled channel: %v", err)
	}
	if err := env.svc.SendTest(context.Background(), env.owner, env.wsID, channel.ID); err != nil {
		t.Fatalf("send test on disabled channel: %v", err)
	}
}

func TestService_SendTestIncomplete(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()
	channel := env.createWebhook("Corrupt hook")
	if _, err := env.pool.Exec(ctx, `UPDATE alert_channels SET config = '{"url_enc":""}' WHERE id = $1`, channel.ID); err != nil {
		t.Fatalf("corrupt config: %v", err)
	}
	if err := env.svc.SendTest(ctx, env.owner, env.wsID, channel.ID); !errors.Is(err, ErrInvalidChannelConfig) {
		t.Fatalf("SendTest = %v, want ErrInvalidChannelConfig", err)
	}
}

func TestService_SendTestAdminOnly(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()
	channel := env.createWebhook("Admin hook")
	if err := env.svc.SendTest(ctx, env.member, env.wsID, channel.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member SendTest = %v, want ErrForbidden", err)
	}
	if err := env.svc.SendTest(ctx, env.viewer, env.wsID, channel.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer SendTest = %v, want ErrForbidden", err)
	}
}

func ptrString(value string) *string {
	return &value
}

func ptrBool(value bool) *bool {
	return &value
}

func TestAuditAlertChannelEvents(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()
	const signingSecret = "0123456789abcdef"

	channel, err := env.svc.Create(ctx, env.owner, env.wsID, CreateInput{
		Kind: KindWebhook, Name: "Audit Ops", URL: webhookURL, SigningSecret: signingSecret,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	assertEvent := func(kind string) string {
		t.Helper()
		var gotKind, detail string
		var actor *uuid.UUID
		if err := env.pool.QueryRow(ctx,
			`SELECT kind, detail::text, actor_user_id FROM security_events WHERE workspace_id=$1 AND kind=$2`,
			env.wsID, kind).Scan(&gotKind, &detail, &actor); err != nil {
			t.Fatalf("load %s: %v", kind, err)
		}
		if actor == nil || *actor != env.owner {
			t.Fatalf("%s actor = %v, want %v", kind, actor, env.owner)
		}
		for _, leak := range []string{webhookURL, signingSecret} {
			if strings.Contains(detail, leak) {
				t.Fatalf("%s leaked %q: %s", kind, leak, detail)
			}
		}
		if !strings.Contains(detail, channel.ID.String()) || !strings.Contains(detail, "Audit Ops") {
			t.Fatalf("%s detail = %s", kind, detail)
		}
		return detail
	}

	assertEvent("alert_channel_created")

	if _, err := env.svc.Update(ctx, env.owner, env.wsID, channel.ID, UpdateInput{Enabled: ptrBool(false)}); err != nil {
		t.Fatalf("update channel: %v", err)
	}
	updated := assertEvent("alert_channel_updated")
	if !strings.Contains(updated, "false") {
		t.Fatalf("alert_channel_updated detail = %s, want enabled=false", updated)
	}

	if err := env.svc.Delete(ctx, env.owner, env.wsID, channel.ID); err != nil {
		t.Fatalf("delete channel: %v", err)
	}
	assertEvent("alert_channel_deleted")
}

# Plan 5 — Non-Email Alert Channels Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add generic webhook, Slack incoming-webhook, and Telegram bot alert channels to the existing incident pipeline, with encrypted secrets, SSRF-safe delivery, per-monitor switches, test sends, and channel-aware delivery history.

**Architecture:** Extend the existing `alert_channels`/`monitor_alert_channels`/`alert_deliveries` schema with soft deletes and three new kinds. A new `internal/alerts` package owns channel config validation, secret sealing, CRUD, and outbound adapters. The existing incident enqueue and River alert worker stay in place; the worker becomes a dispatcher that renders email as before and routes other kinds through the guarded notifier.

**Tech Stack:** Go 1.26, PostgreSQL, goose migrations, sqlc, River, existing `email.Sender` (Mailpit in development), `secretbox`, `probe.Guard`, React 19, TanStack Router/Query, shadcn/ui, Vitest/MSW, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-06-plan5-alert-channels-design.md`

## Global Constraints

- Plan 5 scope only. Do not add the public status page, Slack OAuth, custom webhook headers, per-channel reminder cadence, maintenance windows, on-call schedules, SMS, or production email/TLS.
- Webhook and Slack URLs pass `probe.Guard` validation at save and again before every send. Alert deliveries never follow redirects; a 3xx response is a permanent failure.
- Slack URLs are `https` only on `hooks.slack.com` or `hooks.slack-gov.com`. Telegram bot tokens only go to `https://api.telegram.org`.
- Secrets (webhook URL, Slack URL, Telegram bot token, optional webhook signing secret) are sealed with `secretbox` and never returned by the API after save, logged, stored in a delivery row, or included in an error.
- Non-email delivery `recipient` is the channel UUID. Deduplication must survive channel rename and secret rotation.
- The webhook payload is event-derived: `monitor.status` is `down` for opened/reminder and `up` for resolved; `incident.status` is `open` for opened/reminder and `resolved` for resolved. Timestamps and acknowledgement state come from the current incident row.
- Email behavior, email routes, email labels, and existing query keys remain unchanged. The generic `PATCH`/`DELETE /alert-channels/{id}` reject the email channel with `422`; email is managed only through `PUT /alert-channels/email`.
- Test sends: email sends to every recipient; webhook sends `event:"test"` with null monitor/incident; Slack/Telegram send a test line. Disabled-but-complete channels are testable. A test writes no delivery row.
- New monitors attach a join row for every non-deleted channel, including globally disabled channels. Creating a channel attaches every existing monitor.
- Roles: channel list/create/update/delete/test is admin; monitor channel list/toggle is member; viewers are read-only.
- Soft-delete channels; keep delivery history; an active name may be reused after delete.
- Limits: name 1–60 characters; URL ≤ 2048; signing secret 16–128; bot token ≤ 128 and matching `^[0-9]+:[A-Za-z0-9_-]+$`; chat ID ≤ 64 and numeric (optionally negative) or `@channelusername`; at most 10 active channels per kind and 30 active total.
- Retry classification: 2xx succeeds; 3xx and other 4xx are permanent; 408, 425, 429, 5xx, timeouts, and transport errors retry up to the existing five attempts. Each attempt has a 10-second timeout and drains at most 64 KiB of response body.
- Reminders stay at 30 minutes, maximum 10, and skip acknowledged/resolved/paused/suspended/stale incidents exactly as today.
- UI copy is English. Every task ends with formatting, lint, typecheck, tests, and a commit.

## Review Focus

1. A webhook or Slack URL that resolves to loopback, private, link-local, or metadata space must be rejected at save and again at send, including after DNS changes; a redirect must not move a signed delivery to another host.
2. Secrets must never appear in a channel response, request echo, log line, `last_error`, delivery `recipient`, or test-send error.
3. A retried probe or reminder tick must not duplicate deliveries across mixed channels, and rename/secret rotation must not change dedup; a delivery for a soft-deleted channel must be skipped.
4. An incomplete, disabled, or malformed channel must never stop probing, incident creation, or email delivery; enabling incomplete config is rejected, while a disabled-but-complete channel can be tested.
5. User-controlled workspace/monitor names, chat IDs, and URLs must not corrupt Slack/Telegram JSON or the webhook signature; HMAC covers the exact body bytes.

---

## File Structure

```text
backend/
  internal/db/migrations/00005_alert_channel_kinds.sql   # create
  internal/db/queries/alerts.sql                          # modify
  internal/db/queries/incidents.sql                       # modify: delivery listing joins channel
  internal/db/sqlc/*                                      # regenerated
  internal/db/queries_test.go                             # modify: schema/query test
  internal/alerts/
    types.go                                              # create
    config.go                                             # create
    notifier.go                                           # create
    service.go                                            # create
    handlers.go                                           # create
    config_test.go                                        # create
    notifier_test.go                                      # create
    service_test.go                                       # create
    handlers_test.go                                      # create
  internal/probe/policy.go                                # modify: guarded HTTP client
  internal/probe/policy_test.go                           # modify
  internal/incidents/lifecycle.go                         # modify: mixed-channel enqueue
  internal/incidents/reminders.go                         # modify: mixed-channel reminders
  internal/incidents/lifecycle_test.go                    # modify
  internal/incidents/reminders_test.go                    # modify
  internal/worker/alert_worker.go                         # modify: dispatcher
  internal/worker/alert_worker_test.go                    # modify
  internal/monitors/service.go                            # modify: attach every active channel
  internal/monitors/service_test.go                       # modify
  internal/api/router.go                                  # modify: service + routes
  cmd/worker/main.go                                      # modify: pass box + notifier
web/
  src/lib/api/types.ts                                    # modify
  src/lib/query/keys.ts                                   # modify
  src/features/alerts/api.ts                              # modify
  src/features/alerts/hooks.ts                            # modify
  src/features/alerts/AlertSettingsScreen.tsx             # modify
  src/features/alerts/ChannelDialog.tsx                   # create
  src/features/alerts/ChannelList.tsx                     # create
  src/features/alerts/MonitorAlertChannels.tsx            # create
  src/features/alerts/ChannelDialog.test.tsx              # create
  src/features/alerts/ChannelList.test.tsx                # create
  src/features/alerts/MonitorAlertChannels.test.tsx       # create
  src/features/alerts/AlertSettingsScreen.test.tsx        # modify
  src/features/monitors/MonitorDetailScreen.tsx           # modify
  src/features/monitors/MonitorDetailScreen.test.tsx      # modify
  src/features/incidents/IncidentDetailScreen.tsx         # modify
  src/features/incidents/IncidentDetailScreen.test.tsx    # modify
  tests/e2e/smoke.spec.ts                                 # modify
README.md                                                 # modify: alert channels section
```

Channel CRUD, config, notifiers, and test sends live in `internal/alerts`. Incident lifecycle/enqueue stays in `internal/incidents`. The worker owns delivery execution. Dashboard feature folders own their API calls, hooks, and screens.

---

### Task 1: Alert channel schema and queries

**Files:**
- Create: `backend/internal/db/migrations/00005_alert_channel_kinds.sql`
- Modify: `backend/internal/db/queries/alerts.sql`
- Modify: `backend/internal/db/queries/incidents.sql`
- Modify: `backend/internal/incidents/types.go`
- Modify: `backend/internal/incidents/service.go`
- Generate: `backend/internal/db/sqlc/*`
- Test: `backend/internal/db/queries_test.go`

**Interfaces:**
- Consumes: existing `alert_channels`, `monitor_alert_channels`, `alert_deliveries`, `monitors`, and `workspaces`.
- Produces:
  - `sqlc.AlertChannel.DeletedAt *time.Time`
  - `sqlc.GetAlertChannelByIDParams{ID, WorkspaceID uuid.UUID}`
  - `sqlc.GetActiveAlertChannelByNameParams{WorkspaceID uuid.UUID, Kind, Name string}`
  - `sqlc.CreateAlertChannelParams{ID, WorkspaceID uuid.UUID, Kind, Name string, Config []byte, Enabled bool}`
  - `sqlc.UpdateAlertChannelParams{ID, WorkspaceID uuid.UUID, Name string, Config []byte, Enabled bool}`
  - `CountActiveAlertChannels(ctx, sqlc.CountActiveAlertChannelsParams{WorkspaceID uuid.UUID, Kind string}) (int64, error)`
  - `CountAllActiveAlertChannels(ctx, workspaceID uuid.UUID) (int64, error)`
  - `SoftDeleteAlertChannel(ctx, sqlc.SoftDeleteAlertChannelParams{ID, WorkspaceID uuid.UUID}) (int64, error)`
  - `AttachMonitorsToChannel(ctx, sqlc.AttachMonitorsToChannelParams{ChannelID, WorkspaceID uuid.UUID}) error`
  - `ListMonitorAlertChannels(ctx, sqlc.ListMonitorAlertChannelsParams{MonitorID, WorkspaceID uuid.UUID}) ([]sqlc.ListMonitorAlertChannelsRow, error)` with row fields `ID`, `Kind`, `Name`, `ChannelEnabled`, `MonitorEnabled`
  - `sqlc.GetAlertDeliveryRow` gains `MonitorID uuid.UUID`, `ChannelName string`, `ChannelDeletedAt *time.Time`
  - `sqlc.ListIncidentDeliveriesRow` gains `ChannelKind string`, `ChannelName string`

- [ ] **Step 1: Write the failing schema/query test**

Append to `backend/internal/db/queries_test.go`:

```go
func TestAlertChannelSchema_SoftDeleteAndFilters(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()

	userID := testutil.CreateVerifiedUser(t, pool, "alert-schema@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1,'Alerts','alerts-schema',$2)`, wsID, userID); err != nil {
		t.Fatal(err)
	}
	monitorID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO monitors (id, workspace_id, name, url, method, created_by) VALUES ($1,$2,'API','https://example.com','GET',$3)`, monitorID, wsID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO monitor_state (monitor_id) VALUES ($1)`, monitorID); err != nil {
		t.Fatal(err)
	}

	q := sqlc.New(pool)
	channelID := uuid.New()
	channel, err := q.CreateAlertChannel(ctx, sqlc.CreateAlertChannelParams{
		ID: channelID, WorkspaceID: wsID, Kind: "webhook", Name: "Deploy",
		Config: []byte(`{"url_enc":"v1:x","url_preview":"https://example.com/…"}`), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertMonitorAlertChannel(ctx, sqlc.UpsertMonitorAlertChannelParams{
		MonitorID: monitorID, ChannelID: channelID, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	enabled, err := q.ListEnabledAlertChannelsForMonitor(ctx, monitorID)
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 1 || enabled[0].ID != channelID {
		t.Fatalf("enabled channels = %+v, want the webhook channel", enabled)
	}

	if rows, err := q.SoftDeleteAlertChannel(ctx, sqlc.SoftDeleteAlertChannelParams{
		ID: channel.ID, WorkspaceID: wsID,
	}); err != nil || rows != 1 {
		t.Fatalf("soft delete rows = %d, err = %v", rows, err)
	}
	enabled, err = q.ListEnabledAlertChannelsForMonitor(ctx, monitorID)
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 0 {
		t.Fatalf("enabled channels after delete = %+v, want none", enabled)
	}
	if _, err := q.GetAlertChannelByID(ctx, sqlc.GetAlertChannelByIDParams{ID: channelID, WorkspaceID: wsID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("get deleted channel error = %v, want pgx.ErrNoRows", err)
	}

	// A deleted active name can be reused.
	if _, err := q.CreateAlertChannel(ctx, sqlc.CreateAlertChannelParams{
		ID: uuid.New(), WorkspaceID: wsID, Kind: "webhook", Name: "Deploy",
		Config: []byte(`{"url_enc":"v1:y","url_preview":"https://example.org/…"}`), Enabled: false,
	}); err != nil {
		t.Fatalf("reuse deleted name: %v", err)
	}

	// The email upsert still infers the partial unique index.
	if _, err := q.UpsertEmailAlertChannel(ctx, sqlc.UpsertEmailAlertChannelParams{
		ID: uuid.New(), WorkspaceID: wsID, Config: []byte(`{"recipients":["ops@example.com"]}`), Enabled: true,
	}); err != nil {
		t.Fatalf("upsert email channel: %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/db -run TestAlertChannelSchema -v`
Expected: FAIL because `CreateAlertChannel`, `SoftDeleteAlertChannel`, `GetAlertChannelByID`, `DeletedAt`, and the query filters do not exist.

- [ ] **Step 3: Add migration `00005_alert_channel_kinds.sql`**

Up:
- Drop `alert_channels_kind_check` and add it back with `kind IN ('email','webhook','slack','telegram')`.
- Add `deleted_at timestamptz`.
- Drop `alert_channels_workspace_kind_name_key`.
- Create `alert_channels_workspace_kind_name_key` as a unique index on `(workspace_id, kind, name) WHERE deleted_at IS NULL`.
- Create `alert_channels_active_idx` on `(workspace_id) WHERE deleted_at IS NULL`.

Down: delete non-email rows (so the old unique index can be restored), drop the active index, restore the plain unique index, drop `deleted_at`, and restore the email-only check constraint.

- [ ] **Step 4: Update `alerts.sql` queries**

Pin these changes:
- `UpsertEmailAlertChannel`: `ON CONFLICT (workspace_id, kind, name) WHERE deleted_at IS NULL`.
- `GetEmailAlertChannel`, `GetMonitorAlertState` join, `ListAlertChannels`, and `ListEnabledAlertChannelsForMonitor`: add `deleted_at IS NULL`.
- Add the named queries listed in this task's Interfaces block.
- `ListMonitorAlertChannels` starts from `monitors m` and joins `alert_channels c` on the workspace with `c.deleted_at IS NULL`, then left-joins `monitor_alert_channels` on the monitor and channel. This returns zero rows for an unknown monitor and includes globally disabled channels with `channel_enabled=false`.
- `SoftDeleteAlertChannel` updates `deleted_at`/`updated_at` only where `kind <> 'email'` and `deleted_at IS NULL`, using `:execrows`.

- [ ] **Step 5: Add delivery channel fields**

In `incidents.sql`:
- `GetAlertDelivery`: select `m.id AS monitor_id`, `c.name AS channel_name`, `c.deleted_at AS channel_deleted_at` in addition to the existing fields.
- `ListIncidentDeliveries`: join `alert_channels c ON c.id = d.channel_id` and select `d.*, c.kind AS channel_kind, c.name AS channel_name`.

In `incidents/types.go`, add to `Delivery`:
```go
ChannelID   uuid.UUID `json:"channel_id"`
ChannelKind string    `json:"channel_kind"`
ChannelName string    `json:"channel_name"`
```

In `incidents/service.go`, map those three fields in `Deliveries()` from the generated `ListIncidentDeliveriesRow`. Existing email rows keep their `recipient` value.

- [ ] **Step 6: Regenerate sqlc**

Run: `cd backend && sqlc generate`
Expected: generated structs and methods compile and include the new fields.

- [ ] **Step 7: Run the test to verify it passes**

Run: `cd backend && go test ./internal/db -run TestAlertChannelSchema -v`
Expected: PASS.

- [ ] **Step 8: Quality gate and commit**

Run: `cd backend && gofmt -w internal/db internal/incidents && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/db backend/internal/incidents/types.go backend/internal/incidents/service.go
git commit -m "feat: add alert channel kinds and soft-delete schema"
```

---

### Task 2: Channel config, validation, and secret handling

**Files:**
- Create: `backend/internal/alerts/types.go`
- Create: `backend/internal/alerts/config.go`
- Test: `backend/internal/alerts/config_test.go`

**Interfaces:**
- Consumes: `secretbox.Box`, `probe.Guard`, `sqlc.AlertChannel`.
- Produces:
  - Kind constants `KindEmail`, `KindWebhook`, `KindSlack`, `KindTelegram`; `EmailChannelName = "Email"`.
  - `EmailConfig{Recipients []string}`, `WebhookConfig{URLEnc, URLPreview, SigningSecretEnc string}`, `SlackConfig{URLEnc, URLPreview string}`, `TelegramConfig{BotTokenEnc, ChatID string}`.
  - `Channel` API view with `ID`, `Kind`, `Name`, `Enabled`, `Recipients []string`, `URLPreview string`, `SigningSecretSet bool`, `BotTokenSet bool`, `ChatID string`, `CreatedAt`, `UpdatedAt`.
  - `CreateInput{Kind, Name string, Enabled *bool, URL, SigningSecret, BotToken, ChatID string}`.
  - `UpdateInput{Name *string, Enabled *bool, URL *string, SigningSecret *string, ClearSigningSecret bool, BotToken *string, ChatID *string}`.
  - `MonitorChannel{ID uuid.UUID, Kind, Name string, ChannelEnabled, Enabled bool}`.
  - `Target{Kind, URL, SigningSecret, BotToken, ChatID string}`.
  - `ValidationError{Fields map[string]string}` with `Error()` and `Is(workspaces.ErrValidation)`.
  - Sentinels `ErrInvalidChannelConfig`, `ErrEmailChannel`, `ErrNameTaken`, `ErrLimit`, `ErrSuspended`; alias `ErrNotFound`/`ErrForbidden` to the workspace sentinels.
  - `ValidateName(raw string) (string, error)`
  - `SealCreateConfig(box *secretbox.Box, guard *probe.Guard, kind string, in CreateInput) ([]byte, error)`
  - `MergeUpdateConfig(box *secretbox.Box, guard *probe.Guard, kind string, existing []byte, in UpdateInput) ([]byte, error)`
  - `ChannelView(row sqlc.AlertChannel) (Channel, error)`
  - `OpenTarget(box *secretbox.Box, kind string, raw []byte) (Target, error)`

- [ ] **Step 1: Write the failing validation test**

Create `backend/internal/alerts/config_test.go` with a test resolver implementing `probe.Resolver` and a table test:

```go
type staticResolver struct{ ips []string }

func (r staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	out := make([]net.IPAddr, 0, len(r.ips))
	for _, raw := range r.ips {
		out = append(out, net.IPAddr{IP: net.ParseIP(raw)})
	}
	return out, nil
}

func testGuard(ips ...string) *probe.Guard {
	return probe.NewGuardWithResolver(probe.Policy{
		AllowedPorts: map[int]struct{}{80: {}, 443: {}},
	}, staticResolver{ips: ips})
}

func TestSealCreateConfig_Validation(t *testing.T) {
	box, _ := secretbox.New(bytes.Repeat([]byte{7}, 32))
	public := testGuard("93.184.216.34")
	private := testGuard("127.0.0.1")

	cases := []struct {
		name    string
		kind    string
		in      CreateInput
		guard   *probe.Guard
		wantErr bool
		field   string
	}{
		{"webhook valid", KindWebhook, CreateInput{URL: "https://example.com/o1"}, public, false, ""},
		{"webhook private blocked", KindWebhook, CreateInput{URL: "https://internal.example/o1"}, private, true, "url"},
		{"webhook userinfo blocked", KindWebhook, CreateInput{URL: "https://u:p@example.com/o1"}, public, true, "url"},
		{"webhook fragment blocked", KindWebhook, CreateInput{URL: "https://example.com/o1#x"}, public, true, "url"},
		{"webhook short secret", KindWebhook, CreateInput{URL: "https://example.com/o1", SigningSecret: "short"}, public, true, "signing_secret"},
		{"slack valid", KindSlack, CreateInput{URL: "https://hooks.slack.com/services/T/B/X"}, public, false, ""},
		{"slack wrong host", KindSlack, CreateInput{URL: "https://example.com/services/T/B/X"}, public, true, "url"},
		{"slack http rejected", KindSlack, CreateInput{URL: "http://hooks.slack.com/services/T/B/X"}, public, true, "url"},
		{"telegram valid", KindTelegram, CreateInput{BotToken: "123456:ABC_def-1", ChatID: "-1001234567890"}, public, false, ""},
		{"telegram bad token", KindTelegram, CreateInput{BotToken: "not-a-token", ChatID: "-100123"}, public, true, "bot_token"},
		{"telegram bad chat", KindTelegram, CreateInput{BotToken: "123456:ABC", ChatID: "room one"}, public, true, "chat_id"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SealCreateConfig(box, tc.guard, tc.kind, tc.in)
			if tc.wantErr {
				var validation *ValidationError
				if !errors.As(err, &validation) || validation.Fields[tc.field] == "" {
					t.Fatalf("error = %v, want field %q", err, tc.field)
				}
				return
			}
			if err != nil {
				t.Fatalf("SealCreateConfig: %v", err)
			}
		})
	}

	if _, err := SealCreateConfig(box, public, KindEmail, CreateInput{}); !errors.Is(err, ErrEmailChannel) {
		t.Fatalf("email kind error = %v, want ErrEmailChannel", err)
	}
}
```

Add a second test `TestConfig_RoundTripAndMerge` asserting:
- `SealCreateConfig` for each kind then `ChannelView` exposes `URLPreview`/`SigningSecretSet`/`BotTokenSet`/`ChatID` and never the plaintext.
- `OpenTarget` returns the original URL/token/chat ID and signing secret.
- `MergeUpdateConfig` with a nil `SigningSecret` keeps the existing sealed secret, a non-empty value replaces it, and `ClearSigningSecret: true` clears it.
- `MergeUpdateConfig` with `URL: ptr("")` returns a `url` validation error.
- `ValidateName("  ")` fails; `ValidateName("  Deploy hook  ")` returns `Deploy hook`; 61 runes fail.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/alerts -run TestSealCreateConfig -v`
Expected: FAIL because the package and functions do not exist.

- [ ] **Step 3: Add `types.go` and `config.go`**

Pin these rules:
- `ValidateName` trims with `strings.TrimSpace`, counts runes, and returns a `name` field error outside 1–60.
- `SealCreateConfig` switches on kind. `email` returns `ErrEmailChannel`; an unknown kind returns a `kind` field error.
- Webhook/Slack URL validation calls `guard.ValidateURL`, rejects URLs longer than 2048, and for Slack requires `https`, an empty explicit port, and host `hooks.slack.com` or `hooks.slack-gov.com`.
- `URLPreview` is `scheme://host/…` with the actual port when present. It never includes path, query, fragment, or userinfo.
- Signing secret is 16–128 characters when non-empty.
- Telegram token matches `^[0-9]+:[A-Za-z0-9_-]+$` and is at most 128 characters. Chat ID matches `^-?[0-9]+$` or `^@[A-Za-z][A-Za-z0-9_]{3,}$` and is at most 64 characters.
- Sealing an empty string returns `""`; required fields are rejected when empty.
- Field errors describe the rule (“url is not allowed”, “bot token is invalid”) but never echo the submitted URL, token, or secret value.
- `MergeUpdateConfig` decodes the existing config, applies non-nil/non-empty fields, keeps omitted secrets, rejects clearing required fields, and marshals the final config.
- `ChannelView` decodes without decrypting. Email returns `Recipients` as a non-nil slice. Other kinds return preview/set booleans/chat ID.
- `OpenTarget` decrypts with `box.Open`, maps to `Target`, and returns `ErrInvalidChannelConfig` for missing/corrupt required values. It does not accept email.
- JSON tags match the design exactly: `url_preview`, `signing_secret_set`, `bot_token_set`, `chat_id`, `channel_enabled`. The `Channel` response uses the same field names as the TS type in Task 7.

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd backend && go test ./internal/alerts -run 'TestSealCreateConfig|TestConfig_RoundTripAndMerge' -v`
Expected: PASS.

- [ ] **Step 5: Quality gate and commit**

Run: `cd backend && gofmt -w internal/alerts && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/alerts
git commit -m "feat: add alert channel config validation and secret sealing"
```

---

### Task 3: Guarded HTTP client and outbound notifiers

**Files:**
- Modify: `backend/internal/probe/policy.go`
- Modify: `backend/internal/probe/policy_test.go`
- Create: `backend/internal/alerts/notifier.go`
- Test: `backend/internal/alerts/notifier_test.go`

**Interfaces:**
- Consumes: `probe.Guard`, `Target`, `secretbox`-opened values.
- Produces:
  - `func (g *Guard) HTTPClient(followRedirects bool) *http.Client`
  - `Event{Kind string, Sequence int, WorkspaceName, WorkspaceSlug string, MonitorID uuid.UUID, MonitorName, MonitorURL string, IncidentID uuid.UUID, StartedAt time.Time, AcknowledgedAt, ResolvedAt *time.Time, Link string}`
  - `NotificationSender interface { Send(ctx context.Context, target Target, event Event) error }`
  - `Notifier` with `NewNotifier(guard *probe.Guard, telegramBaseURL string) *Notifier`
  - `func IsPermanent(err error) bool`

- [ ] **Step 1: Write the failing notifier tests**

Create `backend/internal/alerts/notifier_test.go` with an `httptest.Server` capturing method, path, headers, and body. Assert:

```go
func TestNotifier_WebhookPayloadAndSignature(t *testing.T) {
	secret := "0123456789abcdef"
	var gotBody []byte
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(permissiveGuard(t, srv.URL), "")
	started := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	err := n.Send(context.Background(), Target{
		Kind: KindWebhook, URL: srv.URL, SigningSecret: secret,
	}, Event{
		Kind: "opened", Sequence: 0,
		WorkspaceName: "Acme", WorkspaceSlug: "acme",
		MonitorID: uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		MonitorName: "API", MonitorURL: "https://example.com/…",
		IncidentID: uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		StartedAt: started, Link: "https://app.example.com/w/acme/incidents/2222",
	})
	if err != nil { t.Fatalf("Send: %v", err) }

	var payload map[string]any
	if err := json.Unmarshal(gotBody, &payload); err != nil { t.Fatal(err) }
	if payload["event"] != "opened" { t.Fatalf("event = %v", payload["event"]) }
	monitor := payload["monitor"].(map[string]any)
	if monitor["status"] != "down" { t.Fatalf("monitor.status = %v", monitor["status"]) }
	incident := payload["incident"].(map[string]any)
	if incident["status"] != "open" { t.Fatalf("incident.status = %v", incident["status"]) }
	if gotHeaders.Get("X-O1-Event") != "opened" { t.Fatalf("X-O1-Event = %q", gotHeaders.Get("X-O1-Event")) }
	if gotHeaders.Get("User-Agent") != "o1-alerts/1.0" { t.Fatalf("User-Agent = %q", gotHeaders.Get("User-Agent")) }

	timestamp := gotHeaders.Get("X-O1-Timestamp")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + string(gotBody)))
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if gotHeaders.Get("X-O1-Signature") != want { t.Fatalf("signature = %q, want %q", gotHeaders.Get("X-O1-Signature"), want) }
}
```

Add tests:
- `TestNotifier_ResolvedPayload` asserts `monitor.status == "up"`, `incident.status == "resolved"`, `resolved_at` present.
- `TestNotifier_TestPayload` asserts `event == "test"`, `monitor == nil`, `incident == nil`.
- `TestNotifier_SlackPayload` calls the pure Slack body builder (for example `slackPayload(event)`) and asserts `{"text": ...}` contains workspace, monitor, link, and a status emoji. Slack `Send` also validates the Slack host, so its HTTP success path is exercised through the service fake and backend handler tests rather than a localhost URL.
- `TestNotifier_TelegramPayload` uses `NewNotifier(permissiveGuard(t, srv.URL), srv.URL)` and asserts path `/bot<token>/sendMessage`, chat ID, `disable_web_page_preview: true`, and the text. Add a test-event case asserting the text starts with “Test notification from o1 for”.
- `TestNotifier_StatusClassification` table: 200/204 → nil; 302 → `IsPermanent`; 400 → permanent; 408/425/429/500/503 → not permanent. Assert every error string lacks the server URL, path, query, and token.
- `TestNotifier_WebhookPrivateBlocked` uses a guard whose resolver returns `127.0.0.1`, a webhook target for a public-looking hostname, and asserts the send is permanent and no request is made.
- `TestNotifier_SlackHostRejected` uses a `KindSlack` target pointed at `127.0.0.1`; assert `IsPermanent` and that no server request occurred.
- `TestNotifier_UnknownKindPermanent` asserts an unknown target kind is permanent and sends nothing.
- `TestNotifier_NoRedirects` has a server that 302s to a second server; assert the second server is never called and the delivery is permanent.
- `TestProbeGuard_HTTPClientRedirectPolicy` in `policy_test.go` asserts `HTTPClient(false)` returns the 302 response and `HTTPClient(true)` follows within policy.

Create `permissiveGuard(t, rawURL)` in `notifier_test.go`: parse the URL, put its port into `Policy.AllowedPorts`, set `AllowPrivateNetworks: true`, and use `probe.NewGuard`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/alerts -run TestNotifier -v && go test ./internal/probe -run TestProbeGuard_HTTPClient -v`
Expected: FAIL because `HTTPClient`, `Notifier`, and `Event` do not exist.

- [ ] **Step 3: Implement `probe.Guard.HTTPClient`**

Build one `http.Transport` with proxies disabled, keep-alives off, `DialContext: g.dialContext`, and `TLSHandshakeTimeout: g.tlsHandshakeTimeout()`. When `followRedirects` is false, `CheckRedirect` returns `http.ErrUseLastResponse`; otherwise use `g.checkRedirect`. A nil receiver uses `NewGuard(Policy{})` like `NewProber`.

- [ ] **Step 4: Implement `alerts/notifier.go`**

Pin:
- `Notifier` holds the guarded client, a plain Telegram client with a 10-second timeout and `CheckRedirect` returning `http.ErrUseLastResponse`, the Telegram base URL, and a clock function defaulting to `time.Now`.
- `Send` wraps the request in a 10-second context and switches on `target.Kind`.
- Before a webhook/Slack send, re-run `guard.ValidateURL` and the Slack host check. A policy rejection is permanent. Do not re-validate Telegram because its origin is fixed by code.
- Webhook body fields are exactly: `event`, `sequence`, `workspace{name,slug}`, `monitor{id,name,url,status}|null`, `incident{id,status,started_at,acknowledged_at,resolved_at}|null`, `link`.
- `monitor.status`/`incident.status` derive from `Event.Kind`; test events null both objects.
- Headers: `Content-Type: application/json`, `User-Agent: o1-alerts/1.0`, `X-O1-Event`. Add timestamp/signature only when a signing secret is configured. Signature is `sha256=<hex HMAC-SHA256(timestamp + "." + body)>` over the exact marshaled bytes that are sent.
- Build every JSON body with `json.Marshal` from a struct; never assemble JSON with string concatenation, and let `encoding/json` escape user-controlled names.
- Slack posts `{"text":"..."}` with a status emoji, workspace, monitor, redacted URL, event wording, and link.
- Telegram posts `{"chat_id":"...","text":"...","disable_web_page_preview":true}` to `<base>/bot<token>/sendMessage`.
- Response: drain at most 64 KiB and close. 2xx → nil. 408, 425, 429, and 5xx → transient. Every other non-2xx status (including all 3xx and other 4xx) → permanent. Transport/timeout errors → transient.
- `DeliveryError` implements `Error()` returning only a sanitized message (`webhook returned HTTP 500`, `telegram request failed`) and `Unwrap()` returning the cause. `IsPermanent` uses `errors.As`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/alerts -run TestNotifier -v && go test ./internal/probe -run TestProbeGuard_HTTPClient -v`
Expected: PASS.

- [ ] **Step 6: Quality gate and commit**

Run: `cd backend && gofmt -w internal/alerts internal/probe && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/alerts/notifier.go backend/internal/alerts/notifier_test.go backend/internal/probe
git commit -m "feat: add SSRF-safe alert notifiers for webhook, Slack, and Telegram"
```

---

### Task 4: Channel CRUD service and API

**Files:**
- Create: `backend/internal/alerts/service.go`
- Create: `backend/internal/alerts/handlers.go`
- Test: `backend/internal/alerts/service_test.go`
- Test: `backend/internal/alerts/handlers_test.go`
- Modify: `backend/internal/api/router.go`

**Interfaces:**
- Consumes: Task 1 queries, Task 2 config helpers, Task 3 `NotificationSender`.
- Produces:
  - `Service` and `NewService(pool *pgxpool.Pool, q *sqlc.Queries, ws *workspaces.Service, box *secretbox.Box, guard *probe.Guard, notifier NotificationSender, sender email.Sender, baseURL string) *Service`
  - `List(ctx, actorID, workspaceID uuid.UUID) ([]Channel, error)`
  - `Create(ctx, actorID, workspaceID uuid.UUID, in CreateInput) (Channel, error)`
  - `Update(ctx, actorID, workspaceID, channelID uuid.UUID, in UpdateInput) (Channel, error)`
  - `Delete(ctx, actorID, workspaceID, channelID uuid.UUID) error`
  - `ListChannelsHandler`, `CreateChannelHandler`, `UpdateChannelHandler`, `DeleteChannelHandler`
  - `api.Dependencies.AlertService *alerts.Service`

- [ ] **Step 1: Write the failing service tests**

Create `backend/internal/alerts/service_test.go` with a Postgres-backed env:

```go
type serviceEnv struct {
	t      *testing.T
	pool   *pgxpool.Pool
	ws     *workspaces.Service
	svc    *Service
	wsID   uuid.UUID
	owner  uuid.UUID
	admin  uuid.UUID
	member uuid.UUID
	viewer uuid.UUID
}
```

Build `NewService` with `testutil.TestConfig(t).EncryptionKey`, a guard from `probe.NewGuardWithResolver` returning `93.184.216.34`, a recording fake `NotificationSender`, and `testutil.NewFakeSender()`.

Add tests:
- `TestService_CreateChannelPerKind`: create webhook, Slack, and Telegram channels; assert the returned `Channel` has the right kind/name/enabled, the right preview/set/chat fields, and no plaintext secret anywhere in a JSON marshal of the response.
- `TestService_CreateRejectsEmailAndInvalidInput`: `kind:"email"` → `ErrEmailChannel`; bad name/URL/token → `*ValidationError` with the expected field.
- `TestService_CreateAttachesEveryMonitor`: create two monitors first, then a channel; assert two join rows with `enabled=true`.
- `TestService_ListOnlyActiveAndAdmin`: list requires admin; soft-deleted channels are absent; email recipients are present.
- `TestService_UpdateRotationAndKeep`: update the name/enabled; rotate the URL; omit the signing secret and assert it still opens; replace the signing secret; clear it with `ClearSigningSecret`; assert `URLPreview` changes and ciphertext differs.
- `TestService_UpdateRejectsEmail` and `TestService_DeleteRejectsEmail`.
- `TestService_DeleteSoftDeletesAndReusesName`: delete a non-email channel, assert `GetAlertChannelByID` returns `ErrNotFound`, delivery rows still join the channel row, and a new channel with the same kind/name succeeds.
- `TestService_NameDuplicateAndLimits`: duplicate active name → `ErrNameTaken`; 10 channels of one kind then the 11th → `ErrLimit`; 30 total then the 31st → `ErrLimit`.
- `TestService_CrossWorkspaceAndSuspended`: cross-workspace IDs → `ErrNotFound`; suspended workspace mutations → `ErrSuspended`.

- [ ] **Step 2: Run the service tests to verify they fail**

Run: `cd backend && go test ./internal/alerts -run TestService_ -v`
Expected: FAIL because `Service` does not exist.

- [ ] **Step 3: Implement `alerts/service.go` CRUD**

Pin:
- `List` authorizes `workspaces.RoleAdmin`, calls `q.ListAlertChannels`, and maps with `ChannelView`.
- `Create` authorizes admin, begins a transaction, locks the workspace, rejects suspended, validates kind/name, checks `GetActiveAlertChannelByName`, counts per-kind and total active channels, calls `SealCreateConfig`, inserts with a new UUIDv7, attaches every monitor, commits, and returns `ChannelView`.
- `Update` authorizes admin, locks the workspace, loads the channel with `GetAlertChannelByID`, rejects email with `ErrEmailChannel`, validates the merged name, checks duplicate names excluding itself, calls `MergeUpdateConfig`, updates, commits, and returns `ChannelView`.
- `Delete` authorizes admin, locks the workspace, loads the channel, rejects email, calls `SoftDeleteAlertChannel`, and returns `ErrNotFound` when zero rows change.
- A missing channel in any workspace-scoped query maps to `ErrNotFound`.
- Duplicate names are detected inside the workspace lock; also map a Postgres unique violation to `ErrNameTaken` as a safety net.

- [ ] **Step 4: Write the failing handler tests**

Create `backend/internal/alerts/handlers_test.go` in `package alerts_test` (importing `api` from `package alerts` would create a cycle) with its own `newAlertsAPIEnv` modeled on `incidents.newIncidentAPIEnv`. It must build the router with an explicit `AlertService` whose notifier is a recording fake and whose guard allows a local test server port. Tests:
- `TestAlertChannelHandlers_CRUD`: admin POST returns 201 with masked body; GET lists; PATCH returns 200; DELETE returns 204; a member and viewer get 403.
- `TestAlertChannelHandlers_SecretsNeverReturned`: response bodies and problem bodies never contain the submitted URL/token/signing secret.
- `TestAlertChannelHandlers_EmailIsSpecial`: PATCH and DELETE on the email channel return 422 with code `email_channel_managed_elsewhere`; PUT email still works.
- `TestAlertChannelHandlers_ValidationAndConflict`: bad kind/name/URL/token → 400 with fields; duplicate name → 409; limit → 422.
- `TestAlertChannelHandlers_CrossWorkspace`: a channel ID from another workspace returns 404.
- `TestAlertChannelHandlers_Suspended`: mutations return 403 with `workspace_suspended`.

- [ ] **Step 5: Implement handlers and router wiring**

Handlers:
- `GET` → `{"channels":[...]}` with 200.
- `POST` → 201 with the created channel.
- `PATCH` → 200 with the updated channel.
- `DELETE` → 204.
- Decode bodies with `DisallowUnknownFields`.
- Error mapping: `*ValidationError` → 400 with fields; `ErrNameTaken` → 409 `channel_name_taken`; `ErrLimit` → 422 `channel_limit`; `ErrEmailChannel` → 422 `email_channel_managed_elsewhere`; `ErrInvalidChannelConfig` → 422 `invalid_channel_config`; `ErrSuspended` → 403 `workspace_suspended`; `ErrNotFound` → 404; `ErrForbidden` → 403; unknown → 500.

Router:
- Add `AlertService` to `Dependencies`.
- Build it when nil, the pool is non-nil, and the encryption key is 32 bytes: create the box, guard, notifier with `""`, and service with `deps.EmailSender` and `deps.Config.AppBaseURL`.
- Replace the existing `GET /alert-channels` route with `alerts.ListChannelsHandler(alertSvc)`.
- Add `POST /alert-channels`, `PATCH /alert-channels/{channelID}`, and `DELETE /alert-channels/{channelID}`.
- Keep `PUT /alert-channels/email`, the existing monitor email routes, and all incident routes unchanged.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/alerts -run 'TestService_|TestAlertChannelHandlers' -v`
Expected: PASS. Also run `go test ./internal/incidents -run TestAlertChannelHandlers` to confirm the existing email list/PUT behavior still passes through the new list handler.

- [ ] **Step 7: Quality gate and commit**

Run: `cd backend && gofmt -w internal/alerts internal/api && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/alerts backend/internal/api/router.go
git commit -m "feat: add alert channel CRUD API and service"
```

---

### Task 5: Monitor channel switches and test sends

**Files:**
- Modify: `backend/internal/alerts/service.go`
- Modify: `backend/internal/alerts/handlers.go`
- Modify: `backend/internal/alerts/service_test.go`
- Modify: `backend/internal/alerts/handlers_test.go`
- Modify: `backend/internal/api/router.go`

**Interfaces:**
- Consumes: Task 4 service and handlers.
- Produces:
  - `ListMonitorChannels(ctx, actorID, workspaceID, monitorID uuid.UUID) ([]MonitorChannel, error)`
  - `SetMonitorChannel(ctx, actorID, workspaceID, monitorID, channelID uuid.UUID, enabled bool) error`
  - `SendTest(ctx, actorID, workspaceID, channelID uuid.UUID) error`
  - `MonitorChannelsHandler`, `SetMonitorChannelHandler`, `TestChannelHandler`

- [ ] **Step 1: Write the failing service tests**

Add:
- `TestService_ListMonitorChannels`: member allowed; viewer forbidden; unknown monitor → `ErrNotFound`; every active channel appears; globally disabled channel has `ChannelEnabled=false`; a channel created after the monitor has a join row because channel creation attaches monitors.
- `TestService_SetMonitorChannel`: toggles the requested channel only; a second channel stays unchanged; unknown/cross-workspace channel or monitor → `ErrNotFound`; suspended workspace → `ErrSuspended`; member allowed, viewer forbidden.
- `TestService_SendTestEmail`: fake sender receives one message per recipient with the workspace name in the subject/body; no delivery row is written.
- `TestService_SendTestNotifier`: fake notifier receives `Event.Kind == "test"`, workspace name/slug, and the app base URL link; assert the target for webhook, Slack, and Telegram; assert the Slack/Telegram adapter renders a “Test notification from o1 for” line when the fake event is passed through the notifier body builders; no delivery row.
- `TestService_SendTestDisabledComplete`: a disabled but complete channel can send.
- `TestService_SendTestIncomplete`: a channel whose config is manually corrupted returns `ErrInvalidChannelConfig`.
- `TestService_SendTestAdminOnly`: member/viewer forbidden.

- [ ] **Step 2: Run the service tests to verify they fail**

Run: `cd backend && go test ./internal/alerts -run 'TestService_ListMonitorChannels|TestService_SetMonitorChannel|TestService_SendTest' -v`
Expected: FAIL because the methods do not exist.

- [ ] **Step 3: Implement the service methods**

Pin:
- `ListMonitorChannels` authorizes member, verifies `q.GetMonitor` for the workspace, calls `q.ListMonitorAlertChannels`, and maps rows to `MonitorChannel`.
- `SetMonitorChannel` authorizes member, locks the workspace, rejects suspended, verifies the monitor, loads the active channel with `GetAlertChannelByID`, and upserts the join row.
- `SendTest` authorizes admin, loads the active channel, loads the workspace with `q.GetWorkspaceByID`, and:
  - For email: decode `EmailConfig`; reject empty recipients; send one message per recipient with subject `o1 test notification for <workspace name>` and a short text/HTML body (HTML-escape the workspace name).
  - For other kinds: call `OpenTarget`; if it fails return `ErrInvalidChannelConfig`; call `notifier.Send` with `Event{Kind: "test", WorkspaceName, WorkspaceSlug, Link: strings.TrimRight(baseURL, "/")}`.
  - Never write a delivery row. A nil notifier/sender returns an internal error.

- [ ] **Step 4: Write the failing handler tests**

Add:
- `GET /monitors/{monitorID}/alert-channels` returns every active channel with `channel_enabled` and `enabled`; member 200, viewer 403, cross-workspace 404.
- `PATCH /monitors/{monitorID}/alert-channels/{channelID}` toggles and returns 204; wrong channel 404; suspended 403.
- `POST /alert-channels/{channelID}/test` returns 204 on success; 422 `test_send_failed` with a sanitized detail on failure; 403 for member/viewer; 404 for cross-workspace; assert the failure body contains no URL/token.
- `TestAlertChannelHandlers_TestEmailSendsAllRecipients` asserts the fake sender count.

- [ ] **Step 5: Implement handlers and routes**

- `MonitorChannelsHandler` writes `{"channels":[...]}`.
- `SetMonitorChannelHandler` decodes `{"enabled": bool}` and requires the field.
- `TestChannelHandler` writes 204. On `ErrInvalidChannelConfig` or a notifier error it returns 422 `test_send_failed` with a sanitized detail (`err.Error()` for a `DeliveryError`, a fixed “Alert channel configuration is incomplete.” for invalid config). Admin-only and cross-workspace errors use the same mapping as Task 4.
- Add routes:
  - `GET /workspaces/{workspaceID}/monitors/{monitorID}/alert-channels`
  - `PATCH /workspaces/{workspaceID}/monitors/{monitorID}/alert-channels/{channelID}`
  - `POST /workspaces/{workspaceID}/alert-channels/{channelID}/test`
- Keep the existing single-channel email routes.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/alerts -run 'TestService_|TestAlertChannelHandlers' -v`
Expected: PASS.

- [ ] **Step 7: Quality gate and commit**

Run: `cd backend && gofmt -w internal/alerts internal/api && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/alerts backend/internal/api/router.go
git commit -m "feat: add monitor alert switches and test sends"
```

---

### Task 6: Mixed-channel enqueue and worker dispatch

**Files:**
- Modify: `backend/internal/incidents/lifecycle.go`
- Modify: `backend/internal/incidents/reminders.go`
- Modify: `backend/internal/incidents/lifecycle_test.go`
- Modify: `backend/internal/incidents/reminders_test.go`
- Modify: `backend/internal/worker/alert_worker.go`
- Modify: `backend/internal/worker/alert_worker_test.go`
- Modify: `backend/internal/monitors/service.go`
- Modify: `backend/internal/monitors/service_test.go`
- Modify: `cmd/worker/main.go`

**Interfaces:**
- Consumes: Task 1 delivery fields/queries, Task 2 `OpenTarget`, Task 3 `NotificationSender`/`IsPermanent`.
- Produces:
  - `func NewAlertWorker(pool *pgxpool.Pool, q *sqlc.Queries, sender email.Sender, box *secretbox.Box, notifier alerts.NotificationSender, cfg config.Config, logger *slog.Logger) *AlertWorker`
  - shared `enqueueAlertDeliveries(ctx, tx, q, inserter, incidentID, monitorID uuid.UUID, event string, sequence int) (int, error)`
  - new monitors attach every active channel.

- [ ] **Step 1: Write the failing enqueue tests**

In `lifecycle_test.go`, add `TestApplyProbeTransition_EnqueuesMixedChannels`:
- Create one email channel with two recipients and one webhook + one Telegram channel attached to the monitor.
- Trigger an opened transition.
- Assert four delivery rows: two email recipients and one row per non-email channel whose `recipient` equals the channel UUID.
- Assert four River jobs.
- Trigger the same transition again and assert no new rows/jobs.

In `reminders_test.go`, add `TestEnqueueReminders_MixedChannelsAndDedup`:
- Same channel mix, one open unacknowledged incident.
- First tick creates one row per recipient/channel at sequence 1.
- Second tick at the same time creates nothing (unique key), and a later tick at the next interval creates sequence 2.

- [ ] **Step 2: Run the enqueue tests to verify they fail**

Run: `cd backend && go test ./internal/incidents -run 'TestApplyProbeTransition_EnqueuesMixedChannels|TestEnqueueReminders_MixedChannelsAndDedup' -v`
Expected: FAIL because non-email channels are skipped.

- [ ] **Step 3: Implement shared enqueue**

Pin:
- Replace the duplicated loops with `enqueueAlertDeliveries`. Both callers use it (`opened`/`resolved` with sequence 0; reminders with `incident.ReminderCount+1`).
- Email: parse `EmailConfig`, trim/dedupe recipients as today, create one row per recipient. A malformed email config is skipped and does not fail the transaction.
- Webhook/Slack/Telegram: create exactly one row with `Recipient = channel.ID.String()` without parsing the config.
- Unknown kinds are skipped.
- Keep the existing unique-key handling (`pgx.ErrNoRows` means already inserted) and River insert options.

- [ ] **Step 4: Write the failing worker tests**

Update `newAlertWorkerForTest` to the new constructor with a box and a notifier built from a permissive guard. Add a helper that starts an `httptest.Server`, creates a sealed webhook channel, and returns its ID.

Add:
- `TestAlertWorker_SendsWebhook`: 200 server receives one request; delivery becomes `sent` with one attempt.
- `TestAlertWorker_WebhookPermanentFailure`: 400 server; delivery becomes `failed` with one attempt and a sanitized `last_error`; `Work` returns nil.
- `TestAlertWorker_WebhookTransientFailureRetries`: 500 server; delivery stays `pending`, `Work` returns an error, attempts increments; a later successful attempt marks `sent`.
- `TestAlertWorker_DeletedChannelSkipped`: soft-delete the channel before `Work`; delivery becomes `skipped`.
- `TestAlertWorker_SanitizesSecretInLastError`: 500 from a URL containing a query token; assert `last_error` is `webhook returned HTTP 500` and contains neither the URL nor the token.
- `TestAlertWorker_TelegramDispatch`: use the real notifier with its Telegram base URL overridden to the test server and assert the request path/body.
- `TestAlertWorker_SlackDispatch`: use a recording fake `NotificationSender` and assert the target kind/URL and event, because the real Slack adapter correctly refuses a localhost URL. The real Slack body/host behavior is covered in Task 3.

- [ ] **Step 5: Implement worker dispatch**

Pin:
- `Work` keeps the terminal-state and reminder-staleness checks.
- If `ChannelDeletedAt != nil`, mark `skipped` with reason `channel deleted` and return nil.
- Email: keep the existing `incidents.BuildAlertMessage` and `email.Sender` path, including attempt accounting.
- Webhook/Slack/Telegram: `OpenTarget(box, kind, channel_config)`; on failure mark `failed` with `invalid alert channel configuration` and return nil. Build `alerts.Event` from the delivery row (`MonitorID`, names, redacted `MonitorURL`, incident timestamps, dashboard link). Call `notifier.Send`.
- An unknown channel kind marks `failed` with `unsupported alert channel kind` and returns nil. It never panics or retries.
- If `alerts.IsPermanent(err)`, mark failed with `err.Error()`, log with channel kind/ID, and return nil.
- Otherwise use the existing retry budget: `job.Attempt >= job.MaxAttempts` marks failed and returns nil; else mark pending with the sanitized error and return the error.
- Logging includes channel kind and channel ID, never the URL/token.

- [ ] **Step 6: Attach new monitors to every active channel**

In `monitors/service.go`, replace the email-only attach block with `qtx.ListAlertChannels(ctx, workspaceID)` and an `UpsertMonitorAlertChannel` for each row with `Enabled: true`. Update `monitors/service_test.go` so `TestCreate_AttachesEmailChannel` becomes a mixed-channel test asserting a join row for the email channel and a disabled webhook channel.

- [ ] **Step 7: Wire the worker**

In `cmd/worker/main.go`, create `notifier := alerts.NewNotifier(guard, "")` and pass `box` and `notifier` to `worker.NewAlertWorker`. Keep the box/guard creation as is.

- [ ] **Step 8: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/incidents ./internal/worker ./internal/monitors -run 'Mixed|AlertWorker|Attaches' -v`
Expected: PASS, including the existing email worker tests after the constructor update.

- [ ] **Step 9: Quality gate and commit**

Run: `cd backend && gofmt -w internal/incidents internal/worker internal/monitors cmd/worker && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/incidents backend/internal/worker backend/internal/monitors cmd/worker/main.go
git commit -m "feat: dispatch incident alerts across all channel kinds"
```

---

### Task 7: Alert settings channel management UI

**Files:**
- Modify: `web/src/lib/api/types.ts`
- Modify: `web/src/lib/query/keys.ts`
- Modify: `web/src/features/alerts/api.ts`
- Modify: `web/src/features/alerts/hooks.ts`
- Modify: `web/src/features/alerts/AlertSettingsScreen.tsx`
- Create: `web/src/features/alerts/ChannelDialog.tsx`
- Create: `web/src/features/alerts/ChannelList.tsx`
- Create: `web/src/features/alerts/ChannelDialog.test.tsx`
- Create: `web/src/features/alerts/ChannelList.test.tsx`
- Modify: `web/src/features/alerts/AlertSettingsScreen.test.tsx`

**Interfaces:**
- Consumes: Task 4/5 API shapes.
- Produces:
  - `AlertChannelKind = 'email' | 'webhook' | 'slack' | 'telegram'`
  - `AlertChannel` with optional `recipients`, `url_preview`, `signing_secret_set`, `bot_token_set`, `chat_id`.
  - `CreateAlertChannelInput`, `UpdateAlertChannelInput` matching the backend.
  - `createAlertChannel`, `updateAlertChannel`, `deleteAlertChannel`, `testAlertChannel`.
  - Hooks `useCreateAlertChannel`, `useUpdateAlertChannel`, `useDeleteAlertChannel`, `useTestAlertChannel`.
  - Components `ChannelDialog` and `ChannelList`.

- [ ] **Step 1: Write the failing type/API tests**

Extend `web/src/features/alerts/api.test.ts` (create it if the current file only covers email) with MSW handlers asserting:
- `createAlertChannel` POSTs to `/workspaces/{id}/alert-channels` with the exact body.
- `updateAlertChannel` PATCHes `/alert-channels/{channelID}`.
- `deleteAlertChannel` DELETEs and accepts 204.
- `testAlertChannel` POSTs `/alert-channels/{channelID}/test` and surfaces a 422 problem.

- [ ] **Step 2: Write the failing component tests**

`ChannelDialog.test.tsx`:
- Create mode shows a kind selector with Webhook/Slack/Telegram and the matching fields (labels `Webhook URL`, `Signing secret`, `Slack webhook URL`, `Bot token`, `Chat ID`).
- Switching kind swaps the fields.
- Submitting a webhook posts `{kind:'webhook', name, enabled, url, signing_secret}` and closes on success.
- Edit mode fixes the kind, prefills the name/chat ID, and shows “Configured” for an existing secret; submitting with an empty secret omits it.
- Edit mode also treats webhook/Slack URLs as write-only: the URL field starts blank with a “Configured — leave blank to keep” hint, and the request omits `url` unless the user types a replacement.
- A 400 problem renders `ProblemAlert` and keeps the dialog open.

`ChannelList.test.tsx`:
- Renders rows for webhook/Slack/Telegram with name, masked preview/chat ID, and enabled switch.
- “Add channel” opens the dialog.
- “Test” calls the test endpoint and shows a success toast; a 422 shows the sanitized problem.
- “Delete” opens `ConfirmDialog` whose description mentions retained delivery history; confirming deletes and shows a toast.
- Channel rows never render the submitted URL/token/signing secret.

`AlertSettingsScreen.test.tsx`:
- Existing email tests keep passing with the unified list containing email plus other channels.
- The page shows the email section and an “Other channels” section.
- Clicking “Add channel” is present only for admins.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/features/alerts`
Expected: FAIL because the types, functions, hooks, and components do not exist.

- [ ] **Step 4: Add types, keys, API, and hooks**

Types:
```ts
export type AlertChannelKind = 'email' | 'webhook' | 'slack' | 'telegram';

export interface AlertChannel {
  id: string;
  kind: AlertChannelKind;
  name: string;
  enabled: boolean;
  recipients?: string[];
  url_preview?: string;
  signing_secret_set?: boolean;
  bot_token_set?: boolean;
  chat_id?: string;
  created_at: string;
  updated_at: string;
}
```

API bodies exactly:
```ts
export interface CreateAlertChannelInput {
  kind: Exclude<AlertChannelKind, 'email'>;
  name: string;
  enabled: boolean;
  url?: string;
  signing_secret?: string;
  bot_token?: string;
  chat_id?: string;
}

export interface UpdateAlertChannelInput {
  name?: string;
  enabled?: boolean;
  url?: string;
  signing_secret?: string;
  clear_signing_secret?: boolean;
  bot_token?: string;
  chat_id?: string;
}
```

Query keys add only:
```ts
monitorAlertChannels: (workspaceId: string, monitorId: string) =>
  ['monitor-alert-channels', workspaceId, monitorId] as const,
```

Hooks invalidate `alertChannels(workspaceId)`, `['monitor-alert-channels', workspaceId]`, `['monitor-alert-channel', workspaceId]`, `['monitor', workspaceId]`, and `monitors(workspaceId)` after create/update/delete. Test-send does not invalidate.

- [ ] **Step 5: Build the components**

`ChannelDialog`:
- Props `{ workspaceId, channel, onClose }`; `channel === null` means create.
- Kind selector (create only), name, enabled switch, and kind-specific inputs. Use `Input`/`Label`/`Switch`/`Select`.
- Write-only values: webhook/Slack URLs and all secrets are never prefilled from the API. Show “Configured — leave blank to keep” and omit unchanged values from the request. Bot tokens and signing secrets use password inputs; webhook edits also offer “Remove signing secret”.
- Submit calls create/update, shows `toast.success`, closes; on error shows `ProblemAlert` and stays open.
- Labels and button names exactly: “Add alert channel”, “Edit alert channel”, “Name”, “Webhook URL”, “Signing secret”, “Slack webhook URL”, “Bot token”, “Chat ID”, “Save channel”, “Cancel”.

`ChannelList`:
- Props `{ workspaceId, channels }`; filters out email.
- Heading “Other channels”, description, and “Add channel” button.
- Each row shows a small kind icon plus label (`Webhook`, `Slack`, `Telegram`), name, target preview (`url_preview` or `chat_id`), enabled switch with `aria-label={`${name} enabled`}`, and “Edit”/“Test”/“Delete” buttons.
- Enable toggle calls the update hook with `{enabled}`; test/delete use their hooks; delete uses `ConfirmDialog`.
- Empty state: “No other channels yet.”

`AlertSettingsScreen`:
- Keep the entire email form and its behavior.
- Render `<ChannelList workspaceId={workspace.id} channels={channels} />` below it.
- Keep the page heading and existing copy; update the intro to mention all alert channels.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd web && npm test -- --run src/features/alerts`
Expected: PASS.

- [ ] **Step 7: Quality gate and commit**

Run: `cd web && npm run format && npm run lint && npm run typecheck && npm test -- --run && npm run build`
Commit:

```bash
git add web/src/lib web/src/features/alerts
git commit -m "feat: add alert channel management to dashboard settings"
```

---

### Task 8: Monitor channel switches and channel-aware delivery history

**Files:**
- Modify: `web/src/lib/api/types.ts`
- Modify: `web/src/features/alerts/api.ts`
- Modify: `web/src/features/alerts/hooks.ts`
- Create: `web/src/features/alerts/MonitorAlertChannels.tsx`
- Create: `web/src/features/alerts/MonitorAlertChannels.test.tsx`
- Modify: `web/src/features/monitors/MonitorDetailScreen.tsx`
- Modify: `web/src/features/monitors/MonitorDetailScreen.test.tsx`
- Modify: `web/src/features/incidents/IncidentDetailScreen.tsx`
- Modify: `web/src/features/incidents/IncidentDetailScreen.test.tsx`

**Interfaces:**
- Consumes: Task 5 monitor channel API and Task 1 delivery fields.
- Produces:
  - `MonitorChannel{id, kind, name, channel_enabled, enabled}`
  - `MonitorChannelListResponse{channels: MonitorChannel[]}`
  - `listMonitorAlertChannels(workspaceId, monitorId)`
  - `setMonitorChannel(workspaceId, monitorId, channelId, enabled)`
  - `useMonitorAlertChannels(workspaceId, monitorId, enabled?)`
  - `useSetMonitorChannel(workspaceId, monitorId)`
  - `IncidentDelivery` gains `channel_id`, `channel_kind`, `channel_name`.

- [ ] **Step 1: Write the failing API/hook tests**

Add to the alerts API tests:
- `listMonitorAlertChannels` GETs `/monitors/{monitorID}/alert-channels`.
- `setMonitorChannel` PATCHes `/monitors/{monitorID}/alert-channels/{channelID}` with `{enabled}`.

Add a hook test (or extend an existing hooks test) asserting:
- The list query uses `queryKeys.monitorAlertChannels`.
- The toggle mutation optimistically flips only the target channel in the cached list, and rolls back on error.
- Success invalidates `monitorAlertChannels`, `monitor`, and `monitors`.

- [ ] **Step 2: Write the failing component tests**

`MonitorAlertChannels.test.tsx`:
- Renders one row per channel with kind label, name, and switch.
- Toggling the second channel sends only that channel’s ID.
- A globally disabled channel (`channel_enabled=false`) has a disabled switch and a “Disabled for this workspace” note.
- Empty list shows “No alert channels are configured for this workspace.” and, for admins, a link to alert settings.
- A failed toggle shows a `ProblemAlert` and restores the previous switch state.
- Viewer role renders nothing.

`MonitorDetailScreen.test.tsx`:
- Replace the single `alert-channel` mock with the list endpoint.
- Assert the section heading “Alert channels”, both channels, and persistence after toggle.
- Keep the existing viewer/viewer-forbidden behavior.

`IncidentDetailScreen.test.tsx`:
- Update `makeDelivery` with `channel_id`, `channel_kind`, `channel_name`.
- Assert the delivery table has a “Channel” column: email shows “Email” + recipient; webhook shows “Webhook” + channel name and an em dash/empty recipient.
- Existing grouping/status/error tests keep passing.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/features/alerts src/features/monitors/MonitorDetailScreen.test.tsx src/features/incidents/IncidentDetailScreen.test.tsx`
Expected: FAIL because the endpoint, hooks, and component do not exist.

- [ ] **Step 4: Implement the API, hooks, and component**

Pin:
- `MonitorAlertChannels` props `{ workspace, monitorId, canManage }`; it calls `useMonitorAlertChannels` only when `canManage`.
- Rows use labels `Alert channels`, `${name} alerts` for switches, and render kind labels.
- The toggle mutation uses `{channelId, enabled}` variables and optimistic cache updates with rollback.
- `MonitorDetailScreen` removes the old `useMonitorAlertChannel`/`useSetMonitorAlertChannel` usage and imports `MonitorAlertChannels`; delete now-unused imports.
- `IncidentDetailScreen` adds a `Channel` column before `Recipient`; email rows show channel name + recipient, non-email rows show channel name and `—` for recipient.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd web && npm test -- --run src/features/alerts src/features/monitors/MonitorDetailScreen.test.tsx src/features/incidents/IncidentDetailScreen.test.tsx`
Expected: PASS.

- [ ] **Step 6: Quality gate and commit**

Run: `cd web && npm run format && npm run lint && npm run typecheck && npm test -- --run && npm run build`
Commit:

```bash
git add web/src/features web/src/lib
git commit -m "feat: add per-monitor alert switches and channel delivery history"
```

---

### Task 9: End-to-end coverage, README, and final verification

**Files:**
- Modify: `web/tests/e2e/smoke.spec.ts`
- Modify: `README.md`

- [ ] **Step 1: Extend the E2E flow**

In the existing admin smoke test, after the monitor is created and the alert settings page is reachable:

1. Open **Settings → Alerts**.
2. Add a webhook channel named `Deploy hook` with URL `https://example.com/o1`; assert the row shows `https://example.com/…` and not the full URL.
3. Add a Slack channel named `Slack #ops` with URL `https://hooks.slack.com/services/T000/B000/XXXX`; assert the row appears.
4. Add a Telegram channel named `Telegram on-call` with bot token `123456:ABCdef` and chat ID `-1001234567890`; assert the row shows the chat ID and “Configured”, and that the page does not contain the token.
5. Go to the monitor detail page, toggle `Deploy hook` off, reload, and assert it stays off.
6. Return to alert settings, edit `Slack #ops` to `Slack #alerts` and disable it; assert the list updates.
7. Delete `Slack #alerts`; confirm the dialog explains that delivery history is retained; assert the row disappears.
8. Keep the existing incident, email, dark-mode, and a11y assertions green.

Do not call “Test” in E2E; outbound delivery success is covered by backend `httptest` servers.

- [ ] **Step 2: Update the README**

Replace the “additional alert channels are later milestones” sentence and extend the incidents/alerts section with:
- The three channel kinds and where to configure them.
- Write-only secrets and masked previews.
- Per-monitor switches for every channel.
- Test sends.
- The webhook payload and HMAC headers with a short verification note.
- The V1 limitation that private/internal webhook URLs are blocked.

- [ ] **Step 3: Run the complete verification**

Run:

```bash
cd backend && gofmt -l . && go vet ./... && golangci-lint run && go test -short ./... && go test ./...
cd web && npm run format:check && npm run lint && npm run typecheck && npm test -- --run && npm run build
cd web && npm run e2e
```

Expected: all commands exit 0; backend integration tests and E2E use the compose Postgres/Mailpit stack (`make dev-db` from the repository root if it is not already running).

- [ ] **Step 4: Commit**

```bash
git add README.md web/tests/e2e/smoke.spec.ts
git commit -m "test: cover non-email alert channels end to end"
```

---

## Self-Review Checklist

- [ ] Every design section maps to a task: schema (§5) → Task 1; kinds/validation (§6) → Task 2; secrets/redaction (§7) → Tasks 2/4/5/6; delivery pipeline (§8) → Tasks 3/6; API (§9) → Tasks 4/5; dashboard (§10) → Tasks 7/8; security (§11) → Tasks 1–6; configuration (§12) → Tasks 4/6; compatibility (§13) → Tasks 1/4/6; tests (§14) → every task; acceptance (§15) → Task 9.
- [ ] Review Focus items each have a test: (1) Task 2 validation + Task 3 no-redirect/revalidation + Task 6 send-time check; (2) Tasks 2/4/5/6/7 secret assertions; (3) Task 1 dedup schema + Task 6 mixed enqueue/rotation tests; (4) Tasks 4/5 validation and disabled-test tests; (5) Task 3 payload/signature tests.
- [ ] Names and signatures match across tasks: `alerts.Notifier`, `NotificationSender`, `Target`, `Event`, `OpenTarget`, `IsPermanent`, `NewAlertWorker`, `monitorAlertChannels`.

# Plan 5 — Non-Email Alert Channels Design

**Status:** Draft for approval
**Date:** 2026-11-06
**Depends on:** Plans 1–4, all merged into `main`.
**Out of scope:** public status pages (a separate later plan), Slack OAuth apps,
custom webhook headers, per-channel reminder cadence, maintenance windows,
on-call schedules, SMS/phone, billing, SSE, and production email/TLS.

## 1. Intent

Plan 4 made incidents real and delivered them by email. Plan 5 finishes the
alerting side of the product spec's M2 milestone by adding **generic webhooks,
Slack incoming webhooks, and Telegram bot messages**. Every channel reuses the
existing incident lifecycle, per-monitor opt-in, deduplication, reminders,
retry budget, and delivery history.

The public status page is deliberately deferred so this plan stays a
notification slice rather than two independent projects.

## 2. Current state

- `alert_channels` stores `kind` (currently only `email`), `name`, JSON
  `config`, and `enabled`. `monitor_alert_channels` carries per-monitor opt-in.
  `alert_deliveries` carries one row per incident/event/sequence/recipient with
  retry state and a unique key.
- `incidents.ApplyProbeTransition` and `EnqueueReminders` create delivery rows
  and River jobs inside the transaction; reminders fire every 30 minutes, at
  most 10 times, and stop after acknowledgement.
- `worker.AlertWorker` loads a delivery, re-checks reminder usefulness, renders
  an email with `incidents.BuildAlertMessage`, sends through `email.Sender`, and
  retries up to five attempts.
- `incidents.Service` owns channel listing and the email-channel PUT. Only the
  email channel is attached automatically to new monitors and existing
  monitors.
- The dashboard has an email-only alert settings page and a single email switch
  on monitor detail.
- `secretbox` provides AES-256-GCM sealing for tenant secrets.
- `probe.Guard` provides SSRF URL validation plus a guarded dialer and redirect
  policy. `probe.NewProber` uses it, but the guarded HTTP client is not exposed
  for other callers.

## 3. Goals, non-goals, anti-goals

**Goals**

1. An admin can create, edit, enable/disable, test, and delete multiple named
   channels of kind `webhook`, `slack`, and `telegram`, alongside the existing
   email channel.
2. Channel secrets (webhook URL, Slack URL, Telegram bot token, optional
   webhook signing secret) are encrypted at rest and never returned by the API
   after they are saved.
3. Every alert event (opened, reminder, resolved) reaches every enabled channel
   for every opted-in monitor, with the existing dedup and retry guarantees.
4. A generic webhook payload is stable, documented, and optionally signed with
   HMAC-SHA256.
5. Tenant-provided webhook and Slack URLs pass the same SSRF policy as probes,
   both when saved and when called.
6. Delivery history shows the channel kind and name and survives channel
   deletion.
7. The dashboard exposes channel management, per-monitor switches for every
   channel, test sends, and channel-aware delivery history.
8. Existing email behavior, routes, labels, and query keys remain unchanged.

**Non-goals**

- The public status page.
- Slack OAuth installation or bot scopes; Slack uses an incoming-webhook URL.
- Custom webhook headers, retries configured per channel, or per-channel
  reminder cadence.
- Webhook response-body handling; only the status code matters.
- Alert grouping, escalation, or on-call schedules.
- Metrics/tracing for deliveries (M3 hardening).

**Anti-goals**

- A secret returned by `GET`, written to logs, or embedded in a delivery row.
- A webhook URL that can reach loopback, private, link-local, or metadata
  addresses.
- An incident alert that is sent twice because a probe or job retried.
- A channel save that stops probing, incident creation, or email delivery.
- Losing delivery history when a channel is removed.
- A dashboard switch that silently controls a different channel than its label.

## 4. Approaches

**A. Shared channel adapter with three new kinds (chosen).** Extend the existing
`alert_channels` model, encrypt secrets inside its JSON config, and add one
notifier per kind behind the existing worker. Slack uses an incoming-webhook
URL, Telegram a bot token plus chat ID, and generic webhooks an optional signing
secret. The existing delivery/retry/reminder machinery is reused unchanged.
This finishes M2's channel half without new infrastructure.

**B. Webhook only, Slack/Telegram later.** Smallest slice and proves the adapter
model, but leaves M2 incomplete and requires a second plan for two integrations.
Rejected because Slack incoming webhooks and Telegram are small once the
dispatcher exists.

**C. Slack OAuth app.** Richer install flow, but adds app registration, callback
URLs, token refresh, and install state that incident notifications do not need.
Rejected as scope creep.

## 5. Data model and migration

Migration `00005_alert_channel_kinds.sql`:

1. Replace the `alert_channels_kind_check` constraint with
   `kind IN ('email', 'webhook', 'slack', 'telegram')`.
2. Add `deleted_at timestamptz` for soft deletes that preserve delivery history.
3. Replace `alert_channels_workspace_kind_name_key` with a partial unique index
   `(workspace_id, kind, name) WHERE deleted_at IS NULL`, so a deleted name can
   be reused.
4. Add `CREATE INDEX alert_channels_active_idx ON alert_channels (workspace_id)
   WHERE deleted_at IS NULL`.

No other tables change. Existing email rows stay valid.

**Config JSON shapes** (all `*_enc` values are secretbox ciphertext):

```json
{"recipients": ["ops@example.com"]}
{"url_enc": "v1:…", "url_preview": "https://example.com/…", "signing_secret_enc": "v1:…"}
{"url_enc": "v1:…", "url_preview": "https://hooks.slack.com/…"}
{"bot_token_enc": "v1:…", "chat_id": "-1001234567890"}
```

`url_preview` contains only `scheme://host/…`; no path, query, fragment, or
userinfo. `*_enc` fields are never serialized to clients.

## 6. Channel kinds and validation

| Kind | Required input | Rules |
|---|---|---|
| `email` | recipients | Unchanged Plan 4 behavior; managed only through `PUT /alert-channels/email`. |
| `webhook` | `url` | `http`/`https`, no userinfo/fragment, ≤ 2048 chars. Validated by the SSRF guard. Optional `signing_secret` 16–128 chars. |
| `slack` | `url` | `https` only, host `hooks.slack.com` or `hooks.slack-gov.com`, ≤ 2048 chars. Validated by the SSRF guard. |
| `telegram` | `bot_token`, `chat_id` | Token matches `^[0-9]+:[A-Za-z0-9_-]+$`, ≤ 128 chars. Chat ID is a numeric ID (including negative) or `@channelusername`, ≤ 64 chars. |

- Names are trimmed and must be 1–60 characters; duplicates per workspace and
  kind are rejected while active.
- A workspace may have at most 10 active channels per kind and 30 total.
- Enabled channels must have valid complete config. Disabled channels may omit
  secret input but keep their stored config.
- Telegram bot tokens are sent only to the fixed `https://api.telegram.org`
  origin; the configured base URL is overridable in tests, never by tenants.
- URL validation runs at save and again before every call, so DNS changes cannot
  bypass policy.
- Alert deliveries do **not** follow redirects. A 3xx response is a permanent
  failure with a sanitized error. This keeps the method, body, and signature
  predictable and prevents a redirect from moving Slack traffic to another host.
  The send-time check revalidates the Slack host allowlist as well as the SSRF
  policy.

## 7. Secrets and redaction

- `alerts.Service` receives the existing `secretbox.Box` and seals secrets
  before writing. The worker and test-send path unseal at send time.
- `GET` returns `url_preview` and `signing_secret_set` / `bot_token_set`
  booleans, never ciphertext or plaintext.
- On update, an omitted or empty secret field keeps the stored secret. A new
  non-empty value replaces it. `clear_signing_secret: true` removes an optional
  webhook signing secret. Required secrets (webhook/Slack URL, Telegram token)
  cannot be cleared while the channel exists.
- Delivery `recipient` identifies the destination for email only. Webhook,
  Slack, and Telegram deliveries store the channel's UUID as the recipient
  value, which stays stable across renames and URL/token rotation; the API
  exposes the channel name and kind for display and never the URL or token.
  Deduplication therefore does not change when an admin rotates a secret.
- Notifier errors are sanitized before reaching logs or `last_error`: no URL,
  token, query string, or response body. Failures say only what class of error
  occurred (for example `webhook returned HTTP 500`).

## 8. Delivery pipeline

**Enqueue.** `ListEnabledAlertChannelsForMonitor` excludes soft-deleted
channels. For each enabled channel:

- Email creates one delivery per recipient, exactly as today.
- Webhook, Slack, and Telegram create one delivery per channel. The delivery
  `recipient` is the channel UUID; the unique key
  `(incident, channel, event, sequence, recipient)` prevents duplicates, and a
  concurrent tick cannot create a second row when a secret rotates.
- Reminders use the same per-channel rule and share the incident's existing
  30-minute / 10-reminder budget.

**Worker.** `AlertWorker` becomes a dispatcher:

1. Load the delivery and keep the existing terminal-state and reminder-staleness
   checks. A delivery whose channel is soft-deleted is marked `skipped`.
2. Email keeps the existing rendering and `email.Sender`.
3. Other kinds decrypt the channel config and build a notification from the
   same already-redacted monitor URL and dashboard incident link used by email.
4. Treat 2xx as success. Retry 408, 425, 429, 5xx, timeouts, and transport
   errors up to the existing five attempts. Treat other 4xx, malformed config,
   decryption failures, and invalid URLs as permanent and mark the delivery
   `failed` without another attempt.
5. Each attempt has a 10-second timeout and drains at most 64 KiB of response
   body.

**Generic webhook payload** (`Content-Type: application/json`,
`User-Agent: o1-alerts/1.0`, `X-O1-Event: opened|reminder|resolved`):

```json
{
  "event": "opened",
  "sequence": 0,
  "workspace": {"name": "Acme", "slug": "acme"},
  "monitor": {"id": "…", "name": "API", "url": "https://example.com/…",
              "status": "down"},
  "incident": {
    "id": "…",
    "status": "open",
    "started_at": "2026-11-06T10:00:00Z",
    "acknowledged_at": null,
    "resolved_at": null
  },
  "link": "https://app.example.com/w/acme/incidents/…"
}
```

The event-derived fields are fixed so a delayed delivery still describes the
event it belongs to: `monitor.status` is `down` for opened/reminder and `up`
for resolved; `incident.status` is `open` for opened/reminder and `resolved`
for resolved. Timestamps and acknowledgement state come from the current row,
so a late opened delivery may carry an `acknowledged_at` value while its
`status` remains `open`.

With a signing secret, the request also carries
`X-O1-Timestamp: <unix seconds>` and
`X-O1-Signature: sha256=<hex HMAC-SHA256(timestamp + "." + body)>`. Receivers
verify the signature and reject stale timestamps.

**Slack** posts `{"text": "..."}` with mrkdwn: a status emoji, workspace,
monitor, redacted URL, event wording, and the dashboard link.

**Telegram** posts to
`https://api.telegram.org/bot<token>/sendMessage` with
`{"chat_id": "...", "text": "...", "disable_web_page_preview": true}`.

**Test send.** `POST .../alert-channels/{channelID}/test` is synchronous,
admin-only, bounded by the same 10-second timeout, and sends a synthetic
notification through the real notifier. It writes no delivery row and returns
`204` on success or `422` with a sanitized message on failure.

- Email sends one test message to every recipient in the channel.
- Webhook sends a signed (when configured) JSON body with
  `{"event":"test","workspace":{…},"monitor":null,"incident":null,
  "link":"<app base URL>"}` and `X-O1-Event: test`.
- Slack and Telegram send a plain "Test notification from o1 for Workspace X"
  message.
- A disabled channel can be tested when its config is complete; a test never
  enables the channel. An incomplete config is rejected before any send.

## 9. API

All routes are under `/api/v1` and require a session. Errors use the existing
problem+json format.

| Method | Path | Role | Purpose |
|---|---|---|---|
| `GET` | `/workspaces/{ws}/alert-channels` | admin | List active channels, including email. |
| `POST` | `/workspaces/{ws}/alert-channels` | admin | Create a webhook/Slack/Telegram channel. |
| `PATCH` | `/workspaces/{ws}/alert-channels/{channelID}` | admin | Rename, enable/disable, rotate config; non-email only. |
| `DELETE` | `/workspaces/{ws}/alert-channels/{channelID}` | admin | Soft-delete a non-email channel. |
| `POST` | `/workspaces/{ws}/alert-channels/{channelID}/test` | admin | Send a test notification. |
| `GET` | `/workspaces/{ws}/monitors/{monitorID}/alert-channels` | member | List every active channel with the monitor's switch. |
| `PATCH` | `/workspaces/{ws}/monitors/{monitorID}/alert-channels/{channelID}` | member | Toggle one channel for the monitor. |
| `PUT` | `/workspaces/{ws}/alert-channels/email` | admin | Existing email endpoint, unchanged. |
| `GET`/`PATCH` | `/workspaces/{ws}/monitors/{monitorID}/alert-channel` | member | Existing email switch, unchanged. |

**Create bodies:**

```json
{"kind": "webhook", "name": "Deploy hook", "enabled": true,
 "url": "https://example.com/o1", "signing_secret": "optional"}

{"kind": "slack", "name": "Slack #ops", "enabled": true,
 "url": "https://hooks.slack.com/services/…"}

{"kind": "telegram", "name": "Telegram on-call", "enabled": true,
 "bot_token": "123456:ABC…", "chat_id": "-1001234567890"}
```

**Channel response:**

```json
{"id": "…", "kind": "webhook", "name": "Deploy hook", "enabled": true,
 "url_preview": "https://example.com/…", "signing_secret_set": true,
 "created_at": "…", "updated_at": "…"}
```

Telegram returns `chat_id` and `bot_token_set`; email keeps `recipients`.
`POST` returns `201` with the channel. `PATCH` returns `200` with the channel.
The generic `PATCH` and `DELETE` reject the email channel with `422` and point
at `PUT /alert-channels/email`; email keeps its fixed `kind='email'`,
`name='Email'` identity. `POST .../test` returns `204`. Secrets in request
bodies are accepted but never echoed.

**Monitor state response:**

```json
{"channels": [
  {"id": "…", "kind": "email", "name": "Email",
   "channel_enabled": true, "enabled": true},
  {"id": "…", "kind": "slack", "name": "Slack #ops",
   "channel_enabled": true, "enabled": false}
]}
```

**Incident deliveries** gain `channel_id`, `channel_kind`, and `channel_name`
alongside the existing fields. For non-email deliveries `recipient` is omitted
or empty and the UI renders the channel name instead.

## 10. Dashboard

**Alert settings** (`/w/$workspaceSlug/settings/alerts`, admin+):

- Keep the existing email recipients/enable section.
- Add an **Other channels** list showing kind icon, name, enabled switch,
  masked target, edit, test, and delete.
- **Add channel** opens a dialog with a kind selector and the fields for that
  kind. Secrets are write-only: once saved, show "configured" and allow
  replacement, not reveal.
- Test send shows a success toast or the sanitized error; delete asks for
  confirmation and explains that delivery history is retained.

**Monitor detail:**

- Replace the single email switch with an **Alert channels** list. Each active
  channel shows its kind, name, and a switch. A missing workspace channel shows
  a note with an admin link.
- Toggles remain available to members, disabled for viewers, and optimistically
  reflect the last server state on error.

**Incident detail deliveries:**

- Show channel kind and name next to recipient/status/attempts, so admins can
  tell which destination failed. Existing email rows render as before with an
  Email label.

**Query keys:** add `alertChannels(workspaceId)`, `monitorAlertChannels(
workspaceId, monitorId)` and channel mutation invalidations. The existing keys
stay; the new monitor key is separate from `monitorAlertChannel`.

## 11. Security

- Secrets encrypted with `secretbox`; no plaintext in the database, API, logs,
  delivery rows, or errors.
- Webhook and Slack URLs are validated and called through the probe guard,
  including redirects and dial-time re-checks. Private targets remain blocked;
  self-hosted/internal webhooks are unsupported in V1 and documented as such.
- Slack URLs must use the Slack webhook hosts, so the guard cannot be used as a
  generic browser-side proxy.
- Telegram tokens only go to `api.telegram.org`.
- Channel CRUD requires admin; monitor switches require member; viewers see no
  settings.
- Request bodies are bounded: name ≤ 60, URL ≤ 2048, secret ≤ 256, chat ID ≤ 64,
  telegram token ≤ 128.
- Soft-deleted and disabled channels never enqueue new deliveries. Pending
  deliveries for a channel deleted before send are marked `skipped`.

## 12. Configuration and operations

- No new required environment variables.
- Webhook targets on non-80/443 ports continue to require
  `O1_PROBE_ALLOWED_PORTS`, matching monitor policy.
- The worker already creates the secretbox; it passes the box and a dispatcher
  to the alert worker.
- Log delivery outcomes with channel kind and channel ID, never the secret or
  full URL.

## 13. Compatibility and rollout

- The migration is additive apart from the kind constraint and unique-index
  replacement; existing email rows satisfy it.
- Existing email API routes and UI behavior stay. The unified `GET
  /alert-channels` includes email so the settings page can render everything.
- The existing email monitor routes stay; the new per-channel monitor routes
  are additive.
- New monitors attach to every non-deleted channel with the monitor join
  `enabled=true`, including channels whose global switch is currently off. This
  matches the email behavior: a monitor created while a channel is disabled
  still has a row, so enabling the channel later alerts it instead of silently
  missing it.
- Creating a channel attaches every existing monitor with `enabled=true`.
- The monitor channel list includes globally disabled channels so members can
  express intent before an admin enables them; `channel_enabled=false` makes
  that state visible.
- No changes to probe scheduling, incident state, or status-page-adjacent code.

## 14. Tests

**Backend**

- Migration: old email rows survive; new kinds accepted; deleted name reusable;
  partial index behavior.
- Config encode/decode per kind; ciphertext never appears in API JSON.
- Validation table: bad scheme, userinfo, fragment, private IP, Slack host,
  Telegram token/chat ID, name length, duplicate name, channel limits.
- Secret rotation: omitted secret keeps, new value replaces, clear signing
  secret works, required secret cannot be cleared.
- Webhook payload shape, HMAC signature vector, timestamp header.
- Slack and Telegram payloads; Telegram base URL injection for tests.
- Dispatcher: 2xx success, 4xx permanent failure, 3xx permanent failure (no
  redirects), 429/5xx retry, timeout, sanitized errors, deleted-channel skip.
- Enqueue: one delivery per channel, per-recipient email unchanged, duplicate
  event dedup, dedup survives channel rename/secret rotation, reminder behavior
  across mixed channels.
- Test send per kind, including multi-recipient email, disabled-but-complete
  channels, incomplete-config rejection, and signature presence.
- API: RBAC (admin/member/viewer), 404 for cross-workspace and unknown channel,
  masked responses, test-send success/failure, suspended workspace rejection.
- SSRF: save-time rejection and send-time revalidation.

**Dashboard**

- Channel list renders email plus other kinds with masked targets.
- Add/edit/delete/test flows, validation errors, and secret replacement.
- Monitor detail lists every channel, toggles the right channel, and shows the
  admin note when none exist.
- Delivery history shows kind and name.
- Existing email tests keep passing with updated fixtures.

**E2E**

- As admin, create a Slack channel with a fake `hooks.slack.com` URL and a
  webhook channel; assert masked previews and that secrets never appear.
- Toggle the new channel on a monitor, reload, and assert persistence.
- Edit enabled/name and delete a channel; assert the list updates.
- Existing incident/email/dark-mode smoke paths remain green. Actual outbound
  delivery success is covered by backend httptest servers, not external
  network calls in E2E.

## 15. Acceptance criteria

1. Webhook, Slack, and Telegram channels can be created, edited, disabled,
   tested, and soft-deleted by an admin.
2. Secrets are write-only: absent from every API response, log, and delivery
   row after save.
3. Opened, reminder, and resolved incidents reach every enabled channel for
   every opted-in monitor exactly once per event/sequence.
4. Retryable failures retry up to five attempts; permanent failures fail fast;
   reminder staleness and acknowledgement rules are unchanged.
5. Webhook HMAC signatures verify with the stored secret.
6. Private/internal webhook and Slack URLs are rejected at save and at send.
7. Delivery history shows channel kind/name and survives channel deletion.
8. Per-monitor switches work for each channel and default to enabled for new
   monitors/channels.
9. Email behavior and existing routes/tests remain intact.
10. Format, lint, typecheck, unit/integration tests, build, and E2E pass.

## 16. Risks and builder latitude

- The exact package split (`alerts` versus keeping channel logic in `incidents`)
  is the builder's call if the API, worker, and test seams above hold.
- The generic webhook payload is pinned; Slack/Telegram body wording may vary as
  long as status, monitor, workspace, and link content remain.
- The test-send path may share the dispatcher or call a dedicated method; either
  is acceptable if it sends through the real notifier and leaks nothing.
- Soft delete is pinned for history retention. If implementation cost forces a
  choice, preserving delivery history wins over reusing names.
- The plan may add small exported helpers to `probe` (for example a guarded
  `http.Client`) rather than duplicating SSRF dialing.

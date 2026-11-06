# Plan 4 — Incidents and Email Alerts Design

**Status:** Draft for approval
**Date:** 2026-11-06
**Depends on:** Plan 1 (auth, sessions, workspaces, RBAC), Plan 2 (monitors, probing,
results), and Plan 3 (React dashboard), all merged into `main`.
**Out of scope:** webhook/Slack/Telegram alerts, public status pages, production email
provider/TLS, SSE, maintenance windows, manually created incidents, on-call schedules,
escalation policies, phone/SMS alerts, billing.

## 1. Intent

Plan 2 made monitors detect failures, and Plan 3 made the product usable. But a
failed probe currently changes a status badge and nothing else. Plan 4 turns that
state change into the product's core promise: **when a monitor goes down, open an
incident, record what happened, email the workspace, and let someone acknowledge
it; when the monitor recovers, resolve the incident and send the recovery email.**

This is the first half of the product spec's M2 milestone. The second half
(webhook/Slack/Telegram channels and the public status page) is Plan 5. Plan 4
deliberately proves the incident lifecycle and one real delivery channel before
adding more destinations and a public surface.

## 2. Current state

- The probe worker already computes `unknown`, `up`, and `down` with
  confirmation/recovery thresholds via `worker.Advance`.
- `monitor_state` stores the current status, consecutive counters, last check,
  and scheduling fields.
- The scheduler enqueues `probe` jobs through River with transactional
  `InsertTx`.
- The worker consumes a single `probe` River queue.
- `email.Sender` exists with a Mailpit SMTP implementation used by auth emails.
- There are no incident, alert-channel, monitor-alert-channel, or delivery
  tables, and no alert worker.
- The React dashboard has monitor list/detail, workspaces, members, and admin
  pages. It does not have incidents or alert settings.
- `O1_APP_BASE_URL` is the dashboard origin used in email links.
- The API refuses production startup until a production email sender exists;
  Plan 4 continues to use the existing sender and leaves production email to the
  hardening milestone.

## 3. Goals, non-goals, anti-goals

**Goals**

1. A transition into `down` opens exactly one incident per monitor, records a
   timeline event, and enqueues alert deliveries for enabled channels.
2. A transition from `down` back to `up` resolves the active incident and sends
   the recovery alert even if the incident was acknowledged.
3. A workspace can configure a workspace-level email channel with recipients.
4. Each monitor can opt in or out of that channel; new monitors default to
   enabled when the channel exists.
5. Email alert delivery is durable, deduplicated, retried, and visible in the
   dashboard.
6. A member can acknowledge an incident; acknowledgement stops reminder alerts
   but not probes or the resolve alert.
7. The dashboard lists incidents, shows a timeline, acknowledges them, and
   exposes alert settings according to role.

**Non-goals**

- Public status pages.
- Webhook, Slack, or Telegram channels.
- Manually created incidents and maintenance windows.
- On-call schedules, escalation, SMS, or phone calls.
- Alert grouping across monitors.
- Production email provider, TLS, or deliverability infrastructure.
- SSE or websockets; the dashboard polls.

**Anti-goals**

- An incident that exists only in the UI and not in the database.
- An incident or alert that is opened twice because a probe job retried.
- A resolve alert suppressed by acknowledgement.
- An alert email that leaks monitor headers, request bodies, or query strings.
- A channel configuration that silently stops probing or incident state.

## 4. Approaches

**A. Atomic in the probe worker (chosen).** Extend the probe worker's existing
transaction: after computing the next state, open or resolve the incident, append
`incident_events`, create alert deliveries, and insert River `alert` jobs with
`InsertTx`. The probe result, state, incident, timeline, deliveries, and job
enqueue all commit together. This is the chosen approach because the guarantee
becomes structural, alert latency is bounded by the probe itself, and River
already provides durable retry.

**B. Separate incident reconciler.** A process polls `monitor_state` and creates
incidents after the fact. It keeps the probe worker small, but detection lags,
concurrent reconcilers need locking, and deduplication moves into application
code. Rejected for Plan 4.

**C. Outbox table plus dispatcher.** Strong decoupling and replayability, but
River is already a durable job queue inside the same Postgres database. An extra
outbox adds a moving part without buying much at this scale. Rejected for Plan 4;
it can be revisited if alert volume later justifies it.

## 5. Data model

Add `00004_incidents.sql` (goose) and regenerate sqlc. All IDs are UUIDv7 generated
in Go, matching the existing style.

### 5.1 `incidents`

```sql
CREATE TABLE incidents (
  id uuid PRIMARY KEY,
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  monitor_id uuid NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  status text NOT NULL CHECK (status IN ('open', 'acknowledged', 'resolved')),
  started_at timestamptz NOT NULL DEFAULT now(),
  acknowledged_at timestamptz,
  acknowledged_by uuid REFERENCES users(id) ON DELETE SET NULL,
  resolved_at timestamptz,
  last_notified_at timestamptz,
  reminder_count integer NOT NULL DEFAULT 0 CHECK (reminder_count >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- At most one unresolved incident per monitor. This is the database-level
-- guarantee against duplicate opens.
CREATE UNIQUE INDEX incidents_one_active_per_monitor
  ON incidents (monitor_id) WHERE resolved_at IS NULL;

CREATE INDEX incidents_workspace_started_idx
  ON incidents (workspace_id, started_at DESC, id DESC);
CREATE INDEX incidents_monitor_started_idx
  ON incidents (monitor_id, started_at DESC, id DESC);
CREATE INDEX incidents_open_reminder_idx
  ON incidents (last_notified_at)
  WHERE resolved_at IS NULL AND acknowledged_at IS NULL;
```

`status` and the timestamps are kept consistent by the service:

- open: `resolved_at IS NULL`, `acknowledged_at IS NULL`
- acknowledged: `resolved_at IS NULL`, `acknowledged_at IS NOT NULL`
- resolved: `resolved_at IS NOT NULL`

No incident is opened while the monitor is paused or already down.

### 5.2 `incident_events`

```sql
CREATE TABLE incident_events (
  id uuid PRIMARY KEY,
  incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('opened', 'acknowledged', 'resolved')),
  actor_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  detail jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX incident_events_incident_created_idx
  ON incident_events (incident_id, created_at, id);
```

`detail` carries only non-secret context: previous status, new status, trigger
source, and (for acknowledgement) the actor. It never carries headers or bodies.

### 5.3 `alert_channels`

```sql
CREATE TABLE alert_channels (
  id uuid PRIMARY KEY,
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('email')),
  name text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 60),
  config jsonb NOT NULL DEFAULT '{}'::jsonb,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX alert_channels_workspace_kind_name_key
  ON alert_channels (workspace_id, kind, name);
```

Plan 4 has exactly one email channel per workspace, named `Email`. The `kind`
check and the schema leave room for Plan 5's other kinds. Email `config` is:

```json
{ "recipients": ["oncall@example.com", "ops@example.com"] }
```

Recipients are normalized (trimmed, lowercased), unique, at most 20, and each is
a valid address of at most 320 characters. `enabled=false` stops new deliveries
for the channel without deleting it.

### 5.4 `monitor_alert_channels`

```sql
CREATE TABLE monitor_alert_channels (
  monitor_id uuid NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  channel_id uuid NOT NULL REFERENCES alert_channels(id) ON DELETE CASCADE,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (monitor_id, channel_id)
);
```

When an email channel is created, the server inserts an enabled row for every
monitor in the workspace. A newly created monitor inserts an enabled row for the
workspace's email channel when one exists. A monitor can toggle its row.

### 5.5 `alert_deliveries`

```sql
CREATE TABLE alert_deliveries (
  id uuid PRIMARY KEY,
  incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
  channel_id uuid NOT NULL REFERENCES alert_channels(id) ON DELETE CASCADE,
  event text NOT NULL CHECK (event IN ('opened', 'reminder', 'resolved')),
  sequence integer NOT NULL DEFAULT 0 CHECK (sequence >= 0),
  recipient text NOT NULL,
  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'sent', 'failed', 'skipped')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  last_error text,
  sent_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (incident_id, channel_id, event, sequence, recipient)
);
CREATE INDEX alert_deliveries_incident_created_idx
  ON alert_deliveries (incident_id, created_at, id);
CREATE INDEX alert_deliveries_pending_idx
  ON alert_deliveries (status, created_at)
  WHERE status = 'pending';
```

One row per recipient gives independent retry and visibility. `sequence` is 0 for
`opened` and `resolved`, and 1..N for reminders. The unique key is the
deduplication guarantee.

## 6. Incident lifecycle

### 6.1 Opening

In the probe worker transaction, after `Advance` produces the next state:

- If `previous != next.Status` and `next.Status == "down"`:
  - Insert an incident with `status='open'`, `started_at=now()`, and
    `last_notified_at=now()`.
  - Insert an `opened` event with `{previous_status, new_status}`.
  - Insert delivery rows for every enabled channel attached to the monitor, one
    per recipient, `event='opened'`, `sequence=0`.
  - Insert an `alert` River job per inserted delivery with `InsertTx`.
- The partial unique index makes a duplicate open a database conflict; the
  worker treats that as "already open" and does not create another event or
  delivery set. This can only happen if the state update and incident insert
  disagree, which the transaction prevents.
- If the workspace is suspended, no incident is opened: `ListDueMonitors`
  already skips suspended workspaces, and the transaction also treats a
  suspended workspace as a no-op.
- If the monitor is paused by the time the probe result is recorded, the worker
  still stores the probe result for audit, but it does not update
  `monitor_state` and does not open or resolve an incident. This preserves the
  "pause keeps state, counters, and incident" rule when a pause commits while a
  probe is in flight. The resume path probes immediately and applies the next
  real transition.

A monitor that starts failing from `unknown` opens an incident once the
confirmation threshold is reached; it is not a special case.

### 6.2 Resolving

- If `previous == "down"` and `next.Status == "up"`:
  - Look up the active incident for the monitor. **If there is none, do nothing
    and send no recovery email.** This covers monitors that were already down
    before Plan 4 deployed or whose incident was removed.
  - Update the active incident to `status='resolved'`, `resolved_at=now()`.
  - Insert a `resolved` event.
  - Insert `resolved` deliveries for currently enabled monitor/channel pairs,
    one per recipient, `sequence=0`.
  - Enqueue the alert jobs.
- Resolution is independent of acknowledgement. An acknowledged incident still
  sends its resolve alert.
- If the monitor is deleted with an open incident, the cascade removes the
  incident and deliveries. No resolve alert is sent; this is a deliberate
  cleanup, not a recovery.

### 6.3 Acknowledging

`POST /workspaces/{workspaceID}/incidents/{incidentID}/acknowledge`:

- Member+ in the workspace.
- Suspended workspace → 403 `workspace_suspended`.
- Open → acknowledged, set `acknowledged_at=now()`, `acknowledged_by=actor`.
- Acknowledged → idempotent 204, no new event.
- Resolved → 409 `incident_resolved`.
- Insert an `acknowledged` event for the first acknowledgement.
- Acknowledgement stops reminders; probes and the resolve alert continue.

### 6.4 Reminders

- A reminder is sent for unresolved, unacknowledged, non-paused incidents when
  `last_notified_at` is at least 30 minutes old.
- The reminder query also excludes suspended workspaces.
- The first reminder is sequence 1, then 2, up to 10 reminders. After 10, no
  further reminders until the incident is resolved.
- The scheduler's alert tick runs after each probe tick. For each eligible
  incident it:
  - Inserts reminder deliveries with `event='reminder'`, the next sequence, one
    per recipient of each enabled channel.
  - Updates `last_notified_at=now()` on every eligible tick, even when no
    delivery was inserted, so permanently undeliverable incidents cannot occupy
    the front of the reminder batch and starve deliverable ones.
  - Increments `reminder_count` **only when at least one delivery row was
    actually inserted**. The 10-reminder budget is therefore never consumed by
    an incident with no configured channel, no enabled recipients, or malformed
    channel config.
  - Enqueues the alert jobs for inserted deliveries.
- The unique key and the row update happen in one transaction, so two scheduler
  ticks cannot duplicate a reminder.
- A newly configured channel may wait up to one reminder interval for its first
  reminder. This is the deliberate trade-off for preventing batch starvation.
- A paused monitor has no reminders. If it is resumed, the regular probe path
  resumes; existing incidents remain open and reminders continue after the next
  eligible tick.

## 7. Alert delivery

### 7.1 Job

```go
type AlertArgs struct {
    DeliveryID uuid.UUID `json:"delivery_id" river:"unique"`
}
func (AlertArgs) Kind() string { return "alert" }
```

The job is inserted with `MaxAttempts: 5`, queue `alert`, and unique-by-args while
available/scheduled/running/pending.

### 7.2 Worker

The worker process registers an alert worker on the `alert` queue alongside the
probe worker. The queue gets its own concurrency limit so a slow email provider
cannot starve probing; default 4 workers.

For each job:

1. Load the delivery, incident, monitor, workspace, and channel. If the delivery
   is `sent`, `failed`, or `skipped`, return nil (idempotent).
2. **Re-check the reason for a reminder.** If the event is `reminder` and the
   incident is now resolved, acknowledged, or the monitor is paused, or the
   workspace is suspended, mark the delivery `skipped` with a short
   `last_error` reason and return nil. Also skip a reminder whose `created_at`
   is older than 60 minutes (two reminder intervals), so an alert-worker outage
   cannot produce a burst of stale reminders on restart.
3. Render the email for the delivery's recipient and event.
4. Send through `email.Sender`.
5. On success: `status='sent'`, `sent_at=now()`, `attempts=attempts+1`.
6. On failure: increment `attempts`, store `last_error`, leave `pending`. If
   `job.Attempt >= job.MaxAttempts`, mark `failed` and return nil; otherwise
   return the error so River retries with backoff.

`opened` and `resolved` deliveries are point-in-time notifications: once
enqueued they are sent even if the incident later changes, the monitor is paused,
or the channel is disabled. Only reminders are re-evaluated. This keeps a
recovery email from being suppressed by a later action and avoids silently
dropping an alert that was already owed.

The worker never sends to headers or bodies; the email uses only monitor name,
workspace name, status, redacted URL, incident start, and the dashboard link.
The worker startup refuses `O1_ENV=production`, matching the API, until a
production email sender exists.

### 7.3 Email content

Subject examples:

- `[o1] {monitor} is down`
- `[o1] Reminder: {monitor} is still down`
- `[o1] {monitor} recovered`

Text/HTML body:

- Workspace and monitor names.
- Current status and incident start time.
- Redacted URL (`monitors.RedactURL`).
- Link to the incident in the dashboard:
  `${O1_APP_BASE_URL}/w/{workspaceSlug}/incidents/{incidentID}`.
- For reminders, the reminder number.
- A short note that acknowledgement stops reminders, not probes.

Monitor and workspace names are user-controlled. The alert renderer strips CR/LF
from the subject and header values and HTML-escapes every user-controlled value
in the HTML body. The shared `MailpitSender` additionally rejects CR/LF in `To`
and `Subject`, generates a per-message random MIME boundary, and RFC 2047
encodes a non-ASCII subject, so the auth email path gets the same protection.

Email is sent one message per recipient, matching the existing `email.Sender`
contract (which takes a single `To`).

### 7.4 Delivery visibility

Incident detail shows an "Alert deliveries" section to workspace admins (owner
and admin), grouped by event, with recipient, status, attempts, and last error.
Members and viewers see incident status and timeline, not recipient addresses.
Failed deliveries are therefore visible to the people who can fix them.

## 8. API

All routes are under `/api/v1` and use the existing session cookie, RFC 9457
problem responses, cursor pagination, and role middleware.

### 8.1 Incidents

`GET /workspaces/{workspaceID}/incidents?status=active|open|acknowledged|resolved|all&monitor_id=&cursor=&limit=`

- Viewer+.
- Default `status=active` (all unresolved incidents: open and acknowledged).
  `all` includes resolved.
- Newest first by `started_at DESC, id DESC`.
- `limit` default 50, max 100.
- Response:

```json
{
  "incidents": [
    {
      "id": "...",
      "workspace_id": "...",
      "monitor_id": "...",
      "monitor_name": "API",
      "status": "open",
      "started_at": "2026-11-06T10:00:00Z",
      "acknowledged_at": null,
      "acknowledged_by": null,
      "resolved_at": null,
      "last_notified_at": "2026-11-06T10:00:00Z",
      "reminder_count": 0
    }
  ],
  "next_cursor": "..."
}
```

`GET /workspaces/{workspaceID}/incidents/{incidentID}`

- Viewer+.
- Response `{ "incident": { ... }, "events": [ { "id", "kind", "actor_user_id", "detail", "created_at" } ] }`.
- 404 `not_found` when the incident is not in the workspace.

`POST /workspaces/{workspaceID}/incidents/{incidentID}/acknowledge`

- Member+.
- 204 on success or already acknowledged.
- 409 `incident_resolved` when the incident is resolved.
- 404 `not_found` when it is not in the workspace.

`GET /workspaces/{workspaceID}/incidents/{incidentID}/deliveries`

- Admin+.
- Response `{ "deliveries": [ { "id", "event", "sequence", "recipient", "status", "attempts", "last_error", "sent_at", "created_at" } ] }`.

### 8.2 Alert channels

`GET /workspaces/{workspaceID}/alert-channels`

- Admin+.
- Response `{ "channels": [ { "id", "kind": "email", "name": "Email", "enabled", "recipients": ["..."], "created_at", "updated_at" } ] }`.

`GET /workspaces/{workspaceID}/monitors/{monitorID}/alert-channel`

- Member+.
- Returns the monitor's state for the workspace email channel without exposing
  recipients: `{ "available": true, "channel_enabled": true, "enabled": true }`.
- `available=false` when the workspace has no email channel; `enabled` and
  `channel_enabled` are then `false`.
- `channel_enabled` is the workspace channel's global switch. If it is false,
  the UI disables the per-monitor switch and explains that an administrator has
  turned email alerts off.
- 404 `not_found` when the monitor is not in the workspace.

`PUT /workspaces/{workspaceID}/alert-channels/email`

- Admin+.
- Suspended workspace → 403 `workspace_suspended`.
- Body `{ "recipients": ["..."], "enabled": true }`.
- Upserts the workspace email channel by `(workspace_id, 'email', 'Email')`.
- Normalizes and validates recipients: 1–20 unique valid addresses when enabled;
  an empty list is allowed only when `enabled=false`.
- On first creation, inserts enabled `monitor_alert_channels` rows for every
  monitor in the workspace.
- 204 on success; `validation_error.fields.recipients` on bad input.

`PATCH /workspaces/{workspaceID}/monitors/{monitorID}/alert-channel`

- Member+.
- Suspended workspace → 403 `workspace_suspended`.
- Body `{ "enabled": true }`.
- Updates the monitor's row for the workspace email channel.
- 404 `alert_channel_missing` when the workspace has no email channel.
- 204 on success.

### 8.3 Problem codes

No new generic codes except:

- `incident_resolved` (409).
- `alert_channel_missing` (404).
- Existing `validation_error`, `not_found`, `forbidden`, `unauthenticated`.

## 9. Dashboard

### 9.1 Incidents list

Route `/w/$workspaceSlug/incidents`.

- Status filter: `Active`, `Open`, `Acknowledged`, `Resolved`, `All`; default
  `Active`.
- Table/cards: monitor name, status badge, started time, duration, acknowledged
  by/time (if any). Row opens the incident detail.
- Empty state: `No incidents` with a short explanation.
- Poll every 15 seconds while visible; no SSE.
- Viewer+ can view.

### 9.2 Incident detail

Route `/w/$workspaceSlug/incidents/$incidentId`.

- Header: monitor name, status badge, started/acknowledged/resolved times.
- Timeline: opened, acknowledged, resolved, with timestamps.
- `Acknowledge` button for member+ when the incident is open; disabled/hidden
  when acknowledged or resolved.
- Link to the monitor detail.
- Admin+ sees the alert-deliveries section (event, recipient, status, attempts,
  last error).
- 404 renders the standard not-found state.

### 9.3 Monitor detail

- If the monitor has an open/acknowledged incident, show a banner:
  `Incident open — View incident`.
- Add an `Email alerts` switch for member+ when the workspace email channel
  exists; it reads `GET .../monitors/{monitorID}/alert-channel` and changes
  `PATCH .../alert-channel`.
- If the channel is globally disabled, show the switch disabled with
  `Email alerts are disabled for this workspace.`; if no channel exists
  (`available=false`), show a muted note; admins get a link to alert settings.
- Recent incidents (last 5) for the monitor, linking to incident detail.

### 9.4 Alert settings

Route `/w/$workspaceSlug/settings/alerts`, admin+ only.

- Email recipients editor: add/remove addresses, 1–20, validation messages.
- `Email alerts` enabled switch.
- Save via `PUT .../alert-channels/email`; success toast and refreshed state.
- Note that webhook/Slack/Telegram channels and status pages are later
  milestones.
- Non-admins do not see the nav item and are redirected if they visit the URL.

### 9.5 Navigation and query keys

- Add `Incidents` to the workspace navigation for all roles.
- Add `Alerts` under a `Settings` group for admin+.
- Query keys:
  - `incidents(workspaceId, filters)`
  - `incident(workspaceId, incidentId)`
  - `incidentDeliveries(workspaceId, incidentId)` (admin+)
  - `alertChannel(workspaceId)`
  - `monitorAlertChannel(workspaceId, monitorId)`
- Mutations invalidate the relevant incident/channel keys; acknowledgement
  invalidates list and detail.

## 10. Background jobs and configuration

- `alert` River queue, default 4 workers, separate from `probe`.
- No new required config.
- Existing `O1_SMTP_ADDR`, `O1_EMAIL_FROM`, and `O1_APP_BASE_URL` are used.
- Reminder cadence is a constant: 30 minutes, maximum 10 reminders. If operators
  need to tune it later, it becomes config in Plan 5.
- The worker process creates the email sender and passes it to the alert worker.
- The worker startup refuses `O1_ENV=production` until a production email sender
  exists, matching the API's current behavior. The scheduler may still run, but
  no production deployment is supported without the worker.

## 11. RBAC

| Action | Required role |
|---|---|
| View incidents, timeline, monitor incident banner | viewer |
| Acknowledge incident | member |
| Toggle a monitor's email alerts | member |
| View alert deliveries | admin |
| View/update workspace email channel and recipients | admin |
| Global admin pages | global admin + workspace membership as today |

Owners and admins can do everything members can. A global admin who is not a
workspace member does not gain workspace access through these routes.

## 12. Tests

**Unit**

- Incident transition helper: unknown→down opens, up→down opens, down→up
  resolves, down→down does nothing, unknown→up does nothing.
- Recipient normalization/validation.
- Reminder eligibility (unacknowledged, unresolved, not paused, interval, cap,
  suspended workspace excluded).
- Reminder clock only advances when a delivery row is inserted.
- Alert worker skips stale reminders after acknowledgement/resolution and skips
  deliveries for suspended workspaces.
- Alert email rendering contains no headers/body/query string and rejects CR/LF
  in header values.

**Backend integration (testcontainers)**

- Probe worker transaction: a failing probe opens one incident, one opened event,
  and one delivery per recipient; a retry does not duplicate.
- A successful recovery resolves the incident and creates resolve deliveries even
  after acknowledgement; a recovery with no active incident sends nothing.
- Acknowledging sets actor/time, adds one event, is idempotent, and rejects
  resolved incidents and suspended workspaces.
- Reminder tick creates sequence 1..N, stops after acknowledgement, skips
  suspended workspaces, and caps at 10.
- Alert worker: success marks sent; transient failure retries; final failure marks
  failed with last_error; already-sent delivery is a no-op; stale reminders and
  reminders older than 60 minutes are skipped; reminder deliveries for suspended
  workspaces are skipped; opened/resolved deliveries are immutable.
- API: list filters/pagination; detail; acknowledge RBAC and suspension; channel
  upsert validation and suspension; monitor toggle; monitor-scoped alert state;
  new monitor attaches to an existing channel; channel creation attaches existing
  monitors.
- Email uses the fake sender and never contains masked secrets.

**Frontend**

- Incidents list renders statuses, filter, empty state; cursor load more.
- Incident detail renders timeline; acknowledge button visibility by role/status;
  mutation refreshes; 404 state.
- Alert settings admin gating, recipient add/remove, validation, save.
- Monitor detail banner, recent incidents, and email toggle visibility and
  disabled-channel state.
- Query key/pagination tests.

**E2E**

- Extend the Playwright smoke only if it can seed an incident deterministically:
  insert an incident and timeline rows through direct SQL after creating a
  monitor, then assert the incident list/detail/ack flow. Do not wait for a live
  probe to fail; the Go integration tests own the transition. If seeding proves
  brittle, keep the incident UI covered by component tests and document the manual
  smoke.

## 13. Migration and rollout

- Add `00004_incidents.sql` with all five tables and indexes; regenerate sqlc.
- The migration is additive. Existing monitors get `monitor_alert_channels` rows
  only when the first email channel is created; no incidents are backfilled for
  past failures.
- Deploying the migration and the worker together is safe: workers without the
  new code ignore the new tables; the new worker opens incidents on the next
  transition.
- The API can deploy before the dashboard; the dashboard degrades to its existing
  pages until the new routes are present.
- Rollback drops the five tables; incidents and deliveries are lost, which is
  acceptable for a new feature. No monitor or probe data is touched.

## 14. Acceptance criteria

1. A monitor that reaches `down` gets exactly one open incident, one `opened`
   timeline event, and one queued email delivery per configured recipient.
2. A monitor that recovers resolves the incident and queues resolve emails even
   if the incident was acknowledged.
3. Acknowledgement is member+, idempotent, records the actor/time, and stops
   reminders.
4. Reminders run every 30 minutes, stop after acknowledgement, and cap at 10.
5. Email deliveries retry up to 5 attempts and expose failures to admins.
6. Alert email never includes request headers, request bodies, or URL query
   strings, and header injection is rejected.
7. The dashboard lists incidents, shows a timeline, acknowledges them, and
   exposes alert settings according to role.
8. New monitors attach to an existing workspace email channel; channel creation
   attaches existing monitors.
9. Backend integration tests cover open→ack→resolve, duplicate prevention,
   stale-reminder skipping, and suspended-workspace behavior; frontend tests
   cover the incident and settings screens.
10. Existing probe, monitor, workspace, admin, and dashboard behavior is
    unchanged.

## 15. Risks and decisions left to the builder

- Exact SQL query shapes and generated sqlc names are the builder's call.
- The alert worker's final-attempt detection should follow the installed River
  version's `job.Attempt`/`job.MaxAttempts` semantics; the behavior in section
  7.2 is the requirement.
- The email HTML is intentionally simple; template files or a renderer are the
  builder's call as long as the content and redaction rules hold.
- The incident list may use a select or tabs for status; the exact control is the
  builder's call.
- The alert settings page may use a chip editor or a simple list; the exact
  control is the builder's call.
- If the reminder constant needs operational tuning during implementation, the
  builder may add `O1_ALERT_REMINDER_INTERVAL` / `O1_ALERT_REMINDER_MAX` rather
  than hard-coding, but the defaults must be 30 minutes and 10.
- The public status page and non-email channels remain Plan 5; do not add
  placeholder routes or schema for them beyond the `kind` check.

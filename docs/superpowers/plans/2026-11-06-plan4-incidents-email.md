# Plan 4 — Incidents and Email Alerts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn monitor state transitions into durable incidents, timelines, acknowledgements, and retried email alerts, with a dashboard to operate them.

**Architecture:** Extend the probe worker's existing transaction so the state change, incident open/resolve, timeline events, alert deliveries, and River alert jobs commit atomically. A separate `alert` River queue renders and sends one email per recipient with retry. A scheduler alert tick creates reminder deliveries. Incident and alert-channel HTTP APIs expose the feature to the existing React dashboard.

**Tech Stack:** Go 1.26, PostgreSQL, goose migrations, sqlc, River, existing `email.Sender` (Mailpit in development), React 19, TanStack Router/Query, react-hook-form/zod, shadcn/ui, Vitest/MSW, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-06-plan4-incidents-email-design.md`

## Global Constraints

- UI language is English.
- Plan 4 scope is incident core plus email alerts only. Do not add webhook, Slack, Telegram, public status pages, maintenance windows, or manual incidents.
- At most one unresolved incident per monitor. Enforce it with the partial unique index; do not rely on application checks alone.
- Incident open/resolve, timeline events, deliveries, and alert jobs are written in the probe worker's transaction. A duplicate probe must not open a second incident or duplicate a delivery.
- Alert email uses only monitor name, workspace name, status, redacted URL, incident time, and the dashboard link. Never include headers, bodies, or URL query strings.
- Email is sent one message per recipient through `email.Sender`. No production email provider in Plan 4; the worker refuses `O1_ENV=production`.
- Reminders: 30-minute interval, maximum 10, only for unresolved/unacknowledged incidents on non-paused monitors in non-suspended workspaces. Reminders older than 60 minutes are skipped.
- Acknowledgement is member+, idempotent for already acknowledged, 403 for suspended workspaces, 409 for resolved incidents. It never suppresses the resolve email.
- Alert channel recipients and delivery details are admin+; monitor alert toggles are member+; incident reads are viewer+.
- Cursor pagination uses the existing `httpx.PageCursor` and opaque `next_cursor`; omit the cursor from infinite-query keys.
- All migrations are additive and must be applied with `goose`; regenerate sqlc and commit generated code.
- Every task ends with format, lint, typecheck, tests, and a commit. Backend tasks also run `go vet ./...`.

## Review Focus

1. A retried probe or scheduler tick must not create a second active incident, a duplicate opened/resolved delivery, or a duplicate reminder.
2. A reminder that becomes stale after acknowledgement, resolution, pause, or suspension must not send; older-than-60-minute reminders must be skipped.
3. A resolve email must still send after acknowledgement; a `down → up` transition with no active incident must send nothing.
4. Monitor/workspace names are user-controlled: no CRLF header injection, no HTML injection, no MIME boundary collision, and non-ASCII subjects are encoded.
5. Suspended workspaces must reject acknowledge/channel/monitor-toggle writes and must not generate new incidents or reminders.
6. The reminder clock and 10-reminder budget must not advance when no delivery row was actually created.

---

## File Structure

```text
backend/
  internal/db/migrations/00004_incidents.sql
  internal/db/queries/incidents.sql
  internal/db/queries/alerts.sql
  internal/db/sqlc/                  # regenerated
  internal/incidents/
    types.go
    errors.go
    service.go
    lifecycle.go
    reminders.go
    handlers.go
    email.go
    lifecycle_test.go
    reminders_test.go
    handlers_test.go
    email_test.go
  internal/jobs/alert.go
  internal/worker/alert_worker.go
  internal/worker/alert_worker_test.go
  internal/worker/probe_worker.go    # modified
  internal/scheduler/scheduler.go    # modified
  internal/email/mailpit.go          # modified
  internal/email/mailpit_test.go     # modified
  internal/monitors/service.go       # modified: attach new monitors to email channel
  internal/api/router.go             # modified
  cmd/worker/main.go                 # modified
web/
  src/lib/api/types.ts               # modified
  src/lib/query/keys.ts              # modified
  src/lib/roles.ts                   # modified
  src/features/incidents/
    api.ts
    hooks.ts
    IncidentsScreen.tsx
    IncidentDetailScreen.tsx
    IncidentTimeline.tsx
    DeliveryTable.tsx
    IncidentsScreen.test.tsx
    IncidentDetailScreen.test.tsx
    hooks.test.ts
  src/features/alerts/
    api.ts
    hooks.ts
    AlertSettingsScreen.tsx
    RecipientsField.tsx
    AlertSettingsScreen.test.tsx
    RecipientsField.test.tsx
  src/components/layout/Sidebar.tsx  # modified
  src/router.tsx                     # modified
  src/features/monitors/MonitorDetailScreen.tsx  # modified
  src/features/monitors/MonitorDetailScreen.test.tsx  # modified
  tests/e2e/helpers.ts               # modified: SQL seed helper
  tests/e2e/smoke.spec.ts            # modified: incident UI flow
README.md                            # modified: incidents and alerts section
```

Incident HTTP, lifecycle, reminder, and email code live in `internal/incidents`; the worker owns job execution; the scheduler owns reminder timing. Frontend feature folders own their API calls, hooks, and screens.

---

### Task 1: Incident and alert schema plus sqlc queries

**Files:**
- Create: `backend/internal/db/migrations/00004_incidents.sql`
- Create: `backend/internal/db/queries/incidents.sql`, `backend/internal/db/queries/alerts.sql`
- Modify: `backend/internal/db/queries/monitors.sql` (add `GetMonitorPaused`)
- Generate: `backend/internal/db/sqlc/*`
- Test: `backend/internal/db/queries_test.go`

**Interfaces:**
- Consumes: existing `monitors`, `workspaces`, `users` tables and `sqlc`.
- Produces: tables `incidents`, `incident_events`, `alert_channels`, `monitor_alert_channels`, `alert_deliveries`; generated query methods named below.

- [ ] **Step 1: Write the failing schema test**

Append to `backend/internal/db/queries_test.go`:

```go
func TestIncidentSchema_OneActivePerMonitorAndDeliveryDedup(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()

	q := sqlc.New(pool)
	ctx := context.Background()
	userID := testutil.CreateVerifiedUser(t, pool, "incident-schema@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1,'Acme','acme-schema',$2)`, wsID, userID); err != nil {
		t.Fatal(err)
	}
	monitorID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO monitors (id, workspace_id, name, url, method, created_by) VALUES ($1,$2,'API','https://example.com','GET',$3)`, monitorID, wsID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO monitor_state (monitor_id) VALUES ($1)`, monitorID); err != nil {
		t.Fatal(err)
	}

	incidentID := uuid.New()
	if _, err := q.CreateIncident(ctx, sqlc.CreateIncidentParams{
		ID: incidentID, WorkspaceID: wsID, MonitorID: monitorID,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateIncident(ctx, sqlc.CreateIncidentParams{
		ID: uuid.New(), WorkspaceID: wsID, MonitorID: monitorID,
		StartedAt: time.Now().UTC(),
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second active incident error = %v, want pgx.ErrNoRows", err)
	}

	channelID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO alert_channels (id, workspace_id, kind, name, config) VALUES ($1,$2,'email','Email','{"recipients":["ops@example.com"]}')`, channelID, wsID); err != nil {
		t.Fatal(err)
	}
	deliveryID := uuid.New()
	insert := func() error {
		_, err := q.CreateAlertDelivery(ctx, sqlc.CreateAlertDeliveryParams{
			ID: deliveryID, IncidentID: incidentID, ChannelID: channelID,
			Event: "opened", Sequence: 0, Recipient: "ops@example.com",
		})
		return err
	}
	if err := insert(); err != nil {
		t.Fatal(err)
	}
	if err := insert(); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("duplicate delivery error = %v, want pgx.ErrNoRows", err)
	}
}
```

Add `context`, `errors`, `time`, `github.com/google/uuid`, `github.com/jackc/pgx/v5`, `o1/internal/db/sqlc`, and `o1/internal/testutil` to the imports if missing.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/db -run TestIncidentSchema -v`

Expected: FAIL because the tables/queries do not exist.

- [ ] **Step 3: Add the migration**

Create `00004_incidents.sql` with the five tables and indexes from design sections 5.1–5.5. Use `-- +goose Up` / `-- +goose Down`; the down section drops in reverse dependency order. Keep `alert_deliveries.status` including `pending`, `sent`, `failed`, `skipped`.

- [ ] **Step 4: Add the queries**

Add these named queries. Shapes and filters are pinned; exact SQL is idiomatic.

`backend/internal/db/queries/incidents.sql`:

```sql
-- name: CreateIncident :one
INSERT INTO incidents (id, workspace_id, monitor_id, status, started_at, last_notified_at)
VALUES ($1,$2,$3,'open',sqlc.arg('started_at')::timestamptz,sqlc.arg('started_at')::timestamptz)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetActiveIncidentForMonitor :one
SELECT * FROM incidents
WHERE monitor_id = $1 AND resolved_at IS NULL;

-- name: ResolveIncident :exec
UPDATE incidents SET status='resolved', resolved_at=$2, updated_at=now()
WHERE id=$1 AND resolved_at IS NULL;

-- name: AcknowledgeIncident :exec
UPDATE incidents SET status='acknowledged', acknowledged_at=$2, acknowledged_by=$3, updated_at=now()
WHERE id=$1 AND resolved_at IS NULL AND acknowledged_at IS NULL;

-- name: CreateIncidentEvent :exec
INSERT INTO incident_events (id, incident_id, kind, actor_user_id, detail)
VALUES ($1,$2,$3,$4,$5);

-- name: ListIncidents :many
SELECT i.*, m.name AS monitor_name
FROM incidents i
JOIN monitors m ON m.id = i.monitor_id
WHERE i.workspace_id = sqlc.arg('workspace_id')::uuid
  AND (sqlc.arg('active_only')::bool = false OR i.resolved_at IS NULL)
  AND (sqlc.narg('status')::text IS NULL OR i.status = sqlc.narg('status')::text)
  AND (sqlc.narg('monitor_id')::uuid IS NULL OR i.monitor_id = sqlc.narg('monitor_id')::uuid)
  AND (
    sqlc.narg('after_time')::timestamptz IS NULL
    OR (i.started_at, i.id) < (sqlc.narg('after_time')::timestamptz, sqlc.arg('after_id')::uuid)
  )
ORDER BY i.started_at DESC, i.id DESC
LIMIT sqlc.arg('page_limit')::int;

-- name: GetIncident :one
SELECT i.*, m.name AS monitor_name
FROM incidents i JOIN monitors m ON m.id = i.monitor_id
WHERE i.id=$1 AND i.workspace_id=$2;

-- name: ListIncidentEvents :many
SELECT * FROM incident_events
WHERE incident_id=$1
ORDER BY created_at, id;

-- name: ListIncidentsNeedingReminder :many
SELECT i.id, i.workspace_id, i.monitor_id, i.reminder_count
FROM incidents i
JOIN monitors m ON m.id = i.monitor_id
JOIN workspaces w ON w.id = i.workspace_id
WHERE i.resolved_at IS NULL
  AND i.acknowledged_at IS NULL
  AND i.reminder_count < sqlc.arg('max_reminders')::int
  AND i.last_notified_at <= sqlc.arg('before')::timestamptz
  AND m.paused = false
  AND w.suspended_at IS NULL
ORDER BY i.last_notified_at
LIMIT sqlc.arg('batch')::int
FOR UPDATE OF i SKIP LOCKED;

-- name: UpdateIncidentReminder :exec
UPDATE incidents
SET last_notified_at=$2, reminder_count=reminder_count+1, updated_at=now()
WHERE id=$1;

-- name: ListIncidentDeliveries :many
SELECT * FROM alert_deliveries
WHERE incident_id=$1
ORDER BY created_at, id;
```

`backend/internal/db/queries/alerts.sql`:

```sql
-- name: UpsertEmailAlertChannel :one
INSERT INTO alert_channels (id, workspace_id, kind, name, config, enabled)
VALUES ($1,$2,'email','Email',$3,$4)
ON CONFLICT (workspace_id, kind, name)
DO UPDATE SET config=EXCLUDED.config, enabled=EXCLUDED.enabled, updated_at=now()
RETURNING *;

-- name: GetEmailAlertChannel :one
SELECT * FROM alert_channels
WHERE workspace_id=$1 AND kind='email' AND name='Email';

-- name: ListAlertChannels :many
SELECT * FROM alert_channels WHERE workspace_id=$1 ORDER BY created_at, id;

-- name: AttachMonitorsToEmailChannel :exec
INSERT INTO monitor_alert_channels (monitor_id, channel_id)
SELECT m.id, sqlc.arg('channel_id')::uuid
FROM monitors m
WHERE m.workspace_id = sqlc.arg('workspace_id')::uuid
ON CONFLICT DO NOTHING;

-- name: UpsertMonitorAlertChannel :exec
INSERT INTO monitor_alert_channels (monitor_id, channel_id, enabled)
VALUES ($1,$2,$3)
ON CONFLICT (monitor_id, channel_id)
DO UPDATE SET enabled=EXCLUDED.enabled, updated_at=now();

-- name: GetMonitorAlertState :one
SELECT m.id AS monitor_id,
       c.id AS channel_id,
       COALESCE(c.enabled, false) AS channel_enabled,
       COALESCE(mac.enabled, false) AS monitor_enabled
FROM monitors m
LEFT JOIN alert_channels c
  ON c.workspace_id = m.workspace_id AND c.kind='email' AND c.name='Email'
LEFT JOIN monitor_alert_channels mac
  ON mac.monitor_id = m.id AND mac.channel_id = c.id
WHERE m.id=$1 AND m.workspace_id=$2;

-- name: ListEnabledAlertChannelsForMonitor :many
SELECT c.* FROM alert_channels c
JOIN monitor_alert_channels mac ON mac.channel_id = c.id
WHERE mac.monitor_id=$1 AND mac.enabled=true AND c.enabled=true;

-- name: CreateAlertDelivery :one
INSERT INTO alert_deliveries (id, incident_id, channel_id, event, sequence, recipient)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetAlertDelivery :one
SELECT d.*, i.status AS incident_status, i.started_at, i.resolved_at, i.acknowledged_at,
       m.name AS monitor_name, m.paused AS monitor_paused, m.url AS monitor_url,
       w.name AS workspace_name, w.slug AS workspace_slug, w.suspended_at,
       c.kind AS channel_kind, c.config AS channel_config, c.enabled AS channel_enabled
FROM alert_deliveries d
JOIN incidents i ON i.id = d.incident_id
JOIN monitors m ON m.id = i.monitor_id
JOIN workspaces w ON w.id = i.workspace_id
JOIN alert_channels c ON c.id = d.channel_id
WHERE d.id=$1;

-- name: MarkAlertDeliverySent :exec
UPDATE alert_deliveries SET status='sent', sent_at=now(), attempts=attempts+1, last_error=NULL, updated_at=now()
WHERE id=$1;

-- name: MarkAlertDeliveryFailed :exec
UPDATE alert_deliveries SET status=$2, attempts=attempts+1, last_error=$3, updated_at=now()
WHERE id=$1;
```

`failed` is also used for the final failed state; `skipped` is for stale reminders and suspended deliveries. The worker passes the status.

Add to `backend/internal/db/queries/monitors.sql`:

```sql
-- name: GetMonitorPaused :one
SELECT paused FROM monitors WHERE id=$1;
```

- [ ] **Step 5: Regenerate sqlc and run the test**

Run:

```bash
cd backend
sqlc generate
go test ./internal/db -run TestIncidentSchema -v
```

Expected: PASS. If sqlc names differ, adapt the generated call sites, not the query meaning.

- [ ] **Step 6: Run vet and commit**

Run: `cd backend && go vet ./... && go test ./internal/db -short`

```bash
git add backend/internal/db/migrations/00004_incidents.sql backend/internal/db/queries backend/internal/db/sqlc backend/internal/db/queries_test.go
git commit -m "feat: add incident and alert schema"
```

---

### Task 2: Atomic incident lifecycle in the probe worker

**Files:**
- Create: `backend/internal/incidents/types.go`, `errors.go`, `lifecycle.go`, `lifecycle_test.go`
- Create: `backend/internal/jobs/alert.go`
- Modify: `backend/internal/worker/probe_worker.go`
- Modify: `backend/internal/worker/probe_worker_test.go`

**Interfaces:**
- Consumes: Task 1 queries; existing `monitors.RedactURL`, `river.Client[pgx.Tx]`.
- Produces:

```go
package incidents

type Transition struct {
    WorkspaceID uuid.UUID
    MonitorID   uuid.UUID
    Previous    string
    Next        string
    At          time.Time
}

type JobInserter interface {
    InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

func ApplyProbeTransition(ctx context.Context, tx pgx.Tx, q *sqlc.Queries, inserter JobInserter, tr Transition) error
```

```go
package jobs

type AlertArgs struct {
    DeliveryID uuid.UUID `json:"delivery_id" river:"unique"`
}
func (AlertArgs) Kind() string { return "alert" }
```

- [ ] **Step 1: Write the failing lifecycle test**

Create `backend/internal/incidents/lifecycle_test.go` with a test container and a fake inserter:

```go
type fakeInserter struct{ jobs []river.JobArgs }
func (f *fakeInserter) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
    f.jobs = append(f.jobs, args)
    return &rivertype.JobInsertResult{}, nil
}
```

Build a workspace, monitor, state, email channel with two recipients, and monitor-channel row. Then assert:

- `unknown → down`: one incident, one `opened` event, two deliveries, two `AlertArgs` jobs.
- Calling the same transition again (or the row already open) creates no second incident/delivery.
- Adding an `acknowledged` timestamp then `down → up`: incident resolved, one `resolved` event, two resolve deliveries and jobs.
- `up → up` and `down → up` with no active incident do nothing.

Use `sqlc.New(pool).WithTx(tx)` and commit at the end.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/incidents -run TestApplyProbeTransition -v`

Expected: FAIL because the package/functions do not exist.

- [ ] **Step 3: Implement the lifecycle**

`lifecycle.go`:

- `openIncident`: call `CreateIncident`; if `pgx.ErrNoRows`, return nil (already active). Insert `opened` event. For each enabled channel, parse `config.recipients`, create each delivery with `CreateAlertDelivery`; on `pgx.ErrNoRows`, skip; otherwise insert `jobs.AlertArgs` with `MaxAttempts: 5`, `Queue: "alert"`.
- `resolveIncident`: call `GetActiveIncidentForMonitor`; on `pgx.ErrNoRows`, return nil. `ResolveIncident`, insert `resolved` event, create resolved deliveries/jobs the same way.
- `ApplyProbeTransition` switches on `Previous`/`Next`; ignores everything else.
- Channel config JSON shape is `{ "recipients": ["..."] }`; use a small `EmailConfig` type and `json.Unmarshal`.
- Delivery IDs are UUIDv7; event IDs are UUIDv7.

- [ ] **Step 4: Wire the probe worker**

In `probe_worker.go`, after the state transaction updates `monitor_state`, call `ApplyProbeTransition` with `qtx`, the configured `JobInserter`, and the previous/next status before commit. The worker constructor gains a `SetJobInserter(inserter incidents.JobInserter)` method because River's client needs its workers at construction; in `cmd/worker/main.go`, call `probeWorker.SetJobInserter(client)` after `river.NewClient` and before `client.Start`. Before the state update, call `GetMonitorPaused(ctx, monitor.ID)`; if the monitor is paused, insert the probe result and return without state/incident changes.

Update `cmd/worker/main.go` to create the River client with the probe worker, then set the client on the worker, then start it.

- [ ] **Step 5: Add the probe-worker integration test**

Extend `probe_worker_test.go`: a failing probe against a monitor with a configured email channel opens an incident and enqueues an `alert` job; a duplicate job run does not duplicate. The existing fake prober and test container patterns are reused.

- [ ] **Step 6: Run tests and commit**

Run:

```bash
cd backend
go test ./internal/incidents ./internal/worker -v
go vet ./...
```

Expected: PASS, including the duplicate-idempotency cases.

```bash
git add backend/internal/incidents backend/internal/jobs/alert.go backend/internal/worker backend/cmd/worker/main.go
git commit -m "feat: open and resolve incidents from probe transitions"
```

---

### Task 3: Alert worker, email rendering, and sender hardening

**Files:**
- Create: `backend/internal/incidents/email.go`, `email_test.go`
- Modify: `backend/internal/incidents/types.go` (add `AlertEmail`)
- Create: `backend/internal/worker/alert_worker.go`, `alert_worker_test.go`
- Modify: `backend/internal/email/mailpit.go`, `mailpit_test.go`
- Modify: `backend/cmd/worker/main.go`

**Interfaces:**
- Consumes: `jobs.AlertArgs` and delivery rows from Task 2; `email.Sender`.
- Produces:

```go
type AlertEmail struct {
    WorkspaceName string
    WorkspaceSlug string
    MonitorName   string
    MonitorURL    string // already redacted
    Event         string // opened, reminder, resolved
    Sequence      int
    IncidentID    uuid.UUID
    StartedAt     time.Time
    BaseURL       string
    Recipient     string
}
func BuildAlertMessage(in AlertEmail) email.Message
```

```go
func NewAlertWorker(pool *pgxpool.Pool, q *sqlc.Queries, sender email.Sender, cfg config.Config, logger *slog.Logger) *AlertWorker
func (w *AlertWorker) Work(ctx context.Context, job *river.Job[jobs.AlertArgs]) error
```

- [ ] **Step 1: Write the failing email tests**

`email_test.go` asserts:

- `opened` subject contains the monitor name; text/HTML contain workspace, redacted URL, and the incident link; `To` is the requested recipient.
- A monitor name with `\r\nBcc: evil@example.com` produces no newline in the subject and no injected header.
- A monitor name with `<script>` is HTML-escaped in the HTML body.
- A name with the old fixed MIME boundary string does not break the rendered message.

`mailpit_test.go` asserts:

- `Send`/`render` rejects `To` or `Subject` containing CR/LF.
- The MIME boundary differs between two renders.
- A non-ASCII subject is RFC 2047 encoded (`=?utf-8?` or equivalent).
- The existing boundary assertion in `TestMailpitSender_Render` is updated to extract the boundary from the rendered message instead of expecting the old fixed value.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/incidents -run TestBuildAlert -v && go test ./internal/email -run 'TestMailpitSender_(RejectsHeaderInjection|Boundary|SubjectEncoding)' -v`

Expected: FAIL because the functions/hardening do not exist.

- [ ] **Step 3: Implement `BuildAlertMessage` and harden the sender**

- Strip CR/LF from subject and header values; HTML-escape user-controlled body values; use `html.EscapeString`.
- Use `net/textproto`/`mime.QEncoding` for the subject.
- Generate a random boundary per render with `crypto/rand` and hex.
- Reject CR/LF in `To` and `Subject` in `MailpitSender.Send`.
- Keep the existing auth email behavior; its tests must still pass.

- [ ] **Step 4: Implement the alert worker**

`AlertWorker.Work` loads `GetAlertDelivery`; returns nil for `sent`, `failed`, `skipped`; for `reminder`, skips when the incident is resolved/acknowledged, the monitor is paused, the workspace is suspended, or `created_at` is older than 60 minutes, marking `skipped`; builds the message with `BuildAlertMessage`; sends; marks sent or failed/attempts as design section 7.2.

- [ ] **Step 5: Wire the worker and queue**

In `cmd/worker/main.go`, create `email.NewMailpitSender(cfg.SMTPAddr, cfg.EmailFrom)`, register `NewAlertWorker` on the `alert` queue with `MaxWorkers: 4`, and refuse `O1_ENV=production` before starting.

- [ ] **Step 6: Write the failing alert-worker test**

Insert an incident and delivery, run the worker with a fake sender, assert `sent`; make the sender fail and assert attempts/last_error and retry behavior; assert a sent delivery is a no-op; assert a stale reminder after acknowledgement becomes `skipped`; assert an old reminder becomes `skipped`.

- [ ] **Step 7: Run tests and commit**

Run:

```bash
cd backend
go test ./internal/incidents ./internal/worker ./internal/email -v
go vet ./...
```

```bash
git add backend/internal/incidents/email.go backend/internal/incidents/email_test.go backend/internal/incidents/types.go backend/internal/worker/alert_worker.go backend/internal/worker/alert_worker_test.go backend/internal/email backend/cmd/worker/main.go
git commit -m "feat: deliver incident alert emails"
```

---

### Task 4: Reminder scheduling

**Files:**
- Create: `backend/internal/incidents/reminders.go`, `reminders_test.go`
- Modify: `backend/internal/scheduler/scheduler.go`
- Modify: `backend/internal/scheduler/scheduler_test.go`

**Interfaces:**
- Consumes: Task 1 `ListIncidentsNeedingReminder`, `GetEmailAlertChannel`, `ListEnabledAlertChannelsForMonitor`, `CreateAlertDelivery`, `UpdateIncidentReminder`; `jobs.AlertArgs`.
- Produces:

```go
func EnqueueReminders(ctx context.Context, tx pgx.Tx, q *sqlc.Queries, inserter JobInserter, now time.Time) (int, error)
```

- [ ] **Step 1: Write the failing reminder tests**

In `reminders_test.go`, with a fake inserter and real DB:

- An open unacknowledged incident with `last_notified_at` 31 minutes ago and one channel with two recipients creates sequence 1 deliveries and two jobs, increments `reminder_count` and `last_notified_at`.
- An acknowledged incident creates nothing.
- A resolved incident creates nothing.
- A paused monitor creates nothing.
- A suspended workspace creates nothing.
- An incident with no channel/recipients creates nothing and does **not** increment `reminder_count` or `last_notified_at`.
- Calling the function twice at the same `now` does not duplicate sequence 1.
- `reminder_count = 10` creates nothing.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/incidents -run TestEnqueueReminders -v`

Expected: FAIL because `EnqueueReminders` does not exist.

- [ ] **Step 3: Implement `EnqueueReminders`**

Use a transaction-friendly function: `ListIncidentsNeedingReminder` with `max_reminders=10`, `before=now-30m`, `batch` from a constant (e.g. 100). For each incident, list enabled channels, parse recipients, insert deliveries with `sequence = reminder_count+1`; track whether any row was inserted; if so, call `UpdateIncidentReminder` and enqueue `AlertArgs` for each inserted delivery. Return the number of incidents that produced at least one delivery.

- [ ] **Step 4: Wire the scheduler**

Add `EnqueueReminders` to the scheduler's loop after `Tick`, in its own transaction. Do not let a reminder failure block the probe tick; log and continue, or return the error after the probe tick commits. The chosen behavior: log the error and continue the run loop, because a missed reminder is retried on the next tick.

- [ ] **Step 5: Add the scheduler test**

Extend `scheduler_test.go` to assert that a due incident produces alert jobs through `EnqueueReminders` and that a stopped/acknowledged incident does not.

- [ ] **Step 6: Run tests and commit**

```bash
cd backend
go test ./internal/incidents ./internal/scheduler -v
go vet ./...
git add backend/internal/incidents/reminders.go backend/internal/incidents/reminders_test.go backend/internal/scheduler
git commit -m "feat: enqueue incident reminder alerts"
```

---

### Task 5: Incident HTTP API

**Files:**
- Create: `backend/internal/incidents/errors.go`, `types.go`, `service.go`, `handlers.go`, `handlers_test.go`
- Modify: `backend/internal/api/router.go`

**Interfaces:**
- Consumes: Task 1 queries; `workspaces.Service.Authorize`; `httpx.PageCursor`, `httpx.EncodeCursor`/`DecodeCursor`; `auth.PrincipalFrom`.
- Produces:

```go
func NewService(pool *pgxpool.Pool, q *sqlc.Queries, ws *workspaces.Service) *Service

type IncidentFilter struct {
    Status    string // active, open, acknowledged, resolved, all
    MonitorID *uuid.UUID
}

func (s *Service) List(ctx context.Context, actorID, workspaceID uuid.UUID, filter IncidentFilter, cursor *httpx.PageCursor, limit int) ([]Incident, error)
func (s *Service) Get(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) (IncidentDetail, error)
func (s *Service) Acknowledge(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) error
func (s *Service) Deliveries(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) ([]Delivery, error)
```

- [ ] **Step 1: Write the failing handler tests**

Add `handlers_test.go` using `httptest` and `testutil.NewPostgres`; seed a workspace, members with viewer/member/admin roles, a monitor, and incidents. Assert:

- `GET /workspaces/{id}/incidents` viewer sees active incidents by default; `status=resolved` and `status=all` filter; `monitor_id` filters; cursor returns the second page and `next_cursor`.
- `GET /workspaces/{id}/incidents/{incidentID}` returns the incident and ordered events; 404 for another workspace.
- `POST .../acknowledge` member succeeds, viewer 403, suspended 403 `workspace_suspended`, resolved 409 `incident_resolved`, second ack 204 without a second event.
- `GET .../deliveries` admin sees rows, member 403.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/incidents -run TestIncidentHandlers -v`

Expected: FAIL because the service/handlers/routes do not exist.

- [ ] **Step 3: Implement the service and handlers**

- `List` authorizes viewer, maps `Status` to `active_only` plus `status`, validates limit (default 50, max 100), decodes cursor, returns `Incident` rows with `monitor_name`.
- `Get` authorizes viewer, loads incident + events; 404 if not in workspace.
- `Acknowledge` authorizes member, begins a transaction, locks the workspace, rejects suspension, loads the incident, rejects resolved, updates only when open, inserts one event, commits. Already acknowledged returns nil.
- `Deliveries` authorizes admin, loads incident deliveries.
- Handlers parse path IDs, map `ErrNotFound`/`ErrForbidden`/`ErrSuspended`/`ErrIncidentResolved` to the problem codes from the design.
- Register routes in `router.go` using the exact paths from design section 8.

- [ ] **Step 4: Run tests and commit**

```bash
cd backend
go test ./internal/incidents -v
go test ./internal/api -short
go vet ./...
git add backend/internal/incidents backend/internal/api/router.go
git commit -m "feat: add incident API"
```

---

### Task 6: Alert channel API, monitor attachment, and monitor-scoped state

**Files:**
- Modify: `backend/internal/incidents/service.go`, `handlers.go`, `errors.go`, `types.go`, `handlers_test.go`
- Modify: `backend/internal/monitors/service.go`, `backend/internal/monitors/service_test.go`
- Modify: `backend/internal/api/router.go`

**Interfaces:**
- Consumes: Task 1 alert queries; `workspaces.Service.Authorize`.
- Produces:

```go
type AlertChannel struct {
    ID         uuid.UUID
    Kind       string
    Name       string
    Enabled    bool
    Recipients []string
}

type MonitorAlertState struct {
    Available      bool
    ChannelEnabled bool
    Enabled        bool
}

func (s *Service) GetEmailChannel(ctx context.Context, actorID, workspaceID uuid.UUID) (AlertChannel, error)
func (s *Service) PutEmailChannel(ctx context.Context, actorID, workspaceID uuid.UUID, recipients []string, enabled bool) (AlertChannel, error)
func (s *Service) GetMonitorAlertState(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID) (MonitorAlertState, error)
func (s *Service) SetMonitorAlert(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID, enabled bool) error
```

- [ ] **Step 1: Write the failing tests**

- `GET /alert-channels` admin gets the email channel with recipients; member/viewer 403.
- `PUT /alert-channels/email` normalizes/lowercases/dedupes recipients; rejects 0 recipients when enabled, 21 recipients, invalid email, and >320 chars; `enabled=false` accepts an empty list.
- Creating the channel attaches every existing monitor; creating the same channel twice does not duplicate join rows.
- `GET /monitors/{monitorID}/alert-channel` member gets `{available, channel_enabled, enabled}` without recipients; missing channel gives `available=false`.
- `PATCH /monitors/{monitorID}/alert-channel` member toggles; viewer 403; missing channel 404 `alert_channel_missing`; suspended 403.
- Creating a new monitor while the channel exists inserts an enabled join row; creating one without a channel still works.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/incidents -run 'TestAlertChannel|TestMonitorAlert' -v && go test ./internal/monitors -run TestCreate_AttachesEmailChannel -v`

Expected: FAIL.

- [ ] **Step 3: Implement the channel service and handlers**

- `GetEmailChannel` returns `ErrNotFound` or an empty channel? Use `ErrNotFound` when missing; the API returns 404 `alert_channel_missing` for PUT? For GET, return `{channels: []}` from `ListAlertChannels` instead of 404.
- `PutEmailChannel` begins a transaction, locks the workspace, rejects suspension, normalizes recipients, upserts, attaches all monitors with `ON CONFLICT DO NOTHING`.
- `GetMonitorAlertState` uses `GetMonitorAlertState`; `available = channel_id != nil`, `channel_enabled`, `enabled`.
- `SetMonitorAlert` begins a transaction, locks workspace, rejects suspension, ensures the email channel exists, upserts the join.
- Extend `monitors.Service.create` to call `GetEmailAlertChannel` and `UpsertMonitorAlertChannel` for the new monitor when a channel exists.

- [ ] **Step 4: Register routes and map errors**

Add the routes from design section 8.2. Map `ErrAlertChannelMissing` to 404 `alert_channel_missing`, `ErrSuspended` to 403 `workspace_suspended`, validation fields to `validation_error.fields.recipients`.

- [ ] **Step 5: Run tests and commit**

```bash
cd backend
go test ./internal/incidents ./internal/monitors -v
go test ./internal/api -short
go vet ./...
git add backend/internal/incidents backend/internal/monitors backend/internal/api/router.go
git commit -m "feat: add email alert channel API"
```

---

### Task 7: Frontend incident data layer, list, detail, and monitor incident surfaces

**Files:**
- Modify: `web/src/lib/api/types.ts`, `web/src/lib/query/keys.ts`, `web/src/lib/roles.ts`
- Create: `web/src/features/incidents/api.ts`, `hooks.ts`, `IncidentsScreen.tsx`, `IncidentDetailScreen.tsx`, `IncidentTimeline.tsx`
- Create: `web/src/features/incidents/IncidentsScreen.test.tsx`, `IncidentDetailScreen.test.tsx`, `hooks.test.ts`
- Modify: `web/src/router.tsx`, `web/src/components/layout/Sidebar.tsx`
- Modify: `web/src/features/monitors/MonitorDetailScreen.tsx`, `MonitorDetailScreen.test.tsx`

**Interfaces:**
- Consumes: Task 5/6 APIs; existing `apiRequest`, `queryKeys`, `nextPageParam`, `ProblemAlert`, `ErrorPanel`, `EmptyState`, `ConfirmDialog`, `formatRelativeTime`, `formatLatency`, `roleAtLeast`.
- Produces:

```ts
export interface Incident { id: string; workspace_id: string; monitor_id: string; monitor_name: string; status: 'open' | 'acknowledged' | 'resolved'; started_at: string; acknowledged_at: string | null; acknowledged_by: string | null; resolved_at: string | null; last_notified_at: string | null; reminder_count: number; }
export interface IncidentEvent { id: string; kind: 'opened' | 'acknowledged' | 'resolved'; actor_user_id: string | null; detail: unknown; created_at: string; }
export interface IncidentDetail { incident: Incident; events: IncidentEvent[]; }
export interface IncidentDelivery { id: string; event: 'opened' | 'reminder' | 'resolved'; sequence: number; recipient: string; status: 'pending' | 'sent' | 'failed' | 'skipped'; attempts: number; last_error: string | null; sent_at: string | null; created_at: string; }
export interface IncidentFilters { status: 'active' | 'open' | 'acknowledged' | 'resolved' | 'all'; monitorId?: string; }

export function listIncidents(workspaceId: string, filters: IncidentFilters, cursor?: string, signal?: AbortSignal): Promise<{ incidents: Incident[]; next_cursor?: string }>
export function getIncident(workspaceId: string, incidentId: string): Promise<IncidentDetail>
export function acknowledgeIncident(workspaceId: string, incidentId: string): Promise<void>
export function listIncidentDeliveries(workspaceId: string, incidentId: string): Promise<IncidentDelivery[]>
```

Add query keys:

```ts
incidents: (workspaceId: string, filters: IncidentFilters) => ['incidents', workspaceId, filters] as const,
incident: (workspaceId: string, incidentId: string) => ['incident', workspaceId, incidentId] as const,
incidentDeliveries: (workspaceId: string, incidentId: string) => ['incident-deliveries', workspaceId, incidentId] as const,
alertChannels: (workspaceId: string) => ['alert-channels', workspaceId] as const,
monitorAlertChannel: (workspaceId: string, monitorId: string) => ['monitor-alert-channel', workspaceId, monitorId] as const,
```

Add roles:

```ts
export function canAcknowledgeIncidents(role: Role): boolean { return roleAtLeast(role, 'member'); }
export function canManageAlertChannels(role: Role): boolean { return roleAtLeast(role, 'admin'); }
export function canManageMonitorAlerts(role: Role): boolean { return roleAtLeast(role, 'member'); }
```

- [ ] **Step 1: Write the failing tests**

- `hooks.test.ts`: `useIncidents` requests the status filter and `monitor_id`; the second page sends the first `next_cursor`; acknowledge invalidates list/detail.
- `IncidentsScreen.test.tsx`: default `Active` filter; renders monitor name/status/started/duration; changes filter; `Load more`; empty state; viewer sees no acknowledge action (list has no action).
- `IncidentDetailScreen.test.tsx`: timeline order; `Acknowledge` visible for member+ on open; hidden for viewer and for acknowledged/resolved; clicking calls `POST /acknowledge` and refreshes; 404 state.
- `MonitorDetailScreen.test.tsx`: open incident banner links to the incident; recent incidents render; no banner when none.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/features/incidents src/features/monitors/MonitorDetailScreen.test.tsx`

Expected: FAIL because the modules/types do not exist.

- [ ] **Step 3: Implement the data layer and screens**

- `listIncidents` builds `status`, `monitor_id`, `cursor`, `limit=50`; omit empty filters.
- `useIncidents` is an infinite query with `refetchInterval: 15_000`, `nextPageParam`.
- `useIncident`/`useIncidentDeliveries` use 15_000/30_000 stale/refetch settings; deliveries query is enabled only for admin.
- `useAcknowledgeIncident` invalidates the incident, list, and monitor keys.
- `IncidentsScreen`: status filter (`Active`, `Open`, `Acknowledged`, `Resolved`, `All`), table/cards, `Load more`, empty state `No incidents`, links to detail.
- `IncidentDetailScreen`: header, `IncidentTimeline`, acknowledge button, delivery table for admin, link to monitor, 404.
- Add routes `/w/$workspaceSlug/incidents` and `/w/$workspaceSlug/incidents/$incidentId`.
- Add `Incidents` to `Sidebar` for all roles.
- On `MonitorDetailScreen`, add the open-incident banner and a recent-incidents section using `useIncidents` with `monitor_id` and `status: 'all'`, limit 5. Add the banner link.

- [ ] **Step 4: Run tests and checks**

```bash
cd web
npm test -- --run src/features/incidents src/features/monitors
npm run format:check
npm run lint
npm run typecheck
npm run build
```

- [ ] **Step 5: Commit**

```bash
git add web/src/lib/api/types.ts web/src/lib/query/keys.ts web/src/lib/roles.ts web/src/features/incidents web/src/router.tsx web/src/components/layout/Sidebar.tsx web/src/features/monitors/MonitorDetailScreen.tsx web/src/features/monitors/MonitorDetailScreen.test.tsx
git commit -m "feat: add incident dashboard and timeline"
```

---

### Task 8: Frontend alert settings, monitor toggle, navigation, and docs

**Files:**
- Create: `web/src/features/alerts/api.ts`, `hooks.ts`, `AlertSettingsScreen.tsx`, `RecipientsField.tsx`, `AlertSettingsScreen.test.tsx`, `RecipientsField.test.tsx`
- Modify: `web/src/router.tsx`, `web/src/components/layout/Sidebar.tsx`, `web/src/features/monitors/MonitorDetailScreen.tsx`, `MonitorDetailScreen.test.tsx`
- Modify: `README.md`

**Interfaces:**
- Consumes: Task 6 APIs; `canManageAlertChannels`, `canManageMonitorAlerts`; `queryKeys.alertChannels`, `queryKeys.monitorAlertChannel`.
- Produces:

```ts
export interface AlertChannel { id: string; kind: 'email'; name: string; enabled: boolean; recipients: string[]; created_at: string; updated_at: string; }
export interface MonitorAlertState { available: boolean; channel_enabled: boolean; enabled: boolean; }

export function listAlertChannels(workspaceId: string): Promise<{ channels: AlertChannel[] }>
export function putEmailAlertChannel(workspaceId: string, input: { recipients: string[]; enabled: boolean }): Promise<AlertChannel>
export function getMonitorAlertChannel(workspaceId: string, monitorId: string): Promise<MonitorAlertState>
export function setMonitorAlertChannel(workspaceId: string, monitorId: string, enabled: boolean): Promise<void>
```

- [ ] **Step 1: Write the failing tests**

- `AlertSettingsScreen.test.tsx`: admin sees recipients editor and enabled switch; member/viewer are redirected or see a forbidden state; add/remove addresses; save PUT body; validation for empty enabled list, duplicate, invalid address; success refreshes.
- `RecipientsField.test.tsx`: add/remove and validation messages; keyboard accessible.
- `MonitorDetailScreen.test.tsx`: member sees the email switch; viewer does not; channel disabled shows `Email alerts are disabled for this workspace.`; missing channel shows the note; toggle PATCHes.
- Router/sidebar test: `Alerts` nav visible only to admin+; `/settings/alerts` redirects non-admins.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/features/alerts src/features/monitors/MonitorDetailScreen.test.tsx src/components/layout`

- [ ] **Step 3: Implement the alert data layer and screen**

- API functions and hooks; `useAlertChannels`, `useEmailAlertChannel`, `useSaveEmailAlertChannel`, `useMonitorAlertChannel`, `useSetMonitorAlertChannel`.
- `AlertSettingsScreen`: recipients editor, enabled switch, save, ProblemAlert, success toast; admin gate.
- `RecipientsField`: chip/list editor with add/remove and inline validation.
- Add route `/w/$workspaceSlug/settings/alerts` with an admin guard; add `Alerts` under the `Settings` nav group for admin+.
- Add the monitor detail email switch and disabled/missing notes.

- [ ] **Step 4: Update the README**

Add an "Incidents and email alerts" section: incident lifecycle, acknowledgement, email recipients, reminders (30 minutes, max 10), Mailpit development behavior, and the Plan 5 boundary (other channels/status page/production email).

- [ ] **Step 5: Run checks and commit**

```bash
cd web
npm test -- --run src/features/alerts src/features/monitors src/components/layout
npm run format:check
npm run lint
npm run typecheck
npm run build
git add web/src/features/alerts web/src/features/monitors/MonitorDetailScreen.tsx web/src/features/monitors/MonitorDetailScreen.test.tsx web/src/router.tsx web/src/components/layout/Sidebar.tsx README.md
git commit -m "feat: add alert settings and monitor email toggle"
```

---

### Task 9: E2E incident seed and final verification

**Files:**
- Modify: `web/tests/e2e/helpers.ts`, `web/tests/e2e/smoke.spec.ts`
- Modify: `README.md` if the command list changes

**Interfaces:**
- Consumes: Task 7/8 UI, existing E2E SQL helper pattern.
- Produces: deterministic incident UI smoke without waiting for a probe to fail.

- [ ] **Step 1: Add the deterministic seed helper**

In `helpers.ts`, add:

```ts
export function seedIncident(monitorName: string): { incidentId: string; workspaceSlug: string } {
  // psql -tA:
  // SELECT m.id, w.slug FROM monitors m JOIN workspaces w ON w.id=m.workspace_id
  // WHERE m.name=$1 ORDER BY m.created_at DESC LIMIT 1;
  // INSERT INTO incidents (id, workspace_id, monitor_id, status, started_at, last_notified_at)
  // VALUES (gen_random_uuid(), ..., ..., 'open', now(), now()) RETURNING id;
  // INSERT INTO incident_events (id, incident_id, kind, detail)
  // VALUES (gen_random_uuid(), ..., 'opened', '{}');
  // Return the incident id and slug.
}
```

Use the existing `docker compose -f ../compose.yaml exec -T postgres psql` pattern and quote values safely (values are generated test data, but still use `psql -v` variables or literal escaping).

- [ ] **Step 2: Extend the smoke path**

After creating the monitor and before editing/deleting it:

- Seed an incident for `Example`.
- Navigate to `/w/{slug}/incidents`.
- Assert `Example` and `Open` are visible.
- Open the incident, assert the timeline has `Opened`.
- Click `Acknowledge`, assert `Acknowledged`.
- Return to the monitor, refresh, and assert the banner still links to the acknowledged incident (acknowledged incidents remain active), or that the recent-incidents link reflects the acknowledged state.
- Keep the rest of the monitor CRUD and admin flow unchanged.

- [ ] **Step 3: Run the E2E**

```bash
cd web
npm run format:check
npm run lint
npm run typecheck
npx playwright test --list
# If ports are free:
docker compose up -d --build
npx playwright test
docker compose down -v
```

If the local ports are occupied by an unrelated stack, use the same uncommitted host-port override documented in Task 12 of Plan 3; do not commit it. Report whether the real E2E ran.

- [ ] **Step 4: Full verification and commit**

```bash
cd backend && go test -short ./... && go vet ./...
cd ../web && npm run format:check && npm run lint && npm run typecheck && npm test -- --run && npm run build
git add web/tests/e2e README.md
git commit -m "test: cover incident dashboard in e2e smoke"
```

---

## Self-Review

**Spec coverage:**

- Incident open/resolve/acknowledge/timeline → Tasks 2, 5, 7.
- Email channel, recipients, monitor toggle → Task 6, 8.
- Delivery/retry/dedup/reminders → Tasks 2, 3, 4.
- Dashboard incidents and alert settings → Tasks 7, 8.
- Email safety and redaction → Task 3.
- E2E and docs → Task 9.

**Review Focus ownership:**

1. Duplicate incident/delivery → Task 1 unique indexes + Task 2 lifecycle test.
2. Stale reminder → Task 3 worker test + Task 4 reminder tests.
3. Resolve after ack / no active incident → Task 2 lifecycle test.
4. Header/HTML/MIME injection → Task 3 email tests.
5. Suspended workspace writes/no new incidents → Task 5/6 handler tests + Task 4 tests.
6. Reminder clock/budget without deliveries → Task 4 tests.

**Type consistency:** `Incident`, `IncidentEvent`, `IncidentDetail`, `IncidentDelivery`, `AlertChannel`, `MonitorAlertState`, `Transition`, `AlertArgs`, and the query-key names are defined once and consumed by later tasks. `ApplyProbeTransition` and `EnqueueReminders` keep their names across worker/scheduler wiring.

**Proportion:** The plan pins schema, query names, public types, routes, and tests; component and handler bodies are left to the implementer where the interfaces and tests determine them.

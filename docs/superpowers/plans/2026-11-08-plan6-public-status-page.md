# Plan 6 — Public Status Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add one public status page per workspace at `/status/{workspace-slug}` with admin-selected monitors, 90 daily uptime bars, active/recent incidents, a 15-second cache, and per-IP rate limiting.

**Architecture:** New `status_pages` and `status_page_monitors` tables feed an `internal/statuspage` package that owns admin configuration, daily aggregation from raw `probe_results`, an in-process TTL cache, and the public payload. The SPA adds a public route outside the auth layout plus an admin settings screen under Settings.

**Tech Stack:** Go 1.26, PostgreSQL 16, goose migrations, sqlc, existing `workspaces` RBAC, React 19, TanStack Router/Query, shadcn/ui, Vitest/MSW, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-08-plan6-public-status-page-design.md`

## Global Constraints

- Plan 6 scope only. No custom domains, multiple pages per workspace, separate page slugs, subscriber notifications, public incident detail pages, manual announcements, rollups, retention changes, latency charts, or polling.
- One page per workspace; its public address is the immutable `workspaces.slug` at `/status/{slug}`.
- Status pages are created lazily on first save; no backfill.
- `days` always contains exactly 90 buckets, ascending by UTC date, ending today (UTC).
- Uptime = `up / (up + down)`. Days with zero checks are `no_data` and excluded from the denominator. Paused, unknown, and missing data are never downtime.
- Current monitor status is `paused` when `monitors.paused`, otherwise `monitor_state.status` (`up`, `down`, or `unknown`).
- `incidents.active` shows open and acknowledged incidents identically as `ongoing`, for selected monitors only. `incidents.recent` is resolved incidents from the last 7 days, newest first, capped at 20.
- The public payload never contains monitor URLs, headers, bodies, error text, latency, status codes, workspace IDs, acknowledgement identity, reminder counts, or delivery rows.
- Disabled, unknown, and never-configured slugs return the same `404` problem with code `not_found` and `Cache-Control: no-store` from the API. The browser `/status/{slug}` route is a normal SPA shell with HTTP 200.
- Successful public responses carry `Cache-Control: public, max-age=15, stale-while-revalidate=15`; the service caches them in process for 15 seconds per slug and never caches 404s.
- Public reads are limited to 60 requests per minute per IP with burst 20.
- Admin config routes require `workspaces.RoleAdmin` (owner included). Public reads need no session or role.
- A suspended workspace rejects config writes with `403 workspace_suspended` but keeps public reads.
- Validation: title 1–80 runes after trim; description 0–500 runes after trim; monitor IDs must be unique, in-workspace, and non-empty when enabled.
- A config save replaces the selection and invalidates that workspace's cache entry after commit.
- Display order is monitor `created_at, id`; an enabled page that lost all monitors stays public with an empty monitor list.
- UI copy is English. Every task ends with formatting, lint/typecheck where applicable, tests, and a commit.

## Review Focus

1. **UTC day boundaries.** A probe at 23:59 UTC and one at 00:01 UTC must land in different buckets, and the response must still be exactly 90 buckets with today last; a local-time grouping would shift bars and uptime.
2. **404 caching.** A disabled or unknown slug must return `no-store` and never enter the server cache, so enabling a page is visible on the next request; a cached 404 would keep the page dark.
3. **Enabled page with zero monitors.** Deleting the last selected monitor must not panic, divide by zero, or 500; the page returns an empty monitor list and empty incident arrays.
4. **Secret and identity leakage.** Monitor URLs, headers, error text, acknowledgement users, and delivery fields must not appear in public JSON or rendered HTML, even when a monitor name or page description contains JSON-like or HTML-like text.
5. **Cache isolation and bound.** Saving one workspace's config must invalidate only that slug, and requests with random slugs must not grow the cache without limit.

---

## File Structure

```text
backend/
  internal/db/migrations/00006_status_pages.sql        # create
  internal/db/queries/status_pages.sql                 # create
  internal/db/sqlc/*                                   # regenerated
  internal/db/queries_test.go                          # modify: schema + day grouping tests
  internal/statuspage/
    types.go                                           # create
    cache.go                                           # create
    service.go                                         # create
    handlers.go                                        # create
    cache_test.go                                      # create
    service_test.go                                    # create
    handlers_test.go                                   # create
  internal/api/router.go                               # modify: dependency + routes
web/
  src/lib/api/types.ts                                 # modify
  src/lib/query/keys.ts                                # modify
  src/lib/roles.ts                                     # modify: canManageStatusPage
  src/lib/roles.test.ts                                # modify
  src/features/status-pages/api.ts                     # create
  src/features/status-pages/api.test.ts                # create
  src/features/status-pages/hooks.ts                   # create
  src/features/status-pages/hooks.test.tsx             # create
  src/features/status-pages/PublicStatusScreen.tsx     # create
  src/features/status-pages/PublicStatusScreen.test.tsx # create
  src/features/status-pages/StatusPageSettingsScreen.tsx # create
  src/features/status-pages/StatusPageSettingsScreen.test.tsx # create
  src/router.tsx                                       # modify: public + settings routes
  src/router.test.tsx                                  # modify: route coverage
  src/components/layout/Sidebar.tsx                    # modify: settings link
  tests/e2e/helpers.ts                                 # modify: seed probe results
  tests/e2e/smoke.spec.ts                              # modify: public page flow
README.md                                              # modify: public status page section
```

Backend status-page concerns live in `internal/statuspage`; workspace RBAC stays in `workspaces`; SQL lives beside the existing query files. The dashboard feature folder owns its API calls, hooks, and screens.

---

### Task 1: Status page schema, queries, and sqlc generation

**Files:**
- Create: `backend/internal/db/migrations/00006_status_pages.sql`
- Create: `backend/internal/db/queries/status_pages.sql`
- Generate: `backend/internal/db/sqlc/*`
- Test: `backend/internal/db/queries_test.go`

**Interfaces:**
- Consumes: existing `workspaces`, `monitors`, `monitor_state`, `probe_results`, and `incidents`.
- Produces:
  - `sqlc.StatusPage{WorkspaceID uuid.UUID, Enabled bool, Title, Description string, CreatedAt, UpdatedAt time.Time}`
  - `UpsertStatusPage(ctx, sqlc.UpsertStatusPageParams{WorkspaceID uuid.UUID, Enabled bool, Title, Description string}) (sqlc.StatusPage, error)`
  - `GetStatusPage(ctx, workspaceID uuid.UUID) (sqlc.StatusPage, error)`
  - `DeleteStatusPageMonitors(ctx, statusPageID uuid.UUID) error`
  - `InsertStatusPageMonitor(ctx, sqlc.InsertStatusPageMonitorParams{StatusPageID, MonitorID uuid.UUID}) error`
  - `ListStatusPageMonitorIDs(ctx, statusPageID uuid.UUID) ([]uuid.UUID, error)`
  - `ListWorkspaceMonitorIDs(ctx, workspaceID uuid.UUID) ([]uuid.UUID, error)`
  - `ListPublicStatusMonitors(ctx, statusPageID uuid.UUID) ([]sqlc.ListPublicStatusMonitorsRow, error)` with `ID`, `Name`, `Paused`, `Status`
  - `ListProbeDayStats(ctx, sqlc.ListProbeDayStatsParams{MonitorIds []uuid.UUID, Since time.Time}) ([]sqlc.ListProbeDayStatsRow, error)` with `MonitorID`, `Day string`, `Checks`, `UpCount`, `DownCount`
  - `ListPublicActiveIncidents(ctx, monitorIds []uuid.UUID) ([]sqlc.ListPublicActiveIncidentsRow, error)` with `ID`, `MonitorID`, `MonitorName`, `StartedAt`
  - `ListPublicRecentIncidents(ctx, sqlc.ListPublicRecentIncidentsParams{MonitorIds []uuid.UUID, Since time.Time, PageLimit int32}) ([]sqlc.ListPublicRecentIncidentsRow, error)` with `ID`, `MonitorID`, `MonitorName`, `StartedAt`, `ResolvedAt`

- [ ] **Step 1: Write the failing schema test**

Append to `backend/internal/db/queries_test.go` (package `db_test`):

```go
func TestStatusPageSchema_SelectionAndCascade(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()

	userID := testutil.CreateVerifiedUser(t, pool, "status-schema@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1,'Status Schema','status-schema',$2)`, wsID, userID); err != nil {
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
	page, err := q.UpsertStatusPage(ctx, sqlc.UpsertStatusPageParams{
		WorkspaceID: wsID, Enabled: true, Title: "Status Schema", Description: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Enabled || page.Title != "Status Schema" {
		t.Fatalf("page = %+v", page)
	}
	if _, err := q.InsertStatusPageMonitor(ctx, sqlc.InsertStatusPageMonitorParams{
		StatusPageID: page.WorkspaceID, MonitorID: monitorID,
	}); err != nil {
		t.Fatal(err)
	}
	// Inserting the same selection twice is idempotent.
	if _, err := q.InsertStatusPageMonitor(ctx, sqlc.InsertStatusPageMonitorParams{
		StatusPageID: page.WorkspaceID, MonitorID: monitorID,
	}); err != nil {
		t.Fatalf("duplicate selection: %v", err)
	}
	ids, err := q.ListStatusPageMonitorIDs(ctx, page.WorkspaceID)
	if err != nil || len(ids) != 1 || ids[0] != monitorID {
		t.Fatalf("selection ids = %v, err = %v", ids, err)
	}

	// Deleting the monitor removes its selection.
	if _, err := pool.Exec(ctx, `DELETE FROM monitors WHERE id = $1`, monitorID); err != nil {
		t.Fatal(err)
	}
	if ids, err := q.ListStatusPageMonitorIDs(ctx, page.WorkspaceID); err != nil || len(ids) != 0 {
		t.Fatalf("selection after monitor delete = %v, err = %v", ids, err)
	}

	// Deleting the workspace removes the page.
	if _, err := pool.Exec(ctx, `DELETE FROM workspaces WHERE id = $1`, wsID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetStatusPage(ctx, wsID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("page after workspace delete error = %v, want pgx.ErrNoRows", err)
	}
}

func TestProbeDayStats_GroupsByUTCDate(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()

	userID := testutil.CreateVerifiedUser(t, pool, "status-days@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1,'Status Days','status-days',$2)`, wsID, userID); err != nil {
		t.Fatal(err)
	}
	monitorID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO monitors (id, workspace_id, name, url, method, created_by) VALUES ($1,$2,'API','https://example.com','GET',$3)`, monitorID, wsID, userID); err != nil {
		t.Fatal(err)
	}
	insert := func(at time.Time, status string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO probe_results (id, monitor_id, workspace_id, scheduled_for, checked_at, status, latency_ms)
			VALUES ($1,$2,$3,$4,$4,$5,100)`,
			uuid.New(), monitorID, wsID, at, status); err != nil {
			t.Fatal(err)
		}
	}
	// Use yesterday/today so the probe partitions created by migrations exist.
	now := time.Now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	insert(day.Add(23*time.Hour+59*time.Minute), "up")
	insert(day.Add(time.Hour), "down")
	insert(day.Add(25*time.Hour), "up") // next UTC day, 01:00

	rows, err := sqlc.New(pool).ListProbeDayStats(ctx, sqlc.ListProbeDayStatsParams{
		MonitorIds: []uuid.UUID{monitorID},
		Since:      day,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("day rows = %+v, want 2", rows)
	}
	if rows[0].Day != day.Format("2006-01-02") || rows[0].Checks != 2 || rows[0].UpCount != 1 || rows[0].DownCount != 1 {
		t.Fatalf("first day = %+v", rows[0])
	}
	next := day.AddDate(0, 0, 1)
	if rows[1].Day != next.Format("2006-01-02") || rows[1].Checks != 1 || rows[1].UpCount != 1 {
		t.Fatalf("second day = %+v", rows[1])
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/db -run 'TestStatusPageSchema|TestProbeDayStats' -v`
Expected: FAIL because the tables, migration, and generated queries do not exist.

- [ ] **Step 3: Add migration `00006_status_pages.sql`**

```sql
-- +goose Up
CREATE TABLE status_pages (
  workspace_id uuid PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
  enabled boolean NOT NULL DEFAULT false,
  title text NOT NULL CHECK (length(trim(title)) BETWEEN 1 AND 80),
  description text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE status_page_monitors (
  status_page_id uuid NOT NULL REFERENCES status_pages(workspace_id) ON DELETE CASCADE,
  monitor_id uuid NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (status_page_id, monitor_id)
);

CREATE INDEX status_page_monitors_monitor_idx
  ON status_page_monitors (monitor_id);

-- +goose Down
DROP TABLE status_page_monitors;
DROP TABLE status_pages;
```

- [ ] **Step 4: Add `status_pages.sql` queries**

Create `backend/internal/db/queries/status_pages.sql` with these queries. Use `sqlc.arg(...)` names exactly so generated params match the Interfaces block:

```sql
-- name: GetStatusPage :one
SELECT * FROM status_pages WHERE workspace_id = sqlc.arg('workspace_id')::uuid;

-- name: UpsertStatusPage :one
INSERT INTO status_pages (workspace_id, enabled, title, description)
VALUES (sqlc.arg('workspace_id')::uuid, sqlc.arg('enabled'), sqlc.arg('title'), sqlc.arg('description'))
ON CONFLICT (workspace_id) DO UPDATE
SET enabled = EXCLUDED.enabled,
    title = EXCLUDED.title,
    description = EXCLUDED.description,
    updated_at = now()
RETURNING *;

-- name: DeleteStatusPageMonitors :exec
DELETE FROM status_page_monitors
WHERE status_page_id = sqlc.arg('status_page_id')::uuid;

-- name: InsertStatusPageMonitor :exec
INSERT INTO status_page_monitors (status_page_id, monitor_id)
VALUES (sqlc.arg('status_page_id')::uuid, sqlc.arg('monitor_id')::uuid)
ON CONFLICT DO NOTHING;

-- name: ListStatusPageMonitorIDs :many
SELECT spm.monitor_id
FROM status_page_monitors spm
JOIN monitors m ON m.id = spm.monitor_id
WHERE spm.status_page_id = sqlc.arg('status_page_id')::uuid
ORDER BY m.created_at, m.id;

-- name: ListWorkspaceMonitorIDs :many
SELECT id FROM monitors
WHERE workspace_id = sqlc.arg('workspace_id')::uuid
ORDER BY created_at, id;

-- name: ListPublicStatusMonitors :many
SELECT m.id, m.name, m.paused, ms.status
FROM status_page_monitors spm
JOIN monitors m ON m.id = spm.monitor_id
JOIN monitor_state ms ON ms.monitor_id = m.id
WHERE spm.status_page_id = sqlc.arg('status_page_id')::uuid
ORDER BY m.created_at, m.id;

-- name: ListProbeDayStats :many
SELECT
  monitor_id,
  to_char((checked_at AT TIME ZONE 'UTC')::date, 'YYYY-MM-DD') AS day,
  count(*)::bigint AS checks,
  count(*) FILTER (WHERE status = 'up')::bigint AS up_count,
  count(*) FILTER (WHERE status = 'down')::bigint AS down_count
FROM probe_results
WHERE monitor_id = ANY(sqlc.arg('monitor_ids')::uuid[])
  AND checked_at >= sqlc.arg('since')::timestamptz
GROUP BY monitor_id, (checked_at AT TIME ZONE 'UTC')::date
ORDER BY monitor_id, day;

-- name: ListPublicActiveIncidents :many
SELECT i.id, i.monitor_id, m.name AS monitor_name, i.started_at
FROM incidents i
JOIN monitors m ON m.id = i.monitor_id
WHERE i.monitor_id = ANY(sqlc.arg('monitor_ids')::uuid[])
  AND i.resolved_at IS NULL
ORDER BY i.started_at DESC, i.id DESC;

-- name: ListPublicRecentIncidents :many
SELECT i.id, i.monitor_id, m.name AS monitor_name, i.started_at, i.resolved_at
FROM incidents i
JOIN monitors m ON m.id = i.monitor_id
WHERE i.monitor_id = ANY(sqlc.arg('monitor_ids')::uuid[])
  AND i.resolved_at >= sqlc.arg('since')::timestamptz
ORDER BY i.resolved_at DESC, i.id DESC
LIMIT sqlc.arg('page_limit')::int;
```

`ListProbeDayStats` must select the UTC date as a `YYYY-MM-DD` string so the generated Go field is `Day string`; do not add a `date` type override to `sqlc.yaml`.

- [ ] **Step 5: Regenerate sqlc**

Run: `cd backend && sqlc generate`
Expected: `sqlc` succeeds and the generated package contains the methods and row types listed in Interfaces.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/db -run 'TestStatusPageSchema|TestProbeDayStats' -v`
Expected: PASS.

- [ ] **Step 7: Quality gate and commit**

Run: `cd backend && gofmt -w internal/db && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/db
git commit -m "feat: add public status page schema and queries"
```

---

### Task 2: Status page types, errors, and TTL cache

**Files:**
- Create: `backend/internal/statuspage/types.go`
- Create: `backend/internal/statuspage/cache.go`
- Test: `backend/internal/statuspage/cache_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks except the design's response shape.
- Produces:
  - Sentinels `ErrNotFound = workspaces.ErrNotFound`, `ErrForbidden = workspaces.ErrForbidden`, `ErrValidation = workspaces.ErrValidation`, and `ErrSuspended = errors.New("workspace suspended")`.
  - `ValidationError{Fields map[string]string}` with `Error()` and `Is(workspaces.ErrValidation)`.
  - `Config{Enabled bool, Title, Description string, MonitorIDs []uuid.UUID}` with JSON tags `enabled`, `title`, `description`, `monitor_ids`.
  - `ConfigInput{Enabled bool, Title, Description string, MonitorIDs []uuid.UUID}` with the same JSON tags.
  - `PublicWorkspace{Name, Slug string}`, `PublicPageInfo{Title, Description string, UpdatedAt time.Time}`, `PublicDay{Date, Status string, Uptime *float64}`, `PublicMonitor{ID uuid.UUID, Name, Status string, Uptime *float64, Days []PublicDay}`, `PublicIncident{ID, MonitorID uuid.UUID, MonitorName, Status string, StartedAt time.Time, ResolvedAt *time.Time}`, `PublicIncidents{Active, Recent []PublicIncident}`, `PublicPage{Workspace PublicWorkspace, Page PublicPageInfo, Monitors []PublicMonitor, Incidents PublicIncidents}`.
  - `type pageCache struct` with `newPageCache(ttl time.Duration, now func() time.Time) *pageCache`, `get(slug string) (PublicPage, bool)`, `set(slug string, page PublicPage)`, `delete(slug string)`, and `len() int`.

- [ ] **Step 1: Write the failing cache test**

Create `backend/internal/statuspage/cache_test.go` in `package statuspage`:

```go
func TestPageCache_TTLDeleteAndBound(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	cache := newPageCache(15*time.Second, clock)

	cache.set("acme", PublicPage{Workspace: PublicWorkspace{Slug: "acme"}})
	got, ok := cache.get("acme")
	if !ok || got.Workspace.Slug != "acme" {
		t.Fatalf("cache hit = %+v, %v", got, ok)
	}

	now = now.Add(16 * time.Second)
	if _, ok := cache.get("acme"); ok {
		t.Fatal("expired entry still hit")
	}

	cache.set("acme", PublicPage{})
	cache.delete("acme")
	if _, ok := cache.get("acme"); ok {
		t.Fatal("deleted entry still hit")
	}

	for i := 0; i < 1100; i++ {
		cache.set(fmt.Sprintf("slug-%d", i), PublicPage{})
	}
	if got := cache.len(); got > 1024 {
		t.Fatalf("cache len = %d, want <= 1024", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/statuspage -run TestPageCache -v`
Expected: FAIL because the package, types, and cache do not exist.

- [ ] **Step 3: Implement `types.go`**

Pin:
- JSON tags exactly as in Interfaces. `MonitorIDs` is `uuid.UUID` slices; `PublicDay.Uptime` and `PublicMonitor.Uptime` are `*float64` so `no_data` serializes as `null`.
- `ValidationError.Error()` renders fields in sorted order, mirroring `alerts.ValidationError`.
- `ValidationError.Is(target)` returns `target == workspaces.ErrValidation`.

- [ ] **Step 4: Implement `cache.go`**

Pin:
- Mutex-guarded `map[string]pageCacheEntry`, where the entry stores the page, insertion time, and a monotonic sequence.
- `get` returns false and deletes the entry when `now.Sub(insertedAt) >= ttl`.
- `set` overwrites an existing slug.
- The bound is 1024 entries: when exceeded, first remove expired entries; if still over, remove the entries with the smallest insertion sequence until at or below the bound.
- `len()` is used only by tests.

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd backend && go test ./internal/statuspage -run TestPageCache -v`
Expected: PASS.

- [ ] **Step 6: Quality gate and commit**

Run: `cd backend && gofmt -w internal/statuspage && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/statuspage
git commit -m "feat: add status page types and response cache"
```

---

### Task 3: Admin configuration service

**Files:**
- Create: `backend/internal/statuspage/service.go`
- Test: `backend/internal/statuspage/service_test.go`

**Interfaces:**
- Consumes: Task 1 queries, Task 2 types/errors/cache, `workspaces.Service.Authorize`, `sqlc.LockWorkspace`.
- Produces:
  - `Service` and `NewService(pool *pgxpool.Pool, q *sqlc.Queries, ws *workspaces.Service) *Service`.
  - `GetConfig(ctx context.Context, actorID, workspaceID uuid.UUID) (Config, error)`.
  - `UpdateConfig(ctx context.Context, actorID, workspaceID uuid.UUID, in ConfigInput) (Config, error)`.

- [ ] **Step 1: Write the failing service tests**

Create `backend/internal/statuspage/service_test.go` in `package statuspage` with a shared env:

```go
const serviceTestPassword = "correct horse battery staple"

type serviceEnv struct {
	t      *testing.T
	pool   *pgxpool.Pool
	ws     *workspaces.Service
	svc    *Service
	wsID   uuid.UUID
	slug   string
	owner  uuid.UUID
	admin  uuid.UUID
	member uuid.UUID
	viewer uuid.UUID
}

func newServiceEnv(t *testing.T) *serviceEnv {
	t.Helper()
	pool, cleanup := testutil.NewPostgres(t)
	t.Cleanup(cleanup)
	wsSvc := workspaces.NewService(pool, sqlc.New(pool))
	owner := testutil.CreateVerifiedUser(t, pool, "status-svc-owner@example.com", serviceTestPassword)
	ws, err := wsSvc.Create(context.Background(), owner, "Status Service")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	env := &serviceEnv{t: t, pool: pool, ws: wsSvc, svc: NewService(pool, sqlc.New(pool), wsSvc), wsID: ws.ID, slug: ws.Slug, owner: owner}
	env.admin = env.addMember("status-svc-admin@example.com", "admin")
	env.member = env.addMember("status-svc-member@example.com", "member")
	env.viewer = env.addMember("status-svc-viewer@example.com", "viewer")
	// Disable caching by default so tests observe DB changes immediately. The
	// dedicated cache test installs its own 15-second cache with a fake clock.
	env.svc.cache = newPageCache(-1*time.Second, time.Now)
	return env
}

func (e *serviceEnv) addMember(emailAddr, role string) uuid.UUID {
	e.t.Helper()
	id := testutil.CreateVerifiedUser(e.t, e.pool, emailAddr, serviceTestPassword)
	if err := sqlc.New(e.pool).UpsertWorkspaceMember(context.Background(), sqlc.UpsertWorkspaceMemberParams{
		WorkspaceID: e.wsID, UserID: id, Role: role,
	}); err != nil {
		e.t.Fatalf("add member: %v", err)
	}
	return id
}

func (e *serviceEnv) addMonitor(name string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO monitors (id, workspace_id, name, url, method, created_by) VALUES ($1,$2,$3,'https://secret.example.com/path?token=abc','GET',$4)`, id, e.wsID, name, e.owner); err != nil {
		e.t.Fatalf("insert monitor: %v", err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO monitor_state (monitor_id) VALUES ($1)`, id); err != nil {
		e.t.Fatalf("insert monitor state: %v", err)
	}
	return id
}
```

Add these tests:

```go
func TestService_GetConfigDefaults(t *testing.T) {
	env := newServiceEnv(t)
	cfg, err := env.svc.GetConfig(context.Background(), env.owner, env.wsID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || cfg.Title != "Status Service" || cfg.Description != "" || cfg.MonitorIDs == nil || len(cfg.MonitorIDs) != 0 {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestService_UpdateConfigValidation(t *testing.T) {
	env := newServiceEnv(t)
	otherOwner := testutil.CreateVerifiedUser(t, env.pool, "status-svc-other@example.com", serviceTestPassword)
	other, err := env.ws.Create(context.Background(), otherOwner, "Other")
	if err != nil {
		t.Fatal(err)
	}
	otherMonitor := uuid.New()
	if _, err := env.pool.Exec(context.Background(), `INSERT INTO monitors (id, workspace_id, name, url, method, created_by) VALUES ($1,$2,'Other','https://example.com','GET',$3)`, otherMonitor, other.ID, otherOwner); err != nil {
		t.Fatal(err)
	}
	monitorID := env.addMonitor("API")

	cases := []struct {
		name  string
		in    ConfigInput
		field string
	}{
		{"blank title", ConfigInput{Title: "  ", MonitorIDs: []uuid.UUID{monitorID}}, "title"},
		{"long title", ConfigInput{Title: strings.Repeat("a", 81), MonitorIDs: []uuid.UUID{monitorID}}, "title"},
		{"long description", ConfigInput{Title: "Acme", Description: strings.Repeat("a", 501), MonitorIDs: []uuid.UUID{monitorID}}, "description"},
		{"enabled without monitors", ConfigInput{Enabled: true, Title: "Acme"}, "monitor_ids"},
		{"foreign monitor", ConfigInput{Title: "Acme", MonitorIDs: []uuid.UUID{otherMonitor}}, "monitor_ids"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.svc.UpdateConfig(context.Background(), env.owner, env.wsID, tc.in)
			var validation *ValidationError
			if !errors.As(err, &validation) || validation.Fields[tc.field] == "" {
				t.Fatalf("error = %v, want field %q", err, tc.field)
			}
		})
	}
}

func TestService_UpdateConfigReplacesSelection(t *testing.T) {
	env := newServiceEnv(t)
	first := env.addMonitor("API")
	second := env.addMonitor("Web")

	cfg, err := env.svc.UpdateConfig(context.Background(), env.owner, env.wsID, ConfigInput{
		Enabled: true, Title: "  Acme status  ", Description: "Live availability",
		MonitorIDs: []uuid.UUID{second, first, second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Title != "Acme status" || !cfg.Enabled {
		t.Fatalf("cfg = %+v", cfg)
	}
	if len(cfg.MonitorIDs) != 2 || cfg.MonitorIDs[0] != first || cfg.MonitorIDs[1] != second {
		t.Fatalf("monitor ids = %v, want creation order [first second]", cfg.MonitorIDs)
	}
	ids, err := sqlc.New(env.pool).ListStatusPageMonitorIDs(context.Background(), env.wsID)
	if err != nil || len(ids) != 2 {
		t.Fatalf("stored selection = %v, err = %v", ids, err)
	}

	cfg, err = env.svc.UpdateConfig(context.Background(), env.owner, env.wsID, ConfigInput{
		Enabled: true, Title: "Acme status", MonitorIDs: []uuid.UUID{second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MonitorIDs) != 1 || cfg.MonitorIDs[0] != second {
		t.Fatalf("replaced selection = %v", cfg.MonitorIDs)
	}
}
```

Add `TestService_UpdateConfigRolesAndSuspension`:
- member and viewer → `ErrForbidden`; admin and owner succeed.
- set `workspaces.suspended_at = now()` and assert `UpdateConfig` → `ErrSuspended`, while `GetConfig` still returns the saved config.
- another workspace's ID → `ErrNotFound`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/statuspage -run 'TestService_GetConfig|TestService_UpdateConfig' -v`
Expected: FAIL because `Service` and its methods do not exist.

- [ ] **Step 3: Implement `service.go` admin methods**

Pin:
- `NewService` sets `now = time.Now` and `cache = newPageCache(15*time.Second, func() time.Time { return s.now() })` so a test that advances `s.now` also expires cache entries.
- `GetConfig` authorizes `workspaces.RoleAdmin`, loads the workspace with `q.GetWorkspaceByID` (map `pgx.ErrNoRows` to `ErrNotFound`), loads the page row, and returns defaults with `Title = ws.Name`, `MonitorIDs` as a non-nil empty slice, and the stored/empty values otherwise. Load selection IDs with `ListStatusPageMonitorIDs` when the row exists.
- `UpdateConfig` authorizes admin, validates `MonitorIDs` against `ListWorkspaceMonitorIDs`, begins a transaction, calls `qtx.LockWorkspace` and maps `SuspendedAt != nil` to `ErrSuspended`, validates the title/description on trimmed values, then upserts the page, deletes and reinserts the selection in workspace monitor creation order, and commits.
- Duplicate input IDs are deduped. The returned order is workspace monitor creation order, not request order.
- `ErrNoRows` from workspace or page lookups maps to `ErrNotFound`.
- Cache invalidation happens after commit using the workspace slug loaded before the transaction.
- Field errors never echo monitor IDs or other raw values; messages are fixed strings such as `monitor is not in this workspace`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/statuspage -run 'TestService_GetConfig|TestService_UpdateConfig' -v`
Expected: PASS.

- [ ] **Step 5: Quality gate and commit**

Run: `cd backend && gofmt -w internal/statuspage && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/statuspage
git commit -m "feat: add status page configuration service"
```

---

### Task 4: Public page aggregation, incidents, and cache use

**Files:**
- Modify: `backend/internal/statuspage/service.go`
- Test: `backend/internal/statuspage/service_test.go`

**Interfaces:**
- Consumes: Task 1 day/incident queries, Task 2 public types and cache, Task 3 `Service`.
- Produces:
  - `PublicPage(ctx context.Context, slug string) (PublicPage, error)`.
  - Unexported clock field `now func() time.Time` used by tests, and helpers `startOfUTCDay`, `buildDays`, `monitorStatus`, `buildUptime`.

- [ ] **Step 1: Write the failing public tests**

Append to `service_test.go` (add `fmt` to the imports for the partition helper):

```go
func (e *serviceEnv) insertProbe(t *testing.T, monitorID uuid.UUID, at time.Time, status string) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO probe_results (id, monitor_id, workspace_id, scheduled_for, checked_at, status, latency_ms)
		VALUES ($1,$2,$3,$4,$4,$5,100)`,
		uuid.New(), monitorID, e.wsID, at, status); err != nil {
		t.Fatalf("insert probe: %v", err)
	}
}

func (e *serviceEnv) enablePage(t *testing.T, monitors ...uuid.UUID) {
	t.Helper()
	if _, err := e.svc.UpdateConfig(context.Background(), e.owner, e.wsID, ConfigInput{
		Enabled: true, Title: "Acme status", Description: "Live availability", MonitorIDs: monitors,
	}); err != nil {
		t.Fatalf("enable page: %v", err)
	}
}

func (e *serviceEnv) ensureProbePartition(t *testing.T, day time.Time) {
	t.Helper()
	month := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	stmt := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s PARTITION OF probe_results FOR VALUES FROM ('%s') TO ('%s')",
		"probe_results_"+month.Format("2006_01"),
		month.Format(time.RFC3339),
		month.AddDate(0, 1, 0).Format(time.RFC3339),
	)
	if _, err := e.pool.Exec(context.Background(), stmt); err != nil {
		t.Fatalf("create probe partition: %v", err)
	}
}

func TestService_PublicPageBuckets(t *testing.T) {
	env := newServiceEnv(t)
	now := time.Now().UTC().Truncate(time.Second)
	env.svc.now = func() time.Time { return now }
	monitorID := env.addMonitor("API")
	env.enablePage(t, monitorID)

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	old := today.Add(-100 * 24 * time.Hour)
	env.ensureProbePartition(t, old) // the month is outside the maintained window
	env.insertProbe(t, monitorID, today.Add(time.Minute), "up")
	env.insertProbe(t, monitorID, today.Add(2*time.Minute), "down") // partial today
	env.insertProbe(t, monitorID, today.Add(-25*time.Hour), "up")  // yesterday
	env.insertProbe(t, monitorID, today.Add(-48*time.Hour), "down")
	env.insertProbe(t, monitorID, old, "down") // outside 90 days

	page, err := env.svc.PublicPage(context.Background(), env.slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Monitors) != 1 {
		t.Fatalf("monitors = %+v", page.Monitors)
	}
	days := page.Monitors[0].Days
	if len(days) != 90 {
		t.Fatalf("days = %d, want 90", len(days))
	}
	if days[89].Date != today.Format("2006-01-02") || days[89].Status != "partial" {
		t.Fatalf("today = %+v", days[89])
	}
	if days[88].Date != today.AddDate(0, 0, -1).Format("2006-01-02") || days[88].Status != "up" {
		t.Fatalf("yesterday = %+v", days[88])
	}
	if days[87].Status != "down" {
		t.Fatalf("two days ago = %+v", days[87])
	}
	if days[86].Status != "no_data" || days[86].Uptime != nil {
		t.Fatalf("empty day = %+v", days[86])
	}
	if page.Monitors[0].Uptime == nil || *page.Monitors[0].Uptime != 0.5 {
		t.Fatalf("uptime = %v, want 0.5", page.Monitors[0].Uptime)
	}
}

func TestService_PublicPageStatusesAndEmptySelection(t *testing.T) {
	env := newServiceEnv(t)
	monitorID := env.addMonitor("API")
	env.enablePage(t, monitorID)

	page, err := env.svc.PublicPage(context.Background(), env.slug)
	if err != nil {
		t.Fatal(err)
	}
	if page.Monitors[0].Status != "unknown" {
		t.Fatalf("unknown status = %q", page.Monitors[0].Status)
	}
	if page.Page.UpdatedAt.IsZero() {
		t.Fatal("page updated_at is zero")
	}
	if _, err := env.pool.Exec(context.Background(), `UPDATE workspaces SET suspended_at=now() WHERE id=$1`, env.wsID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.PublicPage(context.Background(), env.slug); err != nil {
		t.Fatalf("public read while suspended: %v", err)
	}
	if _, err := env.pool.Exec(context.Background(), `UPDATE monitor_state SET status='down' WHERE monitor_id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(context.Background(), `UPDATE monitors SET paused=true WHERE id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}
	page, err = env.svc.PublicPage(context.Background(), env.slug)
	if err != nil {
		t.Fatal(err)
	}
	if page.Monitors[0].Status != "paused" {
		t.Fatalf("paused status = %q", page.Monitors[0].Status)
	}

	// Removing the last monitor leaves an enabled page with an empty list.
	if _, err := env.pool.Exec(context.Background(), `DELETE FROM monitors WHERE id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}
	page, err = env.svc.PublicPage(context.Background(), env.slug)
	if err != nil {
		t.Fatalf("empty page: %v", err)
	}
	if len(page.Monitors) != 0 || page.Incidents.Active == nil || page.Incidents.Recent == nil {
		t.Fatalf("empty page = %+v", page)
	}
}
```

Add `TestService_PublicPageIncidents`:
- Insert one selected and one unselected monitor.
- Insert an open incident, an acknowledged incident (set `status='acknowledged'`, `acknowledged_at`, `acknowledged_by`), a resolved incident 2 days ago, and a resolved incident 10 days ago; add one open incident for the unselected monitor.
- Assert active has two entries, both `Status == "ongoing"`, newest first; recent has only the 2-day-old incident with `Status == "resolved"`; the unselected monitor's incident never appears.
- Insert 25 resolved incidents inside the window and assert the recent list is capped at 20.

Add `TestService_PublicPageNotFoundAndCache`:

```go
func TestService_PublicPageNotFoundAndCache(t *testing.T) {
	env := newServiceEnv(t)
	now := time.Now().UTC().Truncate(time.Second)
	env.svc.now = func() time.Time { return now }
	env.svc.cache = newPageCache(15*time.Second, func() time.Time { return now })

	ctx := context.Background()
	if _, err := env.svc.PublicPage(ctx, "missing-slug"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing slug error = %v", err)
	}
	monitorID := env.addMonitor("API")
	if _, err := env.svc.PublicPage(ctx, env.slug); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unconfigured page error = %v", err)
	}
	env.enablePage(t, monitorID)

	today := startOfUTCDay(now)
	env.insertProbe(t, monitorID, today.Add(time.Minute), "up")
	first, err := env.svc.PublicPage(ctx, env.slug)
	if err != nil || first.Monitors[0].Uptime == nil || *first.Monitors[0].Uptime != 1 {
		t.Fatalf("first page = %+v, err = %v", first, err)
	}

	env.insertProbe(t, monitorID, today.Add(2*time.Minute), "down")
	second, err := env.svc.PublicPage(ctx, env.slug)
	if err != nil || second.Monitors[0].Uptime == nil || *second.Monitors[0].Uptime != 1 {
		t.Fatalf("cache hit page = %+v, err = %v", second, err)
	}

	now = now.Add(16 * time.Second)
	third, err := env.svc.PublicPage(ctx, env.slug)
	if err != nil || third.Monitors[0].Uptime == nil || *third.Monitors[0].Uptime != 0.5 {
		t.Fatalf("expired cache page = %+v, err = %v", third, err)
	}

	if _, err := env.svc.UpdateConfig(ctx, env.owner, env.wsID, ConfigInput{
		Enabled: true, Title: "Renamed", MonitorIDs: []uuid.UUID{monitorID},
	}); err != nil {
		t.Fatal(err)
	}
	fourth, err := env.svc.PublicPage(ctx, env.slug)
	if err != nil || fourth.Page.Title != "Renamed" {
		t.Fatalf("invalidated page = %+v, err = %v", fourth, err)
	}
}
```

Add `TestService_PublicPagePrivacy`:
- Monitor name `API`, URL `https://secret.example.com/path?token=abc`, monitor_state error `token abc`, and an incident with `acknowledged_by` set.
- Marshal the `PublicPage` to JSON and assert the string does not contain `secret.example.com`, `token=abc`, `acknowledged_by`, `acknowledged_at`, `delivery`, `headers`, `body`, or the workspace UUID.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/statuspage -run 'TestService_PublicPage' -v`
Expected: FAIL because `PublicPage` does not exist.

- [ ] **Step 3: Implement `PublicPage` and helpers**

Pin:
- Check the cache first; on a hit return the stored value.
- `GetWorkspaceBySlug`; `pgx.ErrNoRows` → `ErrNotFound`.
- `GetStatusPage`; `pgx.ErrNoRows` or `!Enabled` → `ErrNotFound`.
- `ListPublicStatusMonitors`. With zero monitors, return the page with empty `Monitors` and empty (non-nil) `Active`/`Recent` slices without running the day or incident queries.
- `since := startOfUTCDay(now()) - 89*24*time.Hour`; call `ListProbeDayStats` with all selected monitor IDs.
- `buildDays(stats, since, now)` walks 90 days, keyed by `YYYY-MM-DD`, and sets `Status` to `no_data`, `up`, `down`, or `partial` and `Uptime` to `up/(up+down)` or `nil`.
- `buildUptime` sums checks/up across the 90 buckets; `nil` when no checks.
- `monitorStatus(paused bool, state string) string` returns `paused` when paused, otherwise the state.
- Incidents: `ListPublicActiveIncidents` mapped to `ongoing` with `ResolvedAt = nil`; `ListPublicRecentIncidents` with `Since = now - 7*24h` and `PageLimit = 20`, mapped to `resolved` with the stored `resolved_at`.
- Cache only successful payloads, after assembly.
- Do not log or return raw monitor fields.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/statuspage -run 'TestService_PublicPage' -v`
Expected: PASS.

- [ ] **Step 5: Run the whole package**

Run: `cd backend && go test ./internal/statuspage -v`
Expected: PASS for cache, config, and public tests.

- [ ] **Step 6: Quality gate and commit**

Run: `cd backend && gofmt -w internal/statuspage && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/statuspage
git commit -m "feat: assemble public status page data"
```

---

### Task 5: Public and admin handlers, router wiring

**Files:**
- Create: `backend/internal/statuspage/handlers.go`
- Create: `backend/internal/statuspage/handlers_test.go`
- Modify: `backend/internal/api/router.go`

**Interfaces:**
- Consumes: Task 3/4 `Service`.
- Produces:
  - `GetConfigHandler(svc *Service) http.HandlerFunc`
  - `UpdateConfigHandler(svc *Service) http.HandlerFunc`
  - `PublicHandler(svc *Service) http.HandlerFunc`
  - `api.Dependencies.StatusPageService *statuspage.Service`

- [ ] **Step 1: Write the failing handler tests**

Create `backend/internal/statuspage/handlers_test.go` in `package statuspage_test` (importing `api` from `package statuspage` would create an import cycle). Model the env on `alerts.newAlertsAPIEnv`:

```go
const handlerPassword = "correct horse battery staple"

type handlerEnv struct {
	t      *testing.T
	pool   *pgxpool.Pool
	cfg    config.Config
	wsSvc  *workspaces.Service
	svc    *statuspage.Service
	server *httptest.Server
	wsID   uuid.UUID
	slug   string
	owner  uuid.UUID
	admin  uuid.UUID
	member uuid.UUID
	viewer uuid.UUID
}

func newHandlerEnv(t *testing.T) *handlerEnv {
	t.Helper()
	pool, cleanup := testutil.NewPostgres(t)
	t.Cleanup(cleanup)
	cfg := testutil.TestConfig(t)
	wsSvc := workspaces.NewService(pool, sqlc.New(pool))
	svc := statuspage.NewService(pool, sqlc.New(pool), wsSvc)
	owner := testutil.CreateVerifiedUser(t, pool, "status-handler-owner@example.com", handlerPassword)
	ws, err := wsSvc.Create(context.Background(), owner, "Status API")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	env := &handlerEnv{t: t, pool: pool, cfg: cfg, wsSvc: wsSvc, svc: svc, wsID: ws.ID, slug: ws.Slug, owner: owner}
	env.admin = env.addMember("status-handler-admin@example.com", "admin")
	env.member = env.addMember("status-handler-member@example.com", "member")
	env.viewer = env.addMember("status-handler-viewer@example.com", "viewer")
	router := api.NewRouter(api.Dependencies{
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Pool:              pool,
		Config:            cfg,
		EmailSender:       testutil.NewFakeSender(),
		WorkspaceService:  wsSvc,
		StatusPageService: svc,
	})
	env.server = httptest.NewServer(router)
	t.Cleanup(env.server.Close)
	return env
}
```

Include `addMember`, `client`, and `do` helpers copied from `alerts/handlers_test.go`. Add a helper `addMonitor(name string) uuid.UUID` that inserts a monitor and `monitor_state`.

Tests:

```go
func TestStatusPageHandlers_Public(t *testing.T) {
	env := newHandlerEnv(t)
	monitorID := env.addMonitor("API")
	if _, err := env.svc.UpdateConfig(context.Background(), env.owner, env.wsID, statuspage.ConfigInput{
		Enabled: true, Title: "Acme status", Description: "Live availability", MonitorIDs: []uuid.UUID{monitorID},
	}); err != nil {
		t.Fatal(err)
	}

	resp, body := env.do(&http.Client{}, http.MethodGet, "/api/v1/public/status/"+env.slug)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "max-age=15") {
		t.Fatalf("Cache-Control = %q", got)
	}
	var page statuspage.PublicPage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if page.Page.Title != "Acme status" || len(page.Monitors) != 1 || len(page.Monitors[0].Days) != 90 {
		t.Fatalf("page = %+v", page)
	}

	resp, body = env.do(&http.Client{}, http.MethodGet, "/api/v1/public/status/missing-slug")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("404 Cache-Control = %q", got)
	}
	if !strings.Contains(string(body), `"code":"not_found"`) {
		t.Fatalf("404 body = %s", body)
	}
}
```

Add:
- `TestStatusPageHandlers_AdminCRUD`: unauthenticated GET → 401; owner GET → 200 defaults; owner PUT → 200; GET reflects; admin PUT works; member/viewer GET/PUT → 403; another workspace's config → 404; suspended workspace PUT → 403 `workspace_suspended`.
- `TestStatusPageHandlers_Validation`: bad title, enabled with no monitors, foreign monitor → 400 with `fields` keys `title`/`monitor_ids`.
- `TestStatusPageHandlers_DisabledIs404`: save a disabled page, assert public 404 with `no-store`; then enable it and assert the next public request is 200 (cache did not retain the 404).
- `TestStatusPageHandlers_RateLimit`: enable a page, fire 21 requests from one client, assert the last response is 429 with code `rate_limited`.
- `TestStatusPageHandlers_NoSecrets`: insert a monitor with a secret URL and error, call public, and assert the body does not contain the URL, `token=`, `acknowledged_by`, or the workspace UUID.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/statuspage -run 'TestStatusPageHandlers' -v`
Expected: FAIL because the handlers and routes do not exist.

- [ ] **Step 3: Implement handlers**

Pin:
- Missing service → 500 `internal_error` without panicking (mirror `alertServiceAvailable`).
- Admin handlers read the principal from `auth.PrincipalFrom`, parse `workspaceID`, decode bodies with `json.Decoder.DisallowUnknownFields`, and map errors exactly: `*ValidationError` → 400 `validation_error` with `fields`; `ErrSuspended` → 403 `workspace_suspended`; `ErrNotFound` → 404 `not_found`; `ErrForbidden` → 403 `forbidden`; unknown → 500.
- `GetConfigHandler` writes the config with 200. `UpdateConfigHandler` writes the saved config with 200.
- `PublicHandler` reads `r.PathValue("slug")`, trims it, and returns 400 `validation_error` for an empty slug. On success set `Cache-Control: public, max-age=15, stale-while-revalidate=15` and write the page with 200. On `ErrNotFound` set `Cache-Control: no-store` and write 404 `not_found`; other errors return 500.
- Use `httpx.WriteJSON`, `httpx.WriteProblem`, and the existing problem codes.

- [ ] **Step 4: Wire the router**

In `backend/internal/api/router.go`:
- Import `o1/internal/statuspage`.
- Add `StatusPageService *statuspage.Service` to `Dependencies`.
- Build it when nil and `deps.Pool != nil` with `statuspage.NewService(deps.Pool, sqlc.New(deps.Pool), wsSvc)`.
- Register, next to the workspace routes:
  - `GET /api/v1/workspaces/{workspaceID}/status-page`
  - `PUT /api/v1/workspaces/{workspaceID}/status-page`
  - both wrapped in `requireSession`.
- Register the public route **outside** `requireSession`:
  - `GET /api/v1/public/status/{slug}`
  - wrapped in `httpx.RateLimit(1, 20, true)`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/statuspage -run 'TestStatusPageHandlers' -v`
Expected: PASS.

Also run the existing handler suites to confirm the route chain still works:

Run: `cd backend && go test ./internal/alerts ./internal/incidents -run 'TestAlertChannelHandlers|TestIncidentHandlers' -v`
Expected: PASS.

- [ ] **Step 6: Quality gate and commit**

Run: `cd backend && gofmt -w internal/statuspage internal/api && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/statuspage backend/internal/api/router.go
git commit -m "feat: expose public and admin status page APIs"
```

---

### Task 6: Dashboard types, query keys, API, and hooks

**Files:**
- Modify: `web/src/lib/api/types.ts`
- Modify: `web/src/lib/query/keys.ts`
- Create: `web/src/features/status-pages/api.ts`
- Create: `web/src/features/status-pages/api.test.ts`
- Create: `web/src/features/status-pages/hooks.ts`
- Create: `web/src/features/status-pages/hooks.test.tsx`

**Interfaces:**
- Consumes: Task 5 API paths and shapes.
- Produces:
  - Types `StatusPageConfig`, `StatusPageConfigInput`, `PublicStatusDayStatus`, `PublicMonitorStatus`, `PublicStatusDay`, `PublicStatusMonitor`, `PublicStatusIncident`, `PublicStatusPage` exactly as in the spec §8.1.
  - `getStatusPage(workspaceId)`, `updateStatusPage(workspaceId, input)`, `getPublicStatus(slug)`.
  - `useStatusPage(workspaceId)`, `useUpdateStatusPage(workspaceId)`, `usePublicStatus(slug)`.
  - Query keys `statusPage(workspaceId)` and `publicStatus(slug)`.

- [ ] **Step 1: Write the failing API tests**

Create `web/src/features/status-pages/api.test.ts`:

```ts
import { describe, expect, test } from 'vitest';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { getPublicStatus, getStatusPage, updateStatusPage } from './api';

const ORIGIN = 'http://localhost:5173';
const configPath = `${ORIGIN}/api/v1/workspaces/w1/status-page`;

describe('status page API', () => {
	test('getStatusPage reads the workspace config', async () => {
		server.use(
			http.get(configPath, () =>
				HttpResponse.json({
					enabled: false,
					title: 'Acme',
					description: '',
					monitor_ids: [],
				}),
			),
		);

		await expect(getStatusPage('w1')).resolves.toEqual({
			enabled: false,
			title: 'Acme',
			description: '',
			monitor_ids: [],
		});
	});

	test('updateStatusPage puts the exact config body', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.put(configPath, async ({ request }) => {
				bodies.push(await request.json());
				return HttpResponse.json({
					enabled: true,
					title: 'Acme status',
					description: 'Live',
					monitor_ids: ['m1'],
				});
			}),
		);

		await updateStatusPage('w1', {
			enabled: true,
			title: 'Acme status',
			description: 'Live',
			monitor_ids: ['m1'],
		});

		expect(bodies).toEqual([
			{
				enabled: true,
				title: 'Acme status',
				description: 'Live',
				monitor_ids: ['m1'],
			},
		]);
	});

	test('getPublicStatus reads the public endpoint', async () => {
		server.use(
			http.get(`${ORIGIN}/api/v1/public/status/acme`, () =>
				HttpResponse.json({
					workspace: { name: 'Acme', slug: 'acme' },
					page: {
						title: 'Acme status',
						description: 'Live',
						updated_at: '2026-11-08T09:00:00Z',
					},
					monitors: [],
					incidents: { active: [], recent: [] },
				}),
			),
		);

		const page = await getPublicStatus('acme');
		expect(page.workspace.slug).toBe('acme');
		expect(page.monitors).toEqual([]);
	});
});
```

- [ ] **Step 2: Write the failing hook tests**

Create `web/src/features/status-pages/hooks.test.tsx` with a `wrapper` helper (copy from `alerts/hooks.test.tsx`) and:

```ts
test('useStatusPage uses the statusPage key', async () => {
	server.use(http.get(configPath, () => HttpResponse.json({ enabled: false, title: 'Acme', description: '', monitor_ids: [] })));
	const client = createTestQueryClient();
	const { result } = renderHook(() => useStatusPage('w1'), { wrapper: wrapper(client) });
	await waitFor(() => expect(result.current.isSuccess).toBe(true));
	expect(client.getQueryCache().findAll({ queryKey: queryKeys.statusPage('w1') })).toHaveLength(1);
});

test('the update mutation invalidates config and public caches', async () => {
	server.use(http.put(configPath, () => HttpResponse.json({ enabled: true, title: 'Acme', description: '', monitor_ids: ['m1'] })));
	const client = createTestQueryClient();
	client.setQueryData(queryKeys.statusPage('w1'), { enabled: false, title: 'Acme', description: '', monitor_ids: [] });
	client.setQueryData(queryKeys.publicStatus('acme'), { workspace: { name: 'Acme', slug: 'acme' } });

	const { result } = renderHook(() => useUpdateStatusPage('w1'), { wrapper: wrapper(client) });
	await act(async () => {
		await result.current.mutateAsync({ enabled: true, title: 'Acme', description: '', monitor_ids: ['m1'] });
	});

	expect(client.getQueryState(queryKeys.statusPage('w1'))?.isInvalidated).toBe(true);
	expect(client.getQueryState(queryKeys.publicStatus('acme'))?.isInvalidated).toBe(true);
});

test('usePublicStatus does not retry a 404', async () => {
	let calls = 0;
	server.use(http.get(`${ORIGIN}/api/v1/public/status/missing`, () => {
		calls += 1;
		return HttpResponse.json(
			{ type: 'about:blank', title: 'Not Found', status: 404, code: 'not_found' },
			{ status: 404, headers: { 'Content-Type': 'application/problem+json' } },
		);
	}));
	const client = createTestQueryClient();
	const { result } = renderHook(() => usePublicStatus('missing'), { wrapper: wrapper(client) });
	await waitFor(() => expect(result.current.isError).toBe(true));
	expect(calls).toBe(1);
});
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/features/status-pages`
Expected: FAIL because the types, functions, keys, and hooks do not exist.

- [ ] **Step 4: Implement types, keys, API, and hooks**

Pin:
- Add the spec §8.1 types verbatim to `types.ts`.
- Add to `queryKeys`:
  ```ts
  statusPage: (workspaceId: string) => ['status-page', workspaceId] as const,
  publicStatus: (slug: string) => ['public-status', slug] as const,
  ```
- `api.ts`: `getStatusPage` GETs `/workspaces/${workspaceId}/status-page`; `updateStatusPage` PUTs the same path with the input; `getPublicStatus` GETs `/public/status/${encodeURIComponent(slug)}`.
- `hooks.ts`:
  - `useStatusPage`: `queryKey: queryKeys.statusPage(workspaceId)`, `staleTime: 30_000`.
  - `useUpdateStatusPage`: mutation calling `updateStatusPage`; on success invalidate `queryKeys.statusPage(workspaceId)` and the prefix `['public-status']`.
  - `usePublicStatus`: `queryKey: queryKeys.publicStatus(slug)`, `retry: false`, `staleTime: 15_000`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd web && npm test -- --run src/features/status-pages`
Expected: PASS.

- [ ] **Step 6: Quality gate and commit**

Run: `cd web && npm run format && npm run lint && npm run typecheck && npm test -- --run`
Commit:

```bash
git add web/src/lib web/src/features/status-pages
git commit -m "feat: add status page dashboard types and hooks"
```

---

### Task 7: Public status page screen and route

**Files:**
- Create: `web/src/features/status-pages/PublicStatusScreen.tsx`
- Create: `web/src/features/status-pages/PublicStatusScreen.test.tsx`
- Modify: `web/src/router.tsx`

**Interfaces:**
- Consumes: Task 6 types and `usePublicStatus`.
- Produces:
  - `PublicStatusScreen({ slug }: { slug: string })`.
  - Exported helpers `formatUptime(value: number | null): string`, `overallBanner(monitors: PublicStatusMonitor[]): { tone: 'ok' | 'issue' | 'paused' | 'none'; text: string }`, and `incidentDuration(incident: PublicStatusIncident): string`.
  - Route `/status/$slug` directly under the root route.

- [ ] **Step 1: Write the failing screen tests**

Create `web/src/features/status-pages/PublicStatusScreen.test.tsx`:

```tsx
import { describe, expect, test, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type { PublicStatusPage, PublicStatusMonitor } from '@/lib/api/types';
import { PublicStatusScreen, overallBanner } from './PublicStatusScreen';

const ORIGIN = 'http://localhost:5173';

function days(downIndexes: number[] = []) {
	return Array.from({ length: 90 }, (_, index) => ({
		date: new Date(Date.UTC(2026, 7, 11 + index)).toISOString().slice(0, 10),
		status: downIndexes.includes(index) ? ('down' as const) : ('up' as const),
		uptime: downIndexes.includes(index) ? 0 : 1,
	}));
}

function monitor(overrides: Partial<PublicStatusMonitor> = {}): PublicStatusMonitor {
	return {
		id: 'm1',
		name: 'API',
		status: 'up',
		uptime: 0.999,
		days: days(),
		...overrides,
	};
}

function page(overrides: Partial<PublicStatusPage> = {}): PublicStatusPage {
	return {
		workspace: { name: 'Acme', slug: 'acme' },
		page: { title: 'Acme status', description: 'Live availability', updated_at: '2026-11-08T09:00:00Z' },
		monitors: [monitor()],
		incidents: { active: [], recent: [] },
		...overrides,
	};
}

function serve(payload: PublicStatusPage) {
	server.use(http.get(`${ORIGIN}/api/v1/public/status/acme`, () => HttpResponse.json(payload)));
}

test('renders the page, monitor, 90 bars, and uptime', async () => {
	serve(page());
	renderWithProviders(<PublicStatusScreen slug="acme" />);

	expect(await screen.findByRole('heading', { name: 'Acme status' })).toBeInTheDocument();
	expect(screen.getByText('Live availability')).toBeInTheDocument();
	expect(screen.getByText('API')).toBeInTheDocument();
	expect(screen.getAllByTestId('status-day')).toHaveLength(90);
	expect(screen.getByText('99.90% uptime over the last 90 days')).toBeInTheDocument();
});

test('shows no data instead of a percentage', async () => {
	serve(page({ monitors: [monitor({ uptime: null, days: days().map((day) => ({ ...day, status: 'no_data', uptime: null })) })] }));
	renderWithProviders(<PublicStatusScreen slug="acme" />);
	expect(await screen.findByText('No data over the last 90 days')).toBeInTheDocument();
});

test('renders active and recent incidents without acknowledgement identity', async () => {
	serve(page({
		incidents: {
			active: [{ id: 'i1', monitor_id: 'm1', monitor_name: 'API', status: 'ongoing', started_at: '2026-11-08T08:00:00Z', resolved_at: null }],
			recent: [{ id: 'i2', monitor_id: 'm1', monitor_name: 'API', status: 'resolved', started_at: '2026-11-07T08:00:00Z', resolved_at: '2026-11-07T08:20:00Z' }],
		},
	}));
	renderWithProviders(<PublicStatusScreen slug="acme" />);
	expect(await screen.findByText('Active incidents')).toBeInTheDocument();
	expect(screen.getByText('Past incidents')).toBeInTheDocument();
	expect(screen.getAllByText('API').length).toBeGreaterThan(0);
	expect(screen.queryByText(/acknowledged/i)).not.toBeInTheDocument();
});

test('renders the not-found state for a 404', async () => {
	server.use(http.get(`${ORIGIN}/api/v1/public/status/missing`, () => HttpResponse.json({ type: 'about:blank', title: 'Not Found', status: 404, code: 'not_found' }, { status: 404, headers: { 'Content-Type': 'application/problem+json' } })));
	renderWithProviders(<PublicStatusScreen slug="missing" />);
	expect(await screen.findByText('Status page not found')).toBeInTheDocument();
});

test('banner precedence', () => {
	expect(overallBanner([]).tone).toBe('none');
	expect(overallBanner([monitor()]).text).toBe('All systems operational');
	expect(overallBanner([monitor({ status: 'paused' })]).text).toBe('Status updates are paused');
	expect(overallBanner([monitor(), monitor({ id: 'm2', status: 'paused' })]).text).toBe('All systems operational');
	expect(overallBanner([monitor(), monitor({ id: 'm2', status: 'down' })]).text).toBe('Some systems are experiencing issues');
});

test('never renders a monitor URL even if an extra field is present', async () => {
	const payload = page({
		monitors: [
			{ ...monitor(), url: 'https://api.example.com/health' } as PublicStatusMonitor,
		],
	});
	serve(payload);
	const { container } = renderWithProviders(<PublicStatusScreen slug="acme" />);
	expect(await screen.findByText('API')).toBeInTheDocument();
	expect(container.textContent).not.toContain('api.example.com');
});

test('sets the document title', async () => {
	serve(page());
	renderWithProviders(<PublicStatusScreen slug="acme" />);
	await screen.findByRole('heading', { name: 'Acme status' });
	expect(document.title).toBe('Acme status · o1 uptime');
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/features/status-pages/PublicStatusScreen.test.tsx`
Expected: FAIL because the component, helpers, and route do not exist.

- [ ] **Step 3: Implement `PublicStatusScreen`**

Pin:
- Layout: centered `max-w-5xl` column with a plain-text "o1 uptime" brand, page title, description, and an overall banner (`ok` green, `issue` destructive, `paused` muted, `none` hidden).
- Use `usePublicStatus(slug)`. Loading → `PageSkeleton`; 404 (`isApiProblem(error) && error.problem.code === 'not_found'`) → "Status page not found"; other errors → message with a "Try again" button calling `refetch`.
- Monitor rows: name, status badge (Up/Down/Unknown/Paused), `formatUptime`, and 90 bars. Each bar is a span with `data-testid="status-day"`, `aria-hidden="true"`, a `title` of `${date}: ${percentage or "No data"}`, and a color class by status. Include a legend (Up, Partial, Down, No data).
- Each row exposes a text summary `"{formatted} uptime over the last 90 days"` or `"No data over the last 90 days"`.
- A visually hidden list announces only non-`up` days as `"November 7: down"` / `"November 7: partial"` / `"November 7: no data"`.
- Incident sections: "Active incidents" and "Past incidents"; show monitor name, start time, and for resolved rows the resolve time and `incidentDuration`. Render "No active incidents." / "No incidents in the last 7 days." when empty.
- Empty monitor list: "No monitors are currently published." and no banner.
- `formatUptime`: `null` → "No data"; otherwise `(value * 100).toFixed(2)%`.
- `overallBanner` follows the precedence in the spec: no monitors → `none`; any non-paused `down` → `issue`; every monitor paused → `paused`; otherwise `ok`.
- `incidentDuration` renders minutes for under an hour, otherwise hours and minutes (for example "20m", "1h 5m").
- Set `document.title` in an effect while the payload is loaded and restore the previous title on unmount.
- No monitor URL is accepted by the type or rendered.

- [ ] **Step 4: Add the public route**

In `web/src/router.tsx`:
- Lazy-import `PublicStatusScreen`.
- Create a route directly under `rootRoute` (not `publicLayoutRoute`):
  ```ts
  const publicStatusRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/status/$slug',
    component: function PublicStatusRoute() {
      const { slug } = publicStatusRoute.useParams();
      return <PublicStatusScreen slug={slug} />;
    },
  });
  ```
- Add `publicStatusRoute` to `rootRoute.addChildren([...])` alongside `publicLayoutRoute` and `appLayoutRoute`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd web && npm test -- --run src/features/status-pages/PublicStatusScreen.test.tsx`
Expected: PASS.

- [ ] **Step 6: Quality gate and commit**

Run: `cd web && npm run format && npm run lint && npm run typecheck && npm test -- --run`
Commit:

```bash
git add web/src/features/status-pages web/src/router.tsx
git commit -m "feat: add the public status page"
```

---

### Task 8: Admin settings screen, sidebar link, and route guard

**Files:**
- Modify: `web/src/lib/roles.ts`
- Modify: `web/src/lib/roles.test.ts`
- Create: `web/src/features/status-pages/StatusPageSettingsScreen.tsx`
- Create: `web/src/features/status-pages/StatusPageSettingsScreen.test.tsx`
- Modify: `web/src/components/layout/Sidebar.tsx`
- Modify: `web/src/router.tsx`
- Modify: `web/src/router.test.tsx`

**Interfaces:**
- Consumes: Task 6 hooks, `Workspace`, `useMonitors`.
- Produces:
  - `canManageStatusPage(role: Role): boolean` (admin threshold).
  - `StatusPageSettingsScreen({ workspace }: { workspace: Workspace })`.
  - Route `/w/$workspaceSlug/settings/status-page`, guarded by `canManageStatusPage`.
  - Sidebar link "Status page" under Settings next to "Alerts".

- [ ] **Step 1: Write the failing role test**

Add to `web/src/lib/roles.test.ts`:

```ts
test('only admins and owners manage the status page', () => {
	expect(canManageStatusPage('admin')).toBe(true);
	expect(canManageStatusPage('owner')).toBe(true);
	expect(canManageStatusPage('member')).toBe(false);
	expect(canManageStatusPage('viewer')).toBe(false);
});
```

- [ ] **Step 2: Write the failing settings tests**

Create `StatusPageSettingsScreen.test.tsx`:

```tsx
import { beforeEach, describe, expect, test, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type { Monitor, StatusPageConfig } from '@/lib/api/types';
import { StatusPageSettingsScreen } from './StatusPageSettingsScreen';

const toast = vi.hoisted(() => ({
	success: vi.fn(),
	error: vi.fn(),
}));

vi.mock('sonner', () => ({ toast }));

const ORIGIN = 'http://localhost:5173';
const workspace = { id: 'w1', name: 'Acme', slug: 'acme', role: 'admin' as const };
const configPath = `${ORIGIN}/api/v1/workspaces/w1/status-page`;
const monitorsPath = `${ORIGIN}/api/v1/workspaces/w1/monitors`;

const monitor: Monitor = {
	id: 'm1', workspace_id: 'w1', name: 'API', url: 'https://secret.example.com/health',
	method: 'GET', headers: {}, has_body: false, content_type: null, expected_status: '2xx',
	interval_seconds: 60, timeout_seconds: 10, confirmation_threshold: 2,
	recovery_threshold: 2, paused: false, status: 'up', last_check: null,
	summary_24h: null, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
};

function serveConfig(config: StatusPageConfig) {
	server.use(
		http.get(configPath, () => HttpResponse.json(config)),
		http.get(monitorsPath, () => HttpResponse.json({ monitors: [monitor] })),
	);
}

beforeEach(() => {
	toast.success.mockReset();
	toast.error.mockReset();
});

test('loads defaults, selects a monitor, and saves', async () => {
	const user = userEvent.setup();
	const bodies: unknown[] = [];
	serveConfig({ enabled: false, title: 'Acme', description: '', monitor_ids: [] });
	server.use(http.put(configPath, async ({ request }) => {
		bodies.push(await request.json());
		return HttpResponse.json({ enabled: true, title: 'Acme status', description: 'Live', monitor_ids: ['m1'] });
	}));

	renderWithProviders(<StatusPageSettingsScreen workspace={workspace} />);
	expect(await screen.findByDisplayValue('Acme')).toBeInTheDocument();
	await user.click(screen.getByLabelText('Publish API'));
	await user.click(screen.getByRole('switch', { name: 'Status page enabled' }));
	await user.clear(screen.getByLabelText('Title'));
	await user.type(screen.getByLabelText('Title'), 'Acme status');
	await user.type(screen.getByLabelText('Description'), 'Live');
	await user.click(screen.getByRole('button', { name: 'Save status page' }));

	await waitFor(() => expect(bodies).toEqual([{
		enabled: true, title: 'Acme status', description: 'Live', monitor_ids: ['m1'],
	}]));
	expect(toast.success).toHaveBeenCalledWith('Status page saved');
});

test('blocks enabling with no monitors', async () => {
	const user = userEvent.setup();
	serveConfig({ enabled: false, title: 'Acme', description: '', monitor_ids: [] });
	renderWithProviders(<StatusPageSettingsScreen workspace={workspace} />);
	await screen.findByDisplayValue('Acme');
	await user.click(screen.getByRole('switch', { name: 'Status page enabled' }));
	await user.click(screen.getByRole('button', { name: 'Save status page' }));
	expect(await screen.findByText('Select at least one monitor before enabling the page.')).toBeInTheDocument();
});

test('shows server field errors and suspended problems', async () => {
	const user = userEvent.setup();
	serveConfig({ enabled: false, title: 'Acme', description: '', monitor_ids: [] });
	server.use(http.put(configPath, () => HttpResponse.json({
		type: 'about:blank', title: 'Validation Error', status: 400, code: 'validation_error',
		fields: { title: 'title is required' },
	}, { status: 400, headers: { 'Content-Type': 'application/problem+json' } })));

	renderWithProviders(<StatusPageSettingsScreen workspace={workspace} />);
	await screen.findByDisplayValue('Acme');
	await user.click(screen.getByRole('button', { name: 'Save status page' }));
	expect(await screen.findByText('title is required')).toBeInTheDocument();
});

test('shows the public link and copies it', async () => {
	const user = userEvent.setup();
	serveConfig({ enabled: true, title: 'Acme status', description: '', monitor_ids: ['m1'] });
	const writeText = vi.fn().mockResolvedValue(undefined);
	Object.assign(navigator, { clipboard: { writeText } });

	renderWithProviders(<StatusPageSettingsScreen workspace={workspace} />);
	const link = await screen.findByDisplayValue(`${ORIGIN}/status/acme`);
	expect(link).toBeInTheDocument();
	await user.click(screen.getByRole('button', { name: 'Copy public link' }));
	expect(writeText).toHaveBeenCalledWith(`${ORIGIN}/status/acme`);
	expect(await screen.findByText('Public link copied')).toBeInTheDocument();
});
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/lib/roles.test.ts src/features/status-pages/StatusPageSettingsScreen.test.tsx`
Expected: FAIL because the helper, screen, route, and sidebar link do not exist.

- [ ] **Step 4: Implement the role helper, settings screen, sidebar, and route**

`roles.ts`:

```ts
export function canManageStatusPage(role: Role): boolean {
	return roleAtLeast(role, 'admin');
}
```

`StatusPageSettingsScreen`:
- Props `{ workspace }`. Use `useStatusPage(workspace.id)` and `useMonitors(workspace.id)`.
- Hydrate local state once each query lands: `enabled`, `title` (fallback `workspace.name`), `description`, and a `Set<string>` of monitor IDs. Keep user edits when queries refetch (hydrate only until the user edits, or compare by a loaded flag).
- Flatten monitors: `monitorsQuery.data?.pages.flatMap((page) => page.monitors) ?? []`.
- Form fields and labels exactly: "Status page enabled" (switch), "Title", "Description", "Monitors", "Save status page". Buttons "Copy public link", "Open page".
- Each monitor row has a checkbox labelled `aria-label={`Publish ${monitor.name}`}` plus the visible monitor name and current status; the accessible name is exactly `Publish API`, not the status text.
- Show a live description counter `{description.length}/500`.
- When monitors are empty, render "Create a monitor before publishing a status page." with a link to `/w/$workspaceSlug/monitors/new`.
- Client validation before submit: enabled && no selected monitor → inline text "Select at least one monitor before enabling the page." and do not call the mutation; title trimmed empty → "Title is required.".
- Submit calls `useUpdateStatusPage(workspace.id)` with trimmed title/description, selected IDs in monitor list order, and `enabled`. On success `toast.success('Status page saved')`; on error `ProblemAlert` from `problemFromError` and show `problem.fields` values as field text under Title/Description/Monitors.
- When `config.enabled`, show a read-only input with `${window.location.origin}/status/${workspace.slug}` and the copy/open buttons. Copy uses `navigator.clipboard.writeText` and `toast.success('Public link copied')`; failure shows `toast.error('Could not copy the link')`.
- Never render monitor URLs.

`Sidebar.tsx`: inside the existing admin-gated Settings group, add a "Status page" link to `/w/$workspaceSlug/settings/status-page`.

`router.tsx`:
- Lazy-import the settings screen.
- Add `/settings/status-page` under `workspaceRoute` with a `beforeLoad` guard redirecting to monitors when `!canManageStatusPage(context.workspace.role)`.
- Add the route to `workspaceRoute.addChildren([...])`.

- [ ] **Step 5: Add the failing router coverage**

In `web/src/router.test.tsx`, add:

```tsx
test('the public status route renders without a session', async () => {
	server.use(
		http.get(`${ORIGIN}/api/v1/me`, () => unauthenticatedProblem()),
		http.get(`${ORIGIN}/api/v1/public/status/acme`, () =>
			HttpResponse.json({
				workspace: { name: 'Acme', slug: 'acme' },
				page: { title: 'Acme status', description: '', updated_at: '2026-11-08T09:00:00Z' },
				monitors: [],
				incidents: { active: [], recent: [] },
			}),
		),
	);
	renderRouter('/status/acme');
	expect(await screen.findByRole('heading', { name: 'Acme status' })).toBeInTheDocument();
	expect(screen.queryByText('Sign in')).not.toBeInTheDocument();
});

test('a signed-in user is not redirected away from the public status route', async () => {
	server.use(
		http.get(`${ORIGIN}/api/v1/me`, () => HttpResponse.json(memberMe)),
		http.get(`${ORIGIN}/api/v1/public/status/acme-status`, () =>
			HttpResponse.json({
				workspace: { name: 'Acme Status', slug: 'acme-status' },
				page: { title: 'Public page', description: '', updated_at: '2026-11-08T09:00:00Z' },
				monitors: [],
				incidents: { active: [], recent: [] },
			}),
		),
	);
	renderRouter('/status/acme-status');
	expect(await screen.findByRole('heading', { name: 'Public page' })).toBeInTheDocument();
});

test('members are redirected away from status page settings', async () => {
	server.use(
		http.get(`${ORIGIN}/api/v1/me`, () => HttpResponse.json(memberMe)),
		http.get(`${ORIGIN}/api/v1/workspaces/w1/monitors`, () => HttpResponse.json({ monitors: [] })),
	);
	const { router } = renderRouter('/w/acme-status/settings/status-page');
	await waitFor(() =>
		expect(router.state.location.pathname).toBe('/w/acme-status/monitors'),
	);
});
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd web && npm test -- --run src/lib/roles.test.ts src/features/status-pages src/router.test.tsx`
Expected: PASS.

- [ ] **Step 7: Quality gate and commit**

Run: `cd web && npm run format && npm run lint && npm run typecheck && npm test -- --run && npm run build`
Commit:

```bash
git add web/src/lib/roles.ts web/src/lib/roles.test.ts web/src/components/layout/Sidebar.tsx web/src/router.tsx web/src/router.test.tsx web/src/features/status-pages
git commit -m "feat: add status page settings to the dashboard"
```

---

### Task 9: End-to-end coverage, README, and final verification

**Files:**
- Modify: `web/tests/e2e/helpers.ts`
- Modify: `web/tests/e2e/smoke.spec.ts`
- Modify: `README.md`

**Interfaces:**
- Consumes: all earlier tasks.
- Produces: `seedProbeResults(monitorName: string): void` in the e2e helpers.

- [ ] **Step 1: Add the probe seed helper**

Append to `web/tests/e2e/helpers.ts`:

```ts
/**
 * Inserts one down probe for yesterday and one up probe for today for the
 * named monitor so the public status page has a non-empty daily history.
 */
export function seedProbeResults(monitorName: string): void {
	composePsql(`
		WITH monitor AS (
			SELECT m.id, m.workspace_id
			FROM monitors m
			WHERE m.name = :'monitor_name'
			ORDER BY m.created_at DESC, m.id DESC
			LIMIT 1
		)
		INSERT INTO probe_results (
			id, monitor_id, workspace_id, scheduled_for, checked_at, status, latency_ms
		)
		SELECT gen_random_uuid(), monitor.id, monitor.workspace_id,
		       now() - interval '1 day', now() - interval '1 day', 'down', 100
		FROM monitor
		UNION ALL
		SELECT gen_random_uuid(), monitor.id, monitor.workspace_id,
		       now(), now(), 'up', 100
		FROM monitor;
	`, { variables: { monitor_name: monitorName } });
}
```

- [ ] **Step 2: Write the failing E2E flow**

In `web/tests/e2e/smoke.spec.ts`, extend the main admin test (import `seedProbeResults` and accept the `browser` fixture):

1. After the monitor is edited, call `seedProbeResults(MONITOR_NAME_EDITED)`.
2. Open **Settings → Status page**.
3. Fill title `Acme status` and description `Live availability for our API`.
4. Check the monitor checkbox and enable the page.
5. Save; assert `toast`/text "Status page saved" and that the public link input contains `/status/`.
6. Create a fresh logged-out context: `const publicPage = await browser.newPage(); await publicPage.goto(publicUrl);`
7. Assert the heading `Acme status`, the description, the monitor name, at least one element with `data-testid="status-day"`, a visible `%` uptime, and that the body text does not contain the monitor URL (`https://example.com`).
8. Run `expectNoSeriousA11yViolations(publicPage)`.
9. Close the public page, return to the admin page, disable the switch, save.
10. Reload `publicUrl` and assert "Status page not found".
11. Keep the existing incident, email, alert-channel, theme, and a11y assertions green.

Do not call any external alert channel.

- [ ] **Step 3: Run the E2E test**

Run: `cd web && npm run e2e -- --grep "register, verify"`
Expected: PASS, because Tasks 1–8 are already in place. If it fails, fix the
wiring before updating the README; do not weaken the assertions.

- [ ] **Step 4: Update the README**

- Replace "Public status pages are a later milestone." with a sentence pointing at the new section.
- Add a **Public status pages** section covering:
  - **Settings → Status page** enables one page per workspace, sets the title/description, and selects monitors.
  - The public URL is `/status/{workspace-slug}`; admins can copy or open it from settings.
  - Visitors see current status, 90 UTC daily bars, 90-day uptime, and active/recent (7-day) incidents.
  - No-data days, paused monitors, and unknown states are not counted as downtime.
  - The page never shows monitor URLs, headers, errors, acknowledgements, or delivery data.
  - Responses are cached for 15 seconds and limited to 60 requests per minute per IP.
  - V1 limits: one page per workspace, no custom domains, no separate page slug, no subscriber notifications, and no public incident detail pages.
  - Suspended workspaces keep the public page read-only.

- [ ] **Step 5: Run the complete verification**

Run:

```bash
cd backend && gofmt -l . && go vet ./... && golangci-lint run && go test -short ./... && go test ./...
cd web && npm run format:check && npm run lint && npm run typecheck && npm test -- --run && npm run build
cd web && npm run e2e
```

Expected: all commands exit 0; backend integration tests and E2E use the Compose Postgres/Mailpit stack (`make dev-db` from the repository root if it is not already running).

- [ ] **Step 6: Commit**

```bash
git add README.md web/tests/e2e/helpers.ts web/tests/e2e/smoke.spec.ts
git commit -m "test: cover the public status page end to end"
```

---

## Self-Review Checklist

- [ ] Every spec section maps to a task: data model (§6) → Task 1; admin API (§7.2) → Tasks 3/5; public API and caching (§7.3/§7.4) → Tasks 4/5; aggregation/incidents (§7.5) → Task 4; dashboard (§8) → Tasks 6–8; testing (§9) → every task; documentation/acceptance (§10–§13) → Task 9.
- [ ] Review Focus items have tests: UTC boundaries → Task 1 `TestProbeDayStats_GroupsByUTCDate` and Task 4 `TestService_PublicPageBuckets`; 404 caching → Tasks 4/5; empty monitor list → Task 4; leakage → Tasks 4/5; cache isolation/bound → Tasks 2/4/5.
- [ ] Names and signatures match across tasks: `statuspage.Service`, `Config`, `ConfigInput`, `PublicPage`, `GetConfig`, `UpdateConfig`, `PublicPage`, `canManageStatusPage`, query keys `statusPage`/`publicStatus`.
- [ ] No task depends on a later task without saying so; each task ends with a passing test cycle and a commit.

# Plan 6 — Public Status Page Design

**Status:** Draft for approval
**Date:** 2026-11-08
**Depends on:** Plans 1–5, all merged into `main`.
**Out of scope:** custom domains, multiple status pages per workspace, subscriber
signup or email/SMS notifications from the status page, public incident detail
pages, manual incident announcements, maintenance windows, on-call schedules,
SEO/sitemap work, rollup tables, retention changes, and SSE/live updates.

## 1. Intent

Plans 1–5 built monitoring, incidents, and private alerting. The product spec's
remaining public-facing milestone is a **public status page per workspace**:
one URL a workspace can share with customers that shows the current health of
the monitors it chooses, 90 days of daily uptime, and ongoing/recent incidents
without exposing tenant secrets.

This plan is one vertical slice: schema, public read API, admin configuration
API, dashboard settings, the public page itself, and end-to-end coverage. It
deliberately leaves scaling work (rollups) and notification features for later.

## 2. Current state

- `workspaces` already has a globally unique, immutable `slug`. The SPA and API
  share one origin; `/status/{slug}` can serve `index.html` through the existing
  Caddy `try_files` fallback.
- `probe_results` stores one row per probe (`status` is only `up` or `down`,
  plus `status_code`, `latency_ms`, and `error`) and is partitioned monthly.
  Indexes `(monitor_id, checked_at DESC, id DESC)` and
  `(workspace_id, checked_at DESC)` support range scans. The current
  partition-maintenance code keeps at least the last month and the next three
  months; nothing drops older partitions, so 90 days of raw data is available.
- `monitor_state` carries current `status` (`unknown`, `up`, `down`) and
  `monitors.paused` marks paused monitors. There is no query that aggregates
  probe results per calendar day.
- `incidents` has `open`, `acknowledged`, and `resolved`; `acknowledged_by` and
  delivery rows must never be public.
- The API router attaches `requireSession` to every workspace route; only the
  auth and health endpoints are currently public. `httpx.RateLimit` already
  provides a per-IP or global token bucket with stale-bucket cleanup.
- The SPA's `publicLayoutRoute` redirects authenticated users to `/`; it cannot
  be reused for a page that logged-in admins also need to inspect.
- There are no status-page tables, routes, types, or UI.

## 3. Goals, non-goals, anti-goals

**Goals**

1. A workspace admin can enable one public status page, set its title and
   description, and choose which workspace monitors appear.
2. Anyone can open `/status/{workspace-slug}` without an account and see the
   page title/description, each selected monitor's current status, 90 daily
   uptime bars, and ongoing/recent incidents.
3. Daily bars follow the product spec's uptime contract: unknown, paused, and
   missing data are "no data" and excluded from the denominator, never counted
   as down.
4. A disabled, unknown, or never-configured page returns a nondescript 404.
5. The public payload never contains monitor URLs, headers, bodies, error
   strings, latency/status codes, acknowledgement identity, delivery rows, or
   workspace IDs.
6. The public endpoint is cheap to call: a short server-side cache and a
   per-IP rate limit.
7. Admins can copy/open the public link from settings and see the saved state
   after reload.
8. Existing dashboard routes, labels, query keys, and alert behavior remain
   unchanged.

**Non-goals**

- Multiple pages per workspace, page-specific slugs, custom domains, or
  branding beyond title/description.
- Per-monitor visibility inside a page; a monitor is either listed or not.
- Public incident detail pages, manual incident updates, or subscriber
  notifications.
- Latency charts on the public page.
- Rollup tables or retention changes.
- Real-time refresh (SSE/polling).

**Anti-goals**

- A monitor URL, secret, error string, or acknowledgement identity appearing in
  the public JSON or HTML.
- Counting a paused, unknown, or missing-data day as downtime.
- A public API returning anything other than a `no-store` 404 for a disabled
  or unknown page, or distinguishing "disabled" from "never existed".
- Making the public page depend on a user session or the `me` query.
- Breaking the dashboard's authenticated routes by adding a public route.

## 4. Approaches

**A. Compute 90 daily buckets on demand from raw `probe_results` (chosen).**
Add `status_pages` and `status_page_monitors`, a public read endpoint, and two
admin config endpoints. The public service runs one grouped query over the
selected monitors' probe results since UTC midnight 89 days ago, fills the
missing days in Go, and caches the assembled payload in process for 15 seconds.
The page is rendered by a normal SPA route and refreshed by TanStack Query.

*Why:* raw probes already cover exactly the window the page needs, the current
monitor quota keeps the scan small, and the index on `(monitor_id, checked_at)`
supports it. No worker, rollup, or retention change is required. A future
rollup plan can replace the query without changing the API or UI.

**B. Add a `monitor_daily_stats` rollup maintained by the probe worker.**
Read pre-aggregated daily rows instead of raw probes. Faster for very large
monitor counts and tolerant of a shorter raw-retention window, but adds a
migration, a worker write path, a backfill, and its own retention/cleanup
rules. Premature while the default quota is five monitors and the raw window is
already 90 days.

**C. Materialized view refreshed on a schedule.**
Offloads grouping to `REFRESH MATERIALIZED VIEW`, but adds a scheduler,
staleness semantics, and deploy complexity, and incidents still need live
queries. More moving parts for no benefit at this scale.

**Decision:** A. Keep the public service's query shape private so B can replace
it later without an API change.

## 5. Constraints

- UI copy is English. Code and documentation are English.
- UTC calendar days define buckets. The response always contains exactly 90
  buckets, oldest first, ending with today (UTC).
- Uptime = `up / (up + down)` over the buckets. `probe_results.status` is only
  `up` or `down`, so every stored check is in the denominator. Days with zero
  checks are `no_data` and excluded from the monitor's 90-day uptime.
- A page is addressable only by the workspace slug; there is no separate slug.
- The public page must work for both anonymous visitors and signed-in users.
- Admin configuration is admin/owner only; public reads require no role.
- Mutations follow existing workspace suspension rules: suspended workspaces
  reject config writes with `workspace_suspended` but keep public reads.
- Soft-deleted resources do not exist in this design; monitors are hard-deleted
  and selection rows cascade.
- All responses use the existing `application/problem+json` error shape and
  problem codes.

## 6. Data model

Migration `00006_status_pages.sql`:

```sql
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
```

Notes:

- One row per workspace. The page is created lazily on the first `PUT`; no
  backfill for existing workspaces.
- `workspace_id` is the primary key, so it also serves as `status_page_id` in
  the join table. No separate page slug exists in V1.
- Deleting a monitor or workspace cascades the selection rows. A page that
  loses its last monitor remains enabled with an empty monitor list; see §7.4.
- Up/down migration: drop the join table then the page table.

SQLC queries (new `internal/db/queries/status_pages.sql`):

- `GetStatusPageByWorkspace(workspace_id) (StatusPage, error)`
- `UpsertStatusPage(workspace_id, enabled, title, description) (StatusPage, error)`
  using `ON CONFLICT (workspace_id) DO UPDATE`.
- `ListStatusPageMonitors(workspace_id) ([]uuid.UUID, error)` ordered by the
  monitor's `created_at, id` for a stable page order.
- `ListPublicStatusMonitors(workspace_id)` returning `id`, `name`,
  `paused`, and `monitor_state.status`, ordered by monitor `created_at, id`.
- `DeleteStatusPageMonitors(workspace_id)` for the replace-on-save flow.
- `InsertStatusPageMonitor(status_page_id, monitor_id)` with `ON CONFLICT DO NOTHING`.
- `ListProbeDayStats(monitor_ids uuid[], since timestamptz)` returning
  `monitor_id`, `day date`, `checks`, `up_count`, `down_count`, grouped by
  `monitor_id, (checked_at AT TIME ZONE 'UTC')::date`. The `since` bound lets
  Postgres prune partitions.
- `ListPublicActiveIncidents(monitor_ids uuid[])` returning `id`, `monitor_id`,
  monitor `name`, `started_at`, ordered by `started_at DESC`.
- `ListPublicRecentIncidents(monitor_ids uuid[], since timestamptz, limit int)`
  returning the same fields plus `resolved_at`, where `resolved_at >= since`,
  ordered by `resolved_at DESC, id DESC`.

Queries select only the columns the API needs; they never select monitor URLs,
headers, bodies, error text, latency, status codes, acknowledgement fields, or
delivery fields.

## 7. Backend design

### 7.1 Package and wiring

New package `internal/statuspage`:

- `types.go` — API views, sentinels, and `ValidationError` (mirroring
  `alerts/types.go`).
- `service.go` — admin config plus public payload assembly.
- `cache.go` — small in-process TTL cache.
- `handlers.go` — admin and public handlers.
- Tests alongside.

`api.Dependencies` gains `StatusPageService *statuspage.Service`. `NewRouter`
builds it when nil and the pool is non-nil:

```go
statuspage.NewService(deps.Pool, sqlc.New(deps.Pool), wsSvc)
```

No encryption key is needed. No worker, scheduler, or probe changes.

### 7.2 Admin API

All admin routes require a session and `workspaces.RoleAdmin` (owner included).

**`GET /api/v1/workspaces/{workspaceID}/status-page`**

Returns the saved config, or defaults when no row exists:

```json
{
  "enabled": false,
  "title": "Acme",
  "description": "",
  "monitor_ids": []
}
```

`title` defaults to the workspace name. `monitor_ids` is ordered by monitor
creation time for stable display. Reading is allowed while suspended.

**`PUT /api/v1/workspaces/{workspaceID}/status-page`**

Body:

```json
{
  "enabled": true,
  "title": "Acme status",
  "description": "Live availability for our API.",
  "monitor_ids": ["uuid", "..."]
}
```

Behavior:

- Admin authorization, then a transaction that locks the workspace and rejects
  suspended workspaces with `workspace_suspended`.
- Trim the title; require 1–80 runes. Trim the description; allow 0–500 runes.
- Deduplicate monitor IDs. Every ID must belong to the workspace; unknown or
  cross-workspace IDs produce a `monitor_ids` field error, not a 404.
- When `enabled` is true, at least one monitor is required.
- Upsert the page row, delete all selection rows, and insert the new selection
  in one transaction. Return the saved config.
- Invalidate that workspace's public cache entry after commit.
- Unknown workspace or missing membership maps to the existing
  `ErrNotFound`/`ErrForbidden` behavior.

Error mapping matches the alerts API: `*ValidationError` → 400
`validation_error` with `fields`; `ErrSuspended` → 403 `workspace_suspended`;
`ErrNotFound` → 404; `ErrForbidden` → 403; otherwise 500. Decode bodies with
`DisallowUnknownFields`.

### 7.3 Public API

**`GET /api/v1/public/status/{slug}`** — no session.

Success response:

```json
{
  "workspace": { "name": "Acme", "slug": "acme" },
  "page": {
    "title": "Acme status",
    "description": "Live availability for our API.",
    "updated_at": "2026-11-08T09:00:00Z"
  },
  "monitors": [
    {
      "id": "uuid",
      "name": "API",
      "status": "up",
      "uptime": 0.9987,
      "days": [
        { "date": "2026-08-11", "status": "up", "uptime": 1 },
        { "date": "2026-08-12", "status": "no_data", "uptime": null }
      ]
    }
  ],
  "incidents": {
    "active": [
      {
        "id": "uuid",
        "monitor_id": "uuid",
        "monitor_name": "API",
        "status": "ongoing",
        "started_at": "2026-11-08T08:00:00Z",
        "resolved_at": null
      }
    ],
    "recent": [
      {
        "id": "uuid",
        "monitor_id": "uuid",
        "monitor_name": "API",
        "status": "resolved",
        "started_at": "2026-11-07T08:00:00Z",
        "resolved_at": "2026-11-07T08:20:00Z"
      }
    ]
  }
}
```

Contract details:

- `workspace` exposes only `name` and `slug`.
- `monitors` is the configured selection in monitor creation order. It is
  empty when the page is enabled but every selected monitor was deleted.
- `status` is `paused` when `monitors.paused` is true, otherwise the
  `monitor_state.status` value (`up`, `down`, or `unknown`).
- `days` always has exactly 90 entries, ascending by UTC date, ending today.
- A day's `status` is `no_data` when `checks = 0`; `up` when `down = 0`;
  `down` when `up = 0`; otherwise `partial`. `uptime` is
  `up / (up + down)` for that day, or `null` for `no_data`.
- A monitor's 90-day `uptime` is `sum(up) / sum(up + down)` across the buckets,
  or `null` when there are no checks.
- `incidents.active` contains every unresolved incident for a selected monitor,
  whether open or acknowledged, all labelled `ongoing`. No acknowledgement
  timestamps, actor IDs, reminder counts, or delivery data appear.
- `incidents.recent` contains incidents for selected monitors resolved in the
  last 7 days, newest resolution first, capped at 20.
- `page.updated_at` is the status-page config row's `updated_at`.
- Values are JSON-escaped by `encoding/json`; no HTML or user string is
  assembled by concatenation.

Unknown, disabled, or not-yet-configured slugs return
`404 application/problem+json` with code `not_found`, a fixed message, and
`Cache-Control: no-store`. The response must not distinguish "never existed"
from "disabled", and a 404 must never be cached by a browser or intermediary so
that enabling the page takes effect on the next request. This 404 contract
applies to the **API**; the browser-facing `/status/{slug}` URL is a normal SPA
route and always receives the index shell with HTTP 200 (see §8.2).

Headers and limits:

- `Cache-Control: public, max-age=15, stale-while-revalidate=15`.
- Server-side cache TTL 15 seconds per slug, in process. Only successful
  payloads are cached; 404s are not, so enabling a page is visible immediately.
  Config updates delete the affected slug's entry.
- Per-IP rate limit of 60 requests per minute with burst 20, using the existing
  `httpx.RateLimit(1, 20, true)` (one request per second sustained). Exceeding
  it returns the existing `429 rate_limited` problem.
- GET and HEAD are both served by the GET pattern. Other methods return 405 via
  `ServeMux`.
- Suspended workspaces keep serving public reads; suspension blocks writes
  only.

### 7.4 Cache

`cache.go` implements:

- `get(slug) (PublicPage, bool)`, `set(slug, page)`, `delete(slug)`.
- A `map[string]entry` guarded by a mutex, an injectable `now func() time.Time`,
  and a TTL. Entries older than the TTL are misses.
- Bounded growth: when the map exceeds 1,024 entries, expired entries are
  swept first; if the map is still over the bound, the oldest insertion is
  dropped. This keeps an attacker from growing memory with random slugs even
  though misses are not cached.
- Tests inject a fake clock and a short TTL; no sleeps.

The service checks the cache only after parsing the slug; an invalid empty slug
is a 404 without touching the cache.

### 7.5 Aggregation and time

The service takes an injectable clock (defaulting to `time.Now`) so tests can
pin "today".

- `since` is `startOfUTCDay(now) - 89 days`.
- `ListProbeDayStats` is called once with the selected monitor IDs.
- Go builds a fixed 90-day slice per monitor by walking from `since` to today
  and looking up each day's aggregate. Missing days become `no_data`.
- The monitor's total uptime is computed from the same aggregates, not from a
  second query.
- The status query and incident query may run concurrently; the implementation
  should keep the public service readable and return the first error.

## 8. Dashboard design

### 8.1 Types, API, and query keys

Extend `lib/api/types.ts`:

```ts
export interface StatusPageConfig {
  enabled: boolean;
  title: string;
  description: string;
  monitor_ids: string[];
}

export interface StatusPageConfigInput {
  enabled: boolean;
  title: string;
  description: string;
  monitor_ids: string[];
}

export type PublicStatusDayStatus = 'up' | 'partial' | 'down' | 'no_data';
export type PublicMonitorStatus = 'up' | 'down' | 'unknown' | 'paused';

export interface PublicStatusDay {
  date: string;
  status: PublicStatusDayStatus;
  uptime: number | null;
}

export interface PublicStatusMonitor {
  id: string;
  name: string;
  status: PublicMonitorStatus;
  uptime: number | null;
  days: PublicStatusDay[];
}

export interface PublicStatusIncident {
  id: string;
  monitor_id: string;
  monitor_name: string;
  status: 'ongoing' | 'resolved';
  started_at: string;
  resolved_at: string | null;
}

export interface PublicStatusPage {
  workspace: { name: string; slug: string };
  page: { title: string; description: string; updated_at: string };
  monitors: PublicStatusMonitor[];
  incidents: { active: PublicStatusIncident[]; recent: PublicStatusIncident[] };
}
```

Query keys:

```ts
statusPage: (workspaceId: string) => ['status-page', workspaceId] as const,
publicStatus: (slug: string) => ['public-status', slug] as const,
```

`features/status-pages/api.ts`:

- `getStatusPage(workspaceId)`
- `updateStatusPage(workspaceId, input)`
- `getPublicStatus(slug)`

`features/status-pages/hooks.ts`:

- `useStatusPage(workspaceId)` and `useUpdateStatusPage(workspaceId)`;
  the mutation invalidates `statusPage(workspaceId)` and
  `publicStatus(slug)`.
- `usePublicStatus(slug)` with `retry: false` so a 404 renders a not-found
  state immediately; `staleTime: 15_000`.

### 8.2 Public route and screen

Add a route directly under the root, **not** under `publicLayoutRoute`:

```ts
const publicStatusRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/status/$slug',
  component: PublicStatusRoute,
});
```

This keeps the page reachable for anonymous visitors (no `me` query, no auth
redirect) and for signed-in admins (no redirect to the dashboard). The screen
provides its own wide layout rather than `PublicLayout`'s centered auth card.

`PublicStatusScreen`:

- Brand header "o1 uptime" as plain text (not a link that would send anonymous
  visitors to the login screen).
- Page title and description from the payload.
- An overall banner derived in this order: no monitors → no banner (show the
  empty state alone); any non-paused monitor `down` → "Some systems are
  experiencing issues"; every monitor paused → "Status updates are paused";
  otherwise → "All systems operational". Paused monitors are ignored for the
  banner when at least one non-paused monitor exists; `unknown` counts as
  healthy. Paused state is always shown per row.
- One row per monitor: name, current-status badge, 90-day uptime percentage
  (or "No data"), and 90 daily bars.
- Bars: 2–3 px wide, green `up`, amber `partial`, red `down`, muted `no_data`;
  hover/`title` text includes the date and either the percentage or "No data";
  a legend below the rows.
- Accessibility: individual bars are `aria-hidden`; each row exposes a text
  summary ("99.98% uptime over the last 90 days"). A visually hidden list
  announces only non-`up` days ("November 7: down"), so screen readers get the
  meaningful history without 90 controls. Bars meet the same contrast targets
  as the rest of the app and are covered by the existing axe smoke check.
- Active incidents section, newest first, with monitor name and start time;
  recently resolved incidents below with start/resolve times and duration. No
  links to incident detail pages (none exist publicly).
- Empty states: "No monitors are currently published." for an enabled page with
  no monitors; "No active incidents." / "No incidents in the last 7 days."
- 404: "Status page not found" with no detail about whether it was disabled or
  never existed. The browser route itself is served by the SPA shell (HTTP 200
  from Caddy); only the API returns 404. Other errors show a retry action.
- Set `document.title` to `{page.title} · o1 uptime` while mounted.
- Public theme follows the existing light/dark/system initialization; no theme
  switcher is added to the public page.

### 8.3 Admin settings screen

New route `/w/$workspaceSlug/settings/status-page`, admin/owner only, guarded
with a new `canManageStatusPage(role)` helper in `lib/roles.ts` (admin
threshold). The sidebar's Settings group gains a "Status page" link next to
"Alerts" under the same gate.

`StatusPageSettingsScreen`:

- Heading "Status page" and a short explanation of what visitors can see.
- Enabled switch.
- Title input, prefilled with the workspace name when no page exists; 1–80
  characters. Description textarea, 0–500 characters, with a live counter.
- Monitor checklist built from the existing monitors query: name plus current
  status. If the workspace has no monitors, show "Create a monitor before
  publishing a status page." with a link to the monitor create screen.
- When enabled, at least one monitor must be selected; submission is blocked
  client-side and the server enforces the same rule.
- Public link block when enabled: the full URL built from
  `window.location.origin` + `/status/{workspace.slug}`, a copy button, and an
  "Open page" link. When disabled, show "The status page is not public yet."
- Save button with pending state; success toast; `ProblemAlert` with field
  errors on 400 and suspended state on 403. No optimistic cache writes are
  needed.
- The settings page must not show any monitor URL, headers, or secrets; it only
  uses monitor names, statuses, and IDs already available to admins.

## 9. Testing

**Backend**

- Schema test: one page per workspace, cascade on workspace/monitor delete,
  duplicate selection rejected, 90-day query has an index-friendly shape.
- Config service tests: defaults for a workspace without a row; validation for
  title/description/enabled-with-no-monitors/unknown monitor/cross-workspace
  monitor; dedup; replace-on-save; suspended workspace rejects writes; member
  and viewer forbidden; owner allowed.
- Public service tests with a fixed clock and manually inserted probe results:
  exactly 90 ascending UTC buckets; `no_data` days; `partial`; full up/down;
  monitor uptime excluding no-data days; paused and unknown status; empty
  selection; deleted monitor cascade; disabled → `ErrNotFound`; missing slug →
  `ErrNotFound`.
- Incident tests: acknowledged incident appears as `ongoing` with no
  acknowledgement fields; resolved incidents older than 7 days are excluded;
  recent list capped at 20 and newest first; incidents for unselected monitors
  are excluded.
- Privacy tests: JSON responses for admin and public pages never contain the
  monitor URL, request headers, body, error text, latency, status code,
  workspace ID, acknowledgement user, or delivery fields.
- Handlers: public GET works without a session and returns the documented
  shape with `Cache-Control`; 404 for unknown/disabled with `no-store`; no
  caching of 404; admin GET/PUT role matrix (admin/owner 200, member/viewer
  403, cross-workspace 404, suspended 403); validation 400 with fields; rate
  limiting returns 429.
- Cache tests with a fake clock: hit within TTL, miss after TTL, config update
  invalidates, 404 not cached, bound sweep keeps memory stable.

**Web**

- API tests with MSW: exact GET/PUT paths and bodies; public GET path; 404
  problem parsing.
- Hook tests: keys, invalidation after update, `usePublicStatus` retry disabled.
- `PublicStatusScreen` tests: title/description, monitor rows and statuses,
  90 bars, uptime formatting, `no_data`, active/recent incidents, empty states,
  the banner precedence (down wins, mixed paused/healthy is operational, all
  paused, empty list has no banner), not-found state, and absence of any monitor
  URL/token in the rendered output.
- `StatusPageSettingsScreen` tests: defaults, enable toggle, monitor selection,
  validation, save payload, copy/open link, suspended and field-error displays.
- Router test: `/status/acme` renders without calling `me`; `/w/.../settings/
  status-page` redirects non-admins and renders for admins.
- Existing tests remain green.

**E2E**

- In the admin smoke flow, create/open a monitor, select it in Status page
  settings with a title and description, save, copy/open the public URL in a
  fresh logged-out context, and assert the title, monitor name, bars, and that
  no monitor URL appears.
- Seed one `up` and one `down` probe result for the monitor through the existing
  compose psql helper so the bars show a real percentage.
- Run the axe check on the public page.
- Disable the page and assert the public URL shows "Status page not found".

## 10. Documentation and final verification

- README: replace the "public status pages are a later milestone" sentence and
  add a **Public status pages** section covering the Settings location, the
  `/status/{slug}` URL, admin-selected monitors, the 90-day uptime contract,
  privacy guarantees, the 15-second cache, the 60/min per-IP limit, and the V1
  limits (one page per workspace, no custom domains, no subscriber
  notifications).
- Full quality gate: backend `gofmt`, `go vet`, `golangci-lint`, short and full
  tests; web format/lint/typecheck/unit/build; Playwright smoke.

## 11. Calls made by the builder

These are settled here so the plan can be written without another round:

- **Page address:** reuse `workspaces.slug`; no separate status-page slug.
- **Laziness:** status pages are created on first save, not backfilled.
- **Display order:** selected monitors ordered by monitor `created_at, id`.
- **Enabled-empty page:** remains enabled and public with an empty monitor list;
  the UI shows an empty-state message rather than a 404.
- **Suspended workspace:** public reads continue; only config writes are
  rejected.
- **Recent incident window:** 7 days, capped at 20.
- **Cache:** fixed 15-second TTL and 60/min per-IP rate limit; no new
  environment variables.
- **No polling:** the public page refreshes on load and on query invalidation;
  no interval.
- **Status banner:** `unknown` is treated as healthy for the banner; `paused` is
  called out separately.
- **404 scope:** only the public API returns 404, with `Cache-Control: no-store`.
  The SPA route is a normal 200 index shell that renders the not-found state;
  no server-side or edge check is added.
- **Banner precedence:** empty list → no banner; any non-paused `down` → issues;
  all paused → paused; otherwise operational. Mixed paused/healthy pages read
  "All systems operational".

## 12. Left to the builder

- Exact SQL/Go helper names beyond the query names above.
- Component decomposition and CSS utility choices, as long as labels, states,
  accessibility, and test seams match this document.
- Whether the 90-bar row is implemented as flex spans or a small SVG; either is
  fine if the accessibility behavior and test hooks are stable.
- Test helper reuse (`testutil.NewPostgres`, MSW handlers, Playwright helpers)
  following existing patterns.

## 13. Acceptance

1. An admin can enable a page, set title/description, select monitors, save,
   reload, and still see the saved selection.
2. An anonymous visitor opening `/status/{slug}` sees the title, description,
   selected monitors, current statuses, 90 daily bars, uptime, and incidents.
3. A disabled or unknown slug returns 404 and the SPA shows the not-found state.
4. A logged-in admin can open the same public URL without being redirected.
5. No public or admin response contains a monitor URL, secret, error string,
   latency/status code, acknowledgement identity, or delivery data.
6. A day with no probes, a paused monitor, and an `unknown` state are never
   counted as downtime.
7. The public endpoint is rate-limited and cached for 15 seconds; a config save
   is visible on the next request in the same process.
8. Member/viewer users cannot change the page; suspended workspaces cannot
   either.
9. Backend, web, and Playwright suites pass, including the axe check on the
   public page.

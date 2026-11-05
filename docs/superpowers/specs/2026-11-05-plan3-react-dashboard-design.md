# Plan 3 — React Dashboard Design

**Status:** Draft for approval
**Date:** 2026-11-05
**Depends on:** Plan 1 (auth, sessions, workspaces, RBAC) and Plan 2 (monitors,
probing, results/summary, admin APIs), both merged into `main`.
**Out of scope:** incidents, alerts, public status pages, SSE, 2FA, API keys,
billing, production hardening.

## 1. Intent

Plan 2 made the product monitor things, but only through the API. Plan 3 gives
the product a real user interface: a React single-page application that lets a
new user sign up, verify their email, create a workspace, add HTTP monitors,
watch their state and recent results, and lets administrators manage abuse.
This is the first slice a non-technical person can actually use.

The UI is English, matching the product's UI/code language decision.

## 2. Current state

The backend already exposes almost everything Plan 3 needs; two small fixes are
called out below and in section 9.

- Auth: `POST /api/v1/auth/register`, `/verify-email`, `/verify-email/resend`,
  `/login`, `/logout`, `/password/forgot`, `/password/reset`.
- Session: HttpOnly `o1_session` cookie, `SameSite=Lax`, `Secure` in
  production. There is no CSRF token; unsafe requests are checked against an
  Origin allowlist in `httpx.CSRF`.
- `GET /api/v1/me` returns the authenticated user plus every workspace and the
  caller's role.
- Workspaces: list, create, get, list members, add member.
- Monitors: list/get/create/update/delete, pause/resume, results, summary.
- Admin: list workspaces/monitors/security events, suspend/unsuspend.

Constraints that shape the UI:

- Errors are RFC 9457 `application/problem+json` with stable `code` and
  optional `fields`.
- List endpoints use opaque `next_cursor` pagination.
- Roles are `viewer`, `member`, `admin`, `owner`. `viewer` reads; `member` and
  above mutate monitors; `admin`/`owner` manage members.
- A suspended workspace returns `workspace_suspended` for mutations.
- Monitor headers are returned masked as `***`; bodies are never returned.
  `has_body` only says whether a body exists.
- Email links point at `/verify-email?token=...` and
  `/reset-password?token=...`, based on `O1_APP_BASE_URL`.
- `GET /me` does not currently expose whether the user is an administrator.
  Plan 3 adds `user.is_admin` so the UI can show admin navigation without
  probing an admin endpoint.
- The Origin allowlist has a real bug: the README says `O1_ALLOWED_ORIGINS`
  defaults to `O1_APP_BASE_URL`, but `config.Load` leaves it empty, and
  `compose.yaml` does not forward it. Browsers send
  `Origin: http://localhost:5173` on same-origin unsafe requests, so every
  mutation through the SPA would return `403 forbidden`. Plan 3 fixes this as
  described in section 9.

## 3. Goals

1. A new user can register, verify email, log in, reset a forgotten password,
   and land in the app.
2. A user can create a workspace, switch between workspaces, and manage
   members according to their role.
3. A user can create, edit, pause/resume, and delete HTTP monitors.
4. A user can see monitor status, last check, 24h/7d uptime and latency, a
   latency chart, and a paginated raw result history.
5. An administrator can list/search workspaces and monitors, suspend and
   unsuspend workspaces, and inspect security events.
6. The UI is responsive, keyboard-accessible, and explicit about loading,
   empty, error, quota, rate-limit, and suspended states.
7. The app is same-origin in development and production. No CORS headers or
   cookie-attribute changes are needed; the existing Origin allowlist only has
   to be configured correctly.

## 4. Non-goals

- No incident timeline, acknowledge flow, alert channels, or status pages.
- No SSE/WebSocket realtime; polling with TanStack Query is enough.
- No dark mode, localization, or custom design system.
- No invite/remove-member flow beyond the existing add/update-member endpoint.
- No offline/PWA support.
- No production TLS, WAF, or full security-header work; Plan 5 owns hardening.
- No OpenAPI-generated client in this plan; handwritten typed client is
  smaller and keeps the plan focused.

## 5. Anti-goals

- A UI that shows fabricated incident or alert data before the backend has it.
- Sending masked header values (`***`) back to the API and destroying secrets.
- Hiding server errors behind generic toasts; users must see the problem code
  and field errors.
- Coupling the frontend build into the Go API binary.
- A dashboard that only works for seeded accounts; the signup and verification
  flow is part of the deliverable.

## 6. Approaches considered

### A. Separate SPA with same-origin reverse proxy (chosen)

`web/` is a Vite SPA. In development, Vite proxies `/api` to the Go API on
`:8080`. In Compose/production, a small Caddy container serves the built assets
and reverse-proxies `/api` to the `api` service.

**Trade-off:** one extra container and a proxy config, but the UI deploys
independently from the API, cookies and CSRF behavior stay unchanged, and it
matches the spec's "React SPA on the same domain" decision.

### B. Go serves the SPA

Build `web/dist` and embed or copy it into the API image; the API serves
`index.html` as a fallback.

**Trade-off:** fewer services, but every UI change rebuilds and redeploys the
API binary, static-cache semantics are worse, and frontend/backend release
cycles become coupled.

### C. Next.js SSR/BFF

**Trade-off:** adds a Node runtime, duplicates session/auth logic, and
contradicts the Vite/TanStack decision. Rejected.

**Decision:** A. The extra proxy is cheap and keeps the architecture honest.

## 7. Architecture

### 7.1 Repository layout

```text
web/
  Dockerfile
  Caddyfile
  package.json
  vite.config.ts
  tsconfig.json
  eslint.config.js
  index.html
  src/
    main.tsx
    router.tsx
    styles.css
    components/
      ui/                 # shadcn/ui primitives
      layout/             # app shell, sidebar, workspace switcher
      feedback/           # empty, error, loading, confirm dialog
    features/
      auth/
      workspaces/
      monitors/
      admin/
    lib/
      api/
        client.ts
        problems.ts
        types.ts
      query/
        keys.ts
        client.ts
      schemas/
      format.ts
    routes/               # route components if not file-based
  tests/
    unit/
    e2e/
```

Feature code owns its API calls, schemas, hooks, and route components. Shared
code only lives in `components/ui` and `lib`.

### 7.2 Stack

- React 19 + TypeScript, built with Vite.
- TanStack Router (code-based route tree is acceptable; choose whichever the
  builder can keep type-safe with the least ceremony).
- TanStack Query for all server state.
- react-hook-form + zod for forms and client validation.
- Tailwind CSS + shadcn/ui primitives.
- Recharts for the latency chart; lazy-load the chart module.
- sonner for toasts, lucide-react for icons.
- Vitest + Testing Library + MSW for unit/component tests.
- Playwright for one end-to-end smoke flow.
- ESLint + Prettier + `tsc --noEmit`.

Pin exact versions in `package-lock.json`. Use Node 24 LTS in the Dockerfile
and CI.

### 7.3 Same-origin delivery

Development:

- Vite runs on `http://localhost:5173`.
- `server.proxy['/api']` targets `http://localhost:8080`; cookies pass through
  unchanged because the browser only sees `localhost:5173`.

Compose/production:

- Add a `web` service built from `web/Dockerfile`.
- Caddy listens on port 80, serves `/srv`, falls back to `/index.html` for SPA
  routes, and proxies `/api/*` to `http://api:8080`.
- Map it as `5173:80` so `O1_APP_BASE_URL=http://localhost:5173` and email links
  keep working.
- The `api` service keeps its `8080` port for direct API/health access.

Both modes present the browser with one origin (`http://localhost:5173`), so
the session cookie stays same-origin and no CORS headers are needed. The API
must allow that origin for unsafe requests: section 9 fixes the allowlist
default and forwards it through Compose.

Caddy is used instead of nginx to match the spec's VPS choice. Plan 5 can add
TLS and stricter headers to the same Caddyfile.

### 7.4 API client

`lib/api/client.ts`:

- Base path `/api/v1`.
- `credentials: 'same-origin'`.
- Sends `Content-Type: application/json` on mutations.
- Parses `application/problem+json` into a typed `ApiProblem`
  (`type`, `title`, `status`, `detail`, `code`, `fields`).
- Treats non-problem errors as a generic `ApiProblem` with `code:
  "internal_error"`.
- A single 401 handler clears the `me` query and redirects to `/login` with a
  `redirect` search param, except on public auth routes.
- Aborts in-flight requests on route unmount via query cancellation.

No CSRF header is required. Do not add one unless the backend adds support.

### 7.5 Routing and guards

```text
/login
/register
/verify-email?token=
/forgot-password
/reset-password?token=

/                                  -> first workspace or /workspaces
/workspaces                        -> workspace list + create
/w/:workspaceSlug/monitors         -> monitor list
/w/:workspaceSlug/monitors/new     -> create form
/w/:workspaceSlug/monitors/:id     -> monitor detail
/w/:workspaceSlug/monitors/:id/edit
/w/:workspaceSlug/members          -> member list + add member

/admin                             -> redirect to /admin/workspaces
/admin/workspaces
/admin/monitors
/admin/security-events
```

Route guards:

- Public routes redirect authenticated users to `/`.
- Protected routes call `ensureQueryData(['me'])`; on 401 redirect to
  `/login?redirect=<current path>`.
- `w/:workspaceSlug` routes verify the slug is present in `me.workspaces`;
  otherwise render the not-found page (do not leak whether it exists).
- Admin routes require `me.user.is_admin`; otherwise redirect to `/`.

Workspace selection is URL-driven by slug. The last-used workspace slug is
stored in `localStorage` only to choose the redirect target from `/`; it is
never trusted for authorization.

### 7.6 Server state

Query keys:

```text
['me']
['monitors', workspaceId, cursor?]
['monitor', workspaceId, monitorId]
['results', workspaceId, monitorId, window]
['summary', workspaceId, monitorId, window]
['members', workspaceId]
['admin', 'workspaces', filters]
['admin', 'monitors', filters]
['admin', 'security-events', filters]
```

Defaults:

- `staleTime`: 10s for monitor/state data, 60s for members and admin lists.
- `refetchInterval`: 15s on monitor list/detail, 30s on results/summary.
- `refetchOnWindowFocus: true`.
- Cursor pages use `useInfiniteQuery`; the cache key excludes the cursor and
  pages are appended from `next_cursor`.
- Mutations invalidate the smallest useful key and, for pause/resume, update
  the monitor detail and list caches optimistically with rollback on error.
- Logout clears the whole query cache.

### 7.7 Layout

Authenticated shell:

- Desktop: left sidebar with workspace switcher, Monitors, Members, and an
  Admin section when `is_admin`.
- Mobile: top bar with a sheet-based navigation.
- User menu with email and Log out.
- A suspended-workspace banner appears in the shell when the last mutation
  returned `workspace_suspended`; reads remain available.
- Page-level loading skeletons, empty states with the primary action, and an
  error panel that shows the problem title, code, and retry button.

## 8. Screens

### 8.1 Auth

- **Login:** email/password; maps `invalid_credentials` and
  `email_not_verified` to specific messages; link to register and forgot
  password; honors `redirect`.
- **Register:** email/password with client-side minimum checks matching the
  backend; on success shows "check your email" with a resend action.
- **Verify email:** reads `token` from the query string, calls the endpoint,
  shows success/expired state, and links to login. Handles missing token.
- **Forgot password:** always shows the same neutral success message after
  submit, matching the anti-enumeration backend behavior.
- **Reset password:** reads `token`, asks for the new password twice, calls the
  endpoint, then links to login. Expired/used token shows a recovery path.

### 8.2 Workspace

- **Workspace list:** cards/table of workspaces with the caller's role, plus a
  create-workspace dialog.
- **Members:** table of email/role. Owners and admins can add or update a
  member by email and role. The role choices shown depend on the actor's role:
  owners can grant viewer/member/admin; admins can grant roles below their own.
  Adding an owner is impossible and is not offered. There is no remove-member
  UI because the API has no delete endpoint.

### 8.3 Monitor list

- Table/cards showing name, URL, status badge (`up`, `down`, `unknown`,
  `paused`), last check time, latency, and 24h uptime.
- Search/filter is client-side over the loaded page; server pagination via
  "Load more".
- Primary action: New monitor. Secondary row actions: view, pause/resume,
  delete.
- Pause/resume is optimistic; delete asks for confirmation.
- Empty state explains what a monitor is and links to the create form.

### 8.4 Monitor create/edit

Fields and client rules mirror the backend:

- `name` 1–100 characters.
- `url` http/https, no userinfo; server remains authoritative for SSRF.
- `method` GET/HEAD/POST/PUT/PATCH/DELETE.
- `expected_status` default `2xx`, accepts the backend's status expressions.
- `interval_seconds` 60–86400, default 60.
- `timeout_seconds` 1–30, default 10.
- `confirmation_threshold` / `recovery_threshold` 1–5, default 2.
- Headers: repeatable name/value rows; blocked/hop-by-hop headers are rejected
  by the server.
- Body + content type: body is only enabled for methods that accept one.

**Secret edit rule:** on edit, the API returns header names with `***` values
and only `has_body`. The form therefore:

1. Shows existing header names read-only and a "Replace headers" switch.
2. Sends `headers` only when the switch is on. The rows shown then are the
   complete replacement set, never the masked values.
3. Turning the switch on with zero rows sends `headers: {}`, which clears all
   headers. The UI warns that all existing headers will be removed.
4. Sends `body`/`content_type` only when the user chooses to replace or clear
   the body.
5. Omits unchanged fields entirely; the API treats missing fields as unchanged.

This prevents writing `***` back into encrypted secrets and gives users a way
to remove all headers deliberately.

Server `validation_error.fields` are mapped onto the corresponding form fields.
The backend reports SSRF blocks as `fields.url = "url is not allowed"` with the
normal `validation_error` code; the form shows that inline on the URL field. The
UI does not string-match the message or invent a separate SSRF alert. Quota,
rate-limit, and suspended errors remain page-level alerts.

### 8.5 Monitor detail

- Header: name, URL, status badge, last checked time, pause/resume, edit,
  delete.
- Summary cards for the selected window: uptime, checks, up, down, average
  latency, p95 latency.
- Window selector: 1h, 6h, 24h, 7d (matches the API).
- Latency chart from the raw results page currently loaded; for larger windows
  the chart shows the loaded page and the table offers "Load more" rather than
  pretending to show every point.
- Results table: checked at, status, HTTP status code, latency, error.
  Cursor-paginated with "Load more".
- Unknown/paused/missed probes are never shown as down; the backend summary
  already implements the uptime contract. The UI labels empty windows as
  "No data".

### 8.6 Admin

Visible only to `is_admin` users.

- **Workspaces:** search by name/slug, filter suspended/active, cursor load
  more, counts for members and monitors. Suspend dialog requires a reason;
  unsuspend asks for confirmation. Rows show suspension reason and time.
- **Monitors:** search and an optional workspace filter, status, last check,
  redacted URL from the API, cursor load more. The workspace filter is a
  searchable combobox fed by `GET /admin/workspaces?q=...`; it stores the
  selected workspace UUID. No new lookup endpoint is added.
- **Security events:** filters for kind and workspace, newest first, cursor
  load more, and a collapsible JSON detail viewer. The kind filter offers the
  five kinds the backend writes: `monitor_created`,
  `monitor_create_rate_limited`, `ssrf_blocked`, `workspace_suspended`, and
  `workspace_unsuspended`. The workspace filter uses the same combobox as the
  monitors page. Detail is already redacted by the backend; the UI must not try
  to reconstruct URLs or headers.

## 9. Backend changes required

### 9.1 Expose administrator status

Add `is_admin` to the authenticated user in `GET /api/v1/me`:

```json
{
  "user": { "id": "...", "email": "...", "is_admin": true },
  "workspaces": [ ... ]
}
```

Update the handler type, the integration test, and any client types. The value
comes from the same `users.is_admin` column the admin middleware checks.

### 9.2 Fix the Origin allowlist

`httpx.CSRF` rejects unsafe requests whose `Origin` is not in
`Config.AllowedOrigins`. Today that list is empty unless the operator sets
`O1_ALLOWED_ORIGINS`, and Compose does not forward the variable, so a browser
using the SPA gets `403 forbidden` on login, register, and every mutation.

Plan 3 makes the documented behavior real:

- In `config.Load`, when `O1_ALLOWED_ORIGINS` is empty, default
  `AllowedOrigins` to `AppBaseURL`. If `O1_ALLOWED_ORIGINS` is set, use it as
  before, so deployment can override.
- In `compose.yaml`, forward
  `O1_ALLOWED_ORIGINS: ${O1_ALLOWED_ORIGINS:-}` in the shared backend
  environment, alongside `O1_APP_BASE_URL`.
- Add a config unit test for the fallback and the explicit-override case, and
  keep the existing CSRF middleware tests passing.

No migration or other API shape changes are required for Plan 3.

### 9.3 Security-event note

The `ssrf_blocked` event written by monitor create/update is already visible in
the admin security-events list. Plan 3 does not add a new event kind or change
its detail shape.

## 10. Validation and formatting

- Zod schemas mirror backend bounds and messages closely enough that users see
  errors before a round trip; the server remains authoritative.
- Email fields are trimmed and lowercased before submit.
- Times are rendered in the browser's locale with a relative hint (for example
  "2 min ago") and an absolute timestamp in the tooltip.
- Latency is shown in ms; uptime as a percentage with one decimal.
- URLs in lists show the full member-visible URL; query strings are only
  stripped where the admin API already returns a redacted URL.
- Status colors: green up, red down, gray unknown, amber paused. Color is never
  the only signal; each badge has a text label.

## 11. Error handling

- Every API error is converted to an `ApiProblem`.
- `validation_error` with `fields` binds to form fields.
- `quota_exceeded`, `monitor_rate_limited`, `workspace_suspended`, and
  `email_not_verified` get specific, actionable copy.
- `unauthenticated` triggers the global login redirect.
- `forbidden` shows an access-denied panel, not a generic toast.
- Network failures and 5xx show a retry action and keep the previous data
  visible where possible.

## 12. Accessibility and responsiveness

- Every input has a label; dialogs trap focus and restore it on close.
- Keyboard navigation for sidebar, tables, dialogs, and form controls.
- Visible focus rings and a skip-to-content link.
- Toasts use an appropriate live region; errors are not toast-only.
- Tables become stacked cards below the `md` breakpoint.
- Touch targets are at least 44px on mobile.
- Target: no serious axe violations on the main flows; Lighthouse
  accessibility >= 90 on login, monitor list, and monitor detail.

## 13. Testing

Unit/component (Vitest + RTL + MSW):

- Problem parsing and error mapping, including `fields`.
- Zod schemas for monitor create/edit and auth forms.
- Auth screens: login error mapping, register success, verify-email
  success/expired/missing-token, forgot-password neutral response, and
  reset-password success/expired.
- Route guard logic: unauthenticated redirect, missing workspace, non-admin.
- Monitor edit secret behavior: headers/body omitted unless explicitly
  replaced; enabling replacement with zero rows sends `{}`.
- Optimistic pause/resume rollback.
- Results pagination appends pages from `next_cursor`.
- Member list and add/update form, including the role choices per actor role.
- Admin navigation hidden for non-admins.
- Admin workspaces: search, suspend dialog requiring a reason, unsuspend
  confirmation.
- Admin monitors and security events: filters, pagination, and detail viewer.

End-to-end (Playwright, one smoke path):

- Start the stack with Docker Compose and Mailpit.
- Register, read the verification link from Mailpit, verify, log in.
- Create a workspace and a monitor pointing at a public URL
  (`https://example.com`). The E2E exercises create, edit, pause, resume,
  delete, and the detail page shell.
- Promote the E2E user to administrator deterministically during setup:
  register and verify through the UI, then run a direct SQL update in the
  Compose Postgres (`UPDATE users SET is_admin = true WHERE email = $1`).
  There is no product API for this and no `.env` in CI; direct SQL avoids
  restarting the API.
- Sign in as the administrator and suspend/unsuspend the workspace.

The E2E asserts UI behavior (forms, lists, dialogs, navigation, error copy). It
does **not** wait for a probe to turn the monitor up: the Go integration tests
already cover SSRF-guarded probing and state transitions, so the E2E must not be
flaky on external network access. Detail-page rendering for up/down/unknown is
covered by component tests with MSW fixtures.

If the Mailpit test proves flaky, register the user through the API, then set
`users.email_verified_at` directly in Postgres (the same direct-SQL setup used
for `is_admin`) and continue with the login flow. Do not add a verification
bypass endpoint. The E2E flow must still cover login, monitor CRUD, and admin
suspend.

The single Playwright path is intentionally a smoke test; the remaining
acceptance flows are covered by the component suites listed above and by the
Go integration tests.

## 14. CI and quality gates

Add a `frontend` CI job:

- `npm ci`
- `npm run lint`
- `npm run typecheck`
- `npm test -- --run`
- `npm run build`
- Docker build for `web/Dockerfile`

The existing backend jobs stay unchanged apart from the new config fallback
test. Playwright runs in a separate job against the Compose stack with a
generous timeout; if it proves too slow or flaky there, it becomes a required
local pre-merge check until Plan 5 adds full E2E infrastructure. The E2E uses a
public monitor URL but never waits on probe results, so CI does not depend on
external network reachability for its assertions.

## 15. Deployment changes

- Add `web/Dockerfile` (Node build stage, Caddy runtime stage) and
  `web/Caddyfile`.
- Add a `web` service to `compose.yaml` on `5173:80`, depending on `api`.
- Keep `O1_APP_BASE_URL=http://localhost:5173`.
- Add `web` to the root README quick-start description and configuration
  notes.
- No migration is needed. The Go changes are limited to `GET /me` and the
  Origin allowlist default.

## 16. Risks and decisions left to the builder

- Exact TanStack Router style (file-based vs code-based) is the builder's call;
  both must produce a typed route tree and lazy-loaded route chunks.
- Exact component composition and visual layout are the builder's call as long
  as the screens and states above exist.
- Recharts bundle size is mitigated by lazy-loading the detail chart; the
  builder may choose a lighter chart if it preserves the latency-over-time
  view.
- The header/body replacement UX is specified as a safety rule; the exact
  control (switch plus editor, or a dedicated dialog) is the builder's call.
- `user.is_admin` is a required boolean in `/me`; adding a field is additive and
  must not break existing consumers. The UI treats a missing value as `false`
  only as a defensive fallback, not as the contract.
- The builder may add a small `GET /api/v1/workspaces` refresh on workspace
  changes, but `/me` remains the source of truth for membership and roles.

## 17. Acceptance criteria

1. `docker compose up --build` serves the SPA at `http://localhost:5173` and
   the API at `http://localhost:8080`, with the SPA proxying `/api` correctly.
2. A new user can register, verify via Mailpit, log in, create a workspace, and
   reach the monitor list without manual API calls.
3. A user can create, edit, pause, resume, and delete a monitor through the UI.
   Probe success and state transitions stay covered by backend integration
   tests, and result-state rendering by component tests; Playwright does not
   depend on an external probe succeeding.
4. Monitor detail shows the summary cards, latency chart, and paginated
   results for 1h/6h/24h/7d.
5. Members can be added with a role the actor is allowed to grant; forbidden
   actions are not offered.
6. An administrator sees admin navigation, can suspend/unsuspend a workspace
   with a reason, and can inspect monitors and security events.
7. Masked headers and bodies are never overwritten by an edit unless the user
   explicitly replaces or clears them.
8. Server problem responses are surfaced with their stable codes and field
   errors; unauthenticated requests redirect to login.
9. Browser mutations through the SPA do not receive `403 forbidden`:
   `O1_ALLOWED_ORIGINS` defaults to `O1_APP_BASE_URL`, and Compose forwards it.
10. Frontend lint, typecheck, unit tests, and build pass in CI, and the Compose
    web image builds.
11. The main flows are keyboard-accessible and usable at mobile width.

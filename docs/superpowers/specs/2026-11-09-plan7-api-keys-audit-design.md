# Plan 7 — API Keys and Audit Log Design

**Status:** Draft for approval
**Date:** 2026-11-09
**Depends on:** Plans 1–6, all merged into `main`.
**Out of scope:** 2FA, observability (metrics/tracing/Sentry), backups and
restore drills, production email, retention/rollup cleanup, load tests,
dependency/image scanning, deploy pipeline, key expiry/rotation, scopes beyond
`viewer`/`member`, `admin`/`owner` keys, IP allowlists, per-key usage analytics,
audit export/SIEM, and audit retention cleanup.

## 1. Intent

Plan 6 finished the customer-facing M2 loop. Plan 7 starts M3 production
hardening with the two things that make the system safe to automate and
accountable to operate: **workspace-scoped API keys** and a **complete,
consistent audit trail** for sensitive actions.

The key goal is least-privilege automation: an external script or dashboard can
read one workspace with a `viewer` key, or run monitor and incident automation
with a `member` key, without ever gaining workspace administration or access to
another tenant. The audit goal is that every sensitive mutation and every login
is attributable to a user or a key, with no secrets in the log.

## 2. Current state

- Sessions are opaque cookies. `auth.RequireSession` resolves the cookie,
  stores an `auth.Principal{UserID, SessionID, Email, IsAdmin}`, and every
  workspace-scoped service calls `workspaces.Authorize(userID, workspaceID,
  minRole)` against `workspace_members`.
- `RequireAdmin` is a second wrapper for global administrator routes. Admin
  routes always run inside `RequireSession`.
- `security_events` already exists: nullable `workspace_id`, nullable
  `actor_user_id`, `kind`, `detail jsonb`, `created_at`. The admin UI lists and
  filters kinds.
- Events are already recorded for monitor creation, monitor-create rate limits,
  SSRF blocks at validation and dial time, and admin workspace
  suspend/unsuspend. There are no login, role-change, monitor-deletion,
  alert-channel, status-page, or key events.
- There are no `api_keys` tables, Bearer auth, or key management endpoints/UI.
- `httpx.RateLimit` supports per-IP or global buckets only. There is no
  per-key limiter.
- `httpx.CSRF` ignores requests with no `Origin`, so non-browser Bearer clients
  are unaffected.
- `auth.Login` already receives the client IP and user agent, so login audit
  events can be recorded without changing its callers.

## 3. Goals, non-goals, anti-goals

**Goals**

1. Admins can create, list, and revoke named API keys in one workspace.
2. A key is shown once at creation and never again; only a SHA-256 hash and a
   public prefix are stored.
3. Keys authenticate with `Authorization: Bearer`, are scoped to exactly one
   workspace, and carry at most the `member` role.
4. `viewer` keys can read; `member` keys can perform monitor, incident, and
   per-monitor alert-switch automation. Neither can manage members, alert
   channels, status-page settings, API keys, or global administration.
5. Keys are rate-limited per key and update `last_used_at` at most once a minute.
6. Revocation is immediate and leaves an audit record.
7. Sensitive actions listed in §7.5 are recorded in `security_events` with actor
   user, actor key when applicable, workspace, kind, and redacted detail.
8. The admin Security events page understands the new kinds and shows whether
   an event came through an API key.
9. Existing session auth, RBAC, CSRF, rate limits, and response shapes remain
   unchanged for browser clients.

The audit scope is the event table in §7.5. Incident acknowledgement and
resolution are already recorded in `incident_events`, monitor state changes are
recorded by probes, and workspace creation is visible in the workspace row;
those are not duplicated into `security_events`. Monitor pause/resume and
monitor edits are operational changes, not credential or configuration-security
events, and are out of scope for this plan.

**Non-goals**

- 2FA, metrics/tracing/Sentry, backups, production email, retention cleanup.
- Key expiry, rotation, multiple scopes, custom permissions, IP allowlists, or
  usage analytics.
- Keys that act as `admin`/`owner` or access `/api/v1/admin/*`.
- Keys that manage workspace members or settings.
- A separate public API version or OpenAPI rewrite.
- Audit retention/deletion or export.
- Auditing every read or every key request (only `last_used_at` changes).
- Duplicating incident timeline, probe state, or workspace-creation data into
  `security_events`.

**Anti-goals**

- A key secret returned by `GET`, stored in plaintext, logged, or embedded in
  an audit detail.
- A key for workspace A reading or writing workspace B.
- A leaked key granting workspace administration.
- Bearer auth that silently falls back to a cookie when the Bearer token is
  invalid.
- An audit insert failure blocking login, monitor deletion, alert saving, or key
  revocation.
- An audit event containing a URL with a query string, a header value, a
  request body, a session token, a key secret, or an email address.

## 4. Approaches

**A. Workspace-scoped key with explicit `viewer`/`member` role, enforced in
`workspaces.Authorize` (chosen).**
`RequireAuth` resolves either a session cookie or a Bearer key. For a key it
stores a principal plus a key scope in the request context. `Authorize` checks
that scope before membership: a different workspace is reported as not found,
an insufficient role as forbidden, and the key role is returned otherwise.
Existing service signatures are unchanged, so every current tenant-scoped
service automatically enforces key scope and role.

*Why:* it is the smallest change that keeps least privilege and tenant
isolation enforceable in one place, supports read-only and automation keys, and
avoids a second authorization path per service. The cost is that `Authorize`
gains a context dependency, which is covered by explicit cross-workspace and
role tests.

**B. Read-only keys only.**
Same storage and Bearer middleware, but no role and only GET/HEAD. Simplest and
safest, but it defers the main automation use case, still needs scope
enforcement in `Authorize`, and would require a second plan for writes.

**C. Keys act as the creating user.**
The key resolves to its creator's full membership. Least code, but a leaked key
inherits the creator's role, including admin, and every other workspace they
belong to. Rejected: it violates least privilege and tenant isolation.

**Decision:** A. The key role is capped at `member`; administration stays
session-only.

## 5. Constraints

- API keys are workspace-scoped and roles are only `viewer` or `member`.
- Full key format: `o1_<12-char-prefix>_<43-char-base64url-secret>`.
  - Prefix alphabet: lowercase letters and digits, 12 characters, unique.
  - Secret: 32 random bytes from `crypto/rand`, base64url without padding.
  - Parse the full key with `strings.SplitN(raw, "_", 3)`; the secret's
    base64url alphabet includes `_`, so splitting on every underscore is wrong.
  - Store `prefix` for display/lookup and `sha256(full_key)` in `token_hash`.
  - Compare hashes with `subtle.ConstantTimeCompare`.
- The secret is returned only from `POST /api-keys`, exactly once, as
  `secret`. No other endpoint, log, error, or audit detail may contain it.
- Maximum 20 active keys per workspace. Names are 1–60 runes after trim and
  unique among active keys. Revoked keys keep their row for history.
- `last_used_at` is updated at most once per 60 seconds per key; failures are
  ignored.
- Per-key rate limit: 600 requests/minute (10 rps) with burst 60. Session
  requests are not affected by this limiter.
- Invalid, malformed, unknown, and revoked keys all return `401` `unauthenticated`
  with `WWW-Authenticate: Bearer`. When an `Authorization` header is present, a
  session cookie is never used as a fallback.
- Keys never carry `IsAdmin` and cannot reach `/api/v1/admin/*`, member
  management, API-key management, alert-channel management, or status-page
  configuration.
- `workspaces.Authorize` treats a key scoped to another workspace as
  `ErrNotFound` and an insufficient key role as `ErrForbidden`.
- Suspended workspaces reject key create/revoke and key-driven writes with the
  existing suspension behavior; key reads continue.
- Audit writes are best-effort: they never fail the primary action. Failures are
  logged at warn level with kind, workspace ID, and actor ID only.
- Audit detail must never contain secrets, tokens, raw URLs, query strings,
  headers, request bodies, passwords, or email addresses.
- UI copy is English. Every task ends with formatting, lint, typecheck, tests,
  and a commit.

## 6. Data model

Migration `00007_api_keys_and_audit.sql`:

```sql
-- +goose Up
CREATE TABLE api_keys (
  id uuid PRIMARY KEY,
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 60),
  prefix text NOT NULL UNIQUE CHECK (length(prefix) = 12),
  token_hash bytea NOT NULL UNIQUE,
  role text NOT NULL CHECK (role IN ('viewer', 'member')),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  revoked_at timestamptz
);

CREATE UNIQUE INDEX api_keys_workspace_active_name_key
  ON api_keys (workspace_id, name)
  WHERE revoked_at IS NULL;
CREATE INDEX api_keys_workspace_active_idx
  ON api_keys (workspace_id)
  WHERE revoked_at IS NULL;

ALTER TABLE security_events
  ADD COLUMN actor_api_key_id uuid REFERENCES api_keys(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE security_events DROP COLUMN actor_api_key_id;
DROP TABLE api_keys;
```

SQLC queries in a new `internal/db/queries/api_keys.sql`:

- `CreateAPIKey(id, workspace_id, name, prefix, token_hash, role, created_by)`
  returning the row.
- `GetActiveAPIKeyByPrefix(prefix)` returning the active key.
- `ListActiveAPIKeys(workspace_id)` ordered by `created_at DESC, id DESC`.
- `CountActiveAPIKeys(workspace_id)`.
- `RevokeAPIKey(id, workspace_id)` as `:execrows`, setting `revoked_at = now()`
  only where active and in the workspace.
- `TouchAPIKeyLastUsed(id)` updating `last_used_at` only when it is null or
  older than one minute.
- `GetAPIKey(id, workspace_id)` for tests and cross-workspace checks.

Update `CreateSecurityEvent` to accept `actor_api_key_id` (nullable), and update
the existing monitor, admin, and worker call sites to pass an invalid UUID.
`ListSecurityEvents` already uses `SELECT *`, so the generated row gains the new
field.

## 7. Backend design

### 7.1 Packages

New `internal/apikeys` package:

- `types.go` — API views, inputs, sentinels, validation errors.
- `service.go` — list/create/revoke.
- `handlers.go` — admin HTTP handlers.
- Tests alongside.

New `internal/audit` package:

- Kind constants and `Record`.
- No dependency on `auth`, so both `auth` and the business services can import
  it without a cycle.

`api.Dependencies` gains `APIKeyService *apikeys.Service`.

No worker or scheduler changes.

### 7.2 Key model and API views

```go
type Key struct {
    ID           uuid.UUID  `json:"id"`
    Name         string     `json:"name"`
    Prefix       string     `json:"prefix"`        // o1_<prefix>
    Role         string     `json:"role"`          // viewer | member
    CreatedBy    uuid.UUID  `json:"created_by"`
    CreatedAt    time.Time  `json:"created_at"`
    LastUsedAt   *time.Time `json:"last_used_at"`
}

type CreatedKey struct {
    Key    Key    `json:"key"`
    Secret string `json:"secret"` // returned once
}

type CreateInput struct {
    Name string `json:"name"`
    Role string `json:"role"`
}
```

Sentinels map to `workspaces.ErrNotFound`, `ErrForbidden`, `ErrValidation`,
plus `ErrSuspended`, `ErrNameTaken`, `ErrLimit`.

### 7.3 Admin API

All routes require a session and `workspaces.RoleAdmin` (owner included):

- `GET /api/v1/workspaces/{workspaceID}/api-keys` → `{"keys":[...]}` with 200.
- `POST /api/v1/workspaces/{workspaceID}/api-keys` → 201 `CreatedKey`.
- `DELETE /api/v1/workspaces/{workspaceID}/api-keys/{keyID}` → 204.

Behavior:

- `List` returns active keys only, newest first, with `prefix` rendered as
  `o1_<prefix>`; it never returns hashes or secrets. It works while suspended.
- `Create` authorizes admin, locks the workspace, rejects suspended, validates
  name 1–60 and role `viewer`/`member`, enforces the 20-key limit and active-name
  uniqueness, generates the key, inserts it, and writes `api_key_created`.
- `Revoke` authorizes admin, locks the workspace, rejects suspended, marks the
  key revoked, and writes `api_key_revoked`. Unknown or cross-workspace keys
  return 404; already revoked is idempotent 204.
- Duplicate-name unique violations map to `ErrNameTaken` (409
  `api_key_name_taken`); limit maps to 422 `api_key_limit`.
- Revoked keys disappear from `GET`.
- The full key is constructed as `o1_<prefix>_<secret>` and hashed; only the
  prefix and hash are stored.

### 7.4 Bearer authentication

Add to `auth`:

```go
type APIKeyPrincipal struct {
    ID          uuid.UUID
    WorkspaceID uuid.UUID
    Name        string
    Prefix      string
    Role        string // "viewer" or "member"; auth must not import workspaces
    CreatedBy   uuid.UUID
}

func (s *Service) AuthenticateAPIKey(ctx context.Context, raw string) (Principal, APIKeyPrincipal, error)
```

`auth` must not import `workspaces` (that would cycle through
`workspaces/handlers.go`). The key role therefore travels as a string, and
`workspaces.Authorize` parses it with `ParseRole` before comparing.

`Principal` gains no public-session fields for keys beyond the existing
`UserID` (always the key's creator, which is non-null), `Email` empty, and
`IsAdmin` false. The key scope travels separately:

```go
func APIKeyFrom(ctx context.Context) (APIKeyPrincipal, bool)
func APIKeyIDFrom(ctx context.Context) *uuid.UUID
func APIKeyRateKey(r *http.Request) string
```

`RequireAuth`:

1. If `Authorization` is present, it must be `Bearer <key>`; malformed,
   unknown, or revoked → 401 and no cookie fallback.
2. Otherwise use the existing session cookie path.
3. For a valid key, touch `last_used_at` best-effort, store the principal and
   key scope, set `WWW-Authenticate: Bearer` only on failures, and continue.
4. For a session, behavior is exactly `RequireSession` today.

The no-fallback rule applies only to `RequireAuth`. `RequireSession` remains for
routes that must never accept keys (auth routes, `GET /me`, admin routes,
member management, API-key management, alert-channel management, and
status-page configuration) and keeps its current cookie-only behavior; an
`Authorization` header on those routes is ignored.

`workspaces.Authorize` becomes:

```go
if key, ok := auth.APIKeyFrom(ctx); ok {
    if key.WorkspaceID != workspaceID {
        return RoleViewer, ErrNotFound
    }
    keyRole, err := ParseRole(key.Role)
    if err != nil {
        return RoleViewer, ErrForbidden
    }
    if !keyRole.AtLeast(min) {
        return keyRole, ErrForbidden
    }
    return keyRole, nil
}
// existing membership lookup
```

This is the single enforcement point; services keep their current signatures.

Routes switched from `requireSession` to `requireAuth` (each wrapped with the
per-key limiter):

- `GET /api/v1/workspaces/{workspaceID}`
- All `/workspaces/{workspaceID}/monitors...` routes
- All `/workspaces/{workspaceID}/incidents...` routes
- Both the plural per-channel switches
  (`/monitors/{monitorID}/alert-channels`) and the singular email switch
  (`/monitor`, `/monitors/{monitorID}/alert-channel`) endpoints. The services
  require `RoleMember` for these, so `viewer` keys receive 403; `member` keys
  can read and toggle them.
- Per-monitor switch toggles do not write `security_events`; the incident
  timeline and monitor state remain their record. Only workspace-level channel
  management is audited.

Everything else stays session-only.

Per-key limiting: add `httpx.KeyedRateLimit(keyFunc, rps, burst)` using the
existing bounded bucket store. It allows requests with an empty key (sessions)
and applies 10 rps/burst 60 to key IDs. Wrap `requireAuth` as
`requireAuth(keyLimiter(handler))`.

### 7.5 Audit

`internal/audit`:

```go
type Event struct {
    WorkspaceID    *uuid.UUID
    ActorUserID    *uuid.UUID
    ActorAPIKeyID  *uuid.UUID
    Kind           string
    Detail         map[string]any
}

func RecordBestEffort(ctx context.Context, q *sqlc.Queries, e Event)
```

`RecordBestEffort` generates a UUIDv7, marshals detail, inserts with
`CreateSecurityEvent`, and on failure logs a warning with kind, workspace ID,
and actor IDs. It never returns an error to the caller. Best-effort semantics
apply to the events added in this plan; the existing transactional events
(`monitor_created`, `workspace_suspended/unsuspended`, dial-time `ssrf_blocked`)
keep their current transaction behavior.

Kinds and detail:

| Kind | Actor | Detail |
|------|-------|--------|
| `login_succeeded` | user | `{ip}` |
| `login_failed` | null | `{ip, reason}` where reason is `invalid_credentials` or `email_unverified` |
| `member_added` | user | `{user_id, role}` |
| `member_role_changed` | user | `{user_id, previous_role, new_role}` |
| `monitor_deleted` | user or key | `{monitor_id, name}` |
| `alert_channel_created` | user | `{channel_id, kind, name}` |
| `alert_channel_updated` | user | `{channel_id, kind, name, enabled}` |
| `alert_channel_deleted` | user | `{channel_id, kind, name}` |
| `status_page_updated` | user | `{enabled, monitor_count}` |
| `api_key_created` | user | `{api_key_id, name, role, prefix}` |
| `api_key_revoked` | user | `{api_key_id, name}` |

Login attempts rejected by the auth rate limiter never reach `auth.Login` and
are **not** audited, so a password-spray campaign cannot amplify writes; they
remain visible in request logs and rate-limit behavior.

Existing kinds remain: `monitor_created`, `monitor_create_rate_limited`,
`ssrf_blocked`, `workspace_suspended`, `workspace_unsuspended`. When those
monitor events are triggered by a key, they gain `actor_api_key_id` from the
request context; the probe worker's dial-time event has none.

Instrumentation points:

- `auth.Login`: success and failure, using the IP already passed in. Never log
  the email or password.
- `workspaces.AddMember`: after a successful insert/update, record
  `member_added` or `member_role_changed` with the previous role. Skip when the
  role is unchanged.
- `monitors.Delete`: after a successful delete, record `monitor_deleted`.
- `alerts.Create/Update/Delete`: after commit, record the channel event. Never
  include config, URL, token, or signing secret.
- `statuspage.UpdateConfig`: after commit, record `status_page_updated`.
- `apikeys.Create/Revoke`: record the key lifecycle event.

API-key-driven events set `ActorAPIKeyID` from `auth.APIKeyIDFrom(ctx)` and
`ActorUserID` to the key's creator. Services do this explicitly so `audit` does
not import `auth`.

Audit writes must not include PII beyond user IDs. Names, monitor names, channel
names, prefixes, roles, and counts are allowed because they are workspace-owned
or public identifiers.

### 7.6 Admin audit views

- `admin.SecurityEvent` and the JSON view gain `actor_api_key_id`.
- `ListSecurityEvents` returns the new column automatically.
- The admin UI adds the new kinds to the filter and shows a "via API key" line
  when `actor_api_key_id` is set.
- `EventDetail` continues to render redacted JSON.

## 8. Dashboard design

### 8.1 Types, API, hooks, routes

Add to `web/src/lib/api/types.ts`:

```ts
export type APIKeyRole = 'viewer' | 'member';

export interface APIKey {
  id: string;
  name: string;
  prefix: string;
  role: APIKeyRole;
  created_by: string;
  created_at: string;
  last_used_at: string | null;
}

export interface CreatedAPIKey {
  key: APIKey;
  secret: string;
}

export interface CreateAPIKeyInput {
  name: string;
  role: APIKeyRole;
}
```

Query keys: `apiKeys(workspaceId) => ['api-keys', workspaceId]`.

`features/api-keys/api.ts`: `listAPIKeys`, `createAPIKey`, `revokeAPIKey`.
`features/api-keys/hooks.ts`: `useAPIKeys`, `useCreateAPIKey`,
`useRevokeAPIKey`; mutations invalidate `apiKeys(workspaceId)`.

`canManageAPIKeys(role)` in `lib/roles.ts` is the admin threshold.

Route `/w/$workspaceSlug/settings/api-keys`, admin-guarded, sidebar link "API
keys" in the Settings group next to Alerts and Status page.

### 8.2 API keys screen

- Heading "API keys" and a short explanation of Bearer authentication and
  workspace scope.
- Table: Name, Prefix (monospace), Role, Last used ("Never used" or relative
  time), Created, Actions.
- "Create key" button opens a dialog with Name and Role (`Viewer` default,
  `Member`). Labels exactly "Name", "Role", "Viewer", "Member", "Create key".
- After creation, the dialog switches to a one-time secret panel: the full key
  in a read-only input, "Copy key" with success toast, a warning that it will
  never be shown again, and "Done". Closing destroys it from state. The secret
  must never be written to local storage, the URL, logs, or the query cache
  beyond the create mutation result.
- Revoke uses `ConfirmDialog` with copy explaining access stops immediately and
  audit history is retained. Confirming revokes and shows a toast.
- Empty state: "No API keys yet."
- Member/viewer users cannot reach the route; the sidebar link is hidden.
- The page never renders a key secret after the creation panel closes.

### 8.3 README

Add an **API keys** section: key creation under Settings → API keys, roles and
what they allow, `Authorization: Bearer` with a curl example, one-time secret,
revocation, per-key limit, workspace scope, and the exclusions (no administration
or settings). Expand the audit section with the new event kinds and the
`actor_api_key_id` attribution.

## 9. Testing

**Backend**

- Schema: key constraints, unique active name, prefix/hash uniqueness, cascade
  on workspace delete, keys cascade on user delete, audit column FK.
- Key service: create returns a valid-format secret once and stores only its
  hash; list omits secrets/hashes; name validation/duplicate/limit; role
  validation; revoke hides the key and is idempotent; cross-workspace 404;
  suspended 403; admin/owner allowed, member/viewer forbidden.
- Auth: Bearer success; malformed/unknown/revoked → 401 with
  `WWW-Authenticate`; invalid Bearer never falls back to a valid cookie;
  session behavior unchanged; principal `IsAdmin=false`; `last_used_at`
  throttling; per-key limiter returns 429; two keys have independent buckets.
- Scope/RBAC: viewer GET 200 and viewer write 403; member GET and monitor
  create/pause/delete, incident acknowledge, and monitor alert-switch toggle
  allowed; member alert-channel/status-page/member/API-key operations 403;
  workspace A key against workspace B → 404 for reads and writes; admin routes
  401/403; suspended workspace writes rejected.
- Audit: each instrumented action writes the expected kind, workspace, actor,
  and key ID; key-driven `monitor_created`, `monitor_create_rate_limited`, and
  `ssrf_blocked` events carry the key ID while worker dial-time events do not;
  detail has no secret/URL/email; audit insert failure does not fail the
  action; login success/failure and role-change details.
- Handlers: API-key CRUD matrix and problem codes; session-only routes reject
  Bearer keys.
- Existing suites stay green.

**Web**

- API/hooks tests for paths, bodies, invalidation, and 409/422 problems.
- Screen tests: list, create dialog, role default, one-time secret panel,
  copy, revoke confirm, empty state, "Never used", admin guard.
- Security events tests: new kinds render/filter; "via API key" appears when
  `actor_api_key_id` is set.
- Router test: non-admin redirected from `settings/api-keys`.

**E2E**

- Create a `viewer` key, assert the secret is shown once, copy it, and use a
  Playwright API request with `Authorization: Bearer` to read monitors (200) and
  attempt a monitor create (403).
- Create a `member` key and use it to create a monitor (201).
- Revoke the viewer key and assert the next request is 401.
- Reload settings and assert the secret is not present anywhere.
- Assert the audit row for `api_key_created` exists through the existing psql
  helper (the test user is not a global admin).
- Keep existing smoke, alerts, incidents, status page, theme, and a11y checks
  green.

## 10. Acceptance

1. An admin can create a key, see its secret exactly once, copy it, list the key
   by prefix/role/last-used, and revoke it.
2. A `viewer` key reads its workspace but cannot write; a `member` key can run
   monitor, incident, and alert-switch automation but cannot administer the
   workspace.
3. A key for workspace A cannot read or write workspace B.
4. Revoked, unknown, and malformed keys return the same 401, and no cookie
   fallback occurs.
5. No endpoint, log, audit row, or UI after the creation panel exposes a key
   secret or hash.
6. Login success/failure, member role changes, monitor deletion, alert-channel
   changes, status-page changes, and key create/revoke appear in Security
   events with actor and redacted detail.
7. Audit failures never break the user action.
8. Existing browser sessions and all prior tests behave exactly as before.

## 11. Calls made by the builder

- One `viewer`/`member` role per key; no custom scopes and no admin keys.
- Keys are workspace-owned but `created_by` is required; deleting the creator's
  user account deletes their keys. No null-actor key writes are possible.
- Full key `o1_<prefix>_<secret>`; SHA-256 hash stored; prefix unique.
- No expiry or rotation in V1; revoke only.
- 20 active keys per workspace; names unique among active keys.
- 600/min, burst 60 per key; `last_used_at` throttled to one minute.
- Bearer takes precedence and never falls back to a cookie.
- Key-auth route set limited to workspace read, monitors, incidents, and
  per-monitor alert switches.
- Audit is best-effort and uses the existing `security_events` table; no new
  audit table.
- Login failures are audited with IP and reason, never the email.
- Audit retention/cleanup is deferred to the production-readiness plan.

## 12. Left to the builder

- Exact helper names beyond the queries and public functions above.
- Whether key generation/parsing lives in `auth` or `apikeys` as long as the
  storage and middleware contracts match.
- Component decomposition and Tailwind choices for the key screen and secret
  panel, provided labels, states, and test seams match.
- Test helper reuse (`testutil.NewPostgres`, MSW, Playwright request fixture).
- The concrete bounded-store implementation for the key limiter, following the
  existing per-IP store.

# Plan 7 — API Keys and Audit Log Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add workspace-scoped Bearer API keys with a `viewer`/`member` role and expand the audit trail with attributable, redacted security events for login, permissions, key lifecycle, monitor deletion, alert-channel changes, and status-page changes.

**Architecture:** A new `api_keys` table stores a public prefix and a SHA-256 hash; `auth.RequireAuth` resolves either a session cookie or a Bearer key and puts the key scope in the request context. `workspaces.Authorize` checks that scope before membership, so every existing service enforces tenant scope and key role without signature changes. A new `internal/audit` package writes best-effort events to the existing `security_events` table, which gains `actor_api_key_id`.

**Tech Stack:** Go 1.26, PostgreSQL 16, goose, sqlc, existing session auth, React 19, TanStack Router/Query, shadcn/ui, Vitest/MSW, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-09-plan7-api-keys-audit-design.md`

## Global Constraints

- Plan 7 scope only. No 2FA, observability, backups, production email, retention, load tests, scans, deploy pipeline, key expiry/rotation, custom scopes, admin/owner keys, IP allowlists, usage analytics, audit export, or audit retention.
- Full key format is `o1_<12-char-prefix>_<43-char-base64url-secret>`. Prefix alphabet is lowercase letters and digits, unique. Secret is 32 bytes from `crypto/rand`, base64url without padding.
- Store only `prefix` and `sha256(full_key)` in `token_hash`; compare with `subtle.ConstantTimeCompare`.
- The secret is returned only from `POST /api-keys`, once, as `secret`. No other endpoint, log, error, query cache, URL, or audit detail may contain it or the hash.
- Keys are workspace-scoped with role `viewer` or `member` only. `viewer` reads; `member` can use monitor, incident, and per-monitor alert-switch routes. Neither can manage members, API keys, alert channels, status-page settings, or global admin routes.
- At most 20 active keys per workspace; names are 1–60 runes after trim and unique among active keys. Revoked keys retain rows.
- `last_used_at` updates at most once per 60 seconds; failures are ignored.
- Per-key rate limit is 600 requests/minute (10 rps) with burst 60. Session requests are unaffected.
- Invalid, malformed, unknown, and revoked Bearer keys all return `401 unauthenticated` with `WWW-Authenticate: Bearer`. When `Authorization` is present, a session cookie is never a fallback.
- `RequireAuth` accepts keys; `RequireSession` stays cookie-only. Auth routes, `GET /me`, admin routes, member management, API-key management, alert-channel management, and status-page configuration stay session-only.
- Key auth applies `IsAdmin=false`. Admin routes cannot be reached with a key.
- A key for workspace A returns `ErrNotFound` for workspace B reads/writes; an insufficient role returns `ErrForbidden`.
- Suspended workspaces reject key create/revoke and key-driven writes with existing suspension behavior; key reads continue.
- Audit writes are best-effort for the new events: they never fail the primary action. Existing transactional events keep their behavior.
- Audit detail must never contain secrets, tokens, raw URLs, query strings, headers, bodies, passwords, or email addresses.
- Incident acknowledge/resolve stay in `incident_events`; probe state and workspace creation are not duplicated into `security_events`.
- Login attempts rejected by the auth rate limiter are not audited.
- UI copy is English. Every task ends with formatting, lint/typecheck where applicable, tests, and a commit.

## Review Focus

1. **Invalid Bearer must not fall back to a cookie.** A request with `Authorization: Bearer garbage` plus a valid session cookie must return 401, not act as the session; only `RequireSession` routes ignore the header.
2. **Tenant scope and role.** A key for workspace A must get 404 for workspace B reads and writes, and a `viewer` key must get 403 on every write, including monitor deletion, incident acknowledgement, and alert-switch toggles.
3. **Secret containment.** The plaintext key and its hash must not appear in list responses, logs, errors, the query cache after the create panel closes, audit rows, or the URL.
4. **Audit never breaks the action.** A failed audit insert must not fail login, member changes, monitor deletion, channel saves, status-page saves, or key revocation; detail must be redacted.
5. **Rate-limit and last-used isolation.** Two keys must have independent buckets, session traffic must not consume them, and `last_used_at` writes must stay throttled so a busy key does not hammer the database.

---

## File Structure

```text
backend/
  internal/db/migrations/00007_api_keys_and_audit.sql   # create
  internal/db/queries/api_keys.sql                      # create
  internal/db/queries/admin.sql                         # modify: CreateSecurityEvent param
  internal/db/sqlc/*                                    # regenerated
  internal/db/queries_test.go                           # modify: schema/query tests
  internal/audit/audit.go                               # create
  internal/audit/audit_test.go                          # create
  internal/auth/apikey.go                               # create: principal, authenticate, RequireAuth
  internal/auth/apikey_test.go                          # create
  internal/auth/session.go                              # modify: login audit events
  internal/auth/middleware.go                           # unchanged session middleware
  internal/httpx/middleware.go                          # modify: KeyedRateLimit
  internal/httpx/middleware_test.go                     # modify
  internal/workspaces/service.go                        # modify: Authorize key scope + member events
  internal/workspaces/service_test.go                   # modify
  internal/monitors/service.go                          # modify: monitor_deleted + key attribution
  internal/monitors/service_test.go                     # modify
  internal/alerts/service.go                            # modify: channel events + key attribution
  internal/alerts/service_test.go                       # modify
  internal/statuspage/service.go                        # modify: status_page_updated event
  internal/statuspage/service_test.go                   # modify
  internal/apikeys/types.go                             # create
  internal/apikeys/service.go                           # create
  internal/apikeys/handlers.go                          # create
  internal/apikeys/service_test.go                      # create
  internal/apikeys/handlers_test.go                     # create
  internal/admin/service.go                             # modify: SecurityEvent actor key
  internal/api/router.go                                # modify: dependency, routes, requireAuth
web/
  src/lib/api/types.ts                                  # modify
  src/lib/query/keys.ts                                 # modify
  src/lib/roles.ts                                      # modify: canManageAPIKeys
  src/lib/roles.test.ts                                 # modify
  src/features/api-keys/api.ts                          # create
  src/features/api-keys/api.test.ts                     # create
  src/features/api-keys/hooks.ts                        # create
  src/features/api-keys/hooks.test.tsx                  # create
  src/features/api-keys/APIKeysScreen.tsx               # create
  src/features/api-keys/APIKeysScreen.test.tsx          # create
  src/features/api-keys/CreateAPIKeyDialog.tsx          # create
  src/features/api-keys/CreateAPIKeyDialog.test.tsx     # create
  src/features/admin/api.ts                             # modify: new event kinds
  src/features/admin/SecurityEventsScreen.tsx           # modify: actor key display
  src/features/admin/SecurityEventsScreen.test.tsx      # modify
  src/router.tsx                                        # modify: settings route
  src/router.test.tsx                                   # modify: guard coverage
  src/components/layout/Sidebar.tsx                     # modify: link
  tests/e2e/smoke.spec.ts                               # modify: API key flow
  tests/e2e/helpers.ts                                  # modify: audit row assertion if needed
README.md                                               # modify: API keys + audit
```

Backend key lifecycle lives in `internal/apikeys`; authentication lives in `internal/auth`; audit writes live in `internal/audit`; SQL sits beside the existing query files. The dashboard feature folder owns key API calls, hooks, and screens.

---

### Task 1: API key schema, queries, and audit column

**Files:**
- Create: `backend/internal/db/migrations/00007_api_keys_and_audit.sql`
- Create: `backend/internal/db/queries/api_keys.sql`
- Modify: `backend/internal/db/queries/admin.sql`
- Modify: `backend/internal/db/queries_test.go`
- Modify: `backend/internal/monitors/service.go`, `backend/internal/admin/service.go`, `backend/internal/worker/probe_worker.go` (compile fix for the new audit param)
- Generate: `backend/internal/db/sqlc/*`

**Interfaces:**
- Consumes: existing `workspaces`, `users`, `security_events`.
- Produces:
  - `sqlc.APIKey{ID, WorkspaceID uuid.UUID; Name, Prefix string; TokenHash []byte; Role string; CreatedBy uuid.UUID; CreatedAt time.Time; LastUsedAt, RevokedAt *time.Time}`
  - `CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{ID, WorkspaceID uuid.UUID, Name, Prefix string, TokenHash []byte, Role string, CreatedBy uuid.UUID}) (sqlc.APIKey, error)`
  - `GetActiveAPIKeyByPrefix(ctx, prefix string) (sqlc.APIKey, error)`
  - `GetAPIKey(ctx, sqlc.GetAPIKeyParams{ID, WorkspaceID uuid.UUID}) (sqlc.APIKey, error)`
  - `ListActiveAPIKeys(ctx, workspaceID uuid.UUID) ([]sqlc.APIKey, error)`
  - `CountActiveAPIKeys(ctx, workspaceID uuid.UUID) (int64, error)`
  - `RevokeAPIKey(ctx, sqlc.RevokeAPIKeyParams{ID, WorkspaceID uuid.UUID}) (int64, error)`
  - `TouchAPIKeyLastUsed(ctx, id uuid.UUID) error`
  - `CreateSecurityEventParams` gains `ActorAPIKeyID pgtype.UUID`; `sqlc.SecurityEvent` gains `ActorAPIKeyID pgtype.UUID`.

- [ ] **Step 1: Write the failing schema test**

Append to `backend/internal/db/queries_test.go` (package `db_test`):

```go
func TestAPIKeySchema_ConstraintsAndCascades(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()

	userID := testutil.CreateVerifiedUser(t, pool, "apikey-schema@example.com", "correct horse battery staple")
	keyOwner := testutil.CreateVerifiedUser(t, pool, "apikey-owner@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1,'Keys','keys-schema',$2)`, wsID, userID); err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(pool)
	keyID := uuid.New()
	hash := auth.HashToken("o1_abcdefghijkl_secret")
	key, err := q.CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{
		ID: keyID, WorkspaceID: wsID, Name: "CI", Prefix: "abcdefghijkl",
		TokenHash: hash, Role: "viewer", CreatedBy: keyOwner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if key.Prefix != "abcdefghijkl" || key.Role != "viewer" || key.RevokedAt != nil {
		t.Fatalf("key = %+v", key)
	}

	if _, err := q.CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{
		ID: uuid.New(), WorkspaceID: wsID, Name: "CI", Prefix: "mnopqrstuvwx",
		TokenHash: auth.HashToken("o1_mnopqrstuvwx_secret"), Role: "member", CreatedBy: keyOwner,
	}); err == nil {
		t.Fatal("duplicate active name was accepted")
	}

	if rows, err := q.RevokeAPIKey(ctx, sqlc.RevokeAPIKeyParams{ID: keyID, WorkspaceID: wsID}); err != nil || rows != 1 {
		t.Fatalf("revoke rows = %d, err = %v", rows, err)
	}
	if _, err := q.GetActiveAPIKeyByPrefix(ctx, "abcdefghijkl"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("revoked key lookup error = %v, want pgx.ErrNoRows", err)
	}

	// A revoked name can be reused.
	if _, err := q.CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{
		ID: uuid.New(), WorkspaceID: wsID, Name: "CI", Prefix: "zzzzzzzzzzzz",
		TokenHash: auth.HashToken("o1_zzzzzzzzzzzz_secret"), Role: "viewer", CreatedBy: keyOwner,
	}); err != nil {
		t.Fatalf("reuse revoked name: %v", err)
	}

	// Security events can attribute an actor key.
	if _, err := pool.Exec(ctx, `INSERT INTO security_events (id, workspace_id, actor_user_id, actor_api_key_id, kind, detail) VALUES ($1,$2,$3,$4,'api_key_created','{}')`, uuid.New(), wsID, keyOwner, keyID); err != nil {
		t.Fatal(err)
	}

	// Deleting the key's creator deletes their keys.
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, keyOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetAPIKey(ctx, sqlc.GetAPIKeyParams{ID: keyID, WorkspaceID: wsID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("key after user delete error = %v, want pgx.ErrNoRows", err)
	}
}
```

Add `"o1/internal/auth"` to the test imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/db -run TestAPIKeySchema -v`
Expected: FAIL because the table, column, and generated methods do not exist.

- [ ] **Step 3: Add migration `00007_api_keys_and_audit.sql`**

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

- [ ] **Step 4: Add `api_keys.sql` queries**

Use the exact query list in the design §6. `RevokeAPIKey` is `:execrows` and updates only `revoked_at = now()` where `id`, `workspace_id`, and `revoked_at IS NULL` match. `TouchAPIKeyLastUsed` updates only when `last_used_at IS NULL OR last_used_at < now() - interval '1 minute'`.

- [ ] **Step 5: Extend `CreateSecurityEvent`**

In `backend/internal/db/queries/admin.sql` change the insert to include `actor_api_key_id` as the sixth value and `sqlc.arg('actor_api_key_id')`. Regenerate sqlc. Update every existing call site (`monitors/service.go`, `admin/service.go`, `worker/probe_worker.go`) to pass `ActorAPIKeyID: pgtype.UUID{}`. Update `admin.SecurityEvent` later in Task 3 when the field is used.

- [ ] **Step 6: Regenerate and run**

Run: `cd backend && sqlc generate && go test ./internal/db -run TestAPIKeySchema -v`
Expected: PASS.

- [ ] **Step 7: Quality gate and commit**

Run: `cd backend && gofmt -w internal/db internal/monitors internal/admin internal/worker && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/db backend/internal/monitors/service.go backend/internal/admin/service.go backend/internal/worker/probe_worker.go
git commit -m "feat: add API key schema and audit key attribution"
```

---

### Task 2: Bearer authentication, key scope, and per-key rate limit

**Files:**
- Create: `backend/internal/auth/apikey.go`
- Create: `backend/internal/auth/apikey_test.go`
- Modify: `backend/internal/httpx/middleware.go`
- Modify: `backend/internal/httpx/middleware_test.go`
- Modify: `backend/internal/workspaces/service.go`
- Modify: `backend/internal/workspaces/service_test.go`

**Interfaces:**
- Consumes: Task 1 `GetActiveAPIKeyByPrefix`, `TouchAPIKeyLastUsed`, `auth.HashToken`.
- Produces:
  - `auth.APIKeyPrincipal{ID, WorkspaceID uuid.UUID; Name, Prefix, Role string; CreatedBy uuid.UUID}`.
  - `auth.AuthenticateAPIKey(ctx, raw string) (Principal, APIKeyPrincipal, error)`.
  - `auth.RequireAuth(svc *Service) func(http.Handler) http.Handler`.
  - `auth.APIKeyFrom(ctx) (APIKeyPrincipal, bool)`, `auth.APIKeyIDFrom(ctx) *uuid.UUID`, `auth.APIKeyRateKey(r *http.Request) string`, `auth.ContextWithAPIKey(ctx, APIKeyPrincipal) context.Context`.
  - `httpx.KeyedRateLimit(key func(*http.Request) string, rps float64, burst int) func(http.Handler) http.Handler`.

- [ ] **Step 1: Write the failing auth tests**

Create `backend/internal/auth/apikey_test.go` in `package auth_test` using `testutil.NewPostgres`. Include a helper that inserts a key directly:

```go
func insertTestKey(t *testing.T, pool *pgxpool.Pool, userID, workspaceID uuid.UUID, role, prefix, raw string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO api_keys (id, workspace_id, name, prefix, token_hash, role, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		id, workspaceID, "Test key "+prefix, prefix, auth.HashToken(raw), role, userID); err != nil {
		t.Fatal(err)
	}
	return id
}

func createTestWorkspace(t *testing.T, pool *pgxpool.Pool, owner uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO workspaces (id, name, slug, created_by)
		VALUES ($1, 'API key test', $2, $3)`,
		id, "api-key-"+id.String()[:8], owner); err != nil {
		t.Fatal(err)
	}
	return id
}
```

Tests:

```go
func TestAuthenticateAPIKey_SuccessAndRevoked(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	u := testutil.CreateVerifiedUser(t, pool, "apikey-auth@example.com", testPassword)
	ws := createTestWorkspace(t, pool, u)
	raw := "o1_abcdefghijkl_" + strings.Repeat("a", 43)
	keyID := insertTestKey(t, pool, u, ws, "member", "abcdefghijkl", raw)

	principal, key, err := svc.AuthenticateAPIKey(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if principal.UserID != u || principal.IsAdmin || principal.SessionID != uuid.Nil {
		t.Fatalf("principal = %+v", principal)
	}
	if key.ID != keyID || key.WorkspaceID != ws || key.Role != "member" || key.Prefix != "abcdefghijkl" {
		t.Fatalf("key = %+v", key)
	}

	if _, err := pool.Exec(context.Background(), `UPDATE api_keys SET revoked_at=now() WHERE id=$1`, keyID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.AuthenticateAPIKey(context.Background(), raw); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("revoked error = %v", err)
	}
	if _, _, err := svc.AuthenticateAPIKey(context.Background(), "not-a-key"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("malformed error = %v", err)
	}
}

func TestRequireAuth_BearerAndNoCookieFallback(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	u := testutil.CreateVerifiedUser(t, pool, "apikey-middleware@example.com", testPassword)
	ws := createTestWorkspace(t, pool, u)
	raw := "o1_abcdefghijkl_" + strings.Repeat("b", 43)
	insertTestKey(t, pool, u, ws, "viewer", "abcdefghijkl", raw)
	session, _, err := svc.Login(context.Background(), "apikey-middleware@example.com", testPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}

	h := auth.RequireAuth(svc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := auth.APIKeyFrom(r.Context())
		if !ok || key.WorkspaceID != ws {
			t.Fatalf("key context = %+v, %v", key, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+ws.String()+"/monitors", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("valid key status = %d", rec.Code)
	}

	// A bad Bearer token must not fall back to a valid session cookie.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+ws.String()+"/monitors", nil)
	req.Header.Set("Authorization", "Bearer garbage")
	req.AddCookie(&http.Cookie{Name: "o1_session", Value: session})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("bad bearer status = %d, header = %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}
```

Add:
- `TestRequireAuth_SessionStillWorks`: no Authorization + session cookie → 204 and principal present.
- `TestAuthenicateAPIKey_TouchesLastUsedOnce` (or service test): first call sets `last_used_at`, a second immediate call does not change it. Pin with a query before/after.
- `TestRequireAuth_KeyPrincipalIsNotAdmin`.

- [ ] **Step 2: Write the failing workspace scope test**

Append to `backend/internal/workspaces/service_test.go`:

```go
func TestAuthorize_APIKeyScopeAndRole(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	owner := testutil.CreateVerifiedUser(t, pool, "key-scope@example.com", "correct horse battery staple")
	svc := workspaces.NewService(pool, sqlc.New(pool))
	ws, err := svc.Create(context.Background(), owner, "Key Scope")
	if err != nil {
		t.Fatal(err)
	}

	keyCtx := auth.ContextWithAPIKey(context.Background(), auth.APIKeyPrincipal{
		ID: uuid.New(), WorkspaceID: ws.ID, Role: "viewer", CreatedBy: owner,
	})
	if _, err := svc.Authorize(keyCtx, owner, ws.ID, workspaces.RoleViewer); err != nil {
		t.Fatalf("viewer read: %v", err)
	}
	if _, err := svc.Authorize(keyCtx, owner, ws.ID, workspaces.RoleMember); !errors.Is(err, workspaces.ErrForbidden) {
		t.Fatalf("viewer write error = %v, want ErrForbidden", err)
	}

	otherKeyCtx := auth.ContextWithAPIKey(context.Background(), auth.APIKeyPrincipal{
		ID: uuid.New(), WorkspaceID: uuid.New(), Role: "member", CreatedBy: owner,
	})
	if _, err := svc.Authorize(otherKeyCtx, owner, ws.ID, workspaces.RoleViewer); !errors.Is(err, workspaces.ErrNotFound) {
		t.Fatalf("cross-workspace error = %v, want ErrNotFound", err)
	}
}
```

Add `"o1/internal/auth"` and `"github.com/google/uuid"` to the test imports; keep the package as `workspaces_test`.

- [ ] **Step 3: Write the failing keyed-limiter test**

Append to `backend/internal/httpx/middleware_test.go`:

```go
func TestKeyedRateLimit_IsolatesKeys(t *testing.T) {
	h := KeyedRateLimit(func(r *http.Request) string {
		return r.Header.Get("X-Test-Key")
	}, 1, 2)(okHandler())

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Test-Key", "a")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("key a request %d status = %d", i, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Test-Key", "a")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("key a third status = %d, want 429", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Test-Key", "b")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("key b status = %d, want 200", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty key status = %d, want 200", rec.Code)
	}
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/auth ./internal/workspaces ./internal/httpx -run 'TestAuthenticateAPIKey|TestRequireAuth|TestAuthorize_APIKey|TestKeyedRateLimit' -v`
Expected: FAIL because the functions do not exist.

- [ ] **Step 5: Implement `auth/apikey.go`**

Pin:
- Parse `o1_<prefix>_<secret>` with `strings.SplitN(raw, "_", 3)` because the
  base64url secret may itself contain `_`. Require exactly three parts, prefix
  length 12 and lowercase alphanumeric, secret non-empty. Malformed →
  `ErrUnauthenticated`.
- `AuthenticateAPIKey` looks up `GetActiveAPIKeyByPrefix`, computes `HashToken(raw)`, compares with `subtle.ConstantTimeCompare`, and returns `Principal{UserID: key.CreatedBy, IsAdmin: false}` plus `APIKeyPrincipal{ID, WorkspaceID, Name, Prefix, Role, CreatedBy}`.
- On success, call `TouchAPIKeyLastUsed` and ignore its error.
- `APIKeyFrom`, `APIKeyIDFrom`, `APIKeyRateKey` read the context/request. `APIKeyIDFrom` returns `nil` when absent.
- `ContextWithAPIKey` stores the principal. `RequireAuth`: if `Authorization` header is present, parse only Bearer; any failure → 401 with `WWW-Authenticate: Bearer`; never read the cookie. If absent, delegate to the existing session lookup path (either call `RequireSession` or reproduce its cookie resolution). Set `IsAdmin=false` for keys.
- 401 uses the existing `unauthenticated` problem code.

- [ ] **Step 6: Implement `httpx.KeyedRateLimit`**

Reuse the bounded bucket store. The key function returns `""` for sessions; empty keys are always allowed. Use the existing 429 problem and `Retry-After`. Do not share state with the per-IP limiter.

- [ ] **Step 7: Implement key scope in `workspaces.Authorize`**

Before the membership lookup:

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
```

`workspaces` may import `auth`; `auth` must not import `workspaces`.

- [ ] **Step 8: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/auth ./internal/workspaces ./internal/httpx -run 'TestAuthenticateAPIKey|TestRequireAuth|TestAuthorize_APIKey|TestKeyedRateLimit' -v`
Expected: PASS.

- [ ] **Step 9: Quality gate and commit**

Run: `cd backend && gofmt -w internal/auth internal/httpx internal/workspaces && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/auth backend/internal/httpx backend/internal/workspaces
git commit -m "feat: add Bearer API key authentication and key-scoped authorization"
```

---

### Task 3: Audit package and event instrumentation

**Files:**
- Create: `backend/internal/audit/audit.go`
- Create: `backend/internal/audit/audit_test.go`
- Modify: `backend/internal/auth/session.go`
- Modify: `backend/internal/auth/session_test.go`
- Modify: `backend/internal/workspaces/service.go`
- Modify: `backend/internal/workspaces/service_test.go`
- Modify: `backend/internal/monitors/service.go`
- Modify: `backend/internal/monitors/service_test.go`
- Modify: `backend/internal/alerts/service.go`
- Modify: `backend/internal/alerts/service_test.go`
- Modify: `backend/internal/statuspage/service.go`
- Modify: `backend/internal/statuspage/service_test.go`
- Modify: `backend/internal/admin/service.go`
- Test: existing service/package tests

**Interfaces:**
- Consumes: Task 1 `CreateSecurityEvent` with `ActorAPIKeyID`, Task 2 `auth.APIKeyIDFrom`.
- Produces:
  - `audit.Event{WorkspaceID, ActorUserID, ActorAPIKeyID *uuid.UUID; Kind string; Detail map[string]any}`.
  - `audit.RecordBestEffort(ctx context.Context, q *sqlc.Queries, e Event)`.
  - Kind constants for every event in the design table.

- [ ] **Step 1: Write the failing audit tests**

Create `backend/internal/audit/audit_test.go` in `package audit_test`:

```go
func TestRecordBestEffort_WritesRedactedEvent(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()
	userID := testutil.CreateVerifiedUser(t, pool, "audit@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1,'Audit','audit',$2)`, wsID, userID); err != nil {
		t.Fatal(err)
	}

	audit.RecordBestEffort(ctx, sqlc.New(pool), audit.Event{
		WorkspaceID: &wsID, ActorUserID: &userID,
		Kind: audit.KindMonitorDeleted,
		Detail: map[string]any{"monitor_id": uuid.New().String(), "name": "API"},
	})

	var kind string
	var detail []byte
	if err := pool.QueryRow(ctx, `SELECT kind, detail FROM security_events WHERE workspace_id=$1`, wsID).Scan(&kind, &detail); err != nil {
		t.Fatal(err)
	}
	if kind != audit.KindMonitorDeleted || !strings.Contains(string(detail), "API") {
		t.Fatalf("event = %q %s", kind, detail)
	}
}

func TestRecordBestEffort_NeverPanicsOnFailure(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	audit.RecordBestEffort(ctx, sqlc.New(pool), audit.Event{Kind: audit.KindLoginFailed})
}
```

Add a service-level test for each instrumented action (query `security_events` for kind/workspace/actor/detail):
- auth login success and failure; failed detail has `ip` and reason, no email.
- workspace AddMember new → `member_added`, role change → `member_role_changed` with previous role, same role → no event.
- monitor Delete → `monitor_deleted` with name.
- alerts Create/Update/Delete → matching kinds and no URL/token/signing secret in detail.
- status page UpdateConfig → `status_page_updated` with enabled and monitor count.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/audit ./internal/auth ./internal/workspaces ./internal/monitors ./internal/alerts ./internal/statuspage -run 'TestRecordBestEffort|TestAudit' -v`
Expected: FAIL because `internal/audit` does not exist and no events are written.

- [ ] **Step 3: Implement `audit`**

Pin:
- Kind constants: `KindLoginSucceeded`, `KindLoginFailed`, `KindMemberAdded`, `KindMemberRoleChanged`, `KindMonitorDeleted`, `KindAlertChannelCreated`, `KindAlertChannelUpdated`, `KindAlertChannelDeleted`, `KindStatusPageUpdated`, `KindAPIKeyCreated`, `KindAPIKeyRevoked`.
- `RecordBestEffort` generates a UUIDv7, marshals detail (empty map if nil), inserts with `CreateSecurityEvent`, and on any error logs a warning with kind, workspace ID, and actor IDs. It never panics or returns an error.
- Do not import `auth`; callers set `ActorAPIKeyID` from `auth.APIKeyIDFrom(ctx)`.

- [ ] **Step 4: Instrument login**

In `auth/session.go`, on `ErrInvalidCredentials` and `ErrEmailNotVerified`, call `audit.RecordBestEffort` with the IP normalized and a fixed reason; no email, password, or user ID on unknown accounts. On success, record `login_succeeded` with the user ID and IP. Do not change the return values or ordering. Rate-limited login attempts never reach this function and are not audited.

- [ ] **Step 5: Instrument member changes**

In `workspaces.AddMember`, after a successful upsert:
- If the target had no membership → `member_added`.
- If the role changed → `member_role_changed` with `previous_role` and `new_role`.
- If the role is identical → no event.
Use best-effort recording after the upsert.

- [ ] **Step 6: Instrument monitor deletion and key attribution**

In `monitors.Delete`, load the monitor with `q.GetMonitor` before deleting (map `pgx.ErrNoRows` to `ErrNotFound`) so the event can include its name, then record `monitor_deleted` after commit. In `insertEvent`, set `ActorAPIKeyID: auth.APIKeyIDFrom(ctx)` so `monitor_created`, `monitor_create_rate_limited`, and `ssrf_blocked` are attributed when key-driven.

- [ ] **Step 7: Instrument alert channels and status page**

In `alerts.Create/Update/Delete`, record the matching event after commit with ID, kind, name, and enabled where applicable. Never include config, URL, token, or signing secret. In `statuspage.UpdateConfig`, record `status_page_updated` after commit with enabled and monitor count.

- [ ] **Step 8: Extend the admin view**

Add `ActorAPIKeyID *uuid.UUID` to `admin.SecurityEvent` and map it from the generated row. The handler/JSON shape gains `actor_api_key_id`.

- [ ] **Step 9: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/audit ./internal/auth ./internal/workspaces ./internal/monitors ./internal/alerts ./internal/statuspage ./internal/admin -v`
Expected: PASS.

- [ ] **Step 10: Quality gate and commit**

Run: `cd backend && gofmt -w internal/audit internal/auth internal/workspaces internal/monitors internal/alerts internal/statuspage internal/admin && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/audit backend/internal/auth backend/internal/workspaces backend/internal/monitors backend/internal/alerts backend/internal/statuspage backend/internal/admin
git commit -m "feat: audit sensitive actions with API key attribution"
```

---

### Task 4: API key service, admin API, and route matrix

**Files:**
- Create: `backend/internal/apikeys/types.go`
- Create: `backend/internal/apikeys/service.go`
- Create: `backend/internal/apikeys/handlers.go`
- Create: `backend/internal/apikeys/service_test.go`
- Create: `backend/internal/apikeys/handlers_test.go`
- Modify: `backend/internal/api/router.go`

**Interfaces:**
- Consumes: Task 1 queries, Task 2 auth/limiter, Task 3 audit.
- Produces:
  - `apikeys.Service`, `NewService(pool, q, ws) *Service`.
  - `List(ctx, actorID, workspaceID) ([]Key, error)`.
  - `Create(ctx, actorID, workspaceID, in CreateInput) (CreatedKey, error)`.
  - `Revoke(ctx, actorID, workspaceID, keyID) error`.
  - Handlers `ListHandler`, `CreateHandler`, `RevokeHandler`.
  - `api.Dependencies.APIKeyService *apikeys.Service`.

- [ ] **Step 1: Write the failing service tests**

Create `backend/internal/apikeys/service_test.go` in `package apikeys` with an env helper like the alerts/statuspage service tests. Tests:

- `TestService_CreateKey`: creates a `viewer` key; the returned secret matches `^o1_[a-z0-9]{12}_[A-Za-z0-9_-]{43}$`; the returned `Prefix` is `o1_<prefix>`; the stored `token_hash` equals `auth.HashToken(secret)` and is not equal to the secret; `last_used_at` and `revoked_at` are nil.
- `TestService_ListAndRevoke`: list returns only active keys, newest first, no secret/hash; revoke removes it from the list and zeroes `CountActiveAPIKeys`; revoking again is idempotent.
- `TestService_CreateValidation`: blank/61-rune name, role `admin`, 21st active key → correct sentinel/field error. Duplicate active name → `ErrNameTaken`; revoked name can be reused.
- `TestService_RolesAndSuspension`: member/viewer forbidden; admin/owner allowed; suspended create/revoke → `ErrSuspended`, list still works.
- `TestService_CrossWorkspace`: key ID from workspace B → `ErrNotFound`.
- `TestService_AuditEvents`: create writes `api_key_created` with id/name/role/prefix and no secret; revoke writes `api_key_revoked`.

- [ ] **Step 2: Run the service tests to verify they fail**

Run: `cd backend && go test ./internal/apikeys -run TestService_ -v`
Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement types and service**

Pin:
- `Key{ID, Name, Prefix, Role, CreatedBy uuid.UUID, CreatedAt time.Time, LastUsedAt *time.Time}` with JSON tags from the design; `Prefix` is rendered `o1_<stored prefix>`; `CreatedBy` is non-null.
- `CreatedKey{Key Key, Secret string}`.
- Sentinels `ErrNotFound`, `ErrForbidden`, `ErrValidation`, `ErrSuspended`, `ErrNameTaken`, `ErrLimit`; `ValidationError` like alerts.
- Key generation: 9 random bytes → 12-char lowercase base36/62 prefix; 32 random bytes → base64url secret; full key `o1_<prefix>_<secret>`; retry prefix collisions up to 5 times.
- `Create` authorizes admin, locks the workspace, rejects suspended, trims/validates name 1–60, validates role `viewer|member`, counts active keys (limit 20), inserts with a UUIDv7 and `auth.HashToken(fullKey)`, commits, records `api_key_created`, and returns the secret once.
- `List` authorizes admin, returns active keys with `o1_` prefix.
- `Revoke` authorizes admin, locks the workspace, rejects suspended, calls `RevokeAPIKey`; zero rows maps to `ErrNotFound`; commits and records `api_key_revoked`.

- [ ] **Step 4: Write the failing handler and matrix tests**

Create `backend/internal/apikeys/handlers_test.go` in `package apikeys_test` (to avoid the `api` import cycle). Build a router with an explicit `APIKeyService` and session clients for owner/admin/member/viewer. Tests:

- `TestAPIKeyHandlers_CRUD`: owner GET 200 with defaults; POST 201 returns `secret` once; GET list omits it; DELETE 204; member/viewer 403.
- `TestAPIKeyHandlers_SecretNeverReturnedAgain`: create, then read the list and problem bodies; assert the secret and hash are absent.
- `TestAPIKeyHandlers_Validation`: bad name/role → 400 with `fields`; duplicate → 409 `api_key_name_taken`; 21st → 422 `api_key_limit`.
- `TestAPIKeyHandlers_SuspendedAndCrossWorkspace`: suspended POST/DELETE 403 `workspace_suspended`; cross-workspace key 404.
- `TestAPIKeyHandlers_AuthMatrix`: create a viewer and a member key through the service, then:
  - viewer GET monitors 200; viewer POST monitors 403.
  - member POST monitors 201; member PATCH monitor alert switch 204; member acknowledge incident 204.
  - member GET alert-channels 401 (session-only); member PUT status-page 401 (session-only); member GET members 401 (session-only); member GET `/api/v1/admin/workspaces` 401 (session-only).
  - workspace A key on workspace B monitors 404 for GET and POST.
  - revoked key 401; malformed key 401; valid session cookie without Authorization still works.
  - per-key limiter: 21 rapid requests with one key pass the first 20 and 429 the 21st; a second key still succeeds.
- `TestAPIKeyHandlers_AuditAttribution`: a member key creates a monitor; assert `monitor_created` has `actor_api_key_id` set and `actor_user_id` is the key creator.

- [ ] **Step 5: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/apikeys -run 'TestAPIKeyHandlers' -v`
Expected: FAIL because handlers/routes do not exist.

- [ ] **Step 6: Implement handlers and router**

Pin:
- `GET` → `{"keys":[...]}` 200; `POST` → 201 `CreatedKey`; `DELETE` → 204. Decode with `DisallowUnknownFields`.
- Error map: `*ValidationError` → 400 `validation_error` with fields; `ErrNameTaken` → 409 `api_key_name_taken`; `ErrLimit` → 422 `api_key_limit`; `ErrSuspended` → 403 `workspace_suspended`; `ErrNotFound` → 404; `ErrForbidden` → 403.
- Add `APIKeyService` to `Dependencies`; build when nil and Pool non-nil.
- Register admin key routes with `requireSession`.
- Add `requireAuth := auth.RequireAuth(authSvc)` and `keyLimiter := httpx.KeyedRateLimit(auth.APIKeyRateKey, 10, 60)`.
- Replace `requireSession` with `requireAuth(keyLimiter(handler))` on:
  - `GET /api/v1/workspaces/{workspaceID}`
  - all `/workspaces/{workspaceID}/monitors...`
  - all `/workspaces/{workspaceID}/incidents...`
  - both plural and singular monitor alert-switch routes.
- Keep every other route on `requireSession`, including alert-channel management, status-page config, members, API-key management, and admin.

- [ ] **Step 7: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/apikeys ./internal/api ./internal/auth ./internal/workspaces -v`
Expected: PASS.

- [ ] **Step 8: Quality gate and commit**

Run: `cd backend && gofmt -w internal/apikeys internal/api && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/apikeys backend/internal/api/router.go
git commit -m "feat: add API key management and key-authenticated routes"
```

---

### Task 5: Dashboard API key types, API, and hooks

**Files:**
- Modify: `web/src/lib/api/types.ts`
- Modify: `web/src/lib/query/keys.ts`
- Modify: `web/src/lib/roles.ts`
- Modify: `web/src/lib/roles.test.ts`
- Create: `web/src/features/api-keys/api.ts`
- Create: `web/src/features/api-keys/api.test.ts`
- Create: `web/src/features/api-keys/hooks.ts`
- Create: `web/src/features/api-keys/hooks.test.tsx`

**Interfaces:**
- Consumes: Task 4 API paths and shapes.
- Produces:
  - Types `APIKeyRole`, `APIKey`, `CreatedAPIKey`, `CreateAPIKeyInput` exactly as in the design §8.1.
  - `listAPIKeys`, `createAPIKey`, `revokeAPIKey`.
  - `useAPIKeys`, `useCreateAPIKey`, `useRevokeAPIKey`.
  - `canManageAPIKeys(role)`; query key `apiKeys(workspaceId)`.

- [ ] **Step 1: Write the failing API/hook/role tests**

`api.test.ts`:
- `listAPIKeys` GETs `/workspaces/w1/api-keys`.
- `createAPIKey` POSTs `{name, role}` and resolves `{key, secret}`.
- `revokeAPIKey` DELETEs `/workspaces/w1/api-keys/k1` and accepts 204.
- A 409 problem surfaces `api_key_name_taken`.

`hooks.test.tsx`:
- `useAPIKeys` uses `queryKeys.apiKeys('w1')`.
- create/revoke invalidate that key.
- `canManageAPIKeys` true for admin/owner, false for member/viewer.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/features/api-keys src/lib/roles.test.ts`
Expected: FAIL because the types, functions, hooks, and helper do not exist.

- [ ] **Step 3: Implement types, keys, API, hooks, role helper**

Pin:
- Type shapes exactly as the design §8.1; `created_by` is `string` (non-null).
- Query key `apiKeys(workspaceId) => ['api-keys', workspaceId]`.
- `useAPIKeys` staleTime 30s; create/revoke invalidate `apiKeys(workspaceId)`.
- `canManageAPIKeys = roleAtLeast(role, 'admin')`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npm test -- --run src/features/api-keys src/lib/roles.test.ts`
Expected: PASS.

- [ ] **Step 5: Quality gate and commit**

Run: `cd web && npm run format && npm run lint && npm run typecheck && npm test -- --run`
Commit:

```bash
git add web/src/lib web/src/features/api-keys
git commit -m "feat: add API key dashboard data layer"
```

---

### Task 6: API keys settings screen and route

**Files:**
- Create: `web/src/features/api-keys/APIKeysScreen.tsx`
- Create: `web/src/features/api-keys/APIKeysScreen.test.tsx`
- Create: `web/src/features/api-keys/CreateAPIKeyDialog.tsx`
- Create: `web/src/features/api-keys/CreateAPIKeyDialog.test.tsx`
- Modify: `web/src/router.tsx`
- Modify: `web/src/router.test.tsx`
- Modify: `web/src/components/layout/Sidebar.tsx`

**Interfaces:**
- Consumes: Task 5 hooks and types.
- Produces:
  - `APIKeysScreen({ workspace }: { workspace: Workspace })`.
  - `CreateAPIKeyDialog({ workspaceId, onClose })`.
  - Route `/w/$workspaceSlug/settings/api-keys`, admin-guarded.
  - Sidebar link "API keys".

- [ ] **Step 1: Write the failing dialog tests**

`CreateAPIKeyDialog.test.tsx`:
- Renders Name, Role (Viewer default), and Create key.
- Switching to Member submits `{name:'CI', role:'member'}`.
- On success, shows the secret in a read-only input, "Copy key", and the one-time warning; clicking Copy writes to `navigator.clipboard` and shows `toast.success('API key copied')`.
- Closing and reopening never shows the secret again.
- A 409 problem renders `ProblemAlert` and keeps the dialog open.

- [ ] **Step 2: Write the failing screen tests**

`APIKeysScreen.test.tsx`:
- Renders table headers Name, Prefix, Role, Last used, Created; "Never used" for a null timestamp; a row per key.
- "Create key" opens the dialog.
- Revoke opens `ConfirmDialog`; confirming calls DELETE and shows a toast; cancelling does nothing.
- Empty state "No API keys yet."
- No secret or token hash appears in the rendered list.
- Viewer/member cannot reach the route (router test).

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/features/api-keys`
Expected: FAIL because the components do not exist.

- [ ] **Step 4: Implement the dialog and screen**

Pin:
- Dialog labels exactly "Name", "Role", "Viewer", "Member", "Create key", "Cancel"; screen button "Create key"; revoke button `Revoke {name}`; heading "API keys".
- On create success, replace the form with the one-time secret panel. The secret exists only in component state; do not put it in the query cache, local storage, or URL. Closing resets state.
- Copy uses `navigator.clipboard.writeText`, success toast "API key copied"; failure toast "Could not copy the key".
- Revoke uses `ConfirmDialog` explaining immediate access loss and retained audit history.
- Empty state "No API keys yet."
- Show "Never used" for null `last_used_at`; otherwise relative time.
- Prefix is monospace.
- No secret/hash column or detail.

- [ ] **Step 5: Add route, guard, sidebar, and router tests**

- Route `/settings/api-keys` under `workspaceRoute` guarded by `canManageAPIKeys`; non-admins redirect to monitors.
- Sidebar Settings group gains "API keys" next to Alerts and Status page.
- `router.test.tsx`: member redirect from `/w/acme-status/settings/api-keys`; admin renders the screen.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd web && npm test -- --run src/features/api-keys src/router.test.tsx`
Expected: PASS.

- [ ] **Step 7: Quality gate and commit**

Run: `cd web && npm run format && npm run lint && npm run typecheck && npm test -- --run && npm run build`
Commit:

```bash
git add web/src/features/api-keys web/src/router.tsx web/src/router.test.tsx web/src/components/layout/Sidebar.tsx
git commit -m "feat: add API key settings screen"
```

---

### Task 7: Audit UI and event kinds

**Files:**
- Modify: `web/src/lib/api/types.ts`
- Modify: `web/src/features/admin/api.ts`
- Modify: `web/src/features/admin/SecurityEventsScreen.tsx`
- Modify: `web/src/features/admin/SecurityEventsScreen.test.tsx`

**Interfaces:**
- Consumes: Task 3 `actor_api_key_id` in the admin event shape and new kinds.
- Produces: filter options and a "via API key" indicator.

- [ ] **Step 1: Write the failing tests**

Extend `SecurityEventsScreen.test.tsx`:
- The kind filter includes `login_succeeded`, `login_failed`, `member_added`, `member_role_changed`, `monitor_deleted`, `alert_channel_created`, `alert_channel_updated`, `alert_channel_deleted`, `status_page_updated`, `api_key_created`, `api_key_revoked`.
- An event with `actor_api_key_id` renders "via API key <id>" (or a stable label containing the id).
- An event without it renders no such line.
- Existing filter/detail/list tests stay green.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/features/admin/SecurityEventsScreen.test.tsx`
Expected: FAIL because the new kinds/field are absent.

- [ ] **Step 3: Implement**

- Add `actor_api_key_id: string | null` to `SecurityEvent`.
- Extend `SECURITY_EVENT_KINDS` with the new kinds.
- Render a small "via API key `<id>`" line when the field is non-null.
- Keep `EventDetail` unchanged.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npm test -- --run src/features/admin`
Expected: PASS.

- [ ] **Step 5: Quality gate and commit**

Run: `cd web && npm run format && npm run lint && npm run typecheck && npm test -- --run`
Commit:

```bash
git add web/src/lib/api/types.ts web/src/features/admin
git commit -m "feat: show API key attribution in security events"
```

---

### Task 8: E2E coverage, README, and final verification

**Files:**
- Modify: `web/tests/e2e/smoke.spec.ts`
- Modify: `web/tests/e2e/helpers.ts` (add `assertAuditEvent` if useful)
- Modify: `README.md`

**Interfaces:**
- Consumes: all earlier tasks.
- Produces: end-to-end key lifecycle and auth proof.

- [ ] **Step 1: Extend the E2E flow**

In the admin smoke test:

1. Open **Settings → API keys**, click **Create key**, name `Viewer key`, keep Viewer, submit.
2. Assert the secret panel shows a value matching `o1_...`; click **Copy key**; assert the copy toast.
3. Before closing, capture the secret from the input. Close the dialog, reload, and assert the secret text is absent from the page.
4. Use Playwright's `request` fixture with `Authorization: Bearer <secret>`:
   - `GET /api/v1/workspaces/{workspaceId}/monitors` → 200.
   - `POST /api/v1/workspaces/{workspaceId}/monitors` with a valid monitor body → 403.
5. Create a `Member key`, use it to `POST` a monitor → 201; then delete that monitor with the key → 204.
6. Revoke the viewer key; assert its next GET is 401.
7. Query `security_events` through the existing psql helper and assert an `api_key_created` row exists for the workspace with `actor_api_key_id` set and no secret in `detail`.
8. Keep the existing monitor, incident, email, alert-channel, status-page, theme, and a11y checks green.

- [ ] **Step 2: Run the E2E suite**

Bring up the Compose stack (`docker compose up -d --build`), then:

Run: `cd web && npm run e2e`
Expected: PASS.

- [ ] **Step 3: Update the README**

Add an **API keys** section:
- Settings → API keys, Viewer vs Member, one-time secret, prefix display, last used, revoke.
- `Authorization: Bearer o1_...` curl example against `GET /api/v1/workspaces/{id}/monitors`.
- Workspace scope, role limits, 600/min per-key limit, no admin/settings access.
- Secret is stored only as a hash and shown once.
- Audit events list and `actor_api_key_id` attribution.
- V1 limits: no expiry/rotation/custom scopes.

- [ ] **Step 4: Run the complete verification**

```bash
cd backend && gofmt -l . && go vet ./... && golangci-lint run && go test -short ./... && go test ./...
cd web && npm run format:check && npm run lint && npm run typecheck && npm test -- --run && npm run build
cd web && npm run e2e
```

Expected: all commands exit 0.

- [ ] **Step 5: Commit**

```bash
git add README.md web/tests/e2e
git commit -m "test: cover API keys and audit events end to end"
```

---

## Self-Review Checklist

- [ ] Every spec section maps to a task: data model (§6) → Task 1; Bearer auth and scope (§7.4) → Task 2; audit (§7.5/§7.6) → Tasks 3/7; key service/API/route matrix (§7.2/§7.3) → Task 4; dashboard (§8) → Tasks 5–7; testing (§9) → every task; docs/acceptance (§8.3/§10) → Task 8.
- [ ] Review Focus items have tests: no cookie fallback → Task 2; tenant scope and viewer writes → Tasks 2/4; secret containment → Tasks 1/3/4/6/8; audit never breaks actions → Task 3; limiter/last-used isolation → Tasks 2/4.
- [ ] Names and signatures match across tasks: `auth.APIKeyPrincipal`, `AuthenticateAPIKey`, `RequireAuth`, `APIKeyFrom`, `APIKeyIDFrom`, `APIKeyRateKey`, `httpx.KeyedRateLimit`, `audit.Event`, `RecordBestEffort`, `apikeys.Key`, `CreatedKey`, `CreateInput`, `canManageAPIKeys`, `queryKeys.apiKeys`.
- [ ] No task depends on a later task without saying so; each ends with a passing test cycle and a commit.

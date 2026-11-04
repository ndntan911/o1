# Uptime Monitor — Foundation, Auth & Tenancy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the backend foundation of the uptime monitor: repo/tooling, Postgres schema, email+password auth, sessions, and multi-tenant workspaces with RBAC.

**Architecture:** Go `net/http` (ServeMux), pgx/sqlc for data access, goose for migrations, PostgreSQL 16. Auth is server-side sessions stored hashed in Postgres, delivered in an HttpOnly cookie. Workspaces own memberships; every workspace-scoped query filters by the authenticated user's membership. This plan is the first executable slice of the spec; monitors/probing and the React app are separate plans.

**Tech Stack:** Go 1.26, net/http ServeMux, pgx v5, sqlc, goose, google/uuid (UUIDv7), x/crypto/argon2, x/time/rate, testcontainers-go, PostgreSQL 16, Mailpit (dev email), GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-11-04-uptime-monitor-design.md`

## Plan Split

The spec's M1–M4 is too large for one plan. Plans are split by deliverable:

1. **This plan — Foundation, auth & tenancy:** deployable API with accounts, sessions, workspaces.
2. Monitors, SSRF guard, scheduler/worker probe loop, dashboard API + React app.
3. Incidents, alerts, status pages.
4. Production hardening, deployment, backups, observability.

Each plan produces working, testable software on its own.

## Global Constraints

- Go module name: `o1`. Go 1.26 (the dependency floor; the original 1.24+ constraint is satisfied). All code identifiers and API messages in English.
- PostgreSQL 16. Migrations are append-only and run with goose.
- API prefix `/api/v1`. Errors use `application/problem+json` with `code`, `message`, `fields` (spec §7.9).
- Primary keys are UUIDv7 generated in Go (`uuid.NewV7()`). All timestamps are `timestamptz`.
- Email is normalized with `strings.ToLower(strings.TrimSpace(email))` before validation/storage; the column is `citext`.
- Password policy: 12–128 characters. Hash with Argon2id, PHC string: `$argon2id$v=19$m=65536,t=1,p=4$<salt>$<key>`.
- Session cookie: name `o1_session`, `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` when `O1_ENV=production`, 30-day sliding expiry. Only a SHA-256 hash of the token is stored.
- Verification token: 32 random bytes, base64url, SHA-256 at rest, expires in 24h, single use. Reset token: same encoding, expires in 1h, single use.
- No secrets, passwords, session tokens, or verification tokens in logs.
- Every workspace-scoped query must include a membership check. Cross-tenant access returns `404`, not `403`.
- Integration tests use a real Postgres 16 via testcontainers. Unit tests must run with `go test -short ./...` and no Docker.
- Commit after every task. Run `gofmt`, `go vet ./...`, and `golangci-lint run` before each commit.
- Env var prefix `O1_`. Required: `O1_DATABASE_URL`, `O1_SECRET_KEY` (base64 32 bytes).

## Review Focus

These are the failure modes most likely to bite users, each pinned by a test in the owning task:

1. **Email case/whitespace duplicates** — `User@Example.com ` must not create a second account; login must find the normalized address (Task 2/3).
2. **Expired or reused verification/reset tokens** — must fail closed and never verify twice (Task 3/4).
3. **Session token exposure/fixation** — raw token only in the cookie; DB stores a hash; login always issues a fresh token; expired/revoked sessions are rejected (Task 4).
4. **Cross-tenant access via guessed workspace UUID** — a non-member must get `404` for every workspace route (Task 5).
5. **CSRF on cookie-authenticated unsafe requests** — a foreign `Origin` must be rejected even with a valid cookie (Task 6).
6. **Password-reset email enumeration** — the endpoint returns `204` whether or not the email exists (Task 4).

---

### Task 1: Repository Scaffold, Config, Logging, Health, CI

**Files:**
- Create: `.gitignore`, `.editorconfig`, `Makefile`, `compose.yaml`, `.env.example`
- Create: `backend/go.mod`
- Create: `backend/cmd/api/main.go`
- Create: `backend/internal/config/config.go`, `backend/internal/config/config_test.go`
- Create: `backend/internal/logging/logging.go`
- Create: `backend/internal/httpx/problem.go`, `backend/internal/httpx/respond.go`
- Create: `backend/internal/httpx/health.go`, `backend/internal/httpx/health_test.go`
- Create: `backend/internal/api/router.go`
- Create: `backend/.golangci.yml`
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `config.Config` struct with `Env, HTTPAddr, DatabaseURL, AppBaseURL string`, `AllowedOrigins []string`, `SessionCookieName string`, `SessionTTL time.Duration`, `SecretKey []byte`, `SMTPAddr, EmailFrom, ResendAPIKey string`.
  - `config.Load() (Config, error)`.
  - `logging.New(w io.Writer, env string) *slog.Logger`.
  - `httpx.WriteProblem(w http.ResponseWriter, p Problem)` and `httpx.WriteJSON(w http.ResponseWriter, status int, v any)`.
  - `api.NewRouter(deps api.Dependencies) http.Handler`; `api.Dependencies{Logger *slog.Logger}` (fields added by later tasks).

- [ ] **Step 1: Write the failing config and health tests**

`backend/internal/config/config_test.go`:

```go
package config_test

import (
	"encoding/base64"
	"testing"
	"o1/internal/config"
)

func TestLoad_RequiresDatabaseURL(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if _, err := config.Load(); err == nil {
		t.Fatal("expected error when O1_DATABASE_URL is empty")
	}
}

func TestLoad_RejectsShortSecretKey(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://localhost/o1")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString([]byte("short")))
	if _, err := config.Load(); err == nil {
		t.Fatal("expected error when secret key is not 32 bytes")
	}
}

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://localhost/o1")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	cfg, err := config.Load()
	if err != nil { t.Fatal(err) }
	if cfg.HTTPAddr != ":8080" { t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr) }
	if cfg.SessionCookieName != "o1_session" { t.Fatalf("cookie = %q", cfg.SessionCookieName) }
	if cfg.SessionTTL.String() != "720h0m0s" { t.Fatalf("ttl = %v", cfg.SessionTTL) }
}
```

`backend/internal/httpx/health_test.go`:

```go
package httpx_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"o1/internal/api"
)

func TestHealthz_OK(t *testing.T) {
	r := api.NewRouter(api.Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK { t.Fatalf("status = %d", rec.Code) }
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/config ./internal/httpx -run 'TestLoad|TestHealthz' -v`

Expected: FAIL because `config.Load`, `api.NewRouter`, and the packages do not exist.

- [ ] **Step 3: Implement scaffold, config, logging, health**

- `compose.yaml` at repo root: `postgres:16-alpine` on `5432` (user/password/db `o1`) and `axllent/mailpit` on `1025` (SMTP) and `8025` (UI). Healthchecks for Postgres.
- `.env.example` with the exact `O1_*` vars from Global Constraints and `O1_SMTP_ADDR=localhost:1025`, `O1_EMAIL_FROM=o1@localhost`, `O1_APP_BASE_URL=http://localhost:5173`, `O1_ALLOWED_ORIGINS=http://localhost:5173`.
- `config.Load()` reads env with defaults: `O1_ENV=development`, `O1_HTTP_ADDR=:8080`, `O1_SESSION_COOKIE_NAME=o1_session`, `O1_SESSION_TTL=720h`. Split comma-separated allowed origins. Decode `SecretKey` as base64 and require exactly 32 bytes.
- `logging.New` returns JSON handler for `production`, text handler otherwise.
- `httpx.Problem` fields: `Type string`, `Title string`, `Status int`, `Detail string`, `Code string`, `Fields map[string]string`. `WriteProblem` sets `Content-Type: application/problem+json`.
- `api.NewRouter` mounts `GET /healthz` returning `{"status":"ok"}` and wraps the mux with request-ID, structured request logging, panic recovery, security headers (`X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`), and a 1 MiB body limit.
- `cmd/api/main.go`: load config, build logger, start `http.Server` on `cfg.HTTPAddr` with 10s read header timeout, handle SIGINT/SIGTERM with 10s graceful shutdown.
- `Makefile` targets: `dev-db` (compose up -d), `test` (`go test -short ./...`), `test-integration` (`go test ./...`), `lint` (`golangci-lint run`), `run` (`go run ./cmd/api`).
- `.golangci.yml` in `backend/`: enable `errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused`, `gosec`.
- `.github/workflows/ci.yml`: on push/PR; setup Go 1.26, run `go vet`, `go test -short ./...`, `golangci-lint` (via `golangci/golangci-lint-action@v8` with `version: v2.11.4`), build all binaries, and run `go test -count=1 ./...` in a separate integration job.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/config ./internal/httpx -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add .gitignore .editorconfig Makefile compose.yaml .env.example .github backend
git commit -m "chore: scaffold backend with config, logging, health, and CI"
```

---

### Task 2: Database, Migrations, sqlc, Users & Sessions Schema

**Files:**
- Create: `backend/internal/db/db.go`, `backend/internal/db/migrate.go`
- Create: `backend/internal/db/migrations/00001_auth.sql`
- Create: `backend/internal/db/queries/users.sql`, `backend/internal/db/queries/sessions.sql`
- Create: `backend/sqlc.yaml`
- Generated: `backend/internal/db/sqlc/*.go` (via `sqlc generate`)
- Create: `backend/internal/testutil/db.go`
- Create: `backend/internal/db/db_test.go`, `backend/internal/db/queries_test.go`

**Interfaces:**
- Consumes: `config.Config` from Task 1.
- Produces:
  - `db.Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error)`.
  - `db.Migrate(ctx context.Context, pool *pgxpool.Pool) error`.
  - `testutil.NewPostgres(t *testing.T) (*pgxpool.Pool, func())` — starts Postgres 16, runs `db.Migrate`.
  - Generated `*sqlc.Queries` methods used by later tasks:
    - `CreateUser(ctx, sqlc.CreateUserParams{ID uuid.UUID, Email string, PasswordHash string}) (sqlc.User, error)`
    - `GetUserByEmail(ctx, email string) (sqlc.User, error)`
    - `GetUserByID(ctx, id uuid.UUID) (sqlc.User, error)`
    - `MarkEmailVerified(ctx, id uuid.UUID) error`
    - `UpdateUserPassword(ctx, sqlc.UpdateUserPasswordParams{ID uuid.UUID, PasswordHash string}) error`
    - `CreateSession(ctx, sqlc.CreateSessionParams) error`
    - `GetSessionByTokenHash(ctx, tokenHash []byte) (sqlc.Session, error)`
    - `TouchSession(ctx, sqlc.TouchSessionParams{ID uuid.UUID, ExpiresAt time.Time}) error`
    - `DeleteSessionByTokenHash(ctx, tokenHash []byte) error`
    - `DeleteSessionsByUserID(ctx, userID uuid.UUID) error`
    - `CreateEmailVerificationToken(ctx, sqlc.CreateEmailVerificationTokenParams) error`
    - `GetEmailVerificationToken(ctx, tokenHash []byte) (sqlc.EmailVerificationToken, error)`
    - `MarkEmailVerificationTokenUsed(ctx, id uuid.UUID) error`
    - `CreatePasswordResetToken(ctx, sqlc.CreatePasswordResetTokenParams) error`
    - `GetPasswordResetToken(ctx, tokenHash []byte) (sqlc.PasswordResetToken, error)`
    - `MarkPasswordResetTokenUsed(ctx, id uuid.UUID) error`

- [ ] **Step 1: Write the failing schema/query tests**

`backend/internal/db/queries_test.go`:

```go
package db_test

import (
	"context"
	"testing"
	"o1/internal/testutil"
	"o1/internal/db/sqlc"
	"github.com/google/uuid"
)

func TestCreateUser_DuplicateEmailIsCaseInsensitive(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	q := sqlc.New(pool)
	ctx := context.Background()
	id, _ := uuid.NewV7()
	if _, err := q.CreateUser(ctx, sqlc.CreateUserParams{ID: id, Email: "User@Example.com", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	id2, _ := uuid.NewV7()
	if _, err := q.CreateUser(ctx, sqlc.CreateUserParams{ID: id2, Email: "user@example.com", PasswordHash: "x"}); err == nil {
		t.Fatal("expected unique violation for case-insensitive duplicate email")
	}
}

func TestGetUserByEmail_NormalizesCase(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	q := sqlc.New(pool)
	ctx := context.Background()
	id, _ := uuid.NewV7()
	if _, err := q.CreateUser(ctx, sqlc.CreateUserParams{ID: id, Email: "user@example.com", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetUserByEmail(ctx, "User@Example.com"); err != nil {
		t.Fatalf("expected case-insensitive lookup: %v", err)
	}
}
```

`backend/internal/db/db_test.go`:

```go
package db_test

import (
	"context"
	"testing"
	"o1/internal/testutil"
	"o1/internal/db"
)

func TestMigrate_IsIdempotent(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	if err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("second migrate failed: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/db -v`

Expected: FAIL because the packages and schema do not exist.

- [ ] **Step 3: Implement schema, sqlc config, DB helpers, test helper**

`00001_auth.sql` (down section can be empty; goose runs Up only):

```sql
-- +goose Up
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE users (
  id uuid PRIMARY KEY,
  email citext NOT NULL UNIQUE,
  password_hash text NOT NULL,
  email_verified_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  ip text,
  user_agent text NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_id_idx ON sessions(user_id);
CREATE INDEX sessions_expires_at_idx ON sessions(expires_at);

CREATE TABLE email_verification_tokens (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE,
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_verification_tokens_user_id_idx ON email_verification_tokens(user_id);

CREATE TABLE password_reset_tokens (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE,
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX password_reset_tokens_user_id_idx ON password_reset_tokens(user_id);
```

`sqlc.yaml`:

```yaml
version: "2"
sql:
  - engine: "postgresql"
    queries: "internal/db/queries"
    schema: "internal/db/migrations"
    gen:
      go:
        package: "sqlc"
        out: "internal/db/sqlc"
        sql_package: "pgx/v5"
        emit_pointers_for_null_types: true
        overrides:
          - db_type: "uuid"
            go_type: "github.com/google/uuid.UUID"
          - db_type: "citext"
            go_type: "string"
```

Write `users.sql`, `sessions.sql`, and token queries with the method names in Interfaces. `GetEmailVerificationToken` and `GetPasswordResetToken` must filter `used_at IS NULL`; the service checks `expires_at` itself so it can distinguish `ErrTokenExpired` from `ErrTokenInvalid`. `CreateSession` inserts `token_hash`, `expires_at`, `ip`, `user_agent`; `TouchSession` updates `last_used_at = now(), expires_at = $2`.

`db.Open` uses `pgxpool.New` with `pool.Config().MaxConns = 10` and pings. `db.Migrate` embeds `migrations/*.sql` with `//go:embed`, opens a `database/sql` connection via `pgx/v5/stdlib` using the URL from `pool.Config().ConnConfig.ConnString()`, and runs `goose.UpContext`.

`testutil.NewPostgres` starts a `postgres:16-alpine` container, waits for "database system is ready to accept connections" twice, opens the pool, runs `db.Migrate`, and registers `t.Cleanup` to terminate the container. It starts with `if testing.Short() { t.Skip("integration test") }`.

- [ ] **Step 4: Generate sqlc code and run tests**

Run: `cd backend && go run github.com/sqlc-dev/sqlc/cmd/sqlc@latest generate && go test ./internal/db -v`

Expected: PASS. `go mod tidy` includes pgx, uuid, goose, testcontainers.

- [ ] **Step 5: Commit**

```bash
git add backend/sqlc.yaml backend/internal/db backend/internal/testutil
git commit -m "feat: add Postgres schema, migrations, and sqlc queries for auth"
```

---

### Task 3: Password Hashing, Tokens, Email Sender, Register & Verify Email

**Files:**
- Create: `backend/internal/auth/password.go`, `backend/internal/auth/password_test.go`
- Create: `backend/internal/auth/tokens.go`, `backend/internal/auth/tokens_test.go`
- Create: `backend/internal/email/sender.go`, `backend/internal/email/mailpit.go`
- Create: `backend/internal/auth/service.go`, `backend/internal/auth/service_test.go`
- Create: `backend/internal/auth/handlers.go`, `backend/internal/auth/handlers_test.go`
- Create: `backend/internal/testutil/auth.go`

**Interfaces:**
- Consumes: `db.Open`, `db.Migrate`, `sqlc.Queries` (Task 2); `config.Config` (Task 1).
- Produces:
  - `auth.HashPassword(password string) (string, error)`, `auth.VerifyPassword(encoded, password string) (bool, error)`.
  - `auth.NewToken() (raw string, hash []byte, err error)`, `auth.HashToken(raw string) []byte`.
  - `email.Sender` interface: `Send(ctx context.Context, msg email.Message) error`; `email.Message{To, Subject, Text, HTML string}`.
  - `email.NewMailpitSender(addr, from string) *MailpitSender`.
  - `testutil.NewFakeSender() *testutil.FakeSender` (captures `.Msgs []email.Message`), `testutil.TestConfig(t *testing.T) config.Config`, `testutil.CreateVerifiedUser(t *testing.T, pool *pgxpool.Pool, email, password string) uuid.UUID`.
  - `auth.Service` with `NewService(pool *pgxpool.Pool, q *sqlc.Queries, sender email.Sender, cfg config.Config) *Service`.  - `(*Service).Register(ctx, email, password string) (sqlc.User, error)`.
  - `(*Service).VerifyEmail(ctx, token string) error`.
  - Sentinel errors: `auth.ErrEmailTaken`, `auth.ErrTokenInvalid`, `auth.ErrTokenExpired`, `auth.ErrValidation`.
  - HTTP handlers `auth.RegisterHandler(svc *Service) http.HandlerFunc`, `auth.VerifyEmailHandler(svc *Service) http.HandlerFunc`; they decode/validate JSON and call the service.

- [ ] **Step 1: Write the failing auth primitive tests**

`backend/internal/auth/password_test.go`:

```go
package auth_test

import (
	"strings"
	"testing"
	"o1/internal/auth"
)

func TestHashPassword_RoundTrip(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil { t.Fatal(err) }
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=1,p=4$") {
		t.Fatalf("unexpected PHC prefix: %s", hash)
	}
	ok, err := auth.VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !ok { t.Fatalf("verify = %v, %v", ok, err) }
	ok, err = auth.VerifyPassword(hash, "wrong password")
	if err != nil { t.Fatal(err) }
	if ok { t.Fatal("wrong password verified") }
}

func TestHashPassword_RejectsShortPassword(t *testing.T) {
	if _, err := auth.HashPassword("short"); err == nil {
		t.Fatal("expected validation error")
	}
}
```

`backend/internal/auth/tokens_test.go`:

```go
package auth_test

import (
	"testing"
	"o1/internal/auth"
)

func TestNewToken_UniqueAndHashed(t *testing.T) {
	a, ha, err := auth.NewToken()
	if err != nil { t.Fatal(err) }
	b, hb, err := auth.NewToken()
	if err != nil { t.Fatal(err) }
	if a == b { t.Fatal("tokens repeated") }
	if len(ha) != 32 || len(hb) != 32 { t.Fatalf("hash lengths = %d, %d", len(ha), len(hb)) }
	if string(ha) == string(a) { t.Fatal("token stored in clear") }
	if string(auth.HashToken(a)) != string(ha) { t.Fatal("HashToken mismatch") }
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/auth -run 'TestHashPassword|TestNewToken' -v`

Expected: FAIL because `auth.HashPassword` and `auth.NewToken` do not exist.

- [ ] **Step 3: Implement password, token, email, and register/verify service**

- `HashPassword` validates 12–128 chars, generates 16 random salt bytes, runs `argon2.IDKey(password, salt, 1, 64*1024, 4, 32)`, and encodes the PHC string with `base64.RawStdEncoding`.
- `VerifyPassword` parses the PHC string, recomputes the key with the stored salt/params, and compares with `subtle.ConstantTimeCompare`.
- `NewToken` returns `base64.RawURLEncoding` of 32 random bytes and `sha256.Sum256(raw)` as `[]byte`.
- `MailpitSender.Send` uses `net/smtp.SendMail(addr, nil, from, []string{msg.To}, message)` with `MIME-Version`, `Content-Type: text/html; charset=utf-8`, and both text and HTML parts.
- `Register` normalizes email, validates it with `net/mail.ParseAddress` (and requires the parsed address to equal the normalized input), validates password, hashes it, and inserts the user. Map Postgres unique violation (`pgconn.PgError` code `23505`) to `ErrEmailTaken`. Then creates a verification token (24h) and calls `sender.Send` with subject "Verify your email" and text containing `{AppBaseURL}/verify-email?token={raw}`. If sending fails, log the error and still return the user (the account can be re-verified later); do not leak the token.
- `VerifyEmail` hashes the token, loads the unused row with `FOR UPDATE`, and in one transaction marks the token used and sets `users.email_verified_at`. If no row, distinguish expired (`expires_at <= now()`) from invalid; return `ErrTokenExpired` or `ErrTokenInvalid`. Calling twice returns `ErrTokenInvalid`.
- Handlers: decode `{"email","password"}` with `DisallowUnknownFields`; map `ErrValidation`/invalid JSON to 400 `validation_error`, `ErrEmailTaken` to 409 `email_taken`; success is 201 with `{"id","email"}`. Verify handler decodes `{"token"}`; success 204, invalid/expired 400 `token_invalid`.
- `testutil/auth.go`: `FakeSender` with `Msgs []email.Message`; `TestConfig` returning the values above; `CreateVerifiedUser` hashing the password, inserting the user with sqlc, setting `email_verified_at = now()`, and returning the UUID.

- [ ] **Step 4: Write and run the failing service tests**

Add to `backend/internal/auth/service_test.go`:

```go
func TestRegister_SendsVerificationAndVerifies(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	fs := testutil.NewFakeSender()
	svc := auth.NewService(pool, sqlc.New(pool), fs, testutil.TestConfig(t))
	u, err := svc.Register(context.Background(), " User@Example.com ", "correct horse battery staple")
	if err != nil { t.Fatal(err) }
	if u.Email != "user@example.com" { t.Fatalf("email = %q", u.Email) }
	if len(fs.Msgs) != 1 { t.Fatalf("sent = %d", len(fs.Msgs)) }
	token := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(fs.Msgs[0].Text)[1]
	if err := svc.VerifyEmail(context.Background(), token); err != nil { t.Fatal(err) }
	if err := svc.VerifyEmail(context.Background(), token); err == nil { t.Fatal("token reused") }
}

func TestRegister_DuplicateEmailReturnsSentinel(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := auth.NewService(pool, sqlc.New(pool), testutil.NewFakeSender(), testutil.TestConfig(t))
	ctx := context.Background()
	if _, err := svc.Register(ctx, "a@example.com", "correct horse battery staple"); err != nil { t.Fatal(err) }
	if _, err := svc.Register(ctx, "A@Example.com", "correct horse battery staple"); !errors.Is(err, auth.ErrEmailTaken) {
		t.Fatalf("err = %v", err)
	}
}
```

`testutil.TestConfig(t)` returns a `config.Config` with `AppBaseURL: "http://localhost:5173"`, `AllowedOrigins: []string{"http://localhost:5173"}`, `Env: "test"`, `SessionTTL: 720*time.Hour`, and 32-byte `SecretKey`.

Also add `TestVerifyEmail_ExpiredToken`: insert a token row directly with `expires_at = now() - interval '1 minute'`, call `VerifyEmail`, expect `ErrTokenExpired`.

- [ ] **Step 5: Run all auth tests**

Run: `cd backend && go test ./internal/auth ./internal/email -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/auth backend/internal/email
git commit -m "feat: add password hashing, tokens, email sender, and registration"
```

---

### Task 4: Login, Sessions, Logout, Me, Password Reset

**Files:**
- Modify: `backend/internal/auth/service.go` (add login/session/reset methods)
- Create: `backend/internal/auth/session.go`, `backend/internal/auth/session_test.go`
- Modify: `backend/internal/auth/handlers.go`
- Create: `backend/internal/auth/middleware.go`, `backend/internal/auth/middleware_test.go`
- Modify: `backend/internal/httpx/problem.go` (add 401/403 codes)

**Interfaces:**
- Consumes: Task 2 queries, Task 3 primitives.
- Produces:
  - `auth.Principal{UserID uuid.UUID, SessionID uuid.UUID, Email string}`.
  - `auth.PrincipalFrom(ctx context.Context) (Principal, bool)`.
  - `(*Service).Login(ctx, email, password, ip, userAgent string) (rawToken string, user sqlc.User, err error)`.
  - `(*Service).Authenticate(ctx, rawToken string) (Principal, error)`.
  - `(*Service).Logout(ctx, rawToken string) error`.
  - `(*Service).RequestPasswordReset(ctx, email string) error` (always returns nil for unknown emails).
  - `(*Service).ResetPassword(ctx, token, newPassword string) error` (revokes all user sessions).
  - `auth.RequireSession(svc *Service) func(http.Handler) http.Handler`; puts `Principal` into context via `auth.PrincipalFrom(ctx)`.
  - `auth.SetSessionCookie(w, cfg, rawToken)` and `auth.ClearSessionCookie(w, cfg)`.
  - `auth.LoginHandler(svc, cfg)`, `auth.LogoutHandler(svc, cfg)`, `auth.ForgotPasswordHandler(svc)`, `auth.ResetPasswordHandler(svc)`. (`GET /me` is implemented in Task 6 because it needs the workspaces service.)
  - Sentinel errors: `auth.ErrInvalidCredentials`, `auth.ErrEmailNotVerified`, `auth.ErrUnauthenticated`.

- [ ] **Step 1: Write the failing session/login tests**

`backend/internal/auth/session_test.go`:

```go
func TestLogin_SuccessAndAuthenticate(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	raw, u, err := svc.Login(ctx, "user@example.com", "correct horse battery staple", "127.0.0.1", "test-agent")
	if err != nil { t.Fatal(err) }
	if raw == "" || u.ID == uuid.Nil { t.Fatal("empty login result") }
	p, err := svc.Authenticate(ctx, raw)
	if err != nil { t.Fatal(err) }
	if p.UserID != u.ID { t.Fatalf("principal = %v", p) }
	var stored []byte
	if err := pool.QueryRow(ctx, "SELECT token_hash FROM sessions WHERE user_id=$1", u.ID).Scan(&stored); err != nil { t.Fatal(err) }
	if string(stored) == raw { t.Fatal("session token stored in clear") }
}

func TestLogin_UnverifiedEmailRejected(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := auth.NewService(pool, sqlc.New(pool), testutil.NewFakeSender(), testutil.TestConfig(t))
	ctx := context.Background()
	if _, err := svc.Register(ctx, "u@example.com", "correct horse battery staple"); err != nil { t.Fatal(err) }
	if _, _, err := svc.Login(ctx, "u@example.com", "correct horse battery staple", "", ""); !errors.Is(err, auth.ErrEmailNotVerified) {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthenticate_RejectsExpiredSession(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	raw, u, err := svc.Login(ctx, "user@example.com", "correct horse battery staple", "", "")
	if err != nil { t.Fatal(err) }
	if _, err := pool.Exec(ctx, "UPDATE sessions SET expires_at = now() - interval '1 minute' WHERE user_id=$1", u.ID); err != nil { t.Fatal(err) }
	if _, err := svc.Authenticate(ctx, raw); !errors.Is(err, auth.ErrUnauthenticated) { t.Fatalf("err = %v", err) }
}

func TestResetPassword_RevokesSessions(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	fs := testutil.NewFakeSender()
	svc := auth.NewService(pool, sqlc.New(pool), fs, testutil.TestConfig(t))
	testutil.CreateVerifiedUser(t, pool, "user@example.com", "correct horse battery staple")
	ctx := context.Background()
	raw, _, err := svc.Login(ctx, "user@example.com", "correct horse battery staple", "", "")
	if err != nil { t.Fatal(err) }
	if err := svc.RequestPasswordReset(ctx, "user@example.com"); err != nil { t.Fatal(err) }
	token := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(fs.Msgs[len(fs.Msgs)-1].Text)[1]
	if err := svc.ResetPassword(ctx, token, "new correct horse battery"); err != nil { t.Fatal(err) }
	if _, err := svc.Authenticate(ctx, raw); err == nil { t.Fatal("old session survived password reset") }
	if _, _, err := svc.Login(ctx, "user@example.com", "new correct horse battery", "", ""); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/auth -run 'TestLogin|TestAuthenticate|TestResetPassword' -v`

Expected: FAIL because the methods do not exist.

Test helper used above: `newVerifiedService(t, pool)` calls `testutil.CreateVerifiedUser(t, pool, "user@example.com", "correct horse battery staple")` and returns `auth.NewService(pool, sqlc.New(pool), testutil.NewFakeSender(), testutil.TestConfig(t))`.

- [ ] **Step 3: Implement sessions, login, middleware, reset**

- `Login`: normalize email, `GetUserByEmail`, return `ErrInvalidCredentials` for unknown user or wrong password (same error to avoid enumeration; run a dummy Argon2 verification when the user is missing so timing stays similar), `ErrEmailNotVerified` if `email_verified_at` is nil. Create a fresh session token (always new), store `token_hash`, `expires_at = now + cfg.SessionTTL`, `ip` (plain string, empty if unparseable), `user_agent` (truncate to 512 chars).
- `Authenticate`: hash token, `GetSessionByTokenHash`, reject missing/expired with `ErrUnauthenticated`, load user, extend `expires_at` via `TouchSession` (sliding 30 days), return `Principal`.
- `Logout`: `DeleteSessionByTokenHash`; missing row is not an error.
- `RequestPasswordReset`: normalize email; look up user; if missing or unverified, return nil. Create token (1h) and send email with `{AppBaseURL}/reset-password?token={raw}`.
- `ResetPassword`: hash token, load the unused row with `FOR UPDATE`, validate new password, hash it, update user, mark token used, and `DeleteSessionsByUserID` — all in one transaction. Return `ErrTokenInvalid`/`ErrTokenExpired`.
- Cookie: `SetSessionCookie` writes value, `Path=/`, `MaxAge=int(cfg.SessionTTL.Seconds())`, `HttpOnly=true`, `SameSite=Lax`, `Secure=cfg.Env=="production"`. `ClearSessionCookie` writes the same with `MaxAge=-1`.
- `RequireSession` reads the cookie, authenticates, returns 401 `unauthenticated` on failure, else stores `Principal` in context.
- Handlers: login returns 200 `{"user":{"id","email"}}` and sets cookie; logout returns 204 and clears cookie; forgot returns 204 always; reset returns 204. (`GET /me` is Task 6.)
- Error mapping: `ErrInvalidCredentials` → 401 `invalid_credentials`, `ErrEmailNotVerified` → 403 `email_not_verified`, `ErrUnauthenticated` → 401 `unauthenticated`, token errors → 400 `token_invalid`.

- [ ] **Step 4: Add and run middleware/handler tests**

Add `TestLoginHandler_SetsSecureCookieFlags` (asserts `HttpOnly`, `SameSite=Lax`, and `Secure` when config env is production), `TestRequireSession_RejectsMissingCookie` (401), and `TestForgotPassword_UnknownEmailStill204` (handler test).

Run: `cd backend && go test ./internal/auth -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/auth backend/internal/httpx
git commit -m "feat: add sessions, login, logout, me, and password reset"
```

---

### Task 5: Workspaces, Memberships, RBAC

**Files:**
- Create: `backend/internal/db/migrations/00002_workspaces.sql`
- Create: `backend/internal/db/queries/workspaces.sql`
- Regenerate: `backend/internal/db/sqlc/*.go`
- Create: `backend/internal/workspaces/service.go`, `backend/internal/workspaces/service_test.go`
- Create: `backend/internal/workspaces/handlers.go`, `backend/internal/workspaces/handlers_test.go`

**Interfaces:**
- Consumes: `auth.Principal` (Task 4), sqlc (Task 2).
- Produces:
  - `workspaces.Role` constants `RoleViewer`, `RoleMember`, `RoleAdmin`, `RoleOwner`; `func (r Role) String() string`, `func ParseRole(s string) (Role, error)`, `func (r Role) AtLeast(min Role) bool`.
  - `workspaces.Workspace{ID uuid.UUID, Name, Slug string, Role Role}`.
  - `workspaces.Member{UserID uuid.UUID, Email string, Role Role}`.
  - `workspaces.Service` with `NewService(pool *pgxpool.Pool, q *sqlc.Queries) *Service`.
  - `(*Service).Create(ctx, userID uuid.UUID, name string) (Workspace, error)`.
  - `(*Service).Get(ctx, userID, workspaceID uuid.UUID) (Workspace, error)`.
  - `(*Service).ListForUser(ctx, userID uuid.UUID) ([]Workspace, error)`.
  - `(*Service).ListMembers(ctx, userID, workspaceID uuid.UUID) ([]Member, error)`.
  - `(*Service).AddMember(ctx, actorID, workspaceID uuid.UUID, email string, role Role) error`.
  - `(*Service).Authorize(ctx, userID, workspaceID uuid.UUID, min Role) (Role, error)`.
  - Sentinel errors: `workspaces.ErrForbidden`, `workspaces.ErrNotFound`, `workspaces.ErrValidation`.
  - Handlers: `workspaces.ListHandler`, `CreateHandler`, `GetHandler`, `MembersHandler`, `AddMemberHandler`.
  - Generated queries:
    - `CreateWorkspace(ctx, sqlc.CreateWorkspaceParams{ID uuid.UUID, Name, Slug string, CreatedBy uuid.UUID}) (sqlc.Workspace, error)`
    - `GetWorkspaceByID(ctx, id uuid.UUID) (sqlc.Workspace, error)`
    - `GetWorkspaceBySlug(ctx, slug string) (sqlc.Workspace, error)`
    - `UpsertWorkspaceMember(ctx, sqlc.UpsertWorkspaceMemberParams{WorkspaceID, UserID uuid.UUID, Role string}) error` — `ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = EXCLUDED.role`
    - `GetWorkspaceMember(ctx, sqlc.GetWorkspaceMemberParams{WorkspaceID, UserID uuid.UUID}) (sqlc.WorkspaceMember, error)`
    - `ListWorkspacesForUser(ctx, userID uuid.UUID) ([]sqlc.ListWorkspacesForUserRow, error)` — row `{ID uuid.UUID, Name, Slug, Role string}`
    - `ListWorkspaceMembers(ctx, workspaceID uuid.UUID) ([]sqlc.ListWorkspaceMembersRow, error)` — row `{UserID uuid.UUID, Email, Role string}`

- [ ] **Step 1: Write the failing workspace tests**

`backend/internal/workspaces/service_test.go`:

```go
func TestCreateWorkspace_CreatorIsOwner(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	userID := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	svc := workspaces.NewService(pool, sqlc.New(pool))
	ws, err := svc.Create(context.Background(), userID, "Acme Status")
	if err != nil { t.Fatal(err) }
	if ws.Role != workspaces.RoleOwner { t.Fatalf("role = %v", ws.Role) }
	if ws.Slug != "acme-status" { t.Fatalf("slug = %q", ws.Slug) }
}

func TestListForUser_OnlyMemberships(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	a := testutil.CreateVerifiedUser(t, pool, "a@example.com", "correct horse battery staple")
	b := testutil.CreateVerifiedUser(t, pool, "b@example.com", "correct horse battery staple")
	svc := workspaces.NewService(pool, sqlc.New(pool))
	ctx := context.Background()
	if _, err := svc.Create(ctx, a, "A Workspace"); err != nil { t.Fatal(err) }
	if _, err := svc.Create(ctx, b, "B Workspace"); err != nil { t.Fatal(err) }
	list, err := svc.ListForUser(ctx, a)
	if err != nil { t.Fatal(err) }
	if len(list) != 1 || list[0].Name != "A Workspace" { t.Fatalf("list = %+v", list) }
}

func TestAuthorize_CrossTenantDenied(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	owner := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	outsider := testutil.CreateVerifiedUser(t, pool, "outsider@example.com", "correct horse battery staple")
	svc := workspaces.NewService(pool, sqlc.New(pool))
	ws, err := svc.Create(context.Background(), owner, "Private")
	if err != nil { t.Fatal(err) }
	if _, err := svc.Authorize(context.Background(), outsider, ws.ID, workspaces.RoleViewer); !errors.Is(err, workspaces.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestAddMember_RequiresAdminAndCapsRole(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	owner := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	viewer := testutil.CreateVerifiedUser(t, pool, "viewer@example.com", "correct horse battery staple")
	admin := testutil.CreateVerifiedUser(t, pool, "admin@example.com", "correct horse battery staple")
	svc := workspaces.NewService(pool, sqlc.New(pool))
	ctx := context.Background()
	ws, err := svc.Create(ctx, owner, "Team")
	if err != nil { t.Fatal(err) }
	if err := svc.AddMember(ctx, owner, ws.ID, "viewer@example.com", workspaces.RoleViewer); err != nil { t.Fatal(err) }
	if err := svc.AddMember(ctx, viewer, ws.ID, "admin@example.com", workspaces.RoleAdmin); !errors.Is(err, workspaces.ErrForbidden) {
		t.Fatalf("viewer add err = %v", err)
	}
	if err := svc.AddMember(ctx, owner, ws.ID, "admin@example.com", workspaces.RoleAdmin); err != nil { t.Fatal(err) }
	if err := svc.AddMember(ctx, admin, ws.ID, "viewer@example.com", workspaces.RoleAdmin); !errors.Is(err, workspaces.ErrForbidden) {
		t.Fatalf("admin cap err = %v", err)
	}
	if err := svc.AddMember(ctx, owner, ws.ID, "owner2@example.com", workspaces.RoleOwner); !errors.Is(err, workspaces.ErrValidation) {
		t.Fatalf("owner grant err = %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/workspaces -v`

Expected: FAIL because the package and schema do not exist.

- [ ] **Step 3: Implement workspace schema, service, handlers**

`00002_workspaces.sql`:

```sql
-- +goose Up
CREATE TABLE workspaces (
  id uuid PRIMARY KEY,
  name text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 80),
  slug citext NOT NULL UNIQUE,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE workspace_members (
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role text NOT NULL CHECK (role IN ('owner','admin','member','viewer')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, user_id)
);
CREATE INDEX workspace_members_user_id_idx ON workspace_members(user_id);
```

- Slug: lowercase, strip accents via `golang.org/x/text/unicode/norm` is optional; V1 uses ASCII letters/digits, runs of other chars → `-`, trim `-`, max 60 chars. If taken, retry with `-` + 6 random lowercase letters up to 5 times.
- `Create`: validate name 1–80 chars after trim; insert workspace + owner membership in one transaction; return workspace with `RoleOwner`.
- `Get`: `Authorize(user, ws, RoleViewer)` then fetch the workspace; return it with the caller's role.
- `Authorize`: query membership; if no row return `ErrNotFound`; if role < min return `ErrForbidden`; else return role.
- `AddMember`: `Authorize(actor, ws, RoleAdmin)`; reject assigning `RoleOwner` with `ErrValidation`; find user by normalized email (404 `ErrNotFound` if absent); reject assigning a role >= actor's role unless actor is owner; upsert membership with `UpsertWorkspaceMember` (`ON CONFLICT DO UPDATE`).
- `ListForUser`: join workspaces to memberships, return role.
- `ListMembers`: `Authorize(actor, ws, RoleViewer)` then join users.
- Handlers: `GET /api/v1/workspaces` → `{"workspaces":[...]}`; `POST /api/v1/workspaces` body `{"name"}` → 201; `GET /api/v1/workspaces/{workspaceID}` → workspace + role; `GET /api/v1/workspaces/{workspaceID}/members` → `{"members":[...]}`; `POST .../members` body `{"email","role"}` → 204. Map `ErrNotFound` to 404, `ErrForbidden` to 403, `ErrValidation` to 400.

- [ ] **Step 4: Run all workspace tests**

Run: `cd backend && go test ./internal/workspaces ./internal/db -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/db backend/internal/workspaces
git commit -m "feat: add workspaces, memberships, and role-based access control"
```

---

### Task 6: Router Wiring, CSRF, Rate Limiting, and Full API Integration Test

**Files:**
- Modify: `backend/internal/api/router.go`
- Create: `backend/internal/httpx/middleware.go`, `backend/internal/httpx/middleware_test.go`
- Create: `backend/internal/api/integration_test.go`
- Modify: `backend/cmd/api/main.go`
- Create: `backend/Dockerfile`

**Interfaces:**
- Consumes: all services/handlers from Tasks 3–5.
- Produces:
  - `api.Dependencies{Logger *slog.Logger, Pool *pgxpool.Pool, Config config.Config, EmailSender email.Sender}`.
  - `httpx.CSRF(allowedOrigins []string) func(http.Handler) http.Handler`.
  - `httpx.RateLimit(rps float64, burst int, keyByIP bool) func(http.Handler) http.Handler`.
  - `api.MeHandler(wsSvc *workspaces.Service) http.HandlerFunc` — returns `{"user":{"id","email"},"workspaces":[...]}`.
  - `api.NewRouter` mounts all routes under `/api/v1` with the exact paths listed below.

- [ ] **Step 1: Write the failing middleware and integration tests**

`backend/internal/httpx/middleware_test.go`:

```go
package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"o1/internal/httpx"
)

func TestCSRF_RejectsForeignOrigin(t *testing.T) {
	h := httpx.CSRF([]string{"https://app.example.com"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden { t.Fatalf("status = %d", rec.Code) }
}

func TestCSRF_AllowsConfiguredOrigin(t *testing.T) {
	h := httpx.CSRF([]string{"https://app.example.com"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent { t.Fatalf("status = %d", rec.Code) }
}

func TestRateLimit_Returns429AfterBurst(t *testing.T) {
	h := httpx.RateLimit(1, 2, true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		req.RemoteAddr = "203.0.113.10:1234"
		h.ServeHTTP(rec, req)
		if rec.Code != 204 { t.Fatalf("request %d = %d", i, rec.Code) }
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests { t.Fatalf("status = %d", rec.Code) }
}
```

`backend/internal/api/integration_test.go`:

```go
func TestAPI_RegisterVerifyLoginCreateWorkspace(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	fs := testutil.NewFakeSender()
	cfg := testutil.TestConfig(t)
	router := api.NewRouter(api.Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Pool: pool, Config: cfg, EmailSender: fs})
	// 1. POST /api/v1/auth/register -> 201
	// 2. extract token from fs last message, POST /api/v1/auth/verify-email -> 204
	// 3. POST /api/v1/auth/login -> 200 + cookie
	// 4. GET /api/v1/me with cookie -> 200, email matches
	// 5. POST /api/v1/workspaces with cookie + Origin header -> 201
	// 6. GET /api/v1/workspaces -> 200, one workspace
	// 7. logout -> 204; GET /me -> 401
}
```

Use `httptest.NewServer(router)` and `http.Client` with a cookie jar, plus `Origin: cfg.AppBaseURL` on unsafe requests. Assert the exact status codes above.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/httpx ./internal/api -run 'TestCSRF|TestRateLimit|TestAPI_' -v`

Expected: FAIL because CSRF/RateLimit and the full router do not exist.

- [ ] **Step 3: Implement router and middleware**

Routes (`/healthz` and `/readyz` are at the root; the rest are under `/api/v1`):

```text
GET    /healthz                     (unauthenticated)
GET    /readyz                      (pings DB)

POST   /auth/register               (rate limited by IP)
POST   /auth/verify-email
POST   /auth/verify-email/resend    (rate limited by IP)
POST   /auth/login                  (rate limited by IP)
POST   /auth/logout                 (requires session)
POST   /auth/password/forgot        (rate limited by IP)
POST   /auth/password/reset
GET    /me                          (requires session)

GET    /workspaces                  (requires session)
POST   /workspaces                  (requires session)
GET    /workspaces/{workspaceID}    (requires session + viewer)
GET    /workspaces/{workspaceID}/members        (requires session + viewer)
POST   /workspaces/{workspaceID}/members        (requires session + admin)
```

- `Dependencies` gains `EmailSender email.Sender`; `main.go` opens the DB with `db.Open`, runs `db.Migrate`, builds `sqlc.New(pool)` + auth/workspaces services, wires `email.NewMailpitSender(cfg.SMTPAddr, cfg.EmailFrom)` in development (startup error in production until the Resend sender is implemented in a later plan), and constructs the router.
- `MeHandler`: read `auth.PrincipalFrom(ctx)`, call `wsSvc.ListForUser`, return user + workspaces.
- `GET /workspaces/{workspaceID}` calls `wsSvc.Get`, which authorizes viewer access and returns 404 for outsiders.
- `CSRF`: for `POST/PUT/PATCH/DELETE`, if `Origin` is present it must match one of `AllowedOrigins`; if absent, allow (SameSite=Lax plus non-browser clients).
- `RateLimit`: token bucket per remote IP with `x/time/rate`; one limiter per IP, mutex-guarded map, 10 req/min with burst 10 for auth routes; return 429 `rate_limited`.
- `/readyz` runs `pool.Ping(ctx)` with a 2s timeout; 200 or 503.
- `Dockerfile`: multi-stage `golang:1.26-alpine` builder → `gcr.io/distroless/static-debian12:nonroot`; build `cmd/api`; expose 8080; run as nonroot.

- [ ] **Step 4: Run the full test suite**

Run: `cd backend && go test ./... -v`

Expected: PASS (Docker required for integration tests). Also run `go vet ./...` and `golangci-lint run`.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api backend/internal/httpx backend/Dockerfile backend/cmd
git commit -m "feat: wire API routes with CSRF, rate limiting, and integration tests"
```

---

## Plan Self-Review

**Spec coverage (this slice):** auth, sessions, email verification, password reset, workspaces, RBAC, API conventions, UUID/timestamps, rate limiting, CSRF, cookie security, integration tests, CI. Monitors, probing, incidents, alerts, React, deployment, observability and backups are explicitly deferred to plans 2–4.

**Step scan:** every task has a failing test, a failing run, an implementation step with exact signatures/values, a passing run, and a commit.

**Type consistency:** `auth.Principal` is used by `workspaces` handlers via context; `sqlc` generated signatures are listed once and referenced by later tasks; role strings match the DB check constraint.

**Review Focus:** each of the six failure modes has a named test: case-insensitive email (Task 2/3), token reuse/expiry (Task 3/4), session hash/expiry/revocation (Task 4), cross-tenant 404 (Task 5), foreign Origin (Task 6), reset enumeration (Task 4).

**Proportion:** this plan is longer than the spec's auth section because it pins file paths, interfaces, and test assertions; implementation bodies are left to the builder.

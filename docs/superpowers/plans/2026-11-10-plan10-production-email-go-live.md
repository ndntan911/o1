# Plan 10 — Production Email and Go-Live Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Resend production email sender, remove the production startup refusal, classify email failures, add idempotency and bounded retries, fix client-IP capture behind Caddy, and ship a separate production Compose/HTTPS/runbook stack.

**Architecture:** `email.NewSender(cfg)` selects Mailpit for dev/test or Resend for production, wraps it in a bounded retry sender, and is wired into the API and worker. The web image's Caddy site address becomes a runtime env; a standalone `deploy/production/compose.yaml` runs Postgres, API, scheduler, worker, web, and an optional Alloy profile with only 80/443 public.

**Tech Stack:** Go 1.26, PostgreSQL 16, Resend HTTP API, Caddy automatic HTTPS, Docker Compose, existing `email.Sender`, River, `golang.org/x/time/rate`, Playwright, Vitest.

**Spec:** `docs/superpowers/specs/2026-11-10-plan10-production-email-go-live-design.md`

## Global Constraints

- Plan 10 scope only. No CI/CD deploy, registry publishing, backups, retention/rollups, load/security review, managed Postgres, multiple regions, bounce webhooks, DKIM automation, inbound/marketing email, or generic SMTP.
- Email providers are `mailpit` and `resend`. Empty `O1_EMAIL_PROVIDER` resolves to `mailpit` in development/test and `resend` in production. Any other value fails config loading.
- Production requires Resend with `O1_RESEND_API_KEY` and a valid `O1_EMAIL_FROM`; production+mailpit fails config loading.
- Resend: `POST {base}/emails`, Bearer key, `Content-Type: application/json`, `User-Agent: o1-email/1.0`, 10s timeout, redirects not followed, 64 KiB response drain. Base defaults to `https://api.resend.com`; `O1_RESEND_API_URL` overrides for tests/dev.
- 2xx succeeds. 408, 425, 429, 5xx, timeouts, and transport errors are transient. Other 4xx are permanent; permanent alert email failures are marked failed without consuming the retry budget.
- Errors never contain the API key, Authorization header, request body, recipient address, or raw provider body.
- `email.Message` gains optional `IdempotencyKey`; Resend sends `Idempotency-Key`; Mailpit ignores it. Alerts use the delivery UUID, verification the token UUID, reset the reset-token UUID.
- A retry sender makes up to three attempts for transient errors with context-aware 250ms/1s backoff. There is no persistent auth outbox; a process crash can still lose auth mail.
- Resend has a per-process rate limiter: `O1_RESEND_RPS` default 2, `O1_RESEND_BURST` default 2, context-aware. The free-tier daily quota is documented; production needs a suitable plan.
- Caddy site address is `{$O1_SITE_ADDRESS::80}` at runtime. Dev keeps `http://localhost:5173`; production uses the domain with ports 80/443 and persistent cert volumes.
- Production publishes only 80/443. Postgres, API, scheduler, worker, health, and metrics stay internal/loopback.
- Production has no Mailpit, dev secrets, raised auth limits, or published DB/API ports. Secrets come from env/untracked `.env`.
- `O1_ENV=production` keeps existing encryption-key validation. API/worker no longer refuse production; they fail only on invalid provider config.
- Auth sessions/audit use trusted forwarded headers only from loopback/private peers, not the Caddy container IP.
- The optional Alloy profile exists in production; no live Grafana provisioning.
- UI/code English. Every task ends with formatting, lint/typecheck where applicable, tests, and a commit.

## Review Focus

1. **Provider selection safety.** Production can never start with Mailpit or missing Resend credentials; dev/test stays Mailpit; explicit overrides are validated.
2. **No leakage.** Resend keys, Authorization headers, request bodies, recipients, and provider bodies never appear in errors or logs; redirects cannot move the bearer token.
3. **Retry correctness and duplicates.** Transient errors retry three times, permanent errors once; alerts get one failed status after a permanent error; idempotency keys prevent duplicate sends on lost responses.
4. **Production exposure.** Only 80/443 are public; Postgres/API/metrics/Mailpit/dev keys are absent; TLS uses the configured domain and persistent cert storage.
5. **Client IP and auth mail.** Sessions/audit record the real client IP behind Caddy; verification/reset still work and carry token idempotency keys.

---

## File Structure

```text
backend/
  internal/email/sender.go                  # modify: Message.IdempotencyKey
  internal/email/resend.go                  # create
  internal/email/resend_test.go             # create
  internal/email/retry.go                   # create
  internal/email/retry_test.go              # create
  internal/email/factory.go                 # create
  internal/email/factory_test.go            # create
  internal/email/mailpit.go                 # modify: shared validation/getters
  internal/config/config.go                 # modify: provider/Resend fields
  internal/config/config_test.go            # modify
  internal/httpx/middleware.go              # modify: exported ClientIP
  internal/auth/handlers.go                 # modify: requestIP
  internal/auth/handlers_test.go            # modify
  internal/auth/service.go                  # modify: verification idempotency key
  internal/auth/session.go                  # modify: reset idempotency key
  internal/auth/*_test.go                   # modify
  internal/worker/alert_worker.go           # modify: idempotency + permanent
  internal/worker/alert_worker_test.go      # modify
  cmd/api/main.go                           # modify: email.NewSender
  cmd/worker/main.go                        # modify: email.NewSender
web/
  Caddyfile                                 # modify: O1_SITE_ADDRESS
  Dockerfile                                # modify: expose 443
deploy/production/
  compose.yaml                              # create
  env.example                               # create
  README.md                                 # create
README.md                                   # modify: production email/go-live
```

`internal/email` owns the sender implementations/factory; config owns provider resolution; the production stack lives in `deploy/production`.

---

### Task 1: Resend sender and retry wrapper

**Files:**
- Modify: `backend/internal/email/sender.go`
- Modify: `backend/internal/email/mailpit.go` (shared validation helpers)
- Create: `backend/internal/email/resend.go`
- Create: `backend/internal/email/resend_test.go`
- Create: `backend/internal/email/retry.go`
- Create: `backend/internal/email/retry_test.go`

**Interfaces:**
- Consumes: existing `MailpitSender`, `email.Message`.
- Produces:
  - `email.Message.IdempotencyKey string`.
  - `NewResendSender(apiKey, from, replyTo, baseURL string, rps float64, burst int) *ResendSender`.
  - `DeliveryError{Message string; Permanent bool; Cause error}`, `IsPermanent(error) bool`.
  - `NewRetrySender(inner Sender, attempts int, backoff time.Duration) *RetrySender`.

- [ ] **Step 1: Write the failing Resend tests**

Using `httptest.Server`, assert:
- Path `/emails`, method POST, `Authorization: Bearer test-key`, `Content-Type`, `User-Agent: o1-email/1.0`.
- JSON `from`, `to` array, `subject`, `text`, `html`; `reply_to` omitted/present; `Idempotency-Key` omitted/present.
- 200/204 success.
- 400/401/403/422 → `IsPermanent` true; 408/425/429/500/503 → false; redirect 302 → permanent and not followed.
- timeout → transient; body larger than 64 KiB is drained/closed safely.
- Error strings do not contain `test-key`, recipient, or provider body.
- Invalid `To`/subject/CRLF rejected before the server is called.
- Rate limiter: with `rps=1,burst=1`, two sends are spaced; context cancellation returns an error.

- [ ] **Step 2: Write the failing retry tests**

- Transient error retried exactly 3 times, then returns the last error.
- Permanent error not retried.
- Context canceled during backoff stops immediately.
- Success on the second attempt returns nil.
- The inner sender receives the same `Message` (idempotency key preserved).

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/email -run 'TestResend|TestRetry' -v`
Expected: FAIL because the senders do not exist.

- [ ] **Step 4: Implement Resend and retry**

Pin:
- `Message.IdempotencyKey` is optional and ignored by Mailpit rendering.
- `ResendSender` holds key/from/replyTo/baseURL/client/limiter.
- `Send` validates before I/O; marshals a struct; sets headers; adds `Idempotency-Key` when non-empty; waits on the limiter; uses a redirect-refusing client; 10s timeout; drains 64 KiB; classifies status.
- `DeliveryError.Error()` returns only `Message`; `Unwrap` returns `Cause`.
- `RetrySender.Send` retries only when `!IsPermanent(err)`, up to 3 total attempts, with 250ms then 1s backoff; every wait respects `ctx.Done()`.
- Shared message validation lives in one helper used by Mailpit and Resend; Mailpit behavior is unchanged.
- No log calls in senders; callers decide logging and never include secrets.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/email -run 'TestResend|TestRetry|TestMailpit' -v`
Expected: PASS.

- [ ] **Step 6: Quality gate and commit**

Run: `cd backend && gofmt -w internal/email && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/email
git commit -m "feat: add Resend email sender with retries and idempotency"
```

---

### Task 2: Provider configuration and email factory

**Files:**
- Modify: `backend/internal/config/config.go`
- Modify: `backend/internal/config/config_test.go`
- Create: `backend/internal/email/factory.go`
- Create: `backend/internal/email/factory_test.go`

**Interfaces:**
- Consumes: Task 1 `NewResendSender`, `NewRetrySender`, `MailpitSender`.
- Produces:
  - `config.Config` fields `EmailProvider`, `ResendAPIURL`, `EmailReplyTo`, `ResendRPS float64`, `ResendBurst int`.
  - `email.NewSender(cfg config.Config) (Sender, error)`.

- [ ] **Step 1: Write the failing config/factory tests**

Config tests:
- dev/test empty provider → `mailpit`.
- production empty provider → `resend`.
- explicit `resend` in dev accepted when key/from set.
- production+mailpit rejected.
- resend without key or without valid from rejected.
- invalid provider rejected.
- `O1_RESEND_API_URL` default/override, `O1_RESEND_RPS`/`BURST` defaults/values, reply-to validation.

Factory test:

```go
func TestNewSender_SelectsProvider(t *testing.T) {
	defaults := testutil.TestConfig(t)
	defaults.EmailProvider = "mailpit"
	defaults.EmailFrom = "o1@example.com"
	sender, err := email.NewSender(defaults)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sender.(*email.RetrySender); !ok {
		t.Fatalf("sender = %T, want retry wrapper", sender)
	}

	defaults.EmailProvider = "resend"
	defaults.ResendAPIKey = "re_test_key"
	defaults.ResendAPIURL = "https://api.resend.com"
	if _, err := email.NewSender(defaults); err != nil {
		t.Fatalf("resend factory: %v", err)
	}

	defaults.EmailProvider = "bogus"
	if _, err := email.NewSender(defaults); err == nil {
		t.Fatal("invalid provider accepted")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/config ./internal/email -run 'TestEmailProvider|TestNewSender' -v`
Expected: FAIL because the config fields/factory do not exist.

- [ ] **Step 3: Add config fields and resolution**

Pin:
- `O1_EMAIL_PROVIDER`, `O1_RESEND_API_URL` (default `https://api.resend.com`), `O1_EMAIL_REPLY_TO`, `O1_RESEND_RPS` (default 2), `O1_RESEND_BURST` (default 2).
- Resolve empty provider: `mailpit` for development/test, `resend` for production.
- Validate `resend` requires a non-empty key and a `mail.ParseAddress`-valid from; `mailpit` requires `O1_SMTP_ADDR` and from.
- Reject production+mailpit.
- Validate reply-to if set; validate RPS > 0 and burst ≥ 1.

- [ ] **Step 4: Implement the factory**

Pin:
- `NewSender` switches on the resolved provider and returns `NewRetrySender(providerSender, 3, 250*time.Millisecond)`; the retry sender uses a 1s second backoff internally.
- Resend uses `ResendAPIURL`, `ResendAPIKey`, `EmailFrom`, `EmailReplyTo`, `ResendRPS`, `ResendBurst`.
- Mailpit uses the existing `SMTPAddr`/`EmailFrom`.
- Invalid/missing configuration returns a descriptive error without echoing secrets.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/config ./internal/email -run 'TestEmailProvider|TestNewSender' -v`
Expected: PASS.

- [ ] **Step 6: Quality gate and commit**

Run: `cd backend && gofmt -w internal/config internal/email && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/internal/config backend/internal/email
git commit -m "feat: add email provider configuration and factory"
```

---

### Task 3: Wire providers, idempotency, permanent failures, and client IP

**Files:**
- Modify: `backend/cmd/api/main.go`
- Modify: `backend/cmd/worker/main.go`
- Modify: `backend/internal/worker/alert_worker.go`
- Modify: `backend/internal/worker/alert_worker_test.go`
- Modify: `backend/internal/auth/service.go`
- Modify: `backend/internal/auth/session.go`
- Modify: `backend/internal/auth/*_test.go`
- Modify: `backend/internal/auth/handlers.go`
- Modify: `backend/internal/auth/handlers_test.go`
- Modify: `backend/internal/httpx/middleware.go`
- Modify: `backend/internal/httpx/middleware_test.go`

**Interfaces:**
- Consumes: Tasks 1–2.
- Produces:
  - `cmd/api`/`cmd/worker` use `email.NewSender(rt.Config)`; the production refusal is removed.
  - Alert email `Message.IdempotencyKey = delivery.ID.String()`; permanent failures use `failWithoutRetry`.
  - Auth verification/reset messages set token idempotency keys.
  - `httpx.ClientIP(r *http.Request) string` exported; `auth.requestIP` uses it.

- [ ] **Step 1: Write the failing tests**

- Worker: a fake sender returns `&email.DeliveryError{Message:"resend returned HTTP 422", Permanent:true}`; delivery becomes failed with one attempt and `Work` returns nil. A transient fake error keeps the existing pending/retry path. The fake records the `IdempotencyKey` equal to the delivery UUID.
- Auth tests: verification and password-reset sends carry non-empty token idempotency keys.
- `httpx.ClientIP` test: XFF trusted from loopback/private peer, ignored from public peer; `auth.requestIP` matches.
- `cmd` compile: production refusal removed; `go build ./cmd/...` passes.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/worker ./internal/auth ./internal/httpx -run 'TestEmail|TestRequestIP|TestClientIP' -v`
Expected: FAIL because idempotency/permanent/IP wiring is absent.

- [ ] **Step 3: Wire the API and worker**

- Replace `email.NewMailpitSender(...)` in `cmd/api` and `cmd/worker` with `email.NewSender(rt.Config)`; return the factory error.
- Delete the production refusal blocks.
- Alert worker sets the idempotency key and checks `email.IsPermanent` before the retry handler.
- Auth verification/reset set token UUID keys.
- Export `httpx.ClientIP`; implement `auth.requestIP` via it.
- Keep existing tests working with the fake sender.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/worker ./internal/auth ./internal/httpx ./cmd/... -v`
Expected: PASS.

- [ ] **Step 5: Quality gate and commit**

Run: `cd backend && gofmt -w cmd internal/worker internal/auth internal/httpx && go vet ./... && go test -short ./...`
Commit:

```bash
git add backend/cmd backend/internal/worker backend/internal/auth backend/internal/httpx
git commit -m "feat: use Resend in production and classify email failures"
```

---

### Task 4: Caddy runtime site address and production stack

**Files:**
- Modify: `web/Caddyfile`
- Modify: `web/Dockerfile`
- Create: `deploy/production/compose.yaml`
- Create: `deploy/production/env.example`
- Create: `deploy/production/README.md`
- Modify: `README.md`

- [ ] **Step 1: Make the Caddy site address runtime-configurable**

Change the first Caddyfile line to:

```caddyfile
{$O1_SITE_ADDRESS::80} {
```

Keep `/healthz`, `/readyz`, `/metrics` 404, `/api/*`, and SPA fallback unchanged. For every API reverse_proxy, set `header_up X-Forwarded-For {remote_host}` so Caddy overwrites any client-supplied forwarding header; the app trusts XFF only from the private Caddy peer. Add `EXPOSE 443` to `web/Dockerfile` (80 remains).

- [ ] **Step 2: Add the production Compose**

Pin:
- `postgres`: strong `POSTGRES_PASSWORD` from env, no published port, volume, healthcheck.
- `api`/`scheduler`/`worker`: `O1_ENV=production`, DB URL from postgres password, real secret/encryption keys, `O1_EMAIL_PROVIDER=resend`, `O1_RESEND_API_KEY`, `O1_EMAIL_FROM`, `O1_EMAIL_REPLY_TO`, `O1_RESEND_RPS`, `O1_APP_BASE_URL=https://${O1_DOMAIN}`, `O1_ALLOWED_ORIGINS=https://${O1_DOMAIN}`, `O1_BOOTSTRAP_ADMIN_EMAIL`, observability envs optional, `restart: unless-stopped`, no published ports.
- `web`: build with VITE args, runtime `O1_SITE_ADDRESS=${O1_DOMAIN}`, ports `80:80`/`443:443`, caddy data/config volumes.
- Optional `alloy` profile with `deploy/observability/alloy.alloy` and remote-write env.
- No Mailpit, no dev keys, no `O1_AUTH_RATE_PER_MINUTE`/burst overrides.

- [ ] **Step 3: Write env.example and runbook**

`env.example`: `O1_DOMAIN`, `POSTGRES_PASSWORD`, `O1_SECRET_KEY`, `O1_ENCRYPTION_KEY`, `O1_EMAIL_FROM`, `O1_RESEND_API_KEY`, optional reply-to/RPS, `O1_BOOTSTRAP_ADMIN_EMAIL`, VITE Sentry args, OTLP/Sentry optional. Placeholders only.

Runbook:
- DNS A/AAAA and ports 80/443.
- Resend domain verification, SPF/DKIM; from address on the verified domain.
- Generate secrets with `openssl rand -base64 32`.
- `cp env.example .env`, `docker compose --env-file .env -f deploy/production/compose.yaml up -d --build`.
- Health check `https://domain/healthz`, `/readyz`; admin bootstrap; registration verification email; incident alert email; TLS cert issuance; logs; update/restart.
- Note backups = Plan 12, CI deploy = Plan 11; `/metrics` private; free-tier Resend quota.
- Optional Alloy profile command.

- [ ] **Step 4: Validate configurations**

```bash
docker compose --env-file deploy/production/env.example -f deploy/production/compose.yaml config
docker run --rm -e O1_SITE_ADDRESS=o1.example.com \
  -v "$PWD/web/Caddyfile:/etc/caddy/Caddyfile:ro" \
  caddy:2-alpine caddy validate --config /etc/caddy/Caddyfile
```

Expected: both valid; production config has no Mailpit, no postgres/API port mappings, and only 80/443 public.

- [ ] **Step 5: Commit**

```bash
git add web/Caddyfile web/Dockerfile deploy/production README.md
git commit -m "feat: add production go-live stack and runtime Caddy address"
```

---

### Task 5: Final verification, docs, and E2E safety

**Files:**
- Modify: `README.md` (if not complete)
- Modify: `web/tests/e2e/smoke.spec.ts` only if the dev Caddy address changes behavior
- Modify: `deploy/production/README.md` (final corrections)

- [ ] **Step 1: Run the complete verification**

```bash
cd backend && gofmt -l . && go vet ./... && golangci-lint run && go test -short ./... && go test ./...
cd web && npm run format:check && npm run lint && npm run typecheck && npm test -- --run && npm run build
docker compose up -d --build
cd web && npm run e2e
```

Expected: all commands exit 0. Dev Compose must still use Mailpit and `http://localhost:5173`.

- [ ] **Step 2: Validate the production artifacts**

```bash
docker compose --env-file deploy/production/env.example -f deploy/production/compose.yaml config >/dev/null
docker run --rm -e O1_SITE_ADDRESS=o1.example.com \
  -v "$PWD/web/Caddyfile:/etc/caddy/Caddyfile:ro" \
  caddy:2-alpine caddy validate --config /etc/caddy/Caddyfile
```

- [ ] **Step 3: Documentation check**

Confirm README covers: `O1_EMAIL_PROVIDER`, Resend key/from/reply-to, production stack path, `O1_DOMAIN`/`O1_SITE_ADDRESS`, TLS, Mailpit dev behavior, permanent/transient email errors, and that backups/CI deploy are future plans.

- [ ] **Step 4: Commit**

```bash
git add README.md web/tests/e2e deploy/production
git commit -m "docs: finalize production email and go-live guidance"
```

---

## Self-Review Checklist

- [ ] Every spec section maps to a task: Resend/retry/idempotency (§7) → Task 1; config/provider (§6) → Task 2; worker/auth/IP (§8) → Task 3; Caddy/production stack (§9) → Task 4; verification/docs (§10/§11) → Task 5.
- [ ] Review Focus items have tests: provider safety → Task 1; leakage → Task 2; retry/idempotency → Tasks 2/3; production exposure → Task 4/5; client IP/auth mail → Task 3.
- [ ] Names/signatures match: `email.NewSender`, `Message.IdempotencyKey`, `NewResendSender`, `DeliveryError`, `IsPermanent`, `RetrySender`, `httpx.ClientIP`, `O1_SITE_ADDRESS`, `O1_EMAIL_PROVIDER`, `O1_RESEND_*`.
- [ ] No task depends on a later task without saying so; each ends with a passing test cycle and a commit.

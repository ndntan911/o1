# Plan 10 — Production Email and Go-Live Configuration Design

**Status:** Draft for approval
**Date:** 2026-11-10
**Depends on:** Plans 1–9, all merged into `main`.
**Out of scope:** CI/CD image publishing and automated deploy (Plan 11),
backup/WAL/restore (Plan 12), rollups/retention (Plan 13), load/security review
(Plan 14), managed Postgres, multiple regions, bounce/complaint webhooks, DKIM
automation, inbound email, and marketing email.

## 1. Intent

Plan 10 makes the application deployable to a real host: **Resend production
email**, removal of the deliberate production startup refusal, automatic HTTPS
through Caddy, and a separate production Compose/runbook that does not inherit
development defaults. It stops short of automating deployment; the operator
still runs a documented one-command deploy.

The success condition is a fresh host where `O1_ENV=production`,
`O1_EMAIL_PROVIDER=resend`, a domain, and secrets are configured; registration
verification email and incident alert email are delivered by Resend, and the
public site is served over valid TLS.

## 2. Current state

- `email.Sender` is a one-method interface. Only `MailpitSender` exists: plain
  unauthenticated SMTP with no TLS/auth.
- `auth.Service` uses the sender for verification and password-reset email.
  `worker.AlertWorker` uses it for email alert deliveries; delivery failures
  go through the shared retry budget.
- `config.Config` already has `ResendAPIKey`, `EmailFrom`, `AppBaseURL`, and
  `AllowedOrigins`, but nothing constructs a Resend sender.
- `cmd/api` and `cmd/worker` explicitly refuse to start with
  `O1_ENV=production` because dropping mail would be worse than failing.
- Caddy listens on `:80` only. The Compose web service publishes `5173:80`.
- `compose.yaml` is development-only: Mailpit, a published Postgres port,
  published API port, dev secret keys, `O1_ENV=development`, raised auth rate
  limits.
- `O1_SECRET_KEY` and `O1_ENCRYPTION_KEY` are already required in production;
  the API validates the encryption key there.
- CI builds images but does not push or deploy them.

## 3. Goals, non-goals, anti-goals

**Goals**

1. Add a Resend HTTP API sender implementing `email.Sender` with request
   timeout, response-size bound, input validation, and sanitized errors.
2. Select the email sender from configuration: Mailpit for development/test,
   Resend for production, with an explicit override for testing.
3. Remove the production startup refusal; refuse startup only when the selected
   provider's required configuration is missing.
4. Add a production Compose file and runbook that do not expose Postgres, the
   API, or metrics; use automatic HTTPS; and require real secrets.
5. Make the Caddy site address configurable so the same web image works on
   `:80` locally and on a domain with TLS in production.
6. Classify Resend failures as transient or permanent so email alert deliveries
   do not retry invalid credentials or malformed addresses five times.
7. Add idempotency keys and bounded in-process retries for transient email
   failures, so a lost response does not duplicate alerts and a brief provider
   hiccup does not immediately kill a verification link.
8. Record the real client IP behind Caddy for sessions and audit events.
9. Include an optional Alloy profile in the production stack so go-live is
   observable without self-hosting Grafana.
10. Document Resend domain verification, SPF/DKIM, production env, quotas, and a
    manual go-live smoke test.

**Non-goals**

- Automated CI/CD deploy, image registry publishing, and rollback automation.
- Backup, WAL, restore drills, retention, or rollups.
- Bounce/complaint webhooks, inbound email, or deliverability dashboards.
- Multi-provider failover, SES/Postmark, or a generic SMTP-with-TLS client.
- Marketing/bulk email or unsubscribe management.
- Multi-domain or custom-domain-per-workspace support.

**Anti-goals**

- Starting production with Mailpit, a dev secret, or no mail sender.
- Logging or returning the Resend API key or full provider response bodies.
- Retrying permanent Resend errors (401/403/422) until the retry budget is
  exhausted.
- Publishing Postgres, the API listener, or `/metrics` on the production host.
- Baking a specific domain into the web image; the site address is runtime
  configuration.
- A production Compose file that silently inherits development secrets or
  Mailpit.

## 4. Approaches

**A. Resend HTTP API sender with env-selected provider (chosen).**
`email.NewSender(cfg)` returns MailpitSender or ResendSender. Production
defaults to Resend and requires its key/from address; dev/test default to
Mailpit. ResendSender POSTs to `/emails` over HTTPS, classifies status codes,
and returns a typed delivery error. Production config is a standalone Compose
file plus a Caddy site-address env.

*Why:* it matches the spec's Resend choice, gives the best error classification
and observability, and keeps Mailpit development behavior unchanged.

**B. Resend SMTP relay.**
Point the existing SMTP client at `smtp.resend.com` with TLS and credentials.
Less code, but the current client has no TLS/auth, and SMTP responses are harder
to classify and surface safely than the HTTP API.

**C. Generic SMTP provider.**
Most portable, but requires TLS/auth/bounce handling and provider-specific
documentation that Plan 10 does not need.

**Decision:** A.

## 5. Constraints

- Email provider values are `mailpit` and `resend`. `O1_EMAIL_PROVIDER` is
  optional; when empty it resolves to `mailpit` for development/test and
  `resend` for production. Any other value fails config loading.
- Production requires `O1_EMAIL_PROVIDER=resend` (explicit or resolved),
  `O1_RESEND_API_KEY`, and a valid `O1_EMAIL_FROM`. Production with `mailpit`
  fails config loading.
- `O1_EMAIL_FROM` must parse as a mailbox (display name allowed for Resend).
  `O1_EMAIL_REPLY_TO`, when set, must parse as a mailbox.
- Resend requests: `POST {base}/emails`, `Authorization: Bearer <key>`,
  `Content-Type: application/json`, `User-Agent: o1-email/1.0`, 10-second
  timeout, at most 64 KiB response drain. Base URL defaults to
  `https://api.resend.com`; `O1_RESEND_API_URL` can override it for tests/dev.
- Resend success is any 2xx; the body is not required. 408, 425, 429, 5xx,
  timeouts, and transport errors are transient. Other 4xx (including 400, 401,
  403, 422) are permanent. A permanent error marks an alert delivery failed
  without consuming the retry budget.
- Sender errors must never include the API key, Authorization header, request
  body, recipient address, or the raw provider body. Error text is a fixed
  message plus the HTTP status when relevant.
- `email.Message` gains an optional `IdempotencyKey`. Resend sends it as
  `Idempotency-Key`; Mailpit ignores it. Alert deliveries use the delivery
  UUID, verification uses the token UUID, and password reset uses the reset
  token UUID, so a retried request with a lost response does not duplicate the
  message.
- Email sends use a bounded retry wrapper: up to three attempts for transient
  errors with short context-aware backoff. Authentication email still has no
  persistent outbox; a process crash between accept and send can lose it, and
  the existing resend-verification flow remains the recovery path.
- The Resend sender applies a per-process rate limiter (`O1_RESEND_RPS`,
  default 2 requests/second, burst 2) and honors context cancellation. The
  free-tier daily quota is documented; production alerting requires a plan
  sized for incident volume.
- Client IPs for auth sessions/audit events come from the same trusted
  forwarding-header logic as HTTP rate limiting (X-Forwarded-For only from
  loopback/private peers), not the Caddy container address.
- Email messages are validated before I/O: non-empty, syntactically valid `To`,
  no CR/LF in `To`, subject, or headers; subject length is bounded.
- Caddy site address is `{$O1_SITE_ADDRESS::80}`. Production sets the domain
  and maps ports 80/443 with persistent `/data` and `/config` volumes.
- Production Compose publishes only the web ports. Postgres, API, scheduler,
  worker, and metrics/health listeners stay on the internal network or
  loopback.
- Secrets are supplied through the host environment or an untracked `.env`;
  no production secret is committed.
- `O1_ENV=production` still requires a 32-byte encryption key and the existing
  production config validation; development/test behavior is unchanged.
- UI copy is English. Every task ends with formatting, lint/typecheck where
  applicable, tests, and a commit.

## 6. Configuration changes

Add to `config.Config`:

| Variable | Default | Meaning |
|----------|---------|---------|
| `O1_EMAIL_PROVIDER` | resolved | `mailpit` or `resend`; dev/test default mailpit, production default resend |
| `O1_RESEND_API_URL` | `https://api.resend.com` | API base URL; test/dev override |
| `O1_EMAIL_REPLY_TO` | empty | optional Reply-To mailbox |
| `O1_RESEND_RPS` | `2` | per-process Resend send rate limit |
| `O1_RESEND_BURST` | `2` | Resend send burst |
| `O1_SITE_ADDRESS` | `:80` | Caddy site address/hostname (web container runtime) |

Resolution rules:

- `O1_EMAIL_PROVIDER` empty → `mailpit` when `O1_ENV` is development/test,
  `resend` when production.
- `resend` requires `O1_RESEND_API_KEY` and `O1_EMAIL_FROM`; `mailpit` requires
  `O1_SMTP_ADDR` and `O1_EMAIL_FROM`.
- Production + `mailpit` is a config error.
- `O1_EMAIL_FROM`/`O1_EMAIL_REPLY_TO` are validated with `net/mail`.

`cmd/api` and `cmd/worker` build the sender with `email.NewSender(cfg)`; the
scheduler does not send email. The production refusal block is deleted in favor
of the config validation.

## 7. Resend sender

New `backend/internal/email/resend.go`:

```go
type ResendSender struct {
    apiKey  string
    from    string
    replyTo string
    baseURL string
    client  *http.Client
}

func NewResendSender(apiKey, from, replyTo, baseURL string) *ResendSender
```

- Constructor trims the base URL/trailing slash and uses a client with a
  10-second timeout.
- `Send` validates the message, marshals a struct:
  ```json
  {
    "from": "o1 <noreply@example.com>",
    "to": ["user@example.com"],
    "subject": "Verify your email",
    "text": "...",
    "html": "...",
    "reply_to": "support@example.com"
  }
  ```
  `reply_to` is omitted when empty. Exactly one recipient per message, matching
  the current `email.Message` shape.
- Uses `http.NewRequestWithContext`; sets headers; adds `Idempotency-Key` when
  `Message.IdempotencyKey` is non-empty; does not follow redirects
  (`CheckRedirect` returns `http.ErrUseLastResponse`) because a redirect must
  not move a bearer token to another host.
- Waits on a per-sender `rate.Limiter` (`O1_RESEND_RPS`/burst) before sending.
- Drains at most 64 KiB and closes the body.
- 2xx → nil. Transient/permanent classification per §5.
- `DeliveryError`:
  ```go
  type DeliveryError struct {
      Message   string // sanitized
      Permanent bool
      Cause     error
  }
  func (e *DeliveryError) Error() string
  func (e *DeliveryError) Unwrap() error
  func IsPermanent(err error) bool
  ```
- Error messages: `"resend returned HTTP 500"`, `"resend request failed"`,
  `"invalid recipient address"`, etc. No provider body, recipient, or key.

Add `email.Message.IdempotencyKey string` (optional, ignored by Mailpit) and a
`email.RetrySender` wrapper: three attempts for transient errors only, with
context-aware 250ms/1s backoff. `email.NewSender` returns the provider sender
wrapped in the retry sender. `email.NewSender(cfg config.Config) (Sender,
error)` resolves the selected provider; avoid import cycles (`config` does not
import `email`; `email` may import `config`).

## 8. Worker and auth integration

- `worker.AlertWorker.sendEmail` sets `IdempotencyKey` to the delivery UUID and
  checks `email.IsPermanent(err)`:
  - permanent → `failWithoutRetry` with the sanitized error.
  - otherwise → existing retry budget (the retry sender already made up to
    three quick attempts).
- Auth verification and password-reset messages set `IdempotencyKey` to the
  token UUID. The retry sender improves transient resilience; a process crash
  can still lose auth mail, and resend-verification remains the recovery path.
- `auth.requestIP` uses the trusted forwarded-header logic from `httpx` so
  sessions and audit rows record the real client IP behind Caddy.
- Email delivery metrics already record `channel_kind=email`; no metric change
  required.

## 9. Production deployment

Add `deploy/production/`:

- `compose.yaml` — standalone production stack:
  - `postgres` with a strong password from `POSTGRES_PASSWORD` and no published
    port; persistent volume; healthcheck.
  - `api`, `scheduler`, `worker` on the internal network, `O1_ENV=production`,
    `O1_DATABASE_URL` pointing at `postgres`, secrets from env, no published
    ports, `restart: unless-stopped`.
  - `O1_EMAIL_PROVIDER=resend`, `O1_RESEND_API_KEY`, `O1_EMAIL_FROM`,
    `O1_EMAIL_REPLY_TO`, `O1_RESEND_RPS`, `O1_APP_BASE_URL=https://${O1_DOMAIN}`,
    `O1_ALLOWED_ORIGINS=https://${O1_DOMAIN}`, `O1_SITE_ADDRESS=${O1_DOMAIN}`.
  - `web` built from the repo web Dockerfile, `O1_SITE_ADDRESS` runtime env,
    ports `80:80` and `443:443`, persistent `caddy_data`/`caddy_config`
    volumes.
  - Optional `alloy` service under the `observability` profile, same scrape
    config as the dev bundle, remote-write credentials from env.
  - No Mailpit, no published Postgres/API/metrics ports, no dev keys, no
    raised auth rate limits. Observability envs optional.
- `env.example` — all required/optional production variables with placeholders
  and comments.
- `README.md` — DNS/TLS prerequisites, Resend domain verification and
  SPF/DKIM, secret generation, first deploy, admin bootstrap, smoke tests,
  update/restart, logs, and troubleshooting. Explicitly says backups are
  Plan 12 and CI deploy is Plan 11.
- `Caddyfile` changes: the existing `web/Caddyfile` first line becomes
  `{$O1_SITE_ADDRESS::80}`; health/metrics/API rules stay. Dev Compose sets
  `O1_SITE_ADDRESS=:80`; production sets the domain.

The production Compose can reuse the existing backend/web Dockerfiles. Images
can be built on the host for Plan 10; registry publishing is Plan 11.

## 10. Testing

**Backend**

- Config: provider resolution (dev/test → mailpit, production → resend,
  explicit override), production+mailpit error, missing Resend key/from error,
  invalid provider, mailbox validation, base URL override, RPS/burst parsing.
- Resend sender with `httptest.Server`:
  - exact URL `/emails`, method POST, Bearer header, User-Agent,
    Content-Type, JSON fields, reply_to and Idempotency-Key omitted/present.
  - 2xx success; 204 success.
  - 400/401/403/422 permanent; 408/425/429/500/503 transient; timeout and
    connection errors transient; redirect not followed.
  - rate limiter delays requests and respects context cancellation.
  - response body drain bound; error strings contain no key, recipient, or
    provider body.
  - invalid to/subject/header injection rejected before any request.
- Retry sender: transient errors retried up to three times; permanent errors
  not retried; context cancellation stops backoff; attempts are idempotent via
  the key.
- Factory: returns the right provider; production refusal removed; `cmd`
  build compiles.
- Worker: permanent email error marks failed with one attempt and `Work`
  returns nil; transient error keeps existing retry behavior; the delivery UUID
  is sent as Idempotency-Key; existing email tests stay green.
- Auth: verification and password-reset messages send through the selected
  sender with token idempotency keys; `requestIP` trusts forwarded headers only
  from loopback/private peers and ignores them from public peers.
- Existing suite stays green.

**Web / deployment**

- Caddy config validates for `:80` and a domain form.
- Production compose validates with `docker compose config` and contains no
  Mailpit, no published Postgres/API/metrics ports, and the required env.
- `O1_SITE_ADDRESS` is forwarded to the web container.
- Manual smoke on a production-like local stack: `O1_RESEND_API_URL` points at
  a local httptest-style mock for automated tests; the real go-live smoke uses
  a verified Resend domain/key and is an operator acceptance step, not a CI
  dependency.
- No E2E change required unless the Compose site address changes the dev URL;
  keep `http://localhost:5173` working.

## 11. Acceptance

1. A development/test stack still uses Mailpit and sends verification/reset
   email exactly as before.
2. A production configuration without Resend key/from fails startup with a
   clear config error; valid configuration starts and sends through Resend.
3. Resend 4xx permanent errors mark alert deliveries failed without retrying;
   transient errors use the retry sender and existing budget; a lost response
   with the same idempotency key does not duplicate an alert.
4. The production Compose stack serves only the web ports publicly, uses
   automatic HTTPS for the configured domain, does not include Mailpit or dev
   secrets, and can start the optional Alloy profile.
5. A new user receives the verification email through Resend, and an incident
   alert email is delivered.
6. API keys, SMTP/Resend secrets, and provider response bodies never appear in
   logs or API errors; sessions/audit rows record the real client IP behind
   Caddy.
7. The existing test suites, E2E, and development workflow remain green.

## 12. Calls made by the builder

- Resend HTTP API, not SMTP relay.
- Sender selected by env with production default `resend`; dev/test default
  `mailpit`; production+mailpit rejected.
- `O1_RESEND_API_URL` is a test/dev override, default `https://api.resend.com`.
- Optional `Message.IdempotencyKey`; alert deliveries use the delivery UUID,
  auth mail uses the token UUID.
- Bounded in-process retries (3 attempts) for transient email errors; no
  persistent outbox. Auth mail can still be lost on a process crash; resend
  verification is the recovery path.
- Per-process Resend rate limit `O1_RESEND_RPS` (default 2) and burst; free-tier
  daily quota is documented rather than solved in code.
- Auth `requestIP` uses trusted forwarded headers behind Caddy.
- Caddy site address runtime env `O1_SITE_ADDRESS`, default `:80`.
- Standalone production Compose with an optional Alloy profile.
- Production publish only web 80/443; no registry/deploy automation.
- Email permanent/transient classification is part of this plan.
- Backups, retention, and CI deploy stay in Plans 11–13.

## 13. Left to the builder

- Exact factory location and file names (as long as the interface stays
  `email.Sender`).
- Whether the production Compose duplicates or extends the dev file, provided
  no dev defaults/Mailpit leak in.
- Caddy TLS storage paths/volume names.
- Resend dashboard/DNS wording in the runbook.
- Exact error wording that remains sanitized.

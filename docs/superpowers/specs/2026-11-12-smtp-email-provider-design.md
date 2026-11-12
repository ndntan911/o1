# SMTP Email Provider — Design

**Status:** Draft for approval
**Date:** 2026-11-12
**Depends on:** Plan 10 (production email go-live), Plan 11 (registry deploys)
**Scope owner:** o1 uptime monitor

## 1. Intent

The production stack can only send transactional email through Resend, and
Resend rejects the operator's current domain (`o1.work.gd`, a free subdomain).
Add a first-class `smtp` provider so production can use any authenticated SMTP
relay — SES, Mailgun, Postmark, a corporate relay, or Gmail with an app
password — without changing application behaviour.

The existing `MailpitSender` already speaks SMTP with a tested MIME builder,
header-injection guards, context cancellation, and connection deadlines. This
change generalises that path instead of introducing a new dependency.

## 2. Current State

- `config.ResolveEmailProvider` accepts only `mailpit` (development) and
  `resend` (production default); production rejects `mailpit`.
- `email.NewSender` switches on those two providers; `MailpitSender` and
  `ResendSender` both return a sanitised `email.DeliveryError` for permanent
  failures, and `RetrySender` retries everything else three times.
- `MailpitSender` builds a multipart/alternative message and validates headers
  before any network I/O. It dials plaintext SMTP with no authentication and
  no TLS, which is correct only for the local Mailpit container.
- `deploy/production/compose.yaml` hardcodes `O1_EMAIL_PROVIDER: resend` and
  requires `O1_RESEND_API_KEY`, so an SMTP-only deployment cannot render.
- Docs state "Production email is Resend only" in `README.md`,
  `deploy/production/README.md`, and `deploy/CICD.md`.

## 3. Goals

1. `O1_EMAIL_PROVIDER=smtp` works in production with STARTTLS or implicit TLS
   and optional PLAIN authentication.
2. Permanent relay failures (5xx) are not retried; transient failures (4xx,
   timeouts) keep the existing three-attempt retry.
3. Compose no longer requires a Resend key when SMTP is selected, and it passes
   the SMTP settings from `/opt/o1/.env`.
4. Credentials never appear in errors, logs, or metric labels; TLS certificate
   verification is on.
5. Documentation covers provider selection, the required variables, and the
   provider-side verification each relay still requires.

## 4. Non-Goals

- OAuth2 (XOAUTH2) for Gmail.
- Connection pooling or a background queue; volume stays inside the existing
  per-send path.
- Attachments, HTML-only messages, or per-workspace SMTP credentials.
- Changing the `mailpit` behaviour or the Resend path.
- Automatic provider failover.

## 5. Approaches

**A. Generalise the existing SMTP sender (chosen).** Extract the message
renderer and header validation into a shared file, add an `SMTPSender` with
TLS and PLAIN auth, and keep `MailpitSender` on the same renderer. Reuses the
tested MIME logic, adds no dependency, and keeps one code path for SMTP
dialing, deadlines, and cancellation.

**B. Add `github.com/wneessen/go-mail`.** Less hand-written protocol code and
built-in pooling, but introduces a dependency and its review/scan surface for a
low-volume sender. Rejected in favour of A.

## 6. Design

### 6.1 Configuration

New constants and fields:

- `EmailProviderSMTP = "smtp"`.
- `SMTPUsername`, `SMTPPassword string`.
- `SMTPTLS string`, one of `starttls`, `implicit`, `none`.

Parsing (`config.Load`):

- `O1_SMTP_USERNAME`, `O1_SMTP_PASSWORD` default to empty.
- `O1_SMTP_TLS` defaults to `starttls`; lowercased and validated.
- `O1_SMTP_ADDR` keeps its existing `localhost:1025` default.

`ResolveEmailProvider` rules:

- The accepted set becomes `mailpit`, `resend`, `smtp`. The default stays
  `mailpit` outside production and `resend` in production.
- `smtp` requires a non-empty `O1_SMTP_ADDR`. In production it also rejects the
  default `localhost:1025`, which can only be the Mailpit address.
- Either both SMTP credentials are empty or both are set; a half-configured
  pair is an error.
- `O1_SMTP_TLS=none` is allowed only outside production and only without
  credentials.
- `starttls`/`implicit` are required as soon as credentials are present.
- `resend` still requires `O1_RESEND_API_KEY`; `smtp` does not.
- Error messages never echo addresses, hosts, or credentials.

### 6.2 SMTP sender

New file `backend/internal/email/smtp.go`:

- `NewSMTPSender(addr, from, replyTo, username, password, tlsMode string) (*SMTPSender, error)`.
- Dialing reuses the current pattern: `net.Dialer` with a 10s connect timeout, a
  30s overall deadline unless the context is earlier, and a goroutine that
  interrupts blocked I/O on cancellation.
- `starttls`: plain dial, `smtp.NewClient`, `StartTLS` with
  `tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}`, then auth.
- `implicit`: `tls.DialWithDialer` with the same TLS config, then
  `smtp.NewClient`.
- Authentication uses `smtp.PlainAuth("", username, password, host)` only when
  both values are set.
- `MAIL FROM`, `RCPT TO`, `DATA`, `QUIT` follow the existing Mailpit sequence.
- Error mapping: a `*textproto.Error` with `Code >= 500` becomes
  `DeliveryError{Message: "smtp: relay rejected the message", Permanent: true}`
  with the numeric code appended (for example `smtp: relay rejected the message
  (550)`); 4xx, dial, TLS, and timeout errors stay transient. Server response
  text is never included because relays can echo the recipient or credentials.
- A test seam accepts an explicit `*tls.Config` so tests can trust a generated
  certificate; production always verifies against system roots.

### 6.3 Shared message code

Move `validateMessage`, `render`, `sanitizeHeader`, and `newBoundary` from
`mailpit.go` into `email/message.go` as package-level functions:

- `validateMessage(from string, msg Message) error` unchanged.
- `renderMessage(from, replyTo string, msg Message) []byte` — same bytes as
  today, plus an optional `Reply-To` header. Mailpit passes an empty reply-to
  and its output stays byte-identical.

`MailpitSender` keeps its public constructor and delegates to the shared
functions.

### 6.4 Factory and Compose

- `email.NewSender` adds `case config.EmailProviderSMTP`, constructing the new
  sender from config.
- `deploy/production/compose.yaml`:
  - `O1_EMAIL_PROVIDER: ${O1_EMAIL_PROVIDER:-resend}`
  - `O1_RESEND_API_KEY: ${O1_RESEND_API_KEY:-}` (validated by the app instead
    of by Compose)
  - `O1_SMTP_ADDR: ${O1_SMTP_ADDR:-}`
  - `O1_SMTP_USERNAME: ${O1_SMTP_USERNAME:-}`
  - `O1_SMTP_PASSWORD: ${O1_SMTP_PASSWORD:-}`
  - `O1_SMTP_TLS: ${O1_SMTP_TLS:-starttls}`
- `deploy/production/env.example` documents the choice: either
  `O1_EMAIL_PROVIDER=resend` + `O1_RESEND_API_KEY`, or
  `O1_EMAIL_PROVIDER=smtp` + the four SMTP values.

### 6.5 Documentation

- `deploy/production/README.md` §2 becomes "Email provider": keep the Resend
  steps and add an SMTP subsection (SES/Mailgun/Postmark/Gmail app password;
  host, port, STARTTLS vs implicit, PLAIN auth; provider-side domain or
  identity verification still required). Update the "email not delivered"
  troubleshooting row and the prerequisite.
- `README.md`: replace "Production email is Resend only" with the provider
  choice.
- `deploy/CICD.md`: the required-env list no longer treats
  `O1_RESEND_API_KEY` as unconditional; add the optional SMTP values.

### 6.6 Tests

- `config_test.go`: SMTP accepted with STARTTLS and credentials; credentials
  require TLS; half-configured credentials rejected; invalid TLS mode rejected;
  production rejects `localhost:1025` and `none`; production `resend` still
  requires a key; an SMTP config needs no Resend key.
- `smtp_test.go`: message rendering with and without `Reply-To`; header
  injection rejection inherited from `validateMessage`; a fake relay that
  answers `550` produces a permanent `DeliveryError`, `451` stays transient;
  STARTTLS handshake succeeds against a generated certificate through the test
  seam; implicit TLS does the same; cancellation during a stalled relay returns
  context cancellation promptly.
- `factory_test.go`: `smtp` builds a sender; unknown provider still errors.
- `deploy/scripts/test-compose-config.sh`: assert the rendered config passes
  `O1_EMAIL_PROVIDER` and the SMTP values through and no longer requires a
  Resend key.

## 7. Security

- TLS 1.2 minimum, certificate verification on; no skip-verify option.
- `smtp.PlainAuth` is only reachable over STARTTLS or implicit TLS.
- Credentials are read from the environment only, never logged; error messages
  contain the numeric SMTP code but never the server text, host, or address.
- `O1_SMTP_TLS=none` is impossible in production and impossible with
  credentials.
- The compose file keeps secrets in `/opt/o1/.env`; `env.example` holds
  placeholders only.

## 8. Rollout

1. Deploy the release with `O1_EMAIL_PROVIDER` still unset (defaults to
   `resend`); nothing changes.
2. Set the SMTP values in `/opt/o1/.env`, set `O1_EMAIL_PROVIDER=smtp`, then
   recreate `api` and `worker` (verification and incident mail both use the
   sender).
3. Confirm a verification email and an incident email, then remove the Resend
   key if it is no longer used.
4. Rollback is configuration-only: set `O1_EMAIL_PROVIDER=resend` and recreate.

## 9. Decisions Left to the Builder

- Exact doc wording and example relay settings.
- Whether to expose a HELO name override; not needed for the providers listed.
- Whether to keep `O1_SMTP_ADDR`'s development default or require an explicit
  value for the `smtp` provider outside production.

## 10. Acceptance

- `O1_EMAIL_PROVIDER=smtp` sends a real verification email through a real relay.
- A wrong password surfaces as a sanitised permanent failure, not a retry loop.
- A transient 4xx retries three times.
- `go test -short ./...`, `golangci-lint run`, and
  `bash deploy/scripts/test-compose-config.sh` pass.

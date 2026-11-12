# SMTP Email Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an authenticated `smtp` email provider so production can send transactional mail through any SMTP relay, without Resend.

**Architecture:** Generalise the existing Mailpit SMTP path: extract the MIME renderer and header validation into shared functions, add an `SMTPSender` with STARTTLS/implicit TLS and PLAIN auth, and wire it through config, factory, Compose, and docs. No new dependencies.

**Tech Stack:** Go 1.26, `net/smtp`, `crypto/tls`, Docker Compose, Bash test scripts.

**Spec:** `docs/superpowers/specs/2026-11-12-smtp-email-provider-design.md`

## Global Constraints

- Provider set becomes `mailpit`, `resend`, `smtp`; the default stays `mailpit` outside production and `resend` in production.
- `smtp` requires `O1_SMTP_ADDR`; production rejects the `localhost:1025` default.
- Credentials are either both empty or both set; credentials require `O1_SMTP_TLS` of `starttls` or `implicit`.
- `O1_SMTP_TLS=none` is allowed only outside production and only without credentials.
- TLS is 1.2+, certificate verification is on, and there is no skip-verify option.
- SMTP 5xx responses become permanent `email.DeliveryError`s; 4xx, dial, TLS, and timeout errors stay transient.
- Error text carries the numeric SMTP code but never relay text, host names, addresses, or credentials.
- Existing Mailpit output stays byte-identical; the Resend path is untouched.
- Commits use conventional prefixes (`feat:`, `test:`, `docs:`, `fix:`).

## Review Focus

1. **Half-configured SMTP credentials** (username without password or the reverse): must fail startup rather than silently send unauthenticated. Pinned by `TestResolveEmailProvider_SMTPRejectsHalfCredentials` (Task 1).
2. **Credentials over plaintext** (`O1_SMTP_TLS=none` or production `none`): must be impossible. Pinned by `TestResolveEmailProvider_SMTPRejectsInsecureTLS` (Task 1) and by `smtp.PlainAuth` only being reachable after TLS in `SMTPSender.Send` (Task 2).
3. **Relay 5xx retried forever**: must be permanent so the worker stops retrying. Pinned by `TestSMTPSender_ClassifiesPermanentAndTransient` (Task 2).
4. **Relay response text leaking into errors/logs**: must carry only the numeric code. Pinned by the same classification test, which asserts the message does not contain the relay text (Task 2).
5. **Compose still requiring a Resend key**: an SMTP-only deploy would fail to render. Pinned by the no-Resend-key render check in `test-compose-config.sh` (Task 3).

---

### Task 1: SMTP configuration and provider resolution

**Files:**
- Modify: `backend/internal/config/config.go`
- Modify: `backend/internal/config/config_test.go`

**Interfaces:**
- Consumes: existing `env()`, `envInt()` helpers and `Config.validate()`.
- Produces: `config.EmailProviderSMTP = "smtp"`, `Config.SMTPUsername`, `Config.SMTPPassword`, `Config.SMTPTLS string`; `ResolveEmailProvider` accepts `smtp`.

- [ ] **Step 1: Write the failing tests**

Append to `backend/internal/config/config_test.go`:

```go
func TestResolveEmailProvider_SMTP(t *testing.T) {
	cfg := config.Config{
		Env:          "production",
		EmailProvider: "smtp",
		SMTPAddr:     "smtp.example.net:587",
		SMTPTLS:      "starttls",
		SMTPUsername: "user",
		SMTPPassword: "pass",
		EmailFrom:    "o1 <noreply@example.net>",
	}
	got, err := cfg.ResolveEmailProvider()
	if err != nil {
		t.Fatal(err)
	}
	if got != config.EmailProviderSMTP {
		t.Fatalf("provider = %q", got)
	}
}

func TestResolveEmailProvider_SMTPNeedsNoResendKey(t *testing.T) {
	cfg := config.Config{
		Env:           "production",
		EmailProvider: "smtp",
		SMTPAddr:      "smtp.example.net:587",
		SMTPTLS:       "starttls",
		EmailFrom:     "o1 <noreply@example.net>",
		ResendAPIKey:  "",
	}
	if _, err := cfg.ResolveEmailProvider(); err != nil {
		t.Fatalf("smtp should not require a Resend key: %v", err)
	}
}

func TestResolveEmailProvider_SMTPRejectsHalfCredentials(t *testing.T) {
	for _, tc := range []struct{ user, pass string }{{"user", ""}, {"", "pass"}} {
		cfg := config.Config{
			Env:           "production",
			EmailProvider: "smtp",
			SMTPAddr:      "smtp.example.net:587",
			SMTPTLS:       "starttls",
			SMTPUsername:  tc.user,
			SMTPPassword:  tc.pass,
			EmailFrom:     "o1 <noreply@example.net>",
		}
		if _, err := cfg.ResolveEmailProvider(); err == nil {
			t.Fatalf("accepted half credentials user=%q", tc.user)
		}
	}
}

func TestResolveEmailProvider_SMTPRejectsInsecureTLS(t *testing.T) {
	base := config.Config{
		Env:           "production",
		EmailProvider: "smtp",
		SMTPAddr:      "smtp.example.net:587",
		EmailFrom:     "o1 <noreply@example.net>",
	}
	for _, tc := range []struct {
		tls  string
		user string
		pass string
	}{
		{"none", "", ""},
		{"none", "user", "pass"},
		{"bogus", "user", "pass"},
	} {
		cfg := base
		cfg.SMTPTLS, cfg.SMTPUsername, cfg.SMTPPassword = tc.tls, tc.user, tc.pass
		if _, err := cfg.ResolveEmailProvider(); err == nil {
			t.Fatalf("accepted tls=%q with credentials=%v", tc.tls, tc.user != "")
		}
	}
}

func TestResolveEmailProvider_SMTPRejectsMailpitAddrInProduction(t *testing.T) {
	cfg := config.Config{
		Env:           "production",
		EmailProvider: "smtp",
		SMTPAddr:      "localhost:1025",
		SMTPTLS:       "starttls",
		EmailFrom:     "o1 <noreply@example.net>",
	}
	if _, err := cfg.ResolveEmailProvider(); err == nil {
		t.Fatal("accepted the Mailpit address in production")
	}
}

func TestLoad_SMTPTLSDefaultsToStartTLS(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_SMTP_TLS", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMTPTLS != "starttls" {
		t.Fatalf("SMTPTLS = %q, want starttls", cfg.SMTPTLS)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go -C backend test ./internal/config -run 'SMTP' -v`
Expected: FAIL — `config.EmailProviderSMTP`, `SMTPUsername`, `SMTPPassword`, `SMTPTLS` are undefined.

- [ ] **Step 3: Implement the configuration**

In `backend/internal/config/config.go`:

1. Add the constant next to the other providers:
   ```go
   EmailProviderSMTP = "smtp"
   ```
2. Add fields after `EmailReplyTo`:
   ```go
   SMTPUsername string
   SMTPPassword string
   SMTPTLS      string
   ```
3. In `Load`, alongside the other string settings:
   ```go
   SMTPUsername: os.Getenv("O1_SMTP_USERNAME"),
   SMTPPassword: os.Getenv("O1_SMTP_PASSWORD"),
   SMTPTLS:      strings.ToLower(strings.TrimSpace(env("O1_SMTP_TLS", "starttls"))),
   ```
4. In `ResolveEmailProvider`, extend the accepted set and add a `case EmailProviderSMTP` that:
   - rejects `strings.TrimSpace(cfg.SMTPAddr) == ""` and `cfg.SMTPAddr == "localhost:1025"` when `cfg.Env == "production"`;
   - rejects a half-configured credential pair;
   - rejects a TLS mode outside `starttls|implicit|none`;
   - rejects `none` in production;
   - rejects `none` when credentials are set.
   Update the invalid-provider error to `must be one of mailpit, resend, smtp`.
   Error strings must not include the address, host, or credentials.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go -C backend test ./internal/config -run 'SMTP' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/config/config.go backend/internal/config/config_test.go
git commit -m "feat: add SMTP email provider configuration"
```

---

### Task 2: Shared message code and the SMTP sender

**Files:**
- Create: `backend/internal/email/message.go`
- Create: `backend/internal/email/smtp.go`
- Create: `backend/internal/email/smtp_test.go`
- Modify: `backend/internal/email/mailpit.go`
- Modify: `backend/internal/email/mailpit_test.go` (only if the extraction changes a test reference)

**Interfaces:**
- Consumes: `config.Config.SMTPAddr`, `SMTPUsername`, `SMTPPassword`, `SMTPTLS`; `Message`, `DeliveryError`, `validateMessage`.
- Produces: `NewSMTPSender(addr, from, replyTo, username, password, tlsMode string) (*SMTPSender, error)`; package functions `renderMessage(from, replyTo string, msg Message) []byte`, `validateMessage(from string, msg Message) error`.

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/email/smtp_test.go` with a fake relay helper and these tests:

```go
func TestRenderMessage_ReplyTo(t *testing.T) {
	raw := string(renderMessage("o1 <a@b.c>", "help@b.c", Message{To: "d@e.f", Subject: "s", Text: "t", HTML: "<p>t</p>"}))
	if !strings.Contains(raw, "Reply-To: help@b.c\r\n") {
		t.Fatalf("missing Reply-To:\n%s", raw)
	}
}

func TestRenderMessage_NoReplyToByDefault(t *testing.T) {
	raw := string(renderMessage("o1 <a@b.c>", "", Message{To: "d@e.f", Subject: "s"}))
	if strings.Contains(raw, "Reply-To:") {
		t.Fatalf("unexpected Reply-To:\n%s", raw)
	}
}

func TestSMTPSender_ClassifiesPermanentAndTransient(t *testing.T) {
	// fake relay returns 550 on MAIL FROM
	s := newSMTPSenderForTest(t, "none", "", "", 550)
	err := s.Send(context.Background(), Message{To: "d@e.f", Subject: "s", Text: "t"})
	if !IsPermanent(err) {
		t.Fatalf("550 should be permanent, got %v", err)
	}
	if strings.Contains(err.Error(), "relay secret text") {
		t.Fatalf("relay text leaked: %v", err)
	}

	// fake relay returns 451
	s = newSMTPSenderForTest(t, "none", "", "", 451)
	err = s.Send(context.Background(), Message{To: "d@e.f", Subject: "s", Text: "t"})
	if err == nil || IsPermanent(err) {
		t.Fatalf("451 should be transient, got %v", err)
	}
}

func TestSMTPSender_STARTTLS(t *testing.T) {
	s := newSMTPSenderForTest(t, "starttls", "user", "pass", 0)
	if err := s.Send(context.Background(), Message{To: "d@e.f", Subject: "s", Text: "t"}); err != nil {
		t.Fatal(err)
	}
}

func TestSMTPSender_ImplicitTLS(t *testing.T) {
	s := newSMTPSenderForTest(t, "implicit", "", "", 0)
	if err := s.Send(context.Background(), Message{To: "d@e.f", Subject: "s", Text: "t"}); err != nil {
		t.Fatal(err)
	}
}
```

`newSMTPSenderForTest` has the signature
`func newSMTPSenderForTest(t *testing.T, tlsMode, username, password string, rejectCode int) *SMTPSender`.
It starts a `net.Listener` on `127.0.0.1:0`, generates a self-signed certificate with `IPAddresses: [127.0.0.1]`, writes `220`, answers `EHLO` with STARTTLS/AUTH capability when configured, wraps with `tls.Server` for `STARTTLS` or immediately for `implicit`, answers `AUTH PLAIN` with `235`, and answers `MAIL FROM` with the configured rejection code (including the text `relay secret text`) or `250`; `RCPT`/`DATA` answer `250`, `DATA` terminates on `\r\n.\r\n`, `QUIT` answers `221`. It returns an `*SMTPSender` built through the unexported test seam so the TLS config trusts the generated certificate.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go -C backend test ./internal/email -run 'SMTP|RenderMessage' -v`
Expected: FAIL — `renderMessage` and `SMTPSender` are undefined.

- [ ] **Step 3: Extract the shared message code**

Create `backend/internal/email/message.go` by moving `validateMessage`, `render`, `sanitizeHeader`, and `newBoundary` out of `mailpit.go`. Rename `render` to `renderMessage(from, replyTo string, msg Message) []byte` and emit `Reply-To` only when `replyTo != ""`. `MailpitSender.Send` and `MailpitSender.render` become calls to `renderMessage(s.from, "", msg)`. Remove the moved functions from `mailpit.go` and its `render` method. Run `go -C backend test ./internal/email -run Mailpit` and expect the existing Mailpit tests to pass unchanged.

- [ ] **Step 4: Implement `SMTPSender`**

Create `backend/internal/email/smtp.go`:

- Struct fields: `addr`, `from`, `replyTo`, `username`, `password`, `tlsMode string`, `tlsConfig *tls.Config`.
- `NewSMTPSender(...)` validates the TLS mode and builds `tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}`; return an error for an unknown mode.
- Unexported `newSMTPSenderWithTLS(..., tlsConfig *tls.Config)` is the test seam; it stores the supplied config.
- `Send(ctx, msg)` mirrors `MailpitSender.Send`: validate the message, compute the deadline (30s default, earlier context deadline wins), dial with `net.Dialer{Timeout: 10s}` and `DialContext`, install the cancellation goroutine, then:
  - `implicit`: wrap the connection with `tls.Client(conn, cfg)` and handshake before `smtp.NewClient`;
  - `starttls`: `smtp.NewClient` on the plain connection, then `client.StartTLS(cfg)`;
  - `none`: no TLS;
  - when both credentials are set, call `client.Auth(smtp.PlainAuth("", user, pass, host))` after TLS is established;
  - `Mail`, `Rcpt`, `Data` write `renderMessage(s.from, s.replyTo, msg)`, `Quit`.
- Error mapping helper: `errors.As(err, &textproto.Error)` with `Code >= 500` returns `&DeliveryError{Message: fmt.Sprintf("smtp: relay rejected the message (%d)", code), Permanent: true}`. All other errors return unchanged.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go -C backend test ./internal/email -v`
Expected: PASS, including the existing Mailpit and Resend suites.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/email
git commit -m "feat: send email through authenticated SMTP relays"
```

---

### Task 3: Factory, Compose, and the Compose test

**Files:**
- Modify: `backend/internal/email/factory.go`
- Modify: `backend/internal/email/factory_test.go`
- Modify: `deploy/production/compose.yaml`
- Modify: `deploy/production/env.example`
- Modify: `deploy/scripts/test-compose-config.sh`

**Interfaces:**
- Consumes: `config.EmailProviderSMTP` and the SMTP config fields from Task 1; `email.NewSMTPSender` from Task 2.
- Produces: Compose environment contract documented for Task 4.

- [ ] **Step 1: Write the failing factory and Compose assertions**

In `backend/internal/email/factory_test.go`, add a test that a `config.Config` with `EmailProvider: config.EmailProviderSMTP`, `SMTPAddr`, `SMTPTLS: "starttls"`, and a valid `EmailFrom` returns a sender without error.

In `deploy/scripts/test-compose-config.sh`, after the DSN checks, add:

```bash
# SMTP deployments do not need a Resend key.
tmp_env=$(mktemp)
trap 'rm -f "$tmp_env"' EXIT
grep -v '^O1_RESEND_API_KEY=' "$env_file" > "$tmp_env"
env -u O1_COMPOSE_PROJECT -u O1_BACKEND_IMAGE -u O1_WEB_IMAGE \
  O1_EMAIL_PROVIDER=smtp O1_SMTP_ADDR=smtp.example.net:587 \
  O1_SMTP_USERNAME=user O1_SMTP_PASSWORD=pass O1_SMTP_TLS=starttls \
  docker compose --env-file "$tmp_env" --env-file "$images_file" \
  -f "$compose_file" config --format json \
  | python3 -c '
import json, sys
env = json.load(sys.stdin)["services"]["api"]["environment"]
assert env["O1_EMAIL_PROVIDER"] == "smtp", env["O1_EMAIL_PROVIDER"]
assert env["O1_SMTP_ADDR"] == "smtp.example.net:587", env["O1_SMTP_ADDR"]
assert env["O1_SMTP_TLS"] == "starttls", env["O1_SMTP_TLS"]
assert env["O1_RESEND_API_KEY"] == "", env["O1_RESEND_API_KEY"]
'
check "smtp email settings render without a Resend key"
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
go -C backend test ./internal/email -run Factory -v
bash deploy/scripts/test-compose-config.sh
```
Expected: the factory test fails (unsupported provider) and the Compose check fails (Resend key required / `O1_EMAIL_PROVIDER` hardcoded).

- [ ] **Step 3: Implement the factory and Compose changes**

`factory.go`: add `case config.EmailProviderSMTP:` constructing `NewSMTPSender(cfg.SMTPAddr, cfg.EmailFrom, cfg.EmailReplyTo, cfg.SMTPUsername, cfg.SMTPPassword, cfg.SMTPTLS)` and return its error. Update the closing comment to list three providers.

`deploy/production/compose.yaml`, in the `&backend-env` anchor:
```yaml
      O1_EMAIL_PROVIDER: ${O1_EMAIL_PROVIDER:-resend}
      O1_RESEND_API_KEY: ${O1_RESEND_API_KEY:-}
      O1_SMTP_ADDR: ${O1_SMTP_ADDR:-}
      O1_SMTP_USERNAME: ${O1_SMTP_USERNAME:-}
      O1_SMTP_PASSWORD: ${O1_SMTP_PASSWORD:-}
      O1_SMTP_TLS: ${O1_SMTP_TLS:-starttls}
```
Keep `O1_EMAIL_FROM` required and `O1_EMAIL_REPLY_TO` optional.

`deploy/production/env.example`: document that exactly one provider is configured. Keep `O1_RESEND_API_KEY` but mark it required only for `resend`; add `O1_SMTP_ADDR`, `O1_SMTP_USERNAME`, `O1_SMTP_PASSWORD`, `O1_SMTP_TLS` with comments (587/STARTTLS, 465/implicit, PLAIN auth).

- [ ] **Step 4: Run the tests to verify they pass**

Run:
```bash
go -C backend test ./internal/email -v
bash deploy/scripts/test-compose-config.sh
```
Expected: both PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/email/factory.go backend/internal/email/factory_test.go \
  deploy/production/compose.yaml deploy/production/env.example \
  deploy/scripts/test-compose-config.sh
git commit -m "feat: wire the SMTP provider into the production stack"
```

---

### Task 4: Documentation

**Files:**
- Modify: `deploy/production/README.md`
- Modify: `README.md`
- Modify: `deploy/CICD.md`

**Interfaces:**
- Consumes: the environment contract from Task 3.

- [ ] **Step 1: Update the production runbook**

In `deploy/production/README.md` §2, rename the section to "Email provider" and add an SMTP subsection after the Resend steps:

- Provider choice: `O1_EMAIL_PROVIDER=resend` (default in production) or `smtp`.
- SMTP settings table: `O1_SMTP_ADDR` (host:port), `O1_SMTP_USERNAME`/`O1_SMTP_PASSWORD` (optional pair), `O1_SMTP_TLS` (`starttls` for 587, `implicit` for 465, never `none` in production).
- Provider examples: SES SMTP endpoint, Mailgun, Postmark, Gmail app password; note that the relay still requires its own verified domain or sender identity.
- Warning that a half-configured credential pair or `none` in production fails startup.
- Update the prerequisites bullet, §4 required-variable list, and the "Email not delivered" troubleshooting row to mention both providers.

- [ ] **Step 2: Update the top-level README and CI/CD guide**

- `README.md`: replace "Production email is Resend only" with the provider choice and note that `mailpit` remains development-only.
- `deploy/CICD.md` §5 step 5: `O1_RESEND_API_KEY` is required only for the Resend provider; list `O1_SMTP_ADDR`, `O1_SMTP_USERNAME`, `O1_SMTP_PASSWORD`, `O1_SMTP_TLS` as the SMTP alternative. Keep `O1_EMAIL_FROM` required.

- [ ] **Step 3: Verify the documents**

Run:
```bash
grep -n "Resend only\|resend only" README.md deploy/production/README.md deploy/CICD.md
grep -n "O1_SMTP_TLS\|O1_SMTP_ADDR\|O1_EMAIL_PROVIDER" README.md deploy/production/README.md deploy/CICD.md
```
Expected: the first command finds no "Resend only"; the second finds the SMTP variables in all three files.

- [ ] **Step 4: Commit**

```bash
git add README.md deploy/production/README.md deploy/CICD.md
git commit -m "docs: document the SMTP email provider"
```

---

### Task 5: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Run the gates**

```bash
bash -n deploy/scripts/*.sh
bash deploy/scripts/test-compose-config.sh
bash deploy/scripts/test-bootstrap.sh
bash deploy/scripts/test-workflows.sh
cd backend && go vet ./... && golangci-lint run && go test -short ./...
```

Expected: all pass.

- [ ] **Step 2: Confirm the two supported configurations render**

```bash
# Resend (default)
docker compose --env-file deploy/production/env.example \
  -f deploy/production/compose.yaml config --quiet && echo RESEND_OK

# SMTP without a Resend key
tmp=$(mktemp); grep -v '^O1_RESEND_API_KEY=' deploy/production/env.example > "$tmp"
O1_EMAIL_PROVIDER=smtp O1_SMTP_ADDR=smtp.example.net:587 \
O1_SMTP_USERNAME=user O1_SMTP_PASSWORD=pass O1_SMTP_TLS=starttls \
docker compose --env-file "$tmp" -f deploy/production/compose.yaml config --quiet && echo SMTP_OK
rm -f "$tmp"
```

- [ ] **Step 3: Commit any verification fixups**

Only if Steps 1–2 required changes; otherwise stop.

---

## Post-Implementation (operator)

Set the chosen provider in `/opt/o1/.env`, then recreate `api` and `worker`. Rollback is configuration-only: set `O1_EMAIL_PROVIDER=resend` (with a valid key) and recreate. The design doc's §8 has the rollout order.

# Plan 8 — Two-Factor Authentication (TOTP) Design

**Status:** Draft for approval
**Date:** 2026-11-09
**Depends on:** Plans 1–7, all merged into `main`.
**Out of scope:** workspace-level enforcement (owners/admins forced to enroll),
WebAuthn/passkeys, SMS or email OTP, trusted devices or “remember this browser”,
administrator reset of another user’s 2FA, 2FA for API keys, SSO, and recovery
code expiry/rotation policies beyond explicit regeneration.

## 1. Intent

Plan 7 added automation credentials and accountability. Plan 8 adds the other
half of account security: **per-user, opt-in TOTP two-factor authentication**
with one-time recovery codes and a two-step login challenge.

A user can enroll an authenticator app, verify a code, and receive ten
single-use recovery codes. After enrollment, password login creates a
short-lived MFA challenge instead of a session; only a valid TOTP code or
recovery code completes login. Disabling 2FA or regenerating recovery codes
requires both the password and a current second factor, so a stolen session
alone cannot turn protection off.

## 2. Current state

- `users` has `id`, `email`, `password_hash`, `email_verified_at`, timestamps,
  and `is_admin` (added with workspaces). There are no TOTP fields.
- `auth.Login` verifies the password, checks email verification, creates a
  session row, sets the session cookie in the handler, and audits
  `login_succeeded` with the IP.
- Sessions are opaque SHA-256-hashed tokens; `RequireSession` resolves the
  cookie and `RequireAuth` additionally accepts Bearer API keys.
- `secretbox` provides AES-256-GCM sealing using `O1_ENCRYPTION_KEY` (derived
  from the secret key in development). It is already used for monitor and
  alert-channel secrets.
- `argon2id` password hashing and SHA-256 token hashing already exist in
  `internal/auth`.
- The dashboard has auth screens and workspace settings, but no account-level
  settings page; `UserMenu` only shows the email, theme toggle, and logout.
- There is no TOTP, QR, or one-time-password dependency in Go or npm.

## 3. Goals, non-goals, anti-goals

**Goals**

1. A signed-in user can enable TOTP from an account Security page after
   re-entering their password, scan a QR code or enter the secret manually, and
   confirm enrollment with a code.
2. Enrollment produces ten one-time recovery codes, shown once and stored only
   as hashes.
3. After enrollment, password login returns an MFA challenge instead of a
   session. A valid TOTP or unused recovery code completes login and issues the
   session.
4. TOTP secrets are encrypted at rest; recovery codes and challenge tokens are
   stored only as hashes.
5. Challenges expire after five minutes, allow five attempts, are single-use,
   and are rate-limited.
6. TOTP codes use a 30-second step with ±1 step skew; a step cannot be replayed.
7. Disabling 2FA and regenerating recovery codes require the password plus a
   current TOTP or recovery code.
8. Login, enrollment, disable, and recovery regeneration are audited with
   redacted detail.
9. API keys remain a separate credential and do not require a TOTP step.
10. Existing password login, password reset, sessions, and API keys behave the
    same for users without 2FA.

**Non-goals**

- Workspace policy that forces owners/admins to enroll.
- WebAuthn/passkeys, SMS, email OTP, or push approval.
- “Remember this device” or trusted-device cookies.
- Admin/helpdesk reset of a user’s 2FA.
- Recovery-code expiry or automatic rotation.
- 2FA on API-key requests.
- Account recovery without a recovery code (support process is out of scope).

**Anti-goals**

- A TOTP secret stored in plaintext, returned after enrollment, or written to
  logs/audit.
- A recovery code stored in plaintext or reusable.
- A session cookie set before the second factor succeeds.
- A challenge that can be replayed, brute-forced past five attempts, or used
  after expiry.
- Disabling 2FA from a stolen session without the password and a second factor.
- Changing the behavior of users who never enabled 2FA.

## 4. Approaches

**A. Two-step login challenge (chosen).**
`POST /auth/login` verifies the password and returns either the usual session
response or `200 {mfa_required: true, challenge_token}` with no cookie.
`POST /auth/login/totp` exchanges the challenge token plus a TOTP or recovery
code for a session. The challenge has its own expiry, attempt count, and
single-use state.

*Why:* the password is not re-sent, failed second-factor attempts do not share
normal login semantics, and `RequireSession` keeps exactly one session state.
The extra challenge table is small and easy to reason about.

**B. One login endpoint with an optional code.**
The client retries `POST /auth/login` with email, password, and code. Fewer
endpoints, but the password is re-sent, failed TOTP attempts mix into password
rate limiting, and the client must retain the password between steps.

**C. Restricted pending-session cookie.**
Login sets an `mfa_pending` cookie and a verify endpoint upgrades it. Avoids a
challenge token but adds a second session state that every middleware and
session read must understand. Rejected as the riskiest option here.

**Decision:** A.

## 5. Constraints

- TOTP: RFC 6238, HMAC-SHA1, 6 digits, 30-second period, secret 20 bytes,
  issuer `o1 uptime`, account name = user email.
- Accept the current step and ±1 step (90 seconds total skew). Every successful
  TOTP verification, including enrollment/disable/regeneration, advances
  `totp_last_used_step` through an atomic conditional update
  (`WHERE last IS NULL OR last < step`). A zero-row update is a replay and is
  rejected. This means a code used to log in cannot be reused for another
  action in the same step.
- Enrolling (`setup` and `enable`) requires the account password in addition to
  the session. A stolen session alone cannot attach an attacker's authenticator.
- Enabling and disabling 2FA delete the user's other sessions, keeping the
  current session. Password reset deletes all outstanding MFA challenges and
  clears any pending TOTP setup for that user.
- Active and pending TOTP secrets are sealed with `secretbox` and never
  returned except the pending secret/otpauth URI during enrollment.
- Recovery codes: ten per generation, format `XXXXX-XXXXX` from Crockford
  base32 (no `0/O/1/I`), 50 bits of entropy. Store only a SHA-256 digest,
  compare in constant time, mark used on success, and never return them after
  the response that created them.
- MFA challenges: 32 random bytes, returned once; store a SHA-256 hash. TTL 5
  minutes, maximum 5 attempts, single-use (`consumed_at`). The fifth wrong
  second factor increments to 5, consumes the challenge, and returns
  `401 invalid_totp`/`invalid_recovery_code`; any later use returns
  `429 mfa_attempts_exhausted`. The failure increment is committed before the
  error is returned.
- Session cookies are set only after the second factor succeeds (or when the
  user has no 2FA).
- Endpoints: login, login/TOTP, setup, enable, disable, and recovery
  regeneration all use the existing shared per-IP auth rate-limit instance.
- Setup/enable/disable/regenerate require both the account password (except
  login/TOTP, which has the challenge) and, where applicable, exactly one of
  `code` or `recovery_code`. Supplying both or neither is a `400
  validation_error` on the `factor` field.
- Disable and regenerate require both the account password and a current TOTP
  or unused recovery code.
- Pending setup expires after 10 minutes; an expired setup is discarded.
- A user without `totp_enabled_at` follows the existing login path exactly.
- API keys bypass TOTP; they are not derived from a user session.
- Audit detail never contains TOTP secrets, recovery codes, challenge tokens,
  passwords, or email addresses. Login success records the method
  (`password`, `totp`, `recovery_code`); failures use fixed reasons.
- UI copy is English. Every task ends with formatting, lint, typecheck, tests,
  and a commit.

## 6. Data model

Migration `00008_totp.sql`:

```sql
-- +goose Up
ALTER TABLE users
  ADD COLUMN totp_secret_enc text,
  ADD COLUMN totp_pending_enc text,
  ADD COLUMN totp_pending_expires_at timestamptz,
  ADD COLUMN totp_enabled_at timestamptz,
  ADD COLUMN totp_last_used_step bigint;

CREATE TABLE user_recovery_codes (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash bytea NOT NULL,
  used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_recovery_codes_user_idx ON user_recovery_codes(user_id);

CREATE TABLE mfa_challenges (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE,
  attempts smallint NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  ip text,
  user_agent text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mfa_challenges_user_idx ON mfa_challenges(user_id);
CREATE INDEX mfa_challenges_expires_idx ON mfa_challenges(expires_at);

-- +goose Down
DROP TABLE mfa_challenges;
DROP TABLE user_recovery_codes;
ALTER TABLE users
  DROP COLUMN totp_last_used_step,
  DROP COLUMN totp_enabled_at,
  DROP COLUMN totp_pending_expires_at,
  DROP COLUMN totp_pending_enc,
  DROP COLUMN totp_secret_enc;
```

SQLC queries in `backend/internal/db/queries/totp.sql`:

- `SetTOTPPending(user_id, pending_enc, expires_at)`.
- `GetTOTPState(user_id)` returning enabled_at, last step, and whether active
  and pending secrets exist.
- `EnableTOTP(user_id, secret_enc, last_used_step)` setting active secret and
  enabled_at, clearing pending.
- `DisableTOTP(user_id)` clearing active/pending/last step.
- `ClearTOTPPending(user_id)` for expiry/cleanup.
- `UpdateTOTPLastUsedStepIfGreater(user_id, step)` with `:execrows`, updating
  only when the stored step is null or smaller; zero rows means replay.
- `DeleteMFAChallengesForUser(user_id)` for reset/disable/enable cleanup.
- `DeleteOtherUserSessions(user_id, keep_session_id)` for enable/disable.
- `CreateRecoveryCode(id, user_id, code_hash)`.
- `ListRecoveryCodes(user_id)` returning id/hash/used.
- `MarkRecoveryCodeUsed(id, used_at)`.
- `DeleteRecoveryCodes(user_id)`.
- `CountUnusedRecoveryCodes(user_id)`.
- `CreateMFAChallenge(id, user_id, token_hash, expires_at, ip, user_agent)`.
- `GetMFAChallengeForUpdate(token_hash)` with `FOR UPDATE`.
- `IncrementMFAChallengeAttempts(id)`.
- `ConsumeMFAChallenge(id)`.
- `DeleteExpiredMFAChallenges(user_id)` best-effort cleanup on new challenge.

`users` model gains the TOTP fields. Existing `GetUserByEmail`/`GetUserByID`
`SELECT *` return them automatically.

## 7. Backend design

### 7.1 Package layout

Extend `internal/auth` rather than create a new package, because login,
sessions, password hashing, token hashing, and the auth middleware already live
there. New files:

- `totp.go` — enrollment, verification, recovery codes, challenge lifecycle.
- `totp_test.go`.
- `handlers_totp.go` — activation/disable/regenerate/login-TOTP handlers.
- `handlers_totp_test.go`.
- Modify `session.go` for the two-step login path.
- Modify `handlers.go` for the MFA-required login response.

`auth.Service` gains a `box *secretbox.Box` built from
`cfg.EncryptionKey` in `NewService`. If the key is not 32 bytes, the box is
nil and TOTP endpoints return an internal error; login for users without 2FA
is unaffected.

Dependencies: add `github.com/pquerna/otp` for generation and validation.

### 7.2 TOTP state

```go
type TOTPState struct {
    Enabled              bool       `json:"enabled"`
    RecoveryCodesRemaining int      `json:"recovery_codes_remaining"`
}

type TOTPSetup struct {
    Secret     string `json:"secret"`
    OtpauthURI string `json:"otpauth_uri"`
}
```

- `GetTOTPState(ctx, userID) (TOTPState, error)`.
- `SetupTOTP(ctx, userID, password string) (TOTPSetup, error)`:
  - Verify the password first. Reject if already enabled
    (`ErrTOTPAlreadyEnabled`, 409).
  - Generate a secret, build `otpauth://totp/o1%20uptime:<email>?secret=...&issuer=o1%20uptime&algorithm=SHA1&digits=6&period=30`.
  - Seal the secret into `totp_pending_enc`, set expiry now+10m.
  - Return the raw secret and URI; never store them in plaintext.
- `EnableTOTP(ctx, userID, password, code string, currentSessionID uuid.UUID) ([]string, error)`:
  - Verify the password, then load state/pending; reject expired or merely
    pending without a secret.
  - Verify the code against the pending secret with ±1 skew.
  - Seal and store the active secret, set `totp_enabled_at`, atomically set
    `totp_last_used_step` to the matched step, clear pending, replace recovery
    codes with ten fresh hashes, delete outstanding challenges and other
    sessions, and return the plaintext codes once.
  - Audit `totp_enabled`.
- `DisableTOTP(ctx, userID, password, code, recoveryCode string, currentSessionID uuid.UUID)`:
  - Verify the password and exactly one second factor.
  - Clear active/pending state, delete recovery codes and challenges, delete
    other sessions, audit `totp_disabled`.
- `RegenerateRecoveryCodes(ctx, userID, password, code, recoveryCode string) ([]string, error)`:
  - Verify password + exactly one second factor; delete old codes, create ten
    new ones, return plaintext once, audit
    `totp_recovery_codes_regenerated`.
- `VerifyRecoveryCode(ctx, userID, raw string) (bool, error)`:
  - Normalize, hash, compare against unused rows in constant time, mark the
    matched row used atomically. A used or unknown code is rejected.

### 7.3 Two-step login

```go
type LoginStart struct {
    Token       string      // session token, empty when MFA is required
    User        sqlc.User
    MFARequired bool
    Challenge   string      // raw challenge token when MFA is required
}

func (s *Service) BeginLogin(ctx, email, password, ip, userAgent string) (LoginStart, error)
func (s *Service) CompleteTOTPLogin(ctx, challengeToken, code, recoveryCode, ip, userAgent string) (string, sqlc.User, error)
```

- `BeginLogin` keeps the existing credential checks and audits. After a
  verified password and verified email:
  - If TOTP is not enabled: create the session exactly as today, audit
    `login_succeeded` with `method: "password"`, return the token/user.
  - If TOTP is enabled: delete expired challenges for the user, create a
    challenge row with a hashed token, expiry now+5m, IP, and user agent, and
    return `MFARequired` with the raw challenge. Do **not** create a session or
    set a cookie, and do not record `login_succeeded` yet.
- `CompleteTOTPLogin`:
  - Require exactly one of `code` or `recovery_code`; otherwise a validation
    error.
  - Begin a transaction, look up the challenge by hash with `FOR UPDATE`.
  - Check `attempts >= 5` first and return `ErrMFAAttemptsExhausted` (429).
  - Reject missing/expired/consumed challenges.
  - If `recovery_code` is non-empty, verify and consume it; otherwise verify
    the TOTP code with the atomic replay-step update. Exactly one factor is
    required.
  - On failure, increment attempts inside the transaction, consume the
    challenge when the new count reaches 5, **commit the increment**, audit
    `login_failed` with `invalid_totp` or `invalid_recovery_code` and the known
    challenge user as actor, then return a typed error.
  - On success, mark the challenge consumed, create the session, commit, audit
    `login_succeeded` with `method: "totp"` or `"recovery_code"`, and return the
    session token/user.
- Password reset deletes all outstanding MFA challenges and clears any pending
  TOTP setup before committing the new password.
- `Login` remains as a compatibility wrapper used by tests/helpers: it calls
  `BeginLogin`; if MFA is required it returns `ErrMFARequired` instead of a
  token. Production login uses `BeginLogin`.
- Challenge token format: 32 random bytes base64url with an `o1mfa_` prefix;
  store only the SHA-256 hash.

### 7.4 API

New session-only routes (all under `requireSession`):

- `GET /api/v1/auth/2fa` → 200 `TOTPState`.
- `POST /api/v1/auth/2fa/setup` body `{password}` → 200 `TOTPSetup`;
  401 `invalid_credentials`; 409 `totp_already_enabled`.
- `POST /api/v1/auth/2fa/enable` body `{password, code}` →
  200 `{recovery_codes: [...]}`; 401 `invalid_credentials`; 400 `invalid_totp`;
  409 already enabled; 410 `totp_setup_expired`.
- `POST /api/v1/auth/2fa/disable` body `{password, code?, recovery_code?}` →
  204; 401 `invalid_credentials`; 400 `invalid_totp`/`invalid_recovery_code`/
  `validation_error`; 409 `totp_not_enabled`.
- `POST /api/v1/auth/2fa/recovery-codes` body `{password, code?, recovery_code?}`
  → 200 `{recovery_codes: [...]}`; same errors as disable.

Login routes:

- `POST /api/v1/auth/login`:
  - No 2FA: sets the session cookie and returns `{user}` exactly as today.
  - 2FA: no cookie, returns 200 `{mfa_required: true, challenge_token}`.
- `POST /api/v1/auth/login/totp` body `{challenge_token, code?, recovery_code?}`:
  - Success sets the session cookie and returns `{user}`.
  - 401 `invalid_totp` / `invalid_recovery_code`; 400 `validation_error` when
    both/neither factor is supplied.
  - 410 `mfa_challenge_expired` for expired/consumed; 429
    `mfa_attempts_exhausted` at five attempts.
  - Both login routes use the existing shared auth rate limiter.

The setup/enable/disable/regenerate handlers apply the same shared auth rate
limiter and `DisallowUnknownFields`. Problem codes are added as constants in
`httpx`.

### 7.5 Audit

Add kinds:

- `totp_enabled` — actor user, detail `{recovery_codes: 10}`.
- `totp_disabled` — actor user, no secret/code detail.
- `totp_recovery_codes_regenerated` — actor user, detail
  `{recovery_codes: 10}`.

`login_succeeded` gains `detail["method"]` = `password`, `totp`, or
`recovery_code`. `login_failed` reasons add `invalid_totp` and
`invalid_recovery_code`; failures on `/login/totp` set the challenged user as
actor because a valid challenge proves the password step. Password failures
remain actor-less. Never record the code, secret, challenge token, or email.
Add the three kinds to the admin Security events filter.

### 7.6 Password reset and account recovery

- Password reset does not disable TOTP or clear recovery codes. After a reset,
  login still requires the second factor.
- Recovery codes are the only self-service fallback. There is no admin reset in
  this plan; a locked-out user with no recovery code needs an operator process
  outside the product.
- API keys continue to work without TOTP; they are scoped workspace
  credentials, not user sessions.

## 8. Dashboard design

### 8.1 Types, API, hooks, routes

Add to `lib/api/types.ts`:

```ts
export interface TOTPState {
  enabled: boolean;
  recovery_codes_remaining: number;
}

export interface TOTPSetup {
  secret: string;
  otpauth_uri: string;
}

export interface LoginResponse {
  user?: AuthUser;
  mfa_required?: boolean;
  challenge_token?: string;
}
```

`features/auth/api.ts` gains `getTOTPState`, `setupTOTP`, `enableTOTP`,
`disableTOTP`, `regenerateRecoveryCodes`, and `completeTOTPLogin`.

Query key `totpState: ['totp-state']`. Hooks invalidate it after enable/disable/
regenerate.

Route `/account/security` under `appLayoutRoute` (authenticated, not
workspace-scoped). `UserMenu` gains a "Security" link above Log out.

### 8.2 Login screen

- The existing password step calls `login`; if the response has
  `mfa_required`, store the challenge token in component state and render the
  second step instead of navigating.
- Second step: label "Authentication code", input with
  `inputMode="numeric"`, `autoComplete="one-time-code"`, `maxLength={6}`.
- A "Use a recovery code" link toggles a recovery-code input; "Use an
  authenticator code" toggles back.
- Submit calls `completeTOTPLogin`; success runs the existing query-cache
  cleanup/navigation path. Errors render `ProblemAlert` and keep the step.
- "Back to sign in" clears the challenge and returns to the password step.
- The challenge token is never put in the URL or local storage.

### 8.3 Account Security page

- Heading "Security" and "Two-factor authentication".
- Disabled state: explanation, a Password field, and "Enable two-factor
  authentication". Setup calls the API with the password; a wrong password
  shows a problem and no QR.
- Setup state: QR code rendered from `otpauth_uri` (`qrcode.react`), the manual
  secret in monospace, a Password field, a code input labelled "Verification
  code", "Verify and enable", and "Cancel".
- After enable: show the ten recovery codes once in a monospace block with
  "Copy codes", a warning that each code works once and they will not be shown
  again, and "Done". Do not store them.
- Enabled state: status "Two-factor authentication is on", remaining recovery
  code count, "Regenerate recovery codes", and "Disable two-factor
  authentication".
- Disable and regenerate use a dialog requiring Password and either an
  authenticator code or a recovery code. On success, show new codes once for
  regeneration, then close.
- No TOTP secret or recovery code is rendered outside the setup/one-time panel.
- The page is account-level; it does not require a workspace.

## 9. Testing

**Backend**

- Schema: TOTP columns, recovery-code cascade, challenge cascade, attempt
  check, unique token hash.
- TOTP service: setup requires the password and returns a valid secret/URI,
  storing only ciphertext; enable requires the password + code, activates, and
  returns 10 codes; wrong/expired setup fails; disable requires password +
  factor; recovery regeneration replaces codes; state reports remaining count.
- TOTP verification: current/±1 step accepted, -2/+2 rejected, replayed step
  rejected across login/disable/regenerate via the conditional update, secret
  decrypts correctly, ciphertext differs from plaintext.
- Recovery codes: format, hash-only storage, single use, constant-time path,
  all invalid after regeneration.
- Login: non-2FA login unchanged; 2FA login returns a challenge and no cookie;
  correct TOTP completes; correct recovery code completes and is consumed;
  wrong code increments attempts; the fifth wrong code returns 401 and consumes
  the challenge; later use returns 429; the increment is committed; expired/
  consumed/unknown challenge; both/neither factor is a validation error; session
  created only on success; audit methods/reasons and TOTP-failure actor.
- Enable/disable delete other sessions and keep the current one; password reset
  clears challenges and pending setup.
- API keys still authenticate without TOTP; 2FA does not affect Bearer auth.
- Handlers: login response/cookie matrix; TOTP endpoints session-only, role-free
  (any signed-in user), rate-limited by the shared auth limiter; error codes;
  `DisallowUnknownFields`.
- Audit: enable/disable/regenerate kinds, no secrets; login methods; failed
  TOTP actor.
- Existing auth/session tests stay green.

**Web**

- Login tests: password-only unchanged; MFA step renders; numeric/one-time-code
  input; success navigates; wrong code stays; recovery-code toggle; back to
  sign-in.
- Security page tests: disabled state, setup QR/secret, enable flow, one-time
  recovery codes, copy, enabled state/count, regenerate, disable dialog
  validation, errors, secret absent after close.
- UserMenu/route tests: Security link, authenticated route, no workspace needed.
- Existing suites stay green.

**E2E**

- Register and verify a fresh user, open `/account/security`, enter the
  password, enable 2FA using a TOTP code computed from the displayed secret,
  and capture two recovery codes.
- Log out, log in with password, assert the MFA step, enter a TOTP code, and
  reach the dashboard.
- Log out, log in again, choose recovery code, use the first captured code, and
  reach the dashboard; assert it cannot be reused.
- Disable 2FA with the password + the second recovery code (the login TOTP step
  is replayed, so a fresh recovery code is used), then log in with only the
  password.
- Run the existing axe checks on the login MFA step and Security page.
- Keep API-key, status-page, incident, alert, theme, and a11y checks green.

## 10. Acceptance

1. A user can enable TOTP, see the secret/QR once, confirm with a code, and
   receive ten one-time recovery codes.
2. Password login for that user returns an MFA challenge and no session cookie.
3. A current TOTP code or an unused recovery code completes login; the session
   then behaves exactly like a normal session.
4. Wrong, expired, replayed, or over-attempted codes fail without creating a
   session.
5. Recovery codes are single-use; regeneration invalidates all old codes.
6. Disabling 2FA or regenerating codes requires password + second factor.
7. TOTP secrets are encrypted at rest; recovery codes and challenge tokens are
   hashed; no secret appears in logs, audit, API responses after enrollment, or
   the URL.
8. Users without 2FA, API keys, password reset, and existing sessions are
   unaffected.
9. Audit shows enable/disable/regenerate and login methods without secrets.

## 11. Calls made by the builder

- Per-user opt-in; no workspace enforcement in this plan.
- Two-step challenge (not password retry or pending-session cookie).
- TOTP secrets sealed with `secretbox`; recovery codes and challenge tokens
  SHA-256 hashed.
- Ten Crockford-base32 recovery codes per generation, 50 bits each, single-use.
- 30-second period, 6 digits, ±1 step, atomic replay-step guard shared by login,
  enrollment, disable, and regeneration.
- Challenge TTL 5 minutes, 5 attempts, single-use. The fifth wrong code returns
  401 and consumes the challenge; later use returns 429. Failure increments are
  committed before returning.
- Setup/enable require the password; disable/regenerate require password + one
  second factor. Enabling/disabling revoke other sessions; password reset clears
  challenges and pending setup.
- Account Security lives at `/account/security` and is linked from UserMenu.
- QR rendering uses `qrcode.react`; E2E TOTP generation uses a test-only JS
  helper/library.
- API keys bypass TOTP.
- No admin reset, trusted devices, or recovery-code expiry in V1.

## 12. Left to the builder

- Exact helper names beyond the public functions and queries above.
- QR component choice and markup, provided labels, states, and tests match.
- Whether challenge cleanup runs inline on login, on a timer, or both, as long
  as expired rows cannot be used and the table does not grow unbounded.
- Test helper reuse (`testutil`, MSW, Playwright request/page fixtures).
- The concrete constant-time recovery-code scan and TOTP step matching helper.

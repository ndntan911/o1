# o1 Threat Model

**Reviewed:** 2026-11-18 · **Owner:** project maintainer · **Next review:** 2027-02-18

This model covers the self-hosted o1 deployment: Caddy on one host, the `api`,
`scheduler`, and `worker` binaries, a managed PostgreSQL, the React dashboard,
outbound probes and alert deliveries, and the CI/release path. It is written
for the engineer who will change the system next, not for an auditor.

## 1. Assets

| Asset | Where it lives | Impact if lost or exposed |
|---|---|---|
| Session tokens | `sessions.token_hash`, cookie `o1_session` | Account takeover; admin actions attributed to the victim |
| Passwords | `users.password_hash` (argon2id) | Offline cracking, credential reuse on other sites |
| TOTP secrets and recovery codes | `users` TOTP columns, hashes only for recovery codes | Second factor bypass |
| API keys | `api_keys.prefix` and `api_keys.token_hash` | Scripted reads/writes inside one workspace |
| Monitor request config | `monitors.headers_enc`, `body_enc` (AES-256-GCM) | Leaks credentials the monitor sends to a target |
| Probe results | `probe_results`, `monitor_hourly_stats` | Reveals internal endpoints, error text, latency |
| Alert channel secrets | webhook URLs, Slack URLs, bot tokens (encrypted at rest) | Spoofed alerts, message exfiltration |
| Audit trail | `security_events` | Loss of attribution; no evidence after an incident |
| Public status page | `status_pages`, `status_page_monitors` | Reputational; deliberately public but must not leak monitor internals |
| Host and release credentials | `/opt/o1`, GitHub Environments, GHCR tokens | Supply-chain compromise |

## 2. Actors

- **Anonymous visitor**: reads public status pages, registers, logs in.
- **Viewer**: reads workspace resources.
- **Member**: mutates monitors, acknowledges incidents, toggles per-monitor alerts.
- **Workspace admin/owner**: manages members, alert channels, status page, API keys.
- **Global administrator**: lists/suspends workspaces, reads the audit trail.
- **API-key automation**: a viewer or member key scoped to one workspace.
- **Malicious tenant**: a legitimate account trying to read or affect another tenant.
- **External target**: a probed host that may be hostile, redirect, or be slow.
- **Alert provider**: Resend, an SMTP relay, a webhook receiver, Slack, Telegram.
- **Host operator**: root on the VPS, can read volumes and secrets.
- **CI/CD**: GitHub Actions, GHCR, and the SSH deploy user.

## 3. Trust boundaries

1. **Browser ↔ Caddy** — TLS, the SPA, and the CSP.
2. **Caddy ↔ API** — same-origin proxy; Caddy rewrites `X-Forwarded-For`.
3. **API ↔ PostgreSQL** — one credential, no row-level security.
4. **Worker ↔ probe target** — the only place the server makes attacker-chosen requests.
5. **Worker ↔ alert provider** — attacker-influenced URLs and payloads.
6. **Operator ↔ host** — volumes, env files, and backups.
7. **CI ↔ GHCR ↔ host** — tags, digests, and the deploy SSH key.

## 4. STRIDE by boundary

| Boundary | Threat | Control | Residual risk |
|---|---|---|---|
| Browser ↔ Caddy | Spoofing | TLS via ACME, HSTS one year | First visit before HSTS is trust-on-first-use |
| Browser ↔ Caddy | Tampering | CSP with hashed inline script, `frame-ancestors 'none'`, nosniff | `style-src` allows inline styles for Radix/charts |
| Browser ↔ Caddy | Information disclosure | No monitor URLs or errors on the public page; `Referrer-Policy: no-referrer` | Screenshots of a private dashboard are out of scope |
| Browser ↔ Caddy | Denial of service | Public page limited per IP (60/min, burst 20) | No WAF; a distributed flood is not mitigated |
| Caddy ↔ API | Spoofing | `X-Forwarded-For` overwritten with the peer address; API trusts forwarded headers only from private peers | A compromised Caddy container controls the source IP |
| Caddy ↔ API | Elevation | Caddy adds no auth; every route is authenticated in the API | A Caddyfile route mistake could bypass nothing, but could expose static paths |
| API ↔ PostgreSQL | Information disclosure | Per-request workspace scoping; cross-tenant reads return 404; parameterized queries via pgx | A compromised API process can read every tenant |
| API ↔ PostgreSQL | Tampering | Authorization in the service layer (`workspaces.Authorize`) before every mutation | A new handler that forgets the call would not be caught by the type system |
| API ↔ PostgreSQL | Repudiation | Audit events carry workspace, actor user, actor API key | Audit writes are best-effort and can be dropped |
| Worker ↔ target | SSRF | Scheme/port/userinfo validation, resolved-IP checks, dial the validated IP, hop limit, cross-host header stripping | DNS rebinding inside the validated IP is not possible; a target may still fingerprint the prober |
| Worker ↔ target | DoS of the prober | Timeouts, global probe rate limit, bounded response reads | A slow target can occupy a worker slot for the timeout |
| Worker ↔ provider | Spoofing | Webhook HMAC-SHA256 over timestamp and body; Slack host allowlist | Receivers must verify signatures themselves |
| Worker ↔ provider | Information disclosure | Secrets are write-only, encrypted, excluded from delivery rows and logs | Provider-side retention is outside our control |
| Operator ↔ host | Elevation | Caddy runs as uid 1000 with only `NET_BIND_SERVICE`; other containers are distroless non-root | Root on the host defeats everything |
| Operator ↔ host | Information disclosure | Secrets in `/opt/o1/.env` and managed-DB credentials; backups are the provider's responsibility | Losing the host loses the only raw probe copy if retention is enabled |
| CI ↔ GHCR ↔ host | Tampering | Digest-pinned images, Trivy and govulncheck gates, reviewed production Environment, no `--build` in deploy | A compromised GitHub account can publish a malicious digest |
| CI ↔ GHCR ↔ host | Spoofing | `StrictHostKeyChecking=yes` with pinned known hosts; dedicated SSH key | Key theft is not detectable by the deploy script |

## 5. Incident copilot and MCP

The copilot and the MCP endpoint reuse the same read-only tool layer and add
two boundaries: **API ↔ model provider** and **MCP client ↔ API**.

| Boundary | Threat | Control | Residual risk |
|---|---|---|---|
| API ↔ model provider | Information disclosure | The model sees an incident seed and bounded tool results only: no monitor URL, request headers or body, recipient, secret, or raw probe error. Tool results are truncated and redacted before serialization. | The provider's own retention and logging are outside our control |
| Model output ↔ dashboard | Tampering / elevation | Every hypothesis claim must resolve to a recorded tool result; unsupported causes are dropped. The agent has no write tool and no outbound HTTP except the provider call. | A verified-looking claim can still be wrong, so the UI presents it as a starting point |
| MCP client ↔ API | Spoofing | Static bearer API key only, with no cookie fallback; `Origin` is checked when present. | A leaked key works until revoked; there is no OAuth, consent, or per-client binding |
| MCP client ↔ API | Elevation / tenant escape | The workspace comes from the key, never from tool arguments. Cross-tenant ids return `not_found`, and a viewer key cannot see the delivery tool. | A static key carries the key's whole role for its workspace |

- The **model boundary is read-only and evidence-bounded**. Tools read
  incidents, monitors, checks, and delivery outcomes; the agent cannot probe,
  mutate, or fetch arbitrary URLs. The run trace and validated answer store
  redacted results, never key material, URLs, or raw errors.
- The **tool boundary is shared** by the in-app copilot and the MCP adapter, so
  both paths enforce the same role and tenant checks.
- **Tenant scope** always comes from the session or API key. No tool accepts a
  workspace id, and the tool list is rebuilt per request from the key's role.
- **Static bearer keys are a deliberate tradeoff.** They are simple for CLI and
  agent clients but have no OAuth discovery, consent, expiry, or rotation beyond
  revoke and recreate. Treat a key like a password and give it the smallest
  role that works.

## 6. Access-control matrix

Enforcement is in the service layer (`workspaces.Authorize`) unless noted.
`/healthz` and `/readyz` are unauthenticated and not tenant-scoped; every other
route below is.
"Key" means a bearer API key; keys are capped at viewer or member and never
carry admin or global rights.

| Route group | Anonymous | Viewer | Member | Admin | Key scope | Coverage |
|---|---|---|---|---|---|---|
| `POST /auth/register`, `/auth/login`, `/auth/login/totp`, `/auth/verify-email*`, `/auth/password/*` | yes (rate limited) | — | — | — | no | `auth` handler + integration tests |
| `GET /auth/2fa`, `POST /auth/2fa/*`, `POST /auth/logout` | no | session owner | session owner | session owner | no | `auth` handler tests; 2FA e2e |
| `GET /me` | no | any session | any session | any session | no | `api` integration |
| `GET/POST /workspaces` | no | list own; create allowed | same | same | no | `api` integration |
| `GET /workspaces/{id}` | no | viewer | viewer | admin | viewer | `workspaces` handler tests |
| `GET /workspaces/{id}/members` | no | read | read | read | no | `workspaces` handler tests |
| `POST /workspaces/{id}/members` | no | denied | denied | admin | no | `workspaces` handler tests |
| `/workspaces/{id}/api-keys` | no | denied | denied | admin | no | `apikeys` handler tests |
| `GET/PUT /workspaces/{id}/status-page` | no | read denied | read denied | admin | no | `statuspage` handler tests |
| `GET /public/status/{slug}` | yes (rate limited) | — | — | — | no | `statuspage` handler tests |
| `POST/GET/PATCH/DELETE /workspaces/{id}/monitors*` | no | read via key/session | mutate | mutate | viewer read, member mutate | `monitors` service + handler tests |
| `.../monitors/{id}/results`, `/summary` | no | viewer | viewer | admin | viewer | `monitors` tests |
| `GET .../incidents*`, `GET .../incidents/{id}` | no | read | read | read | viewer read | `incidents` handler tests |
| `POST .../incidents/{id}/acknowledge` | no | denied | acknowledge | acknowledge | member acknowledge | `incidents` handler tests |
| `GET .../incidents/{id}/deliveries` | no | denied | denied | admin | no | `incidents` handler tests |
| `.../alert-channels*`, `.../alert-channels/email`, `.../test` | no | denied | denied | admin | no | `alerts` handler tests |
| `.../monitors/{id}/alert-channels*` (per-monitor switches) | no | denied | read and toggle | read and toggle | member | `alerts` handler tests |
| `/admin/*` | no | denied | denied | **global admin only** (`requireAdmin`) | no | `backend/internal/api/admin_integration_test.go` |
| `GET /healthz`, `GET /readyz` | yes | — | — | — | no | `httpx` health tests |

## 7. Residual risks

Ranked by what a reasonable user would notice first.

1. **No defence against a distributed flood.** Rate limits are per client IP
   inside one API process; a botnet or a large L3/L4 flood is not mitigated.
   Accepted for a single-host product; a WAF or edge limiter is a follow-up.
2. **Single host, single database.** Host loss or database loss is downtime;
   recovery depends on the provider's backups and point-in-time recovery.
3. **Audit writes are best-effort.** A failed insert logs a warning rather than
   failing the action, so the trail can have gaps exactly when the database is
   unhealthy.
4. **Sessions are bearer cookies with no device binding.** A stolen cookie is
   valid until expiry or logout; 2FA protects the login, not the session.
5. **No administrator rescue for lost 2FA.** A user without the authenticator
   and recovery codes loses browser access permanently, by design.
6. **Alert receivers must verify webhook signatures.** The server signs, but a
   receiver that ignores `X-O1-Signature` accepts forged events.
7. **Inline styles are allowed by the CSP.** Removing `'unsafe-inline'` from
   `style-src` needs a Radix/chart audit; scripting is hash-locked, which is the
   higher-value half.
8. **The demo seeder moves the rollup watermark.** `backend/cmd/seed` is meant for a
   demo deployment; running it beside real tenants can skip one rollup pass.
9. **No external penetration test.** This model and the automated gates are the
   substitute; a paid test remains on the follow-up list.

## 8. Review cadence

- The maintainer re-reads this model and the review checklist quarterly and
  after any change to authentication, tenancy, probing, alert delivery, or the
  deploy path.
- Every Trivy exception is re-checked on its own date; the live one is
  `CVE-2026-78661`, next 2027-01-12 (see `docs/security/container-review.md`).
- The load baseline in `docs/security/load-test.md` is re-run when the reference
  workload, host shape, or a hot path changes.
- There is no second reviewer in this project; this is a self-review, stated
  plainly rather than implied otherwise.

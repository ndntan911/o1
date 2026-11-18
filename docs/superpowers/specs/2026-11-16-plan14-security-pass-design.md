# Plan 14 — Security Pass, Gates, and Load Baseline Design

**Status:** Draft for approval
**Date:** 2026-11-16
**Depends on:** Plans 1–13 (all shipped), the 2026-11-16 web hardening commit
**Scope owner:** o1 uptime monitor

## 1. Intent

Every earlier plan deferred the same thing: "load tests, penetration testing, and
a formal security review (Plan 14)". The repository also carries that deferral in
public: `govulncheck` runs in CI with `continue-on-error`, the deploy guide says
findings are expected until "the Plan 14 security pass", and two Trivy
exceptions carry review dates.

Plan 14 closes that loop. It produces a written threat model, turns the
dependency gate from advisory into blocking, re-verifies the container-scan
exceptions, adds the missing browser security policy, and records a load-test
baseline with thresholds. It is deliberately **not** a paid penetration test:
the value here is a repeatable gate plus a documented, prioritized model of what
could go wrong.

Success looks like: a reviewer can open one document, see the trust boundaries
and residual risks, and run locally the gates that guard a push (the dependency
scan and the test suites — image scans stay at release time, as they are today).

## 2. Current state (measured, 2026-11-16)

- `govulncheck ./...` reports **33 reachable vulnerabilities** across the Go
  standard library (toolchain go1.26.2) and dependencies pulled in by
  testcontainers (`docker/docker`, `moby/go-archive`, `moby/sys/user`,
  `klauspost/compress`, `x/crypto`).
- `govulncheck ./cmd/...`, which matches what actually ships, reports **29
  reachable vulnerabilities, all in the Go standard library**. The fixes land in
  Go 1.26.9–1.26.11.
- Re-running the same scan with `GOTOOLCHAIN=go1.26.11` and `./cmd/...` reports
  **0 reachable vulnerabilities** (1 module-level, not called). The fix is a
  toolchain bump, not code surgery.
- `npm audit --omit=dev` in `web/` reports **0 vulnerabilities**.
- A fresh pull of `caddy:2-alpine` is **v2.11.9** (Go 1.27.3) and still bundles
  **golang.org/x/net v0.60.0**. CVE-2026-78661 is fixed in x/net v0.62.0, so the
  exception stays until Caddy ships a newer dependency set. Its unreachability
  argument (no templates directive, no HTML request parsing) still holds.
- DS-0002 (Caddy runs as root to bind 80/443) has not been re-evaluated against
  a non-root deployment.
- The web container sends HSTS, `X-Content-Type-Options`, `X-Frame-Options`, and
  `Referrer-Policy` (commit `9715d4f`), but there is **no
  `Content-Security-Policy`**.
- Session cookies are `HttpOnly`, `Secure` in production, `SameSite=Lax`, and
  unsafe requests require an allowlisted `Origin`; both are covered by tests.
- Rate limits are compiled in, not configurable: the public status endpoint is
  **1 rps with a burst of 20 per client IP**, and API-key routes share **10 rps
  with a burst of 60 per key** (`internal/api/router.go`). Caddy overwrites
  `X-Forwarded-For` with the peer address, so a load generator on one host looks
  like one client and would mostly receive `429`s.
- Dependabot is enabled for GitHub Actions, Go modules, and npm.
- There is no load test, no threat model, and no security checklist in the repo.

## 3. Goals, non-goals, anti-goals

**Goals**

1. A threat model that names assets, actors, trust boundaries, existing controls,
   residual risks, and who reviews it when.
2. `govulncheck` blocking in CI on the shipping packages, with a documented
   reason for the scan scope.
3. A toolchain and image bump that takes reachable findings to zero, verified by
   the same command CI runs.
4. Both Trivy exceptions re-verified: DS-0002 either fixed or kept with a fresh
   justification; CVE-2026-78661 kept with evidence and a review date.
5. A Content-Security-Policy that works with the SPA, the inline theme script,
   and Sentry, verified by the Playwright suite.
6. A repeatable k6 load test with thresholds and a recorded baseline, including
   an honest account of the rate limiters in front of the measured paths.

**Non-goals**

- A paid external penetration test, bug bounty, or certification (SOC 2, ISO).
- WAF, DDoS mitigation, or multi-region failover.
- Rewriting authentication, adding passkeys, or changing the tenancy model.
- Load testing the live production host or probing third-party sites as part of
  a load scenario.
- A blanket ignore file. Any suppression stays dated, justified, and narrow.

**Anti-goals**

- Turning a gate back to advisory to make it green.
- Shipping a CSP that breaks the dashboard, Sentry, or the status page.
- Publishing load numbers from an unseeded or under-provisioned stack and
  calling them capacity.
- Making rate limits tunable in production just to make a load test pass.
- A threat model that restates the README instead of naming residual risk.

## 4. Constraints

- One host, Docker Compose, no WAF, no paid scanner beyond the existing Trivy
  and Dependabot setup.
- The Go module directive is `go 1.26.0`; the toolchain, Docker builder, and CI
  must agree or the gate becomes noise.
- The web app is a static SPA behind Caddy with one inline script (theme
  bootstrap) and Sentry as the only third-party runtime origin. Sentry's DSN is
  a **build-time** `VITE_SENTRY_DSN`, so Caddy cannot derive the ingest origin
  from it at runtime.
- `deploy/scripts/test-workflows.sh` asserts the current advisory govulncheck
  step name and `continue-on-error` flag; changing CI without changing that
  suite breaks the pipeline job.
- The demo deployment at `o1.work.gd` must keep working; the load test must not
  become a denial-of-service against it.

## 5. Approaches

**A. Gate-first, model alongside (chosen).** Bump the toolchain, make
`govulncheck` blocking, re-verify the scan exceptions, then write the threat
model with the gate results in hand, then load test. The gates are cheap, and
they immediately remove the loudest finding (29 stdlib CVEs). The model is
written the same week, so it is not an afterthought; it just does not block the
mechanical work.

**B. Model-first.** Write the full threat model, rank risks, then fix. Better
narrative purity, but the first week produces no measurable change and the model
risks describing a system whose dependency posture is already known to be stale.

**C. External penetration test.** Rejected: no vendor, no budget, and the
realistic findings for a single-host product at this stage are exactly what the
checklist and scanners already surface. A report from a stranger would look
better in a portfolio than it would improve the product.

The chosen order is A, with the model's asset/actor inventory drafted before the
CSP work so the browser-policy decisions are written down rather than improvised.

## 6. Workstreams

### W1. Threat model and review checklist

`docs/security/threat-model.md`:

- Assets: credentials and sessions, TOTP secrets and recovery codes, API key
  hashes, tenant monitor configuration (headers/bodies), probe results, alert
  channel secrets, audit trail, public status pages.
- Actors: anonymous visitor, authenticated viewer/member/admin, workspace owner,
  global administrator, API-key automation, malicious tenant, external target
  service, email/webhook provider, host operator.
- Trust boundaries: browser↔Caddy, Caddy↔API, API↔PostgreSQL, worker↔probe
  target, worker↔alert provider, operator↔host, CI↔GHCR↔host.
- A STRIDE-shaped table mapping each boundary to the control that addresses it
  and the residual risk that remains.
- Access-control matrix (role × action) checked against the handlers and their
  tests.
- Review term: the owner self-reviews the model and both scan exceptions
  quarterly, and every exception is re-checked on its own date. There is no
  second reviewer in this project; the document says so rather than implying
  independent sign-off.

`docs/security/review-checklist.md` holds the repeatable manual checks (headers,
cookies, rate limits, SSRF, secret handling, backup/restore assumptions) so a
future review does not start from zero.

### W2. Dependency gate

- Move `go 1.26.0` → `go 1.26.11` in `backend/go.mod`, bump the Docker builder
  and CI `setup-go` to match, and confirm `golangci-lint` still loads the module.
- CI: `govulncheck ./cmd/...` becomes blocking (`continue-on-error` removed);
  keep a second advisory `./...` run to surface test-tooling findings without
  blocking on testcontainers.
- Update `deploy/scripts/test-workflows.sh` to assert the new blocking shape and
  the retained advisory `./...` step. The pipeline job must pass before the
  change is considered done.
- Replace the "advisory until Plan 14" language in `README.md` and
  `deploy/CICD.md`, and document why the blocking scope is `./cmd/...`.
- Verify the final state with the exact CI command and record the output in the
  plan's completion notes.

### W3. Container and browser policy

- Re-check Caddy upstream with a fresh pull; if a release with x/net ≥ v0.62.0
  exists, bump and remove the exception. If not, keep it with a fresh review date
  and evidence from `caddy build-info`.
- Evaluate non-root Caddy, time-boxed. The realistic options are (a) run as the
  image's non-root user with `cap_add: NET_BIND_SERVICE` and correctly owned
  `caddy_data`/`caddy_config` volumes, or (b) listen on 8080/8443 inside the
  container and publish 80/443. Option (a) needs a one-time volume ownership
  migration; option (b) changes the compose port mapping and the health checks.
  Whichever is chosen must be reversible; if neither works within the time box,
  keep DS-0002 with the reason and a review date.
- Add `Content-Security-Policy` in the shared Caddyfile. The inline theme script
  is hashed; `script-src 'self' 'sha256-…'`; `img-src 'self' data:`; `frame-ancestors
  'none'`; `base-uri 'none'`; `form-action 'self'`; `connect-src 'self'` plus an
  operator-supplied `O1_CSP_EXTRA_CONNECT` (space-separated origins) for Sentry
  ingest when reporting is enabled. The default empty value keeps the policy
  strict; the runbook documents the Sentry origins to add. Verification is the
  existing Playwright suite plus a manual Sentry check, not a header-only
  assertion.

### W4. Load baseline

- Add validated configuration knobs for the two compiled-in limiters:
  `O1_PUBLIC_RATE_RPS` / `O1_PUBLIC_RATE_BURST` (defaults 1/20) and
  `O1_APIKEY_RATE_RPS` / `O1_APIKEY_RATE_BURST` (defaults 10/60). Defaults do not
  change, and the production env example does not set them. They exist so a
  load run can measure the stack instead of the limiter, and so the limiter
  itself can be tested deliberately.
- `deploy/loadtest/` with k6 scripts and a README:
  - `status.js`: public status page origin reads, against a stack with the
    public limiter raised by the override file.
  - `api.js`: authenticated read API with a dedicated workspace + 10 API keys,
    staying under the per-key budget so the limiter is not the bottleneck.
  - `limits.js`: a short scenario at default limits that asserts `429` behaviour
    rather than throughput.
- Reference workload for the baseline (explicitly a documented reference, not a
  claim about production): 10 tenants × 20 monitors at a 60-second interval
  (≈3.3 probes/second), 90 days of raw history (≈26M probe rows), seeded on a
  4 vCPU / 8 GB Linux host with the managed PostgreSQL replaced by the Compose
  PostgreSQL 16 container.
- Thresholds: read paths p95 < 300 ms, p99 < 600 ms, non-429 error rate < 0.1%
  at 100 VUs for 5 minutes; `limits.js` asserts the documented limit, not
  latency.
- Do not point `status.js` or `api.js` at `o1.work.gd`. At most, one 60-second
  smoke run at 10 VUs with the owner's agreement.
- `docs/security/load-test.md` records the scenario, host shape, dataset size,
  numbers, and what would break first (expected: database connections, then API
  CPU; the queue and probe path are out of scope because probing third-party
  sites is not a load test).

### W5. Fix pass

Everything the model, checklist, or load test turns up that is cheap and local:
header gaps, missing cache directives on authenticated responses, rate-limit
blind spots, log-redaction gaps, and documentation that contradicts behaviour.
Anything structural (for example a real WAF) goes to the backlog with a note,
not into this plan.

### W6. Documentation updates

- `README.md` security notes and development checks reflect the new gates and
  the `O1_*_RATE_*` variables.
- `deploy/CICD.md` loses the "advisory until Plan 14" caveats, documents the scan
  scope, and stops saying Caddy pins x/net v0.60.0 without the fresh version.
- `deploy/production/env.example` documents the CSP and rate variables as
  optional, with defaults.
- `PORTFOLIO.md` "What is next" points at the shipped model and baseline instead
  of the deferral.

## 7. Verification

- `cd backend && go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./cmd/...`
  exits 0 under the bumped toolchain.
- CI shows `govulncheck` as blocking and the pipeline job still passes with the
  updated workflow assertions.
- Trivy filesystem and image scans pass in the release workflow (they are not
  per-push jobs today) with a reviewed ignore file whose entries carry a reason
  and a review date.
- The full web suite (format, lint, typecheck, vitest across 67 files, Playwright
  e2e including axe) passes with the CSP enabled.
- k6 thresholds pass, `limits.js` shows the expected `429`s at defaults, and the
  result file is committed with the stack shape and commit SHA.
- The threat-model access-control matrix matches the handler tests; any
  disagreement is either fixed or recorded as residual risk.

## 8. Risks

- **Toolchain bump breaks lint or tests.** Mitigation: bump in one commit, run
  the full local gate before touching CI.
- **CSP breaks the SPA or Sentry.** Mitigation: build the policy from the actual
  bundle, verify with Playwright, and keep `style-src 'unsafe-inline'` if Radix
  or chart components require it (documented, not silent). Reporting stays off
  by default; enabling it is an operator choice with a documented origin.
- **Non-root Caddy cannot manage certificates cleanly.** Mitigation: time-box the
  experiment and keep the dated exception over a fragile proxy.
- **Load numbers get over-read.** Mitigation: publish host shape, dataset size,
  raised limits, and the first bottleneck in the same document.
- **Gate noise from test-only dependencies.** Mitigation: blocking scope is the
  shipping packages; the advisory full scan stays visible.
- **Rate knobs leak into production.** Mitigation: defaults unchanged, production
  env example leaves them unset, and the load override is a separate file.

## 9. Open questions

1. Load duration and VU count: proposed 100 VUs for 5 minutes for read paths and
   a 60-second limit-behaviour scenario. The numbers change thresholds, not the
   design.
2. CSP reporting: ship header-only by default, or add `report-to` with a
   collector? Header-only is the proposal; reporting needs a collector to run.

## 10. Out of scope, tracked as follow-ups

- External penetration test and letter of attestation.
- WAF / edge rate limiting beyond Caddy.
- Automatic secret rotation and a KMS-backed encryption key.
- Per-tenant retention and billing.
- Connection-pool sizing defaults. The baseline shows the 10-connection default
  queues at ~200 authenticated requests per second; the right value depends on
  the managed database tier, so it stays operator-set.
- Removing `'unsafe-inline'` from `style-src` after a Radix/chart style audit.

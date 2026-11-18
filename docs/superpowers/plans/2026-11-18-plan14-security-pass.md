# Plan 14 — Security Pass and Load Baseline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the deferred security work into shipped gates: zero reachable Go vulnerabilities under a blocking CI check, a documented threat model with reviewed scan exceptions, a working CSP, and a repeatable k6 load baseline with honest thresholds.

**Architecture:** Bump the Go toolchain so the standard library findings disappear, then scope the blocking scan to the shipping packages (`./cmd/...`) while keeping a full-module advisory scan. Add validated env knobs for the two compiled-in rate limiters so load runs measure the stack rather than the limiter, seed a reference dataset with a separate command, and measure read paths with k6. Documentation lives in `docs/security/`.

**Tech Stack:** Go 1.26.11, PostgreSQL 16, Caddy 2.11.9, React 19 + Vite, GitHub Actions, k6, Trivy, govulncheck v1.9.0.

**Spec:** `docs/superpowers/specs/2026-11-16-plan14-security-pass-design.md`

## Global Constraints

- Go toolchain, Docker builder, and both CI jobs use exactly `1.26.11`.
- Limit defaults do not change: public `60`/min and burst `20`; API key `600`/min and burst `60`. Production `.env` leaves the new variables unset.
- Blocking `govulncheck` scope is `./cmd/...`; `./...` stays advisory.
- Trivy runs at release time only; do not add it to per-push CI.
- The load test uses the local Compose stack. Never point `status.js` or `api.js` at `o1.work.gd`.
- CSP is header-only; no `report-to` collector.
- Caddy image stays `caddy:2-alpine` unless a newer release removes the x/net exception.
- Every commit message follows the existing `type: summary` style.

## Review Focus

1. The CSP hash must match the inline theme script exactly, or the page flashes light before React loads — test the hash against `index.html` and run the Playwright suite.
2. Rate-limit variables set to `0`, negative, or non-numeric must fail startup rather than silently disable limiting — config tests cover each variable.
3. Renaming the CI govulncheck step without updating `deploy/scripts/test-workflows.sh` turns the pipeline job red — the script change lands with the workflow change.
4. The load dataset must land in existing monthly partitions and must not be probed — the generator sets `paused=true` and integration tests assert row counts.
5. A toolchain bump can break the pinned golangci-lint build or the module directive — run lint in the same task as the bump.

---

### Task 1: Bump the Go toolchain to 1.26.11

**Files:**
- Modify: `backend/go.mod:3`
- Modify: `backend/Dockerfile:1`
- Modify: `.github/workflows/ci.yml` (both `go-version` entries: backend and integration jobs)

**Interfaces:**
- Consumes: nothing.
- Produces: a repository whose build and tests run on Go 1.26.11, which Task 2's gate depends on.

- [ ] **Step 1: Confirm the current failure**

Run: `cd backend && go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./cmd/...`
Expected: `Your code is affected by 29 vulnerabilities from the Go standard library` and exit status 3.

- [ ] **Step 2: Bump the three places**

`backend/go.mod`: change `go 1.26.0` to `go 1.26.11`.
`backend/Dockerfile`: change `FROM golang:1.26-alpine` to `FROM golang:1.26.11-alpine`.
`.github/workflows/ci.yml`: change both `go-version: "1.26"` values (backend and integration jobs) to `go-version: "1.26.11"`.

- [ ] **Step 3: Verify the build and tests on the new toolchain**

Run:
```sh
cd backend
go version
gofmt -l .
go vet ./...
go test -short ./...
golangci-lint run ./...
```
Expected: `go version go1.26.11 …`, no gofmt output, all tests pass, `0 issues` from the linter.

- [ ] **Step 4: Verify the vulnerability gate is clear**

Run: `cd backend && go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./cmd/...`
Expected: `Your code is affected by 0 vulnerabilities` and exit status 0.

- [ ] **Step 5: Commit**

```sh
git add backend/go.mod backend/Dockerfile .github/workflows/ci.yml
git commit -m "build: move the Go toolchain to 1.26.11"
```

---

### Task 2: Make the dependency gate blocking

**Files:**
- Modify: `.github/workflows/ci.yml:28-34`
- Modify: `deploy/scripts/test-workflows.sh:43-70`
- Modify: `README.md` (CI/CD section, the "advisory until the Plan 14 security pass" sentence)
- Modify: `deploy/CICD.md:44-49`, `deploy/CICD.md:576-586`, `deploy/CICD.md:614-628`

**Interfaces:**
- Consumes: Go 1.26.11 from Task 1.
- Produces: a blocking `govulncheck` step named `govulncheck` over `./cmd/...`, plus `govulncheck (full module, advisory)` over `./...`. Later tasks cite this gate in documentation.

- [ ] **Step 1: Write the failing workflow assertions**

In `deploy/scripts/test-workflows.sh`, replace the three assertions inside the `govulncheck` block with:

```sh
grep -Eq 'govulncheck@v1\.9\.0 \./cmd/\.\.\.' "$ci_file" ||
  fail "$ci_file does not gate the shipping packages"
grep -Fq 'name: govulncheck' "$ci_file" ||
  fail "$ci_file has no blocking govulncheck step"
if grep -A2 -F 'name: govulncheck' "$ci_file" | grep -Fq 'continue-on-error: true'; then
  fail "$ci_file still marks the blocking govulncheck advisory"
fi
grep -Fq 'name: govulncheck (full module, advisory)' "$ci_file" ||
  fail "$ci_file does not keep the full-module advisory scan"
grep -A1 -F 'name: govulncheck (full module, advisory)' "$ci_file" |
  grep -Fq 'continue-on-error: true' ||
  fail "$ci_file does not let the full-module scan continue on error"
check "$ci_file blocks on shipping-package vulnerabilities and advises on the full module"
```

Keep the existing awk check that requires govulncheck to sit in the backend job after `go vet`.

- [ ] **Step 2: Run the script to verify it fails**

Run: `bash deploy/scripts/test-workflows.sh`
Expected: FAIL with `does not gate the shipping packages`.

- [ ] **Step 3: Change the workflow**

Replace the current step in `.github/workflows/ci.yml` with:

```yaml
      - name: govulncheck
        run: go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./cmd/...

      # Advisory: testcontainers and container tooling ship no fixes, so the
      # full-module scan reports without blocking. The gate above covers what
      # is actually built into the images.
      - name: govulncheck (full module, advisory)
        continue-on-error: true
        run: go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

- [ ] **Step 4: Run the script to verify it passes**

Run: `bash deploy/scripts/test-workflows.sh`
Expected: exit 0, ending with the check count.

- [ ] **Step 5: Update the documentation**

- `README.md`: replace "`govulncheck` is advisory until the Plan 14 security pass" with a sentence saying the shipping packages are blocking and the full module is advisory.
- `deploy/CICD.md`: the same replacement in the pipeline description, the scans paragraph, and the local-gate section.
- The local gate block gains the blocking command:
  ```sh
  cd backend && go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./cmd/...
  ```

- [ ] **Step 6: Commit**

```sh
git add .github/workflows/ci.yml deploy/scripts/test-workflows.sh README.md deploy/CICD.md
git commit -m "ci: gate shipping packages on govulncheck"
```

---

### Task 3: Make the rate limiters configurable

**Files:**
- Modify: `backend/internal/config/config.go` (struct near line 62; `Load` near line 337; validation near line 439)
- Modify: `backend/internal/config/config_test.go`
- Modify: `backend/internal/api/router.go:67-82` (helpers) and `:270`/`:305` (limiters)
- Modify: `backend/internal/api/router_test.go`
- Modify: `README.md` configuration list
- Modify: `deploy/production/env.example`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Config.PublicRatePerMinute`, `PublicRateBurst`, `APIKeyRatePerMinute`, `APIKeyRateBurst` (all `int`), and router helpers `publicRPS(config.Config) float64`, `publicBurst(config.Config) int`, `apiKeyRPS(config.Config) float64`, `apiKeyBurst(config.Config) int`. Task 8's Compose override sets `O1_PUBLIC_RATE_PER_MINUTE`, `O1_PUBLIC_RATE_BURST`, `O1_APIKEY_RATE_PER_MINUTE`, `O1_APIKEY_RATE_BURST`.

- [ ] **Step 1: Write the failing config tests**

Add to `backend/internal/config/config_test.go`, mirroring `TestLoad_AuthRateDefaults`:

```go
func TestLoad_RateLimitDefaults(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	for _, name := range []string{
		`O1_PUBLIC_RATE_PER_MINUTE`,
		`O1_PUBLIC_RATE_BURST`,
		`O1_APIKEY_RATE_PER_MINUTE`,
		`O1_APIKEY_RATE_BURST`,
	} {
		t.Setenv(name, "")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PublicRatePerMinute != 60 || cfg.PublicRateBurst != 20 {
		t.Errorf("public rate = %d/%d, want 60/20", cfg.PublicRatePerMinute, cfg.PublicRateBurst)
	}
	if cfg.APIKeyRatePerMinute != 600 || cfg.APIKeyRateBurst != 60 {
		t.Errorf("api key rate = %d/%d, want 600/60", cfg.APIKeyRatePerMinute, cfg.APIKeyRateBurst)
	}
}

func TestLoad_RateLimitOverrides(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_PUBLIC_RATE_PER_MINUTE", "1200")
	t.Setenv("O1_PUBLIC_RATE_BURST", "200")
	t.Setenv("O1_APIKEY_RATE_PER_MINUTE", "6000")
	t.Setenv("O1_APIKEY_RATE_BURST", "600")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PublicRatePerMinute != 1200 || cfg.PublicRateBurst != 200 {
		t.Errorf("public rate = %d/%d, want 1200/200", cfg.PublicRatePerMinute, cfg.PublicRateBurst)
	}
	if cfg.APIKeyRatePerMinute != 6000 || cfg.APIKeyRateBurst != 600 {
		t.Errorf("api key rate = %d/%d, want 6000/600", cfg.APIKeyRatePerMinute, cfg.APIKeyRateBurst)
	}
}

func TestLoad_RejectsInvalidRateLimits(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	for _, name := range []string{
		`O1_PUBLIC_RATE_PER_MINUTE`,
		`O1_PUBLIC_RATE_BURST`,
		`O1_APIKEY_RATE_PER_MINUTE`,
		`O1_APIKEY_RATE_BURST`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "0")
			if _, err := config.Load(); err == nil {
				t.Fatalf("%s=0 was accepted", name)
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/config/ -run 'TestLoad_RateLimit|TestLoad_RejectsInvalidRateLimits'`
Expected: compile failure or assertion failures (`PublicRatePerMinute` undefined).

- [ ] **Step 3: Add the config fields, parsing, and validation**

In `config.go`, next to the auth rate fields:

```go
	// PublicRatePerMinute and PublicRateBurst configure the per-IP token
	// bucket on the public status page. Defaults keep 1 rps with a burst of 20.
	PublicRatePerMinute int
	PublicRateBurst     int

	// APIKeyRatePerMinute and APIKeyRateBurst configure the per-key token
	// bucket on API-key routes. Defaults keep 10 rps with a burst of 60.
	APIKeyRatePerMinute int
	APIKeyRateBurst     int
```

In `Load`, after the auth rate block:

```go
	if cfg.PublicRatePerMinute, err = envInt("O1_PUBLIC_RATE_PER_MINUTE", 60); err != nil {
		return cfg, err
	}
	if cfg.PublicRateBurst, err = envInt("O1_PUBLIC_RATE_BURST", 20); err != nil {
		return cfg, err
	}
	if cfg.APIKeyRatePerMinute, err = envInt("O1_APIKEY_RATE_PER_MINUTE", 600); err != nil {
		return cfg, err
	}
	if cfg.APIKeyRateBurst, err = envInt("O1_APIKEY_RATE_BURST", 60); err != nil {
		return cfg, err
	}
```

In validation, mirroring the auth checks:

```go
	if cfg.PublicRatePerMinute < 1 || cfg.PublicRateBurst < 1 {
		return fmt.Errorf("invalid O1_PUBLIC_RATE_PER_MINUTE/O1_PUBLIC_RATE_BURST: must be at least 1")
	}
	if cfg.APIKeyRatePerMinute < 1 || cfg.APIKeyRateBurst < 1 {
		return fmt.Errorf("invalid O1_APIKEY_RATE_PER_MINUTE/O1_APIKEY_RATE_BURST: must be at least 1")
	}
```

- [ ] **Step 4: Run the config tests**

Run: `cd backend && go test ./internal/config/ -run 'RateLimit' -v`
Expected: PASS.

- [ ] **Step 5: Write the failing router helper test**

Add to `backend/internal/api/router_test.go`:

```go
func TestRateLimitHelpers(t *testing.T) {
	zero := config.Config{}
	if got := publicRPS(zero); math.Abs(got-1.0) > 0.001 {
		t.Fatalf("default public rps = %v, want 1", got)
	}
	if got := publicBurst(zero); got != 20 {
		t.Fatalf("default public burst = %d, want 20", got)
	}
	if got := apiKeyRPS(zero); math.Abs(got-10.0) > 0.001 {
		t.Fatalf("default api key rps = %v, want 10", got)
	}
	if got := apiKeyBurst(zero); got != 60 {
		t.Fatalf("default api key burst = %d, want 60", got)
	}

	cfg := config.Config{
		PublicRatePerMinute: 1200, PublicRateBurst: 200,
		APIKeyRatePerMinute: 6000, APIKeyRateBurst: 600,
	}
	if got := publicRPS(cfg); math.Abs(got-20.0) > 0.001 {
		t.Fatalf("public rps = %v, want 20", got)
	}
	if got := apiKeyRPS(cfg); math.Abs(got-100.0) > 0.001 {
		t.Fatalf("api key rps = %v, want 100", got)
	}
}
```

Add the `math` import if missing.

- [ ] **Step 6: Run it to verify it fails**

Run: `cd backend && go test ./internal/api/ -run TestRateLimitHelpers`
Expected: compile failure (`publicRPS` undefined).

- [ ] **Step 7: Implement the helpers and use them**

Next to `authRPS`/`authBurst` in `router.go`:

```go
const (
	defaultPublicRatePerMinute = 60
	defaultPublicRateBurst     = 20
	defaultAPIKeyRatePerMinute = 600
	defaultAPIKeyRateBurst     = 60
)

func publicRPS(cfg config.Config) float64 {
	perMinute := cfg.PublicRatePerMinute
	if perMinute < 1 {
		perMinute = defaultPublicRatePerMinute
	}
	return float64(perMinute) / 60.0
}

func publicBurst(cfg config.Config) int {
	if cfg.PublicRateBurst < 1 {
		return defaultPublicRateBurst
	}
	return cfg.PublicRateBurst
}

func apiKeyRPS(cfg config.Config) float64 {
	perMinute := cfg.APIKeyRatePerMinute
	if perMinute < 1 {
		perMinute = defaultAPIKeyRatePerMinute
	}
	return float64(perMinute) / 60.0
}

func apiKeyBurst(cfg config.Config) int {
	if cfg.APIKeyRateBurst < 1 {
		return defaultAPIKeyRateBurst
	}
	return cfg.APIKeyRateBurst
}
```

Replace the two literals:

```go
	keyLimiter := httpx.KeyedRateLimit(auth.APIKeyRateKey, apiKeyRPS(deps.Config), apiKeyBurst(deps.Config))
```
```go
	mux.Handle("GET /api/v1/public/status/{slug}", httpx.RateLimit(publicRPS(deps.Config), publicBurst(deps.Config), true)(http.HandlerFunc(statuspage.PublicHandler(statusPageSvc))))
```

- [ ] **Step 8: Run the router and full short suites**

Run:
```sh
cd backend && go test ./internal/api/ ./internal/config/ && go test -short ./...
```
Expected: PASS.

- [ ] **Step 9: Document the variables**

- `README.md` configuration list: add the four variables with defaults and a sentence that they exist for capacity testing and do not change the shipped defaults.
- `deploy/production/env.example`: add a commented block stating they are optional and must stay unset in normal production.

- [ ] **Step 10: Commit**

```sh
git add backend/internal/config backend/internal/api README.md deploy/production/env.example
git commit -m "feat: make the rate limiters configurable"
```

---

### Task 4: Add the Content-Security-Policy

**Files:**
- Modify: `web/src/meta.test.ts`
- Modify: `web/Caddyfile` (inside the `header` block added in `9715d4f`)
- Modify: `web/index.html` only if the hash step shows the inline script must be adjusted
- Modify: `compose.yaml` and `deploy/production/compose.yaml` (web service environment)
- Modify: `deploy/production/env.example`

**Interfaces:**
- Consumes: the inline theme bootstrap script in `web/index.html`.
- Produces: a CSP header whose `connect-src` includes `{$O1_CSP_EXTRA_CONNECT}`, an env variable of the same name in both Compose files, and a test that fails if the hash drifts.

- [ ] **Step 1: Write the failing test**

Add the import at the top of `web/src/meta.test.ts` (the file already carries `/// <reference types="node" />`), then append the test:

```ts
import { createHash } from 'node:crypto';

test('the CSP allows the inline theme script by hash', async () => {
	const html = await readProjectFile('../index.html');
	const inline = html.match(/<script>([\s\S]*?)<\/script>/)?.[1];
	expect(inline).toBeTruthy();

	const hash = createHash('sha256')
		.update(inline as string, 'utf8')
		.digest('base64');
	const caddyfile = await readProjectFile('../Caddyfile');

	expect(caddyfile).toContain("Content-Security-Policy");
	expect(caddyfile).toContain(`'sha256-${hash}'`);
	expect(caddyfile).toContain('{$O1_CSP_EXTRA_CONNECT}');
	expect(caddyfile).toContain("frame-ancestors 'none'");
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npm test -- --run src/meta.test.ts`
Expected: FAIL, `Content-Security-Policy` not found.

- [ ] **Step 3: Compute the hash and confirm the build keeps it**

Run:
```sh
cd web
npm run build >/dev/null
node -e '
const {readFileSync}=require("fs"); const {createHash}=require("crypto");
const html=readFileSync("dist/index.html","utf8");
const inline=html.match(/<script>([\s\S]*?)<\/script>/)[1];
console.log(createHash("sha256").update(inline,"utf8").digest("base64"));
'
```
Expected: one base64 value. If the source and built hashes differ, stop and use the built hash in the Caddyfile while keeping the test on the source — record the difference in the commit message. (Vite normally copies the inline script unchanged.)

- [ ] **Step 4: Add the header**

Inside the existing `header { … }` block in `web/Caddyfile`:

```
		Content-Security-Policy "default-src 'self'; script-src 'self' 'sha256-<VALUE>'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self' {$O1_CSP_EXTRA_CONNECT}; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'"
```

Replace `<VALUE>` with the Step 3 output.

- [ ] **Step 5: Plumb the extra-origin variable**

`compose.yaml` web service environment, and the same for the web service in `deploy/production/compose.yaml`:

```yaml
      O1_CSP_EXTRA_CONNECT: ${O1_CSP_EXTRA_CONNECT:-}
```

`deploy/production/env.example`:

```
# Optional extra connect-src origins for the CSP, space separated. Required for
# Sentry ingest when VITE_SENTRY_DSN is baked into the web build, for example:
# O1_CSP_EXTRA_CONNECT=https://o1.ingest.sentry.io
O1_CSP_EXTRA_CONNECT=
```

- [ ] **Step 6: Verify the test and the real header**

Run:
```sh
cd web && npm test -- --run src/meta.test.ts
cd .. && docker run --rm -e O1_SITE_ADDRESS=:80 -v "$PWD/web/Caddyfile:/etc/caddy/Caddyfile:ro" caddy:2-alpine caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
```
Expected: test PASS; Caddy prints `Valid configuration`.

- [ ] **Step 7: Verify the SPA still boots with the policy**

Run:
```sh
cd web && npm run build
cd .. && docker run -d --name o1-csp -p 18083:80 -e O1_SITE_ADDRESS=:80 \
  -v "$PWD/web/Caddyfile:/etc/caddy/Caddyfile:ro" -v "$PWD/web/dist:/srv:ro" caddy:2-alpine
sleep 2
curl -sSI http://127.0.0.1:18083/ | grep -i 'content-security-policy'
docker rm -f o1-csp
```
Expected: the header is present with the hash. Then run the Playwright suite against a Compose stack (the plan's Task 8 stack is fine) and confirm the theme toggle and dashboard load; no CSP violations in the browser console.

- [ ] **Step 8: Commit**

```sh
git add web/src/meta.test.ts web/Caddyfile web/index.html compose.yaml deploy/production/compose.yaml deploy/production/env.example
git commit -m "feat: send a content security policy"
```

---

### Task 5: Re-verify the container exceptions and evaluate non-root Caddy

**Files:**
- Modify: `.trivyignore` (only if the Caddy dependency moves)
- Modify: `deploy/CICD.md` (exception paragraph) and, if the experiment succeeds, `compose.yaml` + `deploy/production/compose.yaml`
- Create: `docs/security/container-review.md`

**Interfaces:**
- Consumes: nothing.
- Produces: a written decision for DS-0002 and CVE-2026-78661, with the exact Caddy version and x/net version recorded.

- [ ] **Step 1: Record the upstream state**

Run:
```sh
docker pull caddy:2-alpine >/dev/null
docker run --rm --entrypoint caddy caddy:2-alpine version
docker run --rm --entrypoint caddy caddy:2-alpine build-info | grep -E 'golang.org/x/net\s|go\sgo'
```
Expected on 2026-11-18: `v2.11.9`, `go1.27.3`, `golang.org/x/net v0.60.0`.

- [ ] **Step 2: Try the non-root options in a scratch override**

Create a temporary override (do not commit) that runs the web service as `user: "1000:1000"`, publishes `18084:8080`, and sets `O1_SITE_ADDRESS=:8080`, with a named volume for `/data`. Start only `web` and the `api` it proxies to.

Run:
```sh
docker compose -f compose.yaml -f /tmp/nonroot.override.yaml up -d api web
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18084/healthz
docker compose -f compose.yaml -f /tmp/nonroot.override.yaml logs --tail=20 web
```
Expected: `200` when the volume is writable; a permission error naming `/data` or `/config` otherwise.

- [ ] **Step 3: Decide and write it down**

`docs/security/container-review.md` records: the versions from Step 1; whether a high-port remap or a capability change let Caddy run as non-root; the volume ownership migration needed on an existing host (a one-time `chown` or volume recreate); and the decision.

- DS-0002: if the non-root path works and is reversible, ship it in both Compose files with the migration note in `deploy/production/README.md`, then delete the entry from `.trivyignore`.
- DS-0002: if it does not work cleanly in the time box, keep the entry and update its review date and reason with the evidence.
- CVE-2026-78661: remove the entry only if Step 1 shows x/net ≥ v0.62.0; otherwise keep it and refresh the review date to 2027-01-12 with the observed versions.

- [ ] **Step 4: Verify the committed state**

Run:
```sh
docker compose config >/dev/null
docker compose -f deploy/production/compose.yaml config >/dev/null   # after providing deploy/production/.env
docker run --rm -v "$PWD:/repo" -w /repo aquasec/trivy:0.76.2 fs --scanners secret,misconfig --severity HIGH,CRITICAL --exit-code 1 .
```
Expected: both configs valid; Trivy exits 0 with the reviewed ignore file.

- [ ] **Step 5: Commit**

```sh
git add docs/security/container-review.md .trivyignore compose.yaml deploy/production deploy/CICD.md
git commit -m "docs: re-review the container scan exceptions"
```

---

### Task 6: Write the threat model and review checklist

**Files:**
- Create: `docs/security/threat-model.md`
- Create: `docs/security/review-checklist.md`

**Interfaces:**
- Consumes: the control descriptions in `README.md`, `PORTFOLIO.md`, and the code paths listed below.
- Produces: the documents Task 10 links from the top-level docs.

- [ ] **Step 1: Draft the model**

`docs/security/threat-model.md` must contain, in this order: assets; actors; trust boundaries; a STRIDE table with one row per boundary; the access-control matrix; residual risks; review cadence.

Ground each row in code:

- SSRF and probe transport: `backend/internal/probe/policy.go`
- Alert secret handling: `backend/internal/alerts/config.go`
- Sessions and 2FA: `backend/internal/auth/`
- API key hashing and scope: `backend/internal/apikeys/service.go`
- Tenant scoping and 404 responses: `backend/internal/httpx/middleware.go`
- Audit attribution: `backend/internal/audit/audit.go`
- Retention and deletion: `backend/internal/retention/`
- Browser policy and TLS: `web/Caddyfile`

- [ ] **Step 2: Build the access-control matrix from the router**

List every route in `backend/internal/api/router.go` with its required role or key scope, then compare against the handler tests in `backend/internal/api/*_integration_test.go`. Any route whose matrix entry the tests do not cover becomes a residual risk line, not a guess.

- [ ] **Step 3: Write the checklist**

`docs/security/review-checklist.md` holds repeatable checks with commands or file references: response headers, cookie flags, auth rate limits, public rate limit, SSRF policy, secret write-only behaviour, audit redaction, backup/restore assumptions, dependency gates, and exception dates.

- [ ] **Step 4: Verify the documents are honest**

Run: `grep -c '^|' docs/security/threat-model.md` and re-read the residual-risk section. Expected: at least one row per trust boundary, and no residual-risk row that merely repeats a control. Confirm every file path mentioned exists:
```sh
grep -o '`[a-z][^`]*`' docs/security/threat-model.md | tr -d '`' | while read -r p; do [ -e "$p" ] || echo "missing: $p"; done
```
Expected: no `missing:` lines.

- [ ] **Step 5: Commit**

```sh
git add docs/security/threat-model.md docs/security/review-checklist.md
git commit -m "docs: add the threat model and review checklist"
```

---

### Task 7: Add the load dataset generator

**Files:**
- Create: `backend/internal/loadseed/seed.go`
- Create: `backend/internal/loadseed/seed_test.go`
- Create: `backend/cmd/loadseed/main.go`

**Interfaces:**
- Consumes: `db.EnsureProbePartitions`, `pgxpool.Pool`, `auth.HashPassword` (for the load user).
- Produces:
  ```go
  type Options struct {
      Tenants           int
      MonitorsPerTenant int
      RawDays           int
      RollupDays        int
      Now               time.Time
  }
  type Result struct {
      WorkspaceIDs []uuid.UUID
      Monitors     int
      RawRows      int64
      RollupRows   int64
      APIPrefix    string
  }
  func Seed(ctx context.Context, pool *pgxpool.Pool, opts Options) (Result, error)
  ```
  Task 8 uses the generated workspace slug and the API key printed by the command; it does not import this package.

- [ ] **Step 1: Write the failing integration test**

`backend/internal/loadseed/seed_test.go`, using `testutil.NewPostgres(t)`:

```go
func TestSeedCreatesReferenceDataset(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	res, err := Seed(ctx, pool, Options{Tenants: 2, MonitorsPerTenant: 3, RawDays: 1, RollupDays: 2, Now: now})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if res.Monitors != 6 {
		t.Fatalf("monitors = %d, want 6", res.Monitors)
	}
	// One raw row per minute for the last day, per monitor.
	if want := int64(6 * 24 * 60); res.RawRows != want {
		t.Fatalf("raw rows = %d, want %d", res.RawRows, want)
	}
	// Hourly rollups for the completed days, per monitor.
	if want := int64(6 * 24 * 2); res.RollupRows != want {
		t.Fatalf("rollup rows = %d, want %d", res.RollupRows, want)
	}

	var paused, workspaces, keys int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE paused), count(DISTINCT workspace_id) FROM monitors`).Scan(&paused, &workspaces); err != nil {
		t.Fatalf("count monitors: %v", err)
	}
	if paused != 6 || workspaces != 2 {
		t.Fatalf("paused=%d workspaces=%d", paused, workspaces)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE revoked_at IS NULL`).Scan(&keys); err != nil {
		t.Fatalf("count api keys: %v", err)
	}
	if keys != 1 {
		t.Fatalf("api keys = %d, want 1", keys)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd backend && go test ./internal/loadseed/`
Expected: build failure, package has no `Seed`.

- [ ] **Step 3: Implement the generator**

`internal/loadseed/seed.go`:

- Create one verified user per run: `load@o1.work.gd`, using `auth.HashPassword` with a fixed development password `load test password`.
- Create `Tenants` workspaces named `Load 01`… with slugs `load-01`… and the load user as `owner`.
- Create `MonitorsPerTenant` monitors per workspace, named `Load monitor 01`…, URL `https://example.com/`, interval 60, `paused = true`, so no scheduler probe can touch them; `monitor_state.status = 'unknown'`, `next_check_at = now + interval '100 years'`.
- Raw rows: one per minute for `RawDays` days per monitor, `status='up'`, `status_code=200`, deterministic latency, IDs from `uuid.NewSHA1` so re-runs do not duplicate.
- Rollups: one row per hour for `RollupDays` completed days per monitor, `checks=60`, `up_count=60`, latency values.
- Set `rollup_state.watermark` to the start of the current UTC day when it is null.
- Create exactly one `member` API key in the first workspace and return its public prefix in `Result.APIPrefix`. The command prints the full secret once. Hash and prefix follow `internal/apikeys`: key format `o1_<prefix>_<secret>`, 12-character lowercase base-36 prefix, `sha256` of the full key in `token_hash`.

`cmd/loadseed/main.go`: flags `-tenants` (default 10), `-monitors` (default 20), `-raw-days` (default 7), `-rollup-days` (default 90), `-password` (default `load test password`); calls `app.Start`, then `loadseed.Seed`, then prints a summary and the API key secret once.

- [ ] **Step 4: Run the integration test**

Run: `cd backend && go test -count=1 ./internal/loadseed/ -v`
Expected: PASS.

- [ ] **Step 5: Run the linters and short suite**

Run: `cd backend && gofmt -l . && go vet ./... && golangci-lint run ./... && go test -short ./...`
Expected: clean.

- [ ] **Step 6: Commit**

```sh
git add backend/internal/loadseed backend/cmd/loadseed
git commit -m "feat: add a load-test dataset generator"
```

---

### Task 8: Add the k6 harness, run the baseline, and record it

**Files:**
- Create: `deploy/loadtest/compose.override.yaml`
- Create: `deploy/loadtest/status.js`
- Create: `deploy/loadtest/api.js`
- Create: `deploy/loadtest/limits.js`
- Create: `deploy/loadtest/README.md`
- Create: `docs/security/load-test.md`

**Interfaces:**
- Consumes: the four rate environment variables from Task 3 and the generator from Task 7.
- Produces: committed scenarios whose thresholds are documented in `docs/security/load-test.md`.

- [ ] **Step 1: Write the override**

`deploy/loadtest/compose.override.yaml`:

```yaml
# Raises the limiters so a load run measures the stack, not the token bucket.
# Never merge this into a production deployment.
services:
  api:
    environment:
      O1_PUBLIC_RATE_PER_MINUTE: "12000"
      O1_PUBLIC_RATE_BURST: "2000"
      O1_APIKEY_RATE_PER_MINUTE: "60000"
      O1_APIKEY_RATE_BURST: "10000"
```

- [ ] **Step 2: Write the scenarios**

`status.js`: `GET ${BASE_URL}/api/v1/public/status/${SLUG}` at a constant-arrival rate; thresholds `http_req_failed: ['rate<0.001']`, `http_req_duration: ['p(95)<300', 'p(99)<600']`. Default `SLUG=load-01`.

`api.js`: `GET ${BASE_URL}/api/v1/workspaces/${WORKSPACE_ID}/monitors` and `/monitors/{id}/summary` with `Authorization: Bearer ${API_KEY}`; the same thresholds; `WORKSPACE_ID` and `API_KEY` come from env.

`limits.js`: constant 2 rps at defaults against the public endpoint; asserts the limiter returns `429` (`checks` metric) instead of measuring latency.

Each file starts with a comment naming the threshold and the scenario. No scenario writes data, and none targets a URL other than `BASE_URL`.

- [ ] **Step 3: Document the run**

`deploy/loadtest/README.md` documents: start the stack without scheduler and worker (`docker compose -f compose.yaml -f deploy/loadtest/compose.override.yaml up -d postgres api web`), run `go run ./cmd/loadseed -tenants 10 -monitors 20`, take the one API key secret that `loadseed` prints for the `load-01` workspace, export `WORKSPACE_ID`/`API_KEY`, then run the three scripts with `k6 run`. It also records the reference workload and that the load host must be the 4 vCPU / 8 GB reference.

- [ ] **Step 4: Run the baseline**

Run:
```sh
docker compose -f compose.yaml -f deploy/loadtest/compose.override.yaml up -d postgres api web
cd backend && go run ./cmd/loadseed -tenants 10 -monitors 20
# create the API key in load-01 through the dashboard, then:
k6 run -e BASE_URL=http://127.0.0.1:8080 deploy/loadtest/status.js
k6 run -e BASE_URL=http://127.0.0.1:8080 -e WORKSPACE_ID=… -e API_KEY=… deploy/loadtest/api.js
k6 run -e BASE_URL=http://127.0.0.1:8080 deploy/loadtest/limits.js
```
Expected: the first two satisfy their thresholds; `limits.js` shows `429`s.

- [ ] **Step 5: Record the result**

`docs/security/load-test.md`: commit SHA, host shape, dataset counts (`10 tenants`, `200 monitors`, `2,016,000` raw rows, `432,000` rollup rows), raised limits, p95/p99, error rate, observed CPU/memory, and the first bottleneck. If a threshold failed, record the failure and the cause instead of tuning the threshold.

- [ ] **Step 6: Commit**

```sh
git add deploy/loadtest docs/security/load-test.md .gitignore
git commit -m "test: add a k6 load baseline"
```

---

### Task 9: Fix what the review and the load run found

**Files:**
- Modify: whatever the findings require (expected: `web/Caddyfile`, `backend/internal/httpx/`, `backend/internal/audit/`, or compose files)
- Modify: `docs/security/review-checklist.md` (append the findings list)

**Interfaces:**
- Consumes: Task 5's container findings, Task 6's residual-risk lines, Task 8's bottleneck.
- Produces: a short, dated findings list where every entry is either fixed with a test or moved to the follow-ups section of `docs/security/threat-model.md`.

- [ ] **Step 1: Collect the findings**

Append a `## Findings, 2026-11-18` section to `docs/security/review-checklist.md` with one line per finding from Tasks 5, 6, and 8, each tagged `fix` or `follow-up`.

- [ ] **Step 2: Fix each `fix` item with a test first**

For every `fix` line: write the failing test (a `web/src/meta.test.ts` assertion for a header, a `httpx` unit test for middleware behaviour, or an integration test for a handler), run it to confirm red, implement, run to confirm green. If a whole class of items shares one test, one test may cover them.

- [ ] **Step 3: Move the rest**

For every `follow-up` line, add it to §10 of `docs/superpowers/specs/2026-11-16-plan14-security-pass-design.md` (or the threat model's residual risks when it is a risk rather than a project). Do not leave an unclassified line.

- [ ] **Step 4: Run the full gate**

Run the Task 10 Step 3 commands. Expected: all exit 0.

- [ ] **Step 5: Commit**

```sh
git add docs/security/review-checklist.md docs/superpowers/specs/2026-11-16-plan14-security-pass-design.md <changed source files>
git commit -m "fix: close the findings from the security pass"
```

---

### Task 10: Close the loop in the top-level docs

**Files:**
- Modify: `README.md` (Security notes, development checks)
- Modify: `deploy/CICD.md` (exception and advisory language)
- Modify: `PORTFOLIO.md` ("What is next" and the engineering-practice list)
- Modify: `deploy/production/README.md` (rate and CSP variables if Task 5 shipped them)

**Interfaces:**
- Consumes: the gates from Tasks 1–2, the CSP from Task 4, the exceptions from Task 5, the model from Task 6, and the baseline from Task 8.
- Produces: documentation that no longer promises Plan 14.

- [ ] **Step 1: Update the four documents**

- `README.md` security notes: link the threat model and checklist; state the CSP and the four rate variables.
- `deploy/CICD.md`: remove every "until the Plan 14 security pass" phrase; link `docs/security/threat-model.md` and `docs/security/load-test.md`.
- `PORTFOLIO.md`: replace the "What is next" paragraph with a sentence pointing at the shipped security documentation, and add the load baseline to the engineering-practice list.
- `deploy/production/README.md`: document `O1_CSP_EXTRA_CONNECT` and the optional rate variables, with defaults.

- [ ] **Step 2: Verify no stale promise remains**

Run:
```sh
grep -rn 'Plan 14' README.md deploy/CICD.md PORTFOLIO.md deploy/production/README.md
```
Expected: no matches that call the work future or advisory. References to the plan as history are acceptable only in `docs/superpowers/`.

- [ ] **Step 3: Run the whole local gate**

Run:
```sh
cd backend && gofmt -l . && go vet ./... && go test -short ./... && golangci-lint run ./... && go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./cmd/...
cd ../web && npm run format:check && npm run lint && npm run typecheck && npm test -- --run
cd .. && bash deploy/scripts/test-workflows.sh && docker compose config >/dev/null
```
Expected: every command exits 0.

- [ ] **Step 4: Commit**

```sh
git add README.md deploy/CICD.md PORTFOLIO.md deploy/production/README.md
git commit -m "docs: report the shipped security work"
```

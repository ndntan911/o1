# Managed PostgreSQL Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove PostgreSQL from the production Compose stack and run production against a managed database whose DSN comes from the environment.

**Architecture:** The production Compose file stops defining a database; `O1_DATABASE_URL` becomes required input and `O1_DB_MAX_CONNS` sizes each process pool. No application logic changes beyond reading the pool size from config. The runbook gains provisioning, one-time cutover, and rollback-constraint sections.

**Tech Stack:** Go 1.26 (`pgx/v5/pgxpool`), Docker Compose v2, Bash test scripts, GitHub Actions release bundles.

**Spec:** `docs/superpowers/specs/2026-11-11-managed-postgres-design.md`

## Global Constraints

- The production Compose file must contain no `postgres` service and no `postgres-data` volume. The development stack (`compose.yaml` and `.env.example` at the repository root) is unchanged.
- The production DSN must never contain `sslmode=disable`.
- `O1_DB_MAX_CONNS` defaults to `10`, accepts integers `1–100`, and is per process (`api`, `scheduler`, `worker`).
- The runbook must name the direct/session endpoint requirement and must not describe transaction poolers as supported.
- No doc under `deploy/` may instruct an operator to set `POSTGRES_PASSWORD`, run `docker compose exec postgres`, or expect application data in `postgres-data`.
- Commits use conventional prefixes, matching repository history (`feat:`, `test:`, `docs:`, `fix:`).
- Tooling verified on this machine: Go 1.26.2, Docker 29.4.0, Compose v5.1.1.

## Review Focus

1. **Missing `O1_DATABASE_URL`** at deploy time: Compose must refuse to render rather than start a stack that crash-loops. Pinned by the missing-DSN check in `deploy/scripts/test-compose-config.sh` (Task 2).
2. **`sslmode=disable` copied from the development `.env`**: plaintext database traffic over the internet. Pinned by the `sslmode=disable` assertion in `deploy/scripts/test-compose-config.sh` (Task 2).
3. **Transaction-pooler DSN** (Neon `-pooler`, Supabase port `6543`): startup migrations take advisory locks and River needs session semantics, so the app hangs or fails. Pinned by the runbook wording check in Task 3, step 6.
4. **Tier connection cap below `3 × O1_DB_MAX_CONNS`**: intermittent startup and readiness failures under load. Pinned by the config validation test (Task 1) and the connection-budget paragraph (Task 3).
5. **Rollback to a pre-cutover release**: its Compose file starts an empty local PostgreSQL and `/readyz` goes green, which looks like total data loss. Pinned by the rollback-constraint wording checks in Tasks 3 and 4.

---

### Task 1: Configurable database pool size

**Files:**
- Modify: `backend/internal/config/config.go` (struct field, `Load`, `validate`)
- Modify: `backend/internal/config/config_test.go`
- Modify: `backend/internal/db/db.go:14-30`
- Modify: `backend/internal/app/app.go:39`

**Interfaces:**
- Consumes: existing `envInt(name string, def int) (int, error)` helper and the `Config.validate()` method.
- Produces: `config.Config.DBMaxConns int`; `db.Open(ctx context.Context, databaseURL string, maxConns int) (*pgxpool.Pool, error)`.

- [ ] **Step 1: Write the failing tests**

Append to `backend/internal/config/config_test.go`:

```go
func TestLoad_DBMaxConnsDefault(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_DB_MAX_CONNS", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBMaxConns != 10 {
		t.Fatalf("DBMaxConns = %d, want 10", cfg.DBMaxConns)
	}
}

func TestLoad_DBMaxConnsExplicit(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_DB_MAX_CONNS", "7")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBMaxConns != 7 {
		t.Fatalf("DBMaxConns = %d, want 7", cfg.DBMaxConns)
	}
}

func TestLoad_RejectsInvalidDBMaxConns(t *testing.T) {
	for _, value := range []string{"0", "101", "abc"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("O1_DATABASE_URL", "postgres://example")
			t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
			t.Setenv("O1_DB_MAX_CONNS", value)
			if _, err := config.Load(); err == nil {
				t.Fatalf("Load() accepted O1_DB_MAX_CONNS=%q", value)
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/config -run 'DBMaxConns' -v`
Expected: FAIL — `cfg.DBMaxConns` is undefined (compile error).

- [ ] **Step 3: Implement the setting**

In `backend/internal/config/config.go`:

1. Add `DBMaxConns int` to the `Config` struct, immediately after `DatabaseURL string`.
2. In `Load`, alongside the other integer settings (for example after the `O1_WORKER_CONCURRENCY` line), add:
   ```go
   if cfg.DBMaxConns, err = envInt("O1_DB_MAX_CONNS", 10); err != nil {
       return cfg, err
   }
   ```
3. In `validate()`, after the `WorkerConcurrency` check, add:
   ```go
   if cfg.DBMaxConns < 1 || cfg.DBMaxConns > 100 {
       return fmt.Errorf("invalid O1_DB_MAX_CONNS: must be between 1 and 100, got %d", cfg.DBMaxConns)
   }
   ```

- [ ] **Step 4: Thread the value into the pool**

In `backend/internal/db/db.go`, change the signature to `func Open(ctx context.Context, databaseURL string, maxConns int) (*pgxpool.Pool, error)`, replace `cfg.MaxConns = 10` with `cfg.MaxConns = int32(maxConns)`, and update the doc comment to say the caller chooses the cap.

In `backend/internal/app/app.go`, change the call to `db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns)`.

- [ ] **Step 5: Run the tests and the build**

Run: `cd backend && go test -short ./... && go build ./...`
Expected: PASS, no build errors.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/config/config.go backend/internal/config/config_test.go backend/internal/db/db.go backend/internal/app/app.go
git commit -m "feat: make the database pool size configurable"
```

---

### Task 2: Remove the bundled database from the production stack

**Files:**
- Modify: `deploy/production/compose.yaml`
- Modify: `deploy/production/env.example`
- Modify: `deploy/scripts/test-compose-config.sh`

**Interfaces:**
- Consumes: `O1_DATABASE_URL` and `O1_DB_MAX_CONNS` names from Task 1.
- Produces: production Compose environment contract used by Tasks 3 and 4 in the runbook.

- [ ] **Step 1: Add the failing Compose assertions**

In `deploy/scripts/test-compose-config.sh`, after the "registry images come from the fixture" check, insert:

```bash
# The production stack has no bundled database and takes the DSN from the env.
env -u O1_COMPOSE_PROJECT -u O1_BACKEND_IMAGE -u O1_WEB_IMAGE \
  docker compose --env-file "$env_file" \
  --env-file "$images_file" \
  -f "$compose_file" config --format json \
  | python3 -c '
import json, sys
c = json.load(sys.stdin)
assert "postgres" not in c["services"], sorted(c["services"])
assert "postgres-data" not in c.get("volumes", {}), sorted(c.get("volumes", {}))
dsns = {n: c["services"][n]["environment"]["O1_DATABASE_URL"]
        for n in ("api", "scheduler", "worker")}
assert len(set(dsns.values())) == 1, dsns
dsn = next(iter(dsns.values()))
assert dsn.startswith("postgres://"), dsn
assert "sslmode=disable" not in dsn, dsn
assert c["services"]["api"]["environment"]["O1_DB_MAX_CONNS"] == "10"
'
check "no bundled database; DSN and pool size come from the environment"

# Rendering without a DSN fails before any container starts.
tmp_env=$(mktemp)
trap 'rm -f "$tmp_env"' EXIT
grep -v '^O1_DATABASE_URL=' "$env_file" > "$tmp_env"
if env -u O1_COMPOSE_PROJECT -u O1_BACKEND_IMAGE -u O1_WEB_IMAGE \
  docker compose --env-file "$tmp_env" --env-file "$images_file" \
  -f "$compose_file" config --quiet >/dev/null 2>&1; then
  fail "compose rendered without O1_DATABASE_URL"
fi
check "compose refuses to render without O1_DATABASE_URL"
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash deploy/scripts/test-compose-config.sh`
Expected: FAIL — a `postgres` service still exists (or `KeyError: 'O1_DATABASE_URL'`).

- [ ] **Step 3: Edit the Compose file**

In `deploy/production/compose.yaml`:

1. Delete the entire `postgres:` service block (image, environment, healthcheck, restart, volume).
2. Delete `depends_on: postgres: {condition: service_healthy}` from `api`, `scheduler`, and `worker`; keep `web`'s `depends_on: [api]`.
3. In the `&backend-env` anchor, replace the hardcoded URL line with:
   ```yaml
       O1_DATABASE_URL: "${O1_DATABASE_URL:?set O1_DATABASE_URL in .env}"
       O1_DB_MAX_CONNS: ${O1_DB_MAX_CONNS:-10}
   ```
4. Delete `postgres-data:` from the top-level `volumes:` block, leaving `caddy_data`, `caddy_config`, and `alloy-data`.
5. Update the header comment to say the stack runs no database; `O1_DATABASE_URL` must point at a managed PostgreSQL service.

- [ ] **Step 4: Edit the environment example**

In `deploy/production/env.example`, delete the `POSTGRES_PASSWORD` block and its comment. Add under `# --- Required ---`:

```dotenv
# Connection string for the managed PostgreSQL database. Use the provider's
# direct (session) endpoint, never a transaction pooler: startup migrations use
# advisory locks and the River queue needs session semantics. TLS is required;
# use sslmode=verify-full when the provider has a publicly trusted CA, or
# sslmode=require plus sslrootcert for a private CA. Percent-encode special
# characters in the password.
O1_DATABASE_URL=postgres://o1:replace-with-password@db.example.com:5432/o1?sslmode=verify-full
```

Add under `# --- Optional ---`:

```dotenv
# Maximum PostgreSQL connections per process (1-100). api, scheduler, and
# worker each open their own pool, so the provider tier must allow at least
# 3 x O1_DB_MAX_CONNS connections plus migration headroom.
# O1_DB_MAX_CONNS=10
```

- [ ] **Step 5: Run the tests to verify they pass**

Run:
```bash
bash deploy/scripts/test-compose-config.sh
bash deploy/scripts/test-bootstrap.sh
```
Expected: both print `PASS`.

- [ ] **Step 6: Commit**

```bash
git add deploy/production/compose.yaml deploy/production/env.example deploy/scripts/test-compose-config.sh
git commit -m "feat: run production against a managed database"
```

---

### Task 3: Rewrite the production runbook for managed PostgreSQL

**Files:**
- Modify: `deploy/production/README.md`

**Interfaces:**
- Consumes: the environment contract from Task 2 (`O1_DATABASE_URL`, `O1_DB_MAX_CONNS`, no database container).
- Produces: operator procedure used by Task 4's CI/CD cross-references.

- [ ] **Step 1: Provisioning section**

First fix the document intro: the file-purpose table row for `compose.yaml` must no longer say it runs PostgreSQL, and the paragraph below the table must say the stack has no database container and takes `O1_DATABASE_URL` from the environment.

Insert a new `## 3. Managed PostgreSQL` before the current `## 3. Generate secrets`, and renumber every later section (`Generate secrets` becomes 4, `Configure the environment` 5, `First deploy` 6, `Verify the deployment` 7, `Logs and day-to-day operations` 8, `Update and restart` 9, `Rollback` 10, `Troubleshooting` 11, `Optional metrics with Grafana Alloy` 12, `Security notes` 13). Update the internal link in Troubleshooting from `[step 3](#3-generate-secrets)` to `[step 4](#4-generate-secrets)`.

The new section covers, in this order:

1. Requirements: PostgreSQL 16-compatible; a database named `o1`; a role that owns that database (migrations install `citext`, create the River schema, and manage partitions); TLS enforced; a **direct/session** endpoint, not a transaction pooler; a region near the deploy host because every probe writes over this link.
2. Suggested providers: OCI Database with PostgreSQL in the VPS region, Neon (direct endpoint), Supabase (direct connection, not the `6543` pooler), RDS, Aiven.
3. DSN format: `postgres://USER:PASSWORD@HOST:5432/o1?sslmode=verify-full`; percent-encode `@ : / ? #` in the password; for a private CA use `sslmode=require` with `sslrootcert`, or the provider's documented CA material.
4. Connection budget: `api`, `scheduler`, and `worker` each hold up to `O1_DB_MAX_CONNS` (default 10), so the tier must allow at least 30 connections plus migration headroom.
5. Backups: backups and point-in-time recovery are the provider's responsibility; confirm the retention window before enabling retention deletion, and note that rollups are aggregates, not a backup of raw probe rows.
6. One-time cutover subsection (numbered):
   - Stop writers: `docker compose --env-file deploy/production/.env -f deploy/production/compose.yaml stop api scheduler worker`.
   - Dump from the still-running container: `docker compose ... exec -T postgres pg_dump -U o1 -d o1 -Fc > o1-pre-managed.dump`.
   - Restore into the managed database: `pg_restore --no-owner --no-privileges -d "$O1_DATABASE_URL" o1-pre-managed.dump`.
   - Spot-check `SELECT count(*) FROM monitors;`, `SELECT count(*) FROM users;`, and `SELECT count(*) FROM probe_results;` on both sides.
   - Set `O1_DATABASE_URL` in `deploy/production/.env` (or `/opt/o1/.env` on a registry host), deploy the managed-aware release, and confirm `/readyz`.
   - Keep the dump and the old `postgres-data` volume until the managed database has served production for a full retention cycle; then remove the volume manually and prune pre-cutover releases (see `../CICD.md`).

- [ ] **Step 2: Update secrets and environment sections**

In the renumbered `Generate secrets` section: remove `POSTGRES_PASSWORD` from the `openssl` lines and its three bullets, and delete the entire "placeholder password already initialized the volume" recovery block (`ALTER ROLE ...`, `$EDITOR`, `--force-recreate`). Replace it with one sentence: the database password belongs to the managed service and is reset from the provider console, not from the host.

In the renumbered `Configure the environment` section: replace the `POSTGRES_PASSWORD` bullet with `O1_DATABASE_URL` (the managed DSN) and add `O1_DB_MAX_CONNS` as optional. Keep the `docker compose ... config >/dev/null` sanity check.

- [ ] **Step 3: Update deploy, verification, and operations text**

- `First deploy`: replace the "PostgreSQL starts and becomes healthy" bullet with a line that the API, scheduler, and worker connect to the managed database and run migrations there; add a `docker compose ... ps` check showing no `postgres` container.
- `Verify the deployment`: no change to `/healthz`, `/readyz`, `/metrics`, or TLS checks.
- `Retention`: replace both `docker compose ... exec postgres psql` invocations with `psql "$O1_DATABASE_URL" -c "..."` (note that the host needs a `psql` client or the operator uses the provider console).
- `Update and restart`: no change.
- `Rollback`: add a paragraph that image/config rollback does not move data back, and that rolling back to a release whose Compose file still contains the bundled `postgres` service starts an empty local database; operators must roll back only to managed-aware releases and keep the pre-cutover dump until the managed database is accepted.
- `Troubleshooting`: replace the "Database authentication fails" row with a DSN/TLS row (sslmode, private CA, direct vs pooler endpoint, percent-encoded password, provider IP allowlist/firewall, `O1_DB_MAX_CONNS` vs tier limit), and extend the "API exits at startup with a config error" row with `O1_DB_MAX_CONNS` range errors.
- `Security notes`: remove the `postgres-data` bullet; state that application data lives in the managed database, the host keeps only Caddy certificate volumes, and no database port is published.
- `Plan boundaries`: replace the "Backups are not implemented" bullet with the managed-database position (provider backups/PITR, retention still deletes raw rows, rollups are not a backup).
- `Prerequisites`: add a managed PostgreSQL instance to the list.

- [ ] **Step 4: Verify the document**

Run:
```bash
rg -n "POSTGRES_PASSWORD|postgres-data|exec postgres|exec -T postgres pg_dump" deploy/production/README.md
```
Expected: the only match is the intentional `exec -T postgres pg_dump` cutover step and the manual volume-removal sentence; no `POSTGRES_PASSWORD` or `exec postgres psql`.

Run:
```bash
rg -n "Managed PostgreSQL|O1_DATABASE_URL|O1_DB_MAX_CONNS|direct \(session\)|pre-cutover|pg_restore" deploy/production/README.md
```
Expected: every term appears.

- [ ] **Step 5: Commit**

```bash
git add deploy/production/README.md
git commit -m "docs: document managed PostgreSQL for production"
```

---

### Task 4: Update the CI/CD guide

**Files:**
- Modify: `deploy/CICD.md`

**Interfaces:**
- Consumes: Task 2 environment contract and Task 3 runbook sections.
- Produces: consistent operator guidance across both documents.

- [ ] **Step 1: Update required environment values**

In §5 step 5, replace `POSTGRES_PASSWORD` with `O1_DATABASE_URL` in the required list and add `O1_DB_MAX_CONNS` to the optional list. Note that the database is external to the host and is not part of a release bundle.

- [ ] **Step 2: Update host and rollback guidance**

- §5 step 3: keep the port warning, but state that `5432` has no local listener because the database is managed.
- §7 "Roll back to the previous release": add the constraint that a release built before the managed database cutover carries a Compose file with a local `postgres` service, so rolling back to it starts an empty database; prune pre-cutover releases after the first verified managed deploy, and keep the pre-cutover dump until the managed database is accepted.
- §8 Migrations: add that migrations now run against the managed database over the network, that a failed migration still fails `/readyz`, and that provider PITR — not release rollback — is the data recovery path.
- §10 Troubleshooting: update the "`deploy: missing /opt/o1/.env`" row to name `O1_DATABASE_URL` instead of `POSTGRES_PASSWORD`, and add a row for managed database connectivity (DNS/TLS/direct endpoint/connection cap/provider firewall).

- [ ] **Step 3: Verify the document**

Run:
```bash
rg -n "POSTGRES_PASSWORD|postgres-data|exec postgres" deploy/CICD.md
```
Expected: no matches.

Run:
```bash
rg -n "O1_DATABASE_URL|O1_DB_MAX_CONNS|pre-cutover|point-in-time|direct" deploy/CICD.md
```
Expected: `O1_DATABASE_URL` and `pre-cutover` appear; add the others where the edit calls for them.

- [ ] **Step 4: Commit**

```bash
git add deploy/CICD.md
git commit -m "docs: update CI/CD guidance for managed PostgreSQL"
```

---

### Task 5: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Run the script and Go gates**

```bash
bash -n deploy/scripts/*.sh
bash deploy/scripts/test-compose-config.sh
bash deploy/scripts/test-bootstrap.sh
bash deploy/scripts/test-deploy.sh
cd backend && go vet ./... && go test -short ./...
```

Expected: all pass.

- [ ] **Step 2: Sweep for stale production references**

```bash
rg -n "POSTGRES_PASSWORD|postgres-data|exec postgres" deploy README.md
```
Expected: no matches (`compose.yaml` and `.env.example` at the repository root are the unchanged development stack and are out of scope).

- [ ] **Step 3: Confirm the rendered production config**

```bash
docker compose --env-file deploy/production/env.example \
  -f deploy/production/compose.yaml config --format json |
  python3 -c 'import json,sys; c=json.load(sys.stdin); print(sorted(c["services"]), sorted(c.get("volumes", {})))'
```
Expected: services without `postgres`; volumes without `postgres-data`.

- [ ] **Step 4: Commit any verification fixups**

Only if Steps 1–3 required changes; otherwise stop. Use a `fix:` or `test:` prefix describing the correction.

---

## Post-Implementation (operator, not part of this plan's code)

The live cutover on a real host — provisioning the managed service, running the dump/restore, deploying the managed-aware release, and pruning pre-cutover releases — is an operator procedure documented in `deploy/production/README.md`. It is not exercised by this plan's tests; the live acceptance checklist in the design doc covers it.

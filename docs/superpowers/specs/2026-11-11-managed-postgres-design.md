# Managed PostgreSQL for the Production Stack — Design

**Status:** Draft for approval
**Date:** 2026-11-11
**Depends on:** Plan 10 (production go-live stack), Plan 11 (CI/CD registry deploys), Plan 13 (rollups and retention)
**Scope owner:** o1 uptime monitor

## 1. Intent

Move the production database off the VPS and onto a managed PostgreSQL
service. Today PostgreSQL is the largest single consumer of RAM and disk I/O on
the host, it is the only component whose failure mode is data loss, and Plan 12
(backups/restore) is skipped. A managed database removes that load from the
host, gives the deployment real backups and point-in-time recovery, and makes
`docker compose down -v` harmless to application data.

The development stack (`compose.yaml` at the repository root, `.env.example`) is
not part of this change and keeps its containerised PostgreSQL.

## 2. Current State

- `deploy/production/compose.yaml` defines a `postgres` service with a
  `postgres-data` volume, a `pg_isready` healthcheck, and no published port.
  `api`, `scheduler`, and `worker` declare `depends_on: postgres: service_healthy`.
- The same file hardcodes the connection string:
  `O1_DATABASE_URL: postgres://o1:${POSTGRES_PASSWORD}@postgres:5432/o1?sslmode=disable`.
- `backend/internal/config/config.go` already reads `O1_DATABASE_URL` and fails
  startup when it is empty. No application code assumes a local database.
- `backend/internal/db/db.go` hardcodes `cfg.MaxConns = 10`. Three long-running
  processes (`api`, `scheduler`, `worker`) therefore hold up to 30 connections,
  plus short-lived migration connections.
- Startup migrations use a PostgreSQL advisory lock; River creates and owns its
  own schema; `00001_auth.sql` installs `citext`; the scheduler creates and
  drops `probe_results` partitions. All of this works for a database owner but
  not for a bare non-owner role, and none of it is safe through a transaction
  pooler.
- `deploy/production/env.example` contains `POSTGRES_PASSWORD`. `bootstrap-host.sh`
  seeds `/opt/o1/.env` from that file verbatim, and `test-compose-config.sh`
  renders the production Compose file with it.
- `deploy/production/README.md` documents generating `POSTGRES_PASSWORD`,
  recovering a mismatched password with `docker compose exec postgres psql`,
  querying state through the `postgres` container, and warns that application
  data lives in `postgres-data`.
- `deploy/CICD.md` lists `POSTGRES_PASSWORD` as a required `/opt/o1/.env` value
  and mentions `5432` among ports that must stay closed.

## 3. Goals

1. The production Compose file has no database service and no database volume.
2. `O1_DATABASE_URL` comes from the environment in every service, with TLS
   (`sslmode=verify-full` when the provider has a publicly trusted CA,
   `sslmode=require` otherwise).
3. Connection count per process is configurable (`O1_DB_MAX_CONNS`, default 10)
   so a small managed tier can be used without editing code.
4. The runbook documents provisioning, the one-time data cutover, verification,
   backup/PITR expectations, and the new rollback constraint.
5. The deploy test suite still passes and asserts the new shape.

## 4. Non-Goals

- No Terraform/provider automation, no secrets manager, no read replicas, no
  automatic failover, no connection pooler support.
- No schema, query, or migration changes.
- No change to the development stack or to `validate-release.sh`/`deploy.sh`
  release semantics.
- No support for PgBouncer transaction pooling.

## 5. Approaches

**A. Remove PostgreSQL from the production stack (chosen).** The production
Compose file stops defining a database at all; the managed DSN is required
input. One configuration path, no wasted RAM on a disabled container, and the
test suite keeps a single shape to assert. The cost is that the production
stack no longer has a bundled local database for offline recovery; the
development stack still provides one, and the managed service provides
backups.

**B. Managed by default, local PostgreSQL as an opt-in profile.** Keeps a
`localdb` profile with the old service. Rejected: `depends_on` with profiled
services is fragile in Compose, it doubles the configurations and tests to
maintain, and the bundled database is precisely the load this change exists to
remove.

## 6. Design

### 6.1 `deploy/production/compose.yaml`

- Delete the `postgres` service, its healthcheck, and the `postgres-data`
  volume.
- Delete every `depends_on: postgres` entry. `web` keeps
  `depends_on: [api]`.
- In the shared backend environment anchor, replace the hardcoded URL with
  `O1_DATABASE_URL: "${O1_DATABASE_URL:?set O1_DATABASE_URL in .env}"`.
- Add `O1_DB_MAX_CONNS: ${O1_DB_MAX_CONNS:-10}` to the same anchor.
- Update the file header comment: the stack no longer runs a database; the DSN
  points at the managed service.

`deploy/production/compose.build.yaml` is unchanged (it adds build contexts to
`api`/`web` only).

### 6.2 `deploy/production/env.example`

- Remove `POSTGRES_PASSWORD` and its explanation.
- Add, under Required:
  `O1_DATABASE_URL=postgres://USER:PASSWORD@HOST:5432/o1?sslmode=verify-full`
  with a comment that it must be a direct/session endpoint (not a transaction
  pooler), that special characters in the password must be percent-encoded, and
  that providers with a private CA use `sslmode=require` plus `sslrootcert` (or
  the provider's documented CA mount).
- Add, under Optional: `O1_DB_MAX_CONNS=10` with a note that three processes
  connect and that the tier's connection limit must be at least
  `3 × O1_DB_MAX_CONNS` plus migration headroom.

### 6.3 Backend configuration and pool sizing

- `config.Config` gains `DBMaxConns int`.
- `config.Load` parses `O1_DB_MAX_CONNS` (default `10`) and rejects values
  outside `1–100` or non-numeric input with a clear error.
- `db.Open(ctx, databaseURL)` becomes `db.Open(ctx, databaseURL, maxConns int)`
  and sets `cfg.MaxConns = maxConns`. The single caller in
  `backend/internal/app/app.go` passes `cfg.DBMaxConns`.
- No other query, migration, or River code changes.

### 6.4 Documentation

`deploy/production/README.md`:

- Prerequisites and §3 "Generate secrets": remove `POSTGRES_PASSWORD`; state
  that the database is managed and the only application secrets are
  `O1_SECRET_KEY` and `O1_ENCRYPTION_KEY`.
- New section "Managed PostgreSQL" covering: provisioning requirements (a
  database named `o1` and a role that owns it, `citext` available, TLS
  enforced, direct endpoint), the DSN format, region co-location with the VPS,
  connection budget, and that backups/PITR are the provider's responsibility.
- New subsection "One-time cutover from the bundled database": freeze writes
  (`docker compose stop api scheduler worker`), `pg_dump -Fc` from the
  running container, `pg_restore --no-owner` into the managed database, spot-check
  row counts, update `O1_DATABASE_URL`, deploy the new release, verify
  `/readyz`, then remove the old volume only after a verified backup exists.
- §4 "Configure the environment": replace the `POSTGRES_PASSWORD` bullet with
  `O1_DATABASE_URL`.
- §6/§7/§10: replace `docker compose exec postgres psql` instructions with
  `psql "$O1_DATABASE_URL"` (or the provider console); rewrite the
  "database authentication fails" troubleshooting row for TLS/DSN errors.
- §9 "Rollback" and §12 "Security notes": note that application data is no
  longer in `postgres-data`, and add the rollback constraint below.
- Keep the retention section, but its database queries must run against the
  managed DSN.

`deploy/CICD.md`:

- Host setup required values: `O1_DATABASE_URL` replaces `POSTGRES_PASSWORD`;
  mention `O1_DB_MAX_CONNS`.
- Troubleshooting row "missing .env": `O1_DATABASE_URL` replaces
  `POSTGRES_PASSWORD`.
- Add a release note that a rollback to a release whose Compose file still
  contains the bundled `postgres` service would start an empty local database.
  Operators must prune pre-cutover releases after the cutover is verified, and
  keep the pre-cutover `pg_dump` until the new database is accepted.

### 6.5 Tests

- `deploy/scripts/test-compose-config.sh`: keep the existing checks and add
  assertions that the rendered config has no `postgres` service and no
  `postgres-data` volume, that `api`, `scheduler`, and `worker` all receive the
  `O1_DATABASE_URL` from the env file, and that `O1_DB_MAX_CONNS` defaults to
  `10`.
- `backend/internal/config/config_test.go`: default value, explicit value, and
  rejection of `0`, `101`, and non-numeric input.
- `backend/internal/db`: no test exists for `Open`; the compile-time signature
  change is covered by `go test -short ./...`.

## 7. Rollback Constraint

Image and config rollback cannot undo a database move. After the cutover, a
release built before this change carries a Compose file that starts an empty
local PostgreSQL and points the app at it. The runbook therefore requires:

1. Keep a `pg_dump` of the old database until the managed database is accepted.
2. After the first successful managed deploy, prune the `current`/`previous`
   releases that predate the change before relying on `rollback.sh`.
3. Roll back to a managed-aware release, or forward, never to a pre-cutover
   bundle.

## 8. Decisions Left to the Builder

- Exact wording and ordering inside the runbook sections.
- Whether to ship a CA-mount example for providers with a private CA, or only
  the `sslmode=require` note.
- Whether to add a `make` target for the cutover dump/restore; not required.

## 9. Acceptance

- `docker compose --env-file deploy/production/env.example -f deploy/production/compose.yaml config`
  renders with no `postgres` service and no database volume.
- `bash deploy/scripts/test-compose-config.sh` passes.
- `cd backend && go test -short ./...` passes, including the new config tests.
- No production doc tells an operator to generate `POSTGRES_PASSWORD`, exec into
  a `postgres` container, or expect application data in `postgres-data`.
- Live acceptance on a provisioned host: deploy a managed-aware release, confirm
  `/readyz` is healthy against the managed DSN, confirm no `postgres` container
  or `postgres-data` volume exists, and confirm the pre-cutover dump restores
  into a scratch database.

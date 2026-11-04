# Plan 2 — Monitors, Probing & Abuse Controls (Backend) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the backend monitoring loop: tenant-scoped HTTP monitor CRUD, SSRF-safe probing through River, an up/down state machine, dashboard read APIs, quotas, and minimal admin suspension controls.

**Architecture:** New Go packages (`secretbox`, `probe`, `monitors`, `jobs`, `scheduler`, `worker`, `admin`, `app`) on top of the Plan 1 API. Postgres remains the source of truth; monitor secrets are AES-GCM encrypted; due probes are enqueued by a `scheduler` binary into River and executed by a `worker` binary. All three binaries share startup, migrations, health, and shutdown code.

**Tech Stack:** Go 1.26, `net/http` ServeMux, pgx v5, sqlc 1.31, goose, River v0.51.0 (`riverpgxv5`, `rivermigrate`), PostgreSQL 16, testcontainers-go, `crypto/hkdf`, `crypto/aes`/`cipher`, `x/time/rate`.

**Spec:** `docs/superpowers/specs/2026-11-04-plan2-monitors-probing-design.md` (approved). Parent spec: `docs/superpowers/specs/2026-11-04-uptime-monitor-design.md`.

## Global Constraints

- Go module `o1`, Go 1.26. Run Go commands from `backend/`.
- PostgreSQL 16; migrations are append-only goose files in `backend/internal/db/migrations/`; sqlc queries in `backend/internal/db/queries/`; run `sqlc generate` after changing either.
- API prefix `/api/v1`; errors are `application/problem+json` (`httpx.Problem`) with `code`, `detail`, `fields`.
- Every workspace-scoped query includes `workspace_id`; non-members get `404`, never `403`.
- Roles: `viewer` reads; `member`, `admin`, `owner` mutate monitors. Admin endpoints require `users.is_admin`.
- Primary keys are UUIDv7 generated in Go. Timestamps are `timestamptz`.
- Monitor headers and bodies are encrypted with AES-256-GCM; plaintext must never reach the database, logs, or API responses. Header values are always masked as `"***"` in responses.
- SSRF policy: `http`/`https` only, ports from `O1_PROBE_ALLOWED_PORTS` (default 80/443), no userinfo, no fragments, re-check resolved IP at dial time, max 5 redirects, reject redirects to private targets, 1 MiB body drain cap, no proxy, no user `Host` header.
- Probe failures are recorded as `down` results, not job failures. Only DB/decryption/internal errors return an error from the worker.
- Probe job args never contain secrets; only `monitor_id` and `scheduled_for`.
- Probe results are monthly partitions; create `[current month − 1, current month + 2]` at startup and daily.
- M1 quotas: 5 monitors/workspace (per-workspace override column), 10 monitor creations/hour/workspace, minimum interval 60s.
- Config env prefix `O1_`. Production requires `O1_ENCRYPTION_KEY`; dev/test may derive it from `O1_SECRET_KEY`.
- No secrets, header values, request bodies, or unredacted query strings in logs.
- Unit tests must pass with `go test -short ./...` and no Docker. Integration tests use `testutil.NewPostgres`.
- Before every commit: `gofmt -w`, `go vet ./...`, `golangci-lint run`, and the task's tests.
- Commit after every task.

## Review Focus

Failure modes most likely to hurt real users, each pinned by a test:

1. **SSRF bypass through redirects, DNS rebinding, IPv4-mapped IPv6, or a blocked port** — probing must refuse before connecting (Task 3).
2. **Cross-tenant monitor access via a guessed UUID** — every monitor and admin lookup must return `404`/empty for another tenant (Tasks 4 and 9).
3. **Quota and creation-rate races under concurrent requests** — two simultaneous creates must not exceed 5 monitors or 10/hour (Task 4).
4. **At-least-once probe delivery double-counting state** — replaying a job must not insert a second result or advance counters twice (Task 8).
5. **Secret leakage** — encrypted columns, masked API values, redacted admin URLs, and no plaintext in logs (Tasks 4, 5, and 9).

---

### Task 1: Configuration and Secret Box

**Files:**
- Modify: `backend/internal/config/config.go`
- Modify: `backend/internal/config/config_test.go`
- Create: `backend/internal/secretbox/secretbox.go`
- Create: `backend/internal/secretbox/secretbox_test.go`
- Modify: `backend/internal/testutil/auth.go` (`TestConfig`)
- Modify: `.env.example`

**Interfaces:**
- Produces:
  - `config.Config` gains: `EncryptionKey []byte`, `MonitorMinInterval time.Duration`, `MonitorMaxInterval time.Duration`, `MonitorCreateLimit int`, `ProbeAllowedPorts []int`, `ProbeMaxRedirects int`, `ProbeMaxBodyBytes int64`, `ProbeGlobalRPS float64`, `SchedulerTick time.Duration`, `SchedulerBatch int`, `WorkerConcurrency int`, `SchedulerHealthAddr string`, `WorkerHealthAddr string`, `BootstrapAdminEmail string`.
  - `secretbox.New(key []byte) (*Box, error)`, `(*Box).Seal(plaintext []byte) (string, error)`, `(*Box).Open(value string) ([]byte, error)`, `secretbox.ErrInvalidCiphertext`.
  - `testutil.TestConfig` returns a 32-byte `EncryptionKey`.

- [ ] **Step 1: Write the failing config tests**

Append to `backend/internal/config/config_test.go`:

```go
func TestLoad_DefaultsForMonitoring(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_ENCRYPTION_KEY", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MonitorCreateLimit != 10 {
		t.Fatalf("create limit = %d, want 10", cfg.MonitorCreateLimit)
	}
	if cfg.MonitorMinInterval != 60*time.Second || cfg.MonitorMaxInterval != 24*time.Hour {
		t.Fatalf("intervals = %v/%v", cfg.MonitorMinInterval, cfg.MonitorMaxInterval)
	}
	if len(cfg.ProbeAllowedPorts) != 2 || cfg.ProbeAllowedPorts[0] != 80 || cfg.ProbeAllowedPorts[1] != 443 {
		t.Fatalf("ports = %v, want [80 443]", cfg.ProbeAllowedPorts)
	}
	if cfg.ProbeMaxRedirects != 5 || cfg.ProbeMaxBodyBytes != 1<<20 || cfg.ProbeGlobalRPS != 50 {
		t.Fatalf("probe config = %d/%d/%v", cfg.ProbeMaxRedirects, cfg.ProbeMaxBodyBytes, cfg.ProbeGlobalRPS)
	}
	if cfg.SchedulerTick != 10*time.Second || cfg.SchedulerBatch != 500 || cfg.WorkerConcurrency != 50 {
		t.Fatalf("runtime config = %v/%d/%d", cfg.SchedulerTick, cfg.SchedulerBatch, cfg.WorkerConcurrency)
	}
	if len(cfg.EncryptionKey) != 32 {
		t.Fatalf("derived encryption key length = %d, want 32", len(cfg.EncryptionKey))
	}
}

func TestLoad_ProductionRequiresEncryptionKey(t *testing.T) {
	t.Setenv("O1_ENV", "production")
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_ENCRYPTION_KEY", "")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() succeeded without O1_ENCRYPTION_KEY in production")
	}
}

func TestLoad_RejectsInvalidProbePort(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_PROBE_ALLOWED_PORTS", "80,70000")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() accepted an out-of-range port")
	}
}
```

- [ ] **Step 2: Write the failing secretbox tests**

`backend/internal/secretbox/secretbox_test.go`:

```go
package secretbox_test

import (
	"bytes"
	"strings"
	"testing"

	"o1/internal/secretbox"
)

func TestBox_RoundTripAndTamper(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sealed, err := box.Seal([]byte(`{"Authorization":"Bearer x"}`))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !strings.HasPrefix(sealed, "v1:") || strings.Contains(sealed, "Bearer") {
		t.Fatalf("sealed value = %q", sealed)
	}
	got, err := box.Open(sealed)
	if err != nil || string(got) != `{"Authorization":"Bearer x"}` {
		t.Fatalf("Open = %q, %v", got, err)
	}

	tampered := sealed[:len(sealed)-2] + "AA"
	if _, err := box.Open(tampered); err == nil {
		t.Fatal("Open accepted a tampered ciphertext")
	}
	if _, err := box.Open("v2:abc"); err == nil {
		t.Fatal("Open accepted an unknown key version")
	}
}

func TestBox_NonceIsFresh(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		sealed, err := box.Seal([]byte("same"))
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		if seen[sealed] {
			t.Fatal("Seal reused a nonce")
		}
		seen[sealed] = true
	}
}
```

- [ ] **Step 3: Run the tests and verify they fail**

Run: `go test ./internal/config ./internal/secretbox -run 'TestLoad_DefaultsForMonitoring|TestBox_' -count=1`
Expected: FAIL (unknown fields / package does not exist).

- [ ] **Step 4: Implement config fields and the secret box**

In `config.Load`, after `SecretKey` handling:

- Parse integers with a helper (`envInt`) and durations with `time.ParseDuration`.
- Defaults exactly as the tests assert.
- Parse `O1_PROBE_ALLOWED_PORTS` as comma-separated integers; reject empty, non-numeric, or outside 1–65535.
- Validate `MonitorMinInterval <= MonitorMaxInterval`, create limit/concurrency/batch > 0, redirects 0–10, body bytes > 0, RPS > 0.
- The per-workspace monitor limit is the `workspaces.monitor_limit` column (DB default 5); there is no config default because the column is authoritative.
- Encryption key: if `O1_ENCRYPTION_KEY` is set, base64-decode and require 32 bytes. If unset: in `production` return an error; otherwise derive with `hkdf.Key(sha256.New, cfg.SecretKey, nil, "o1:tenant-secrets:v1", 32)`.

`secretbox` implementation:

```go
package secretbox

type Box struct{ aead cipher.AEAD }

func New(key []byte) (*Box, error) // require len(key) == 32
func (b *Box) Seal(plaintext []byte) (string, error) // "" for empty input
func (b *Box) Open(value string) ([]byte, error)      // "" -> empty
```

Format: `"v1:" + base64.RawURLEncoding.EncodeToString(nonce || ciphertext)`. Use `cipher.NewGCM`, 12-byte nonce from `crypto/rand`, and a distinct `ErrInvalidCiphertext` for malformed/unknown-version/auth failures.

Update `testutil.TestConfig` to set `EncryptionKey: bytes.Repeat([]byte{2}, 32)`. Add the new optional vars to `.env.example` with defaults.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/config ./internal/secretbox -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/config backend/internal/secretbox backend/internal/testutil/auth.go .env.example
git commit -m "feat: add monitoring config and secret encryption"
```

---

### Task 2: Schema, Partitions, and sqlc Queries

**Files:**
- Create: `backend/internal/db/migrations/00003_monitors.sql`
- Create: `backend/internal/db/queries/monitors.sql`
- Create: `backend/internal/db/queries/admin.sql`
- Regenerate: `backend/internal/db/sqlc/models.go`, `backend/internal/db/sqlc/monitors.sql.go`, `backend/internal/db/sqlc/admin.sql.go`
- Create: `backend/internal/db/partitions.go`
- Create: `backend/internal/db/partitions_test.go`
- Modify: `backend/internal/db/migrate.go`
- Modify: `backend/internal/db/queries_test.go`

**Interfaces:**
- Produces:
  - Schema exactly as design §4 (`monitors`, `monitor_state`, partitioned `probe_results`, `security_events`, `users.is_admin`, workspace suspension/quota columns).
  - `db.EnsureProbePartitions(ctx context.Context, pool *pgxpool.Pool, now time.Time) error` creates month partitions for `now−1` through `now+2` idempotently.
  - `db.Migrate` holds `pg_advisory_lock(0x6f310002)` for the whole migration and calls `EnsureProbePartitions` after goose.
  - sqlc queries listed in Steps 2 and 3, with generated param/row structs.

- [ ] **Step 1: Write the migration**

Create `00003_monitors.sql` with the Up/Down sections from design §4. Include `CREATE EXTENSION IF NOT EXISTS pgcrypto`? No — UUIDs come from Go. Order: alter `users`, alter `workspaces`, create `monitors`, `monitor_state`, `probe_results`, `security_events`. Add the indexes exactly as designed. Down drops in reverse order and drops the added columns.

- [ ] **Step 2: Write monitor queries**

`backend/internal/db/queries/monitors.sql` must define these `-- name:` queries (use `sqlc.narg` / `sqlc.arg` for named cursor params):

| Query | Kind | Notes |
|---|---|---|
| `LockWorkspace` | `:one` | `SELECT id, monitor_limit, suspended_at FROM workspaces WHERE id = $1 FOR UPDATE` |
| `CountMonitors` | `:one` | count by `workspace_id` |
| `CountMonitorCreationsSince` | `:one` | count by `workspace_id` and `created_at >= $2` |
| `OldestMonitorCreationSince` | `:one` | `ORDER BY created_at LIMIT 1` |
| `CreateMonitor` | `:one` | all columns; `RETURNING *` |
| `GetMonitor` | `:one` | `WHERE id = $1 AND workspace_id = $2` |
| `ListMonitors` | `:many` | join `monitor_state`; keyset on `(created_at, id) < (after_time, after_id)` when provided; `ORDER BY created_at DESC, id DESC`; `LIMIT` |
| `ListMonitorSummaries` | `:many` | `WHERE monitor_id = ANY(sqlc.arg(monitor_ids)::uuid[]) AND checked_at >= $2 GROUP BY monitor_id`; returns counts, avg, `percentile_cont(0.95) WITHIN GROUP`, min, max |
| `UpdateMonitor` | `:one` | full update of editable columns + `updated_at = now()`; `WHERE id AND workspace_id` |
| `DeleteMonitor` | `:execrows` | `WHERE id AND workspace_id` |
| `SetMonitorPaused` | `:one` | update `paused`, `updated_at`; `RETURNING *` |
| `CreateMonitorState` | `:exec` | insert `monitor_id` only |
| `GetMonitorState` | `:one` | by `monitor_id` |
| `GetMonitorForProbe` | `:one` | `m.*` + `ms.status`, state counters, `w.suspended_at` |
| `ListDueMonitors` | `:many` | design §8.3 query; `FOR UPDATE OF ms SKIP LOCKED`; takes `now` and `batch` |
| `SetNextCheckAt` | `:exec` | `next_check_at = $2, updated_at = now()` |
| `ResumeMonitor` | `:one` | `paused = false` and `next_check_at = now()` on `monitors`/`monitor_state` respectively — use two statements (`:exec` each) |
| `InsertProbeResult` | `:execrows` | `ON CONFLICT (monitor_id, scheduled_for, checked_at) DO NOTHING`; rows affected tells the worker whether to advance state |
| `ProbeResultExists` | `:one` | boolean `EXISTS` |
| `UpdateMonitorState` | `:exec` | status, counters, last check fields |
| `ListProbeResults` | `:many` | window start + keyset on `(checked_at, id)`; newest first; limit |
| `MonitorSummary` | `:one` | counts, uptime inputs, avg/p95/min/max for one monitor and window |

For `ListMonitors`, the cursor predicate is:

```sql
AND (
  sqlc.narg('after_time')::timestamptz IS NULL
  OR (m.created_at, m.id) < (sqlc.narg('after_time')::timestamptz, sqlc.arg('after_id')::uuid)
)
```

- [ ] **Step 3: Write admin queries and regenerate**

`backend/internal/db/queries/admin.sql`:

| Query | Kind | Notes |
|---|---|---|
| `GetUserIsAdmin` | `:one` | `SELECT is_admin FROM users WHERE id = $1` (or extend the existing user lookup) |
| `ListWorkspacesForAdmin` | `:many` | filter `query` (name/slug, case-insensitive) and optional `suspended`, keyset pagination, member/monitor counts |
| `SuspendWorkspace` | `:exec` | set `suspended_at`, `suspended_by`, `suspended_reason` |
| `UnsuspendWorkspace` | `:exec` | clear the three columns |
| `ListMonitorsForAdmin` | `:many` | join workspaces; optional `workspace_id`, optional name/URL query, keyset on `(created_at, id)`; newest first; limit |
| `CreateSecurityEvent` | `:exec` | insert event |
| `ListSecurityEvents` | `:many` | optional `kind`, optional `workspace_id`, keyset on `(created_at, id)`; newest first, limit |

Run: `sqlc generate` (from `backend/`). Confirm the generated structs expose `Users.IsAdmin`, `Workspaces.SuspendedAt/SuspendedBy/SuspendedReason/MonitorLimit`, and the monitor/state/result models.

- [ ] **Step 4: Write the failing partition test**

`backend/internal/db/partitions_test.go`:

```go
func TestEnsureProbePartitions_CreatesWindowAndIsIdempotent(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()

	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	if err := db.EnsureProbePartitions(ctx, pool, now); err != nil {
		t.Fatalf("EnsureProbePartitions: %v", err)
	}
	if err := db.EnsureProbePartitions(ctx, pool, now); err != nil {
		t.Fatalf("second EnsureProbePartitions: %v", err)
	}

	var names []string
	if err := pool.QueryRow(ctx, `
		SELECT array_agg(relname::text ORDER BY relname)
		FROM pg_class
		WHERE relname LIKE 'probe_results_2026_%'`).Scan(&names); err != nil {
		t.Fatalf("list partitions: %v", err)
	}
	want := []string{"probe_results_2026_10", "probe_results_2026_11", "probe_results_2026_12", "probe_results_2027_01"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Fatalf("partitions = %v, want %v", names, want)
	}
}
```

- [ ] **Step 5: Run the test and verify it fails**

Run: `go test ./internal/db -run TestEnsureProbePartitions -count=1`
Expected: FAIL (`db.EnsureProbePartitions` undefined).

- [ ] **Step 6: Implement partition maintenance and the migration lock**

`partitions.go`:

```go
func EnsureProbePartitions(ctx context.Context, pool *pgxpool.Pool, now time.Time) error
```

For each month from `now.AddDate(0, -1, 0)` through `now.AddDate(0, 2, 0)`: compute the first day in UTC, format the name `probe_results_YYYY_MM`, and execute

```sql
CREATE TABLE IF NOT EXISTS <name> PARTITION OF probe_results
FOR VALUES FROM ('<first>') TO ('<next>')
```

Identifiers come only from `time.Format`; values use a `time.Time` parameter for the bounds (`FROM ($1) TO ($2)`) so no string interpolation of user input occurs.

Serialize concurrent maintenance across processes: acquire `pg_advisory_lock(0x6f310003)` on a dedicated pooled connection for the duration of the function, and release it on a background context in a defer.

`migrate.go`: acquire a pooled connection, `SELECT pg_advisory_lock(0x6f310002)`, defer unlock on a fresh background context, run goose (existing code), then `EnsureProbePartitions(ctx, pool, time.Now().UTC())`. Keep the existing `sql.DB` migration handle.

- [ ] **Step 7: Add query tests and run the DB tests**

In `queries_test.go`, add one integration test that creates a workspace + monitor + state, inserts two results, and asserts `ListMonitors` keyset pagination, `MonitorSummary` counts, and `InsertProbeResult` conflict behavior.

Run: `go test ./internal/db -count=1`
Expected: PASS (requires Docker).

- [ ] **Step 8: Commit**

```bash
git add backend/internal/db backend/internal/db/sqlc
git commit -m "feat: add monitor schema, partitions, and queries"
```

---

### Task 3: SSRF Guard and Safe HTTP Prober

**Files:**
- Create: `backend/internal/probe/policy.go`
- Create: `backend/internal/probe/status.go`
- Create: `backend/internal/probe/probe.go`
- Create: `backend/internal/probe/policy_test.go`
- Create: `backend/internal/probe/status_test.go`
- Create: `backend/internal/probe/probe_test.go`
- Modify: `backend/go.mod` (no new dependency needed; stdlib only)

**Interfaces:**
- Produces:
  - `probe.Policy{AllowedPorts map[int]struct{}, MaxRedirects int, MaxResponseBody int64, ConnectTimeout, TLSHandshakeTimeout time.Duration, AllowPrivateNetworks bool}`.
  - `probe.PolicyFromConfig(cfg config.Config) Policy`.
  - `probe.Resolver` interface (`LookupIPAddr`); `probe.NewGuard(p Policy) *Guard`, `probe.NewGuardWithResolver(p Policy, r Resolver) *Guard`.
  - `(*Guard).ValidateURL(ctx context.Context, raw string) (*url.URL, error)`; errors wrap `probe.ErrBlocked` or `probe.ErrInvalidURL`.
  - `probe.ParseExpectedStatus(s string) (ExpectedStatus, error)`, `(ExpectedStatus).Matches(code int) bool`, `(ExpectedStatus).String() string`.
  - `probe.ValidateHeaders(h map[string]string) error`, `probe.ValidateBody(method string, body *string, contentType *string) error`.
  - `probe.Request{URL, Method string, Headers map[string]string, Body []byte, ContentType string, Expected ExpectedStatus, Timeout time.Duration}`.
  - `probe.Result{Up bool, StatusCode int, LatencyMS int, ErrorClass, ErrorMessage string}`.
  - `probe.NewProber(g *Guard) *Prober`, `(*Prober).Probe(ctx context.Context, req Request) Result`.

- [ ] **Step 1: Write the failing classification and URL tests**

`policy_test.go` (package `probe`):

```go
func TestValidateURL_BlocksDangerousTargets(t *testing.T) {
	g := newTestGuard(t, Policy{
		AllowedPorts: map[int]struct{}{80: {}, 443: {}},
		MaxRedirects: 5, MaxResponseBody: 1 << 20,
	})
	cases := []string{
		"http://127.0.0.1/", "http://10.0.0.1/", "http://169.254.1.1/",
		"http://[::1]/", "http://[fc00::1]/", "http://[fe80::1]/",
		"http://[::ffff:127.0.0.1]/", "http://100.64.0.1/",
		"http://192.0.0.1/", "http://198.18.0.1/", "http://224.0.0.1/",
		"http://0.0.0.0/", "http://example.com:8080/",
		"ftp://example.com/", "http://user:pass@example.com/",
		"http://example.com/#fragment",
	}
	for _, raw := range cases {
		if _, err := g.ValidateURL(context.Background(), raw); err == nil {
			t.Errorf("ValidateURL(%q) succeeded, want rejection", raw)
		}
	}
}

func TestValidateURL_AcceptsPublicHTTP(t *testing.T) {
	g := newTestGuard(t, Policy{AllowedPorts: map[int]struct{}{80: {}, 443: {}}, MaxRedirects: 5})
	for _, raw := range []string{"http://example.com/health", "https://example.com:443/x?y=1", "https://93.184.216.34/"} {
		if _, err := g.ValidateURL(context.Background(), raw); err != nil {
			t.Errorf("ValidateURL(%q): %v", raw, err)
		}
	}
}
```

`newTestGuard(t, p)` builds `NewGuardWithResolver(p, staticResolver{ip: "93.184.216.34"})`; `staticResolver` is a test type implementing `LookupIPAddr`. Include one case where the resolver returns `10.0.0.5` and assert `errors.Is(err, ErrBlocked)`.

- [ ] **Step 2: Write the failing status and prober tests**

`status_test.go`:

```go
func TestExpectedStatus(t *testing.T) {
	cases := []struct {
		spec string
		code int
		ok   bool
	}{
		{"2xx", 204, true},
		{"2xx", 301, false},
		{"200,204", 204, true},
		{"200-299", 250, true},
		{"200-299", 300, false},
	}
	for _, tc := range cases {
		st, err := ParseExpectedStatus(tc.spec)
		if err != nil {
			t.Fatalf("ParseExpectedStatus(%q): %v", tc.spec, err)
		}
		if got := st.Matches(tc.code); got != tc.ok {
			t.Errorf("%q matches %d = %v, want %v", tc.spec, tc.code, got, tc.ok)
		}
	}
	for _, bad := range []string{"", "abc", "99", "600", "200-", "200,", "2xx,,3xx"} {
		if _, err := ParseExpectedStatus(bad); err == nil {
			t.Errorf("ParseExpectedStatus(%q) succeeded", bad)
		}
	}
}
```

`probe_test.go` (package `probe`, loopback allowed for httptest):

```go
func TestProber_SuccessStatusAndTimeout(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Host") != "" {
			t.Error("probe sent a user Host header")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ok.Close()

	p := newPermissiveProber(t, ok.URL) // AllowPrivateNetworks=true, port from the URL
	res := p.Probe(context.Background(), Request{
		URL: ok.URL, Method: http.MethodGet,
		Expected: mustStatus(t, "2xx"), Timeout: 2 * time.Second,
	})
	if !res.Up || res.StatusCode != http.StatusNoContent || res.ErrorClass != "" {
		t.Fatalf("success result = %+v", res)
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer slow.Close()
	res = p.Probe(context.Background(), Request{
		URL: slow.URL, Method: http.MethodGet,
		Expected: mustStatus(t, "2xx"), Timeout: 50 * time.Millisecond,
	})
	if res.Up || res.ErrorClass != "timeout" {
		t.Fatalf("timeout result = %+v", res)
	}
}
```

`newPermissiveProber(t, rawURL)` parses the port from `rawURL`, builds `Policy{AllowedPorts: {port}, MaxRedirects: 5, MaxResponseBody: 1<<20, AllowPrivateNetworks: true}`, and returns `NewProber(NewGuardWithResolver(policy, staticResolver{ip: "127.0.0.1"}))`.

Also add a redirect test: a chain of 6 servers returns `res.ErrorClass == "redirect"`; and a unit test on the unexported redirect policy asserting a `http://127.0.0.1/` target is rejected when private networks are disallowed.

- [ ] **Step 3: Run the tests and verify they fail**

Run: `go test ./internal/probe -count=1`
Expected: FAIL (package/APIs undefined).

- [ ] **Step 4: Implement the guard**

Implement:

- `IsPublicIP(ip net.IP) bool` with `Unmap()`, the stdlib predicates, and the explicit CIDR list from design §6.3.
- `ValidateURL`: parse, enforce scheme/userinfo/fragment/host; port check; if resolution succeeds, reject when **any** returned address is non-public (unless `AllowPrivateNetworks`). Wrap failures with `ErrInvalidURL` or `ErrBlocked`.
- Tests build a permissive `Policy` with `AllowPrivateNetworks: true` and the `httptest` URL's port (helper `policyForURL` in `probe_test.go`). Do not export a test-only constructor from the production package.

`PolicyFromConfig` maps `cfg.ProbeAllowedPorts`, `cfg.ProbeMaxRedirects`, `cfg.ProbeMaxBodyBytes`; connect/TLS timeouts default to 5s and 5s. It never enables `AllowPrivateNetworks`.

At write time, a DNS resolution failure is not a rejection: `ValidateURL` accepts the URL when the resolver errors, and the probe later records a DNS failure. It rejects only when resolution succeeds and at least one address is non-public.

- [ ] **Step 5: Implement header/body validation and the prober**

- Header validation: max 20; names non-empty and matching a local RFC 7230 token check (ASCII letters, digits, and ``!#$%&'*+-.^_`|~``), case-insensitive blocklist (`Host`, `Content-Length`, `Connection`, `Transfer-Encoding`, `Proxy-Connection`, `Upgrade`, `TE`), value ≤ 1024 and no CR/LF, total ≤ 8 KiB.
- Body validation: ≤ 16 KiB; reject bodies for `GET`/`HEAD`; `ContentType` ≤ 200 chars and only with a body.
- Prober: single shared `http.Transport` with `Proxy: nil`, `DisableKeepAlives: true`, `DialContext` doing the resolve → classify → `net.Dialer.DialContext` to the numeric IP, `TLSHandshakeTimeout`, and `ResponseHeaderTimeout = req.Timeout`. Set `Client.CheckRedirect` to the policy in Step 4 of design §6.5.
- Build the request with `http.NewRequestWithContext` and a `context.WithTimeout(ctx, req.Timeout)`; copy allowed headers; set `User-Agent: o1-probe/1.0`; set the body.
- On response: record `time.Since(start)` to headers, drain up to `MaxResponseBody`, close, compare status.
- Classify errors: `context.DeadlineExceeded`/`net.Error.Timeout()` → `timeout`; `ErrBlocked` → `ssrf_blocked`; `x509.UnknownAuthorityError`/`tls.RecordHeaderError` → `tls`; `*net.DNSError` → `dns`; redirect policy error → `redirect`; otherwise `connection`. Sanitize the message (no URL query, no headers, no body).

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/probe -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/probe backend/go.mod backend/go.sum
git commit -m "feat: add SSRF guard and safe HTTP prober"
```

---

### Task 4: Monitor Domain Service (CRUD, Quota, Rate Limit, Encryption)

**Files:**
- Create: `backend/internal/monitors/errors.go`
- Create: `backend/internal/monitors/validate.go`
- Create: `backend/internal/monitors/service.go`
- Create: `backend/internal/monitors/service_test.go`
- Create: `backend/internal/monitors/validate_test.go`
- Modify: `backend/internal/db/queries/monitors.sql` + regenerate (only if a missing query surfaces)

**Interfaces:**
- Produces:
  - `monitors.Service` with:
    - `NewService(pool *pgxpool.Pool, q *sqlc.Queries, ws *workspaces.Service, box *secretbox.Box, guard *probe.Guard, cfg config.Config) *Service`
    - `Create(ctx, actorID, workspaceID uuid.UUID, in Input) (Monitor, error)`
    - `List(ctx, actorID, workspaceID uuid.UUID, cursor *Cursor, limit int) ([]Monitor, error)`
    - `Get(ctx, actorID, workspaceID, monitorID uuid.UUID) (Monitor, error)`
    - `Update(ctx, actorID, workspaceID, monitorID uuid.UUID, in UpdateInput) (Monitor, error)`
    - `Delete(ctx, actorID, workspaceID, monitorID uuid.UUID) error`
    - `SetPaused(ctx, actorID, workspaceID, monitorID uuid.UUID, paused bool) (Monitor, error)`
  - `monitors.Input`, `monitors.UpdateInput`, `monitors.Monitor`, `monitors.State`, `monitors.Summary`, `monitors.Cursor`.
  - `monitors.RedactURL(raw string) string` (parse, clear `RawQuery`/`Fragment`; usable by the admin package).
  - Sentinels: `ErrValidation`, `ErrNotFound` (= `workspaces.ErrNotFound`), `ErrForbidden` (= `workspaces.ErrForbidden`), `ErrSuspended`, `ErrQuotaExceeded`.
  - `*ValidationError{Fields map[string]string}` implementing `Is(ErrValidation)`; `*RateLimitError{RetryAfter time.Duration}`.

- [ ] **Step 1: Write the failing validation tests**

`validate_test.go` is in package `monitors` so it can call the unexported `validate`; `service_test.go` is in `monitors_test`. Define a local `staticResolver` implementing `probe.Resolver`.

```go
func TestValidateInput(t *testing.T) {
	base := Input{
		Name: "API", URL: "https://example.com/health", Method: "GET",
		ExpectedStatus: "2xx", IntervalSeconds: 60, TimeoutSeconds: 10,
		ConfirmationThreshold: 2, RecoveryThreshold: 2,
	}
	guard := probe.NewGuardWithResolver(probe.Policy{
		AllowedPorts: map[int]struct{}{80: {}, 443: {}},
		MaxRedirects: 5, MaxResponseBody: 1 << 20,
	}, staticResolver{ip: "93.184.216.34"})
	if _, err := validate(base, guard); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}

	cases := []struct {
		name  string
		mutate func(*Input)
		field string
	}{
		{"empty name", func(in *Input) { in.Name = "  " }, "name"},
		{"bad method", func(in *Input) { in.Method = "TRACE" }, "method"},
		{"short interval", func(in *Input) { in.IntervalSeconds = 30 }, "interval_seconds"},
		{"long timeout", func(in *Input) { in.TimeoutSeconds = 60 }, "timeout_seconds"},
		{"bad threshold", func(in *Input) { in.ConfirmationThreshold = 6 }, "confirmation_threshold"},
		{"bad expected", func(in *Input) { in.ExpectedStatus = "nope" }, "expected_status"},
		{"host header", func(in *Input) { in.Headers = map[string]string{"Host": "evil"} }, "headers"},
		{"get body", func(in *Input) { b := "x"; in.Body = &b }, "body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.mutate(&in)
			_, err := validate(in, guard)
			var verr *ValidationError
			if !errors.As(err, &verr) || verr.Fields[tc.field] == "" {
				t.Fatalf("err = %v, want field %q", err, tc.field)
			}
		})
	}
}
```

- [ ] **Step 2: Write the failing service integration tests**

`service_test.go` (package `monitors_test`):

```go
func TestCreate_Quota(t *testing.T) {
	env := newTestEnv(t) // default MonitorCreateLimit 10, so this test exercises quota only
	ws := env.createWorkspace(t, "owner@example.com")

	for i := 0; i < 5; i++ {
		if _, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(i)); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(5)); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("6th create err = %v, want ErrQuotaExceeded", err)
	}

	// Deleting a monitor frees a quota slot.
	if err := env.svc.Delete(context.Background(), env.ownerID, ws, env.monitorID(t, ws)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(6)); err != nil {
		t.Fatalf("create after delete: %v", err)
	}
}

func TestCreate_RateLimit(t *testing.T) {
	env := newTestEnvWithCreateLimit(t, 3) // quota 5, so the rate limit fires first
	ws := env.createWorkspace(t, "owner@example.com")

	for i := 0; i < 3; i++ {
		if _, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(i)); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	_, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(3))
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter <= 0 {
		t.Fatalf("4th create err = %v, want RateLimitError with RetryAfter", err)
	}
}

func TestCreate_EncryptsSecretsAtRest(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "owner@example.com")
	in := validInput(0)
	in.Headers = map[string]string{"Authorization": "Bearer super-secret"}
	body := `{"token":"super-secret"}`
	in.Body = &body
	in.Method = "POST"
	created, err := env.svc.Create(context.Background(), env.ownerID, ws, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var headers, rawBody string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT headers_enc, body_enc FROM monitors WHERE id = $1`, created.ID).Scan(&headers, &rawBody); err != nil {
		t.Fatalf("read encrypted columns: %v", err)
	}
	if strings.Contains(headers, "super-secret") || strings.Contains(rawBody, "super-secret") {
		t.Fatal("plaintext secret found at rest")
	}
}
```

Also add: cross-tenant `Get`/`Update`/`Delete` returns `ErrNotFound`; viewer mutation returns `ErrForbidden`; suspended workspace mutations return `ErrSuspended`; and a concurrency test that starts 10 goroutines creating monitors in one workspace and asserts exactly 5 succeed and 5 return `ErrQuotaExceeded` (the workspace `FOR UPDATE` lock must serialize them).

- [ ] **Step 3: Run the tests and verify they fail**

Run: `go test ./internal/monitors -run 'TestValidateInput|TestCreate_' -count=1`
Expected: FAIL (package/APIs undefined).

- [ ] **Step 4: Implement validation**

`validate.go`:

- Trim name; 1–100 characters.
- Signature: `func validate(in Input, guard *probe.Guard) (Input, error)`; call `guard.ValidateURL`; map failures to `url`.
- Method allowlist; uppercase.
- `probe.ValidateHeaders`, `probe.ValidateBody`; map to `headers`/`body`/`content_type`.
- `probe.ParseExpectedStatus`; store canonical `.String()`.
- Interval within `[cfg.MonitorMinInterval, cfg.MonitorMaxInterval]`; timeout 1–30; thresholds 1–5.
- Return `normalized Input` and `*ValidationError` with one message per invalid field.

`UpdateInput` merges over the stored `Input` before validation; changing URL revalidates. `Headers == nil` means unchanged; `Body == nil` means unchanged; `Body` pointing at `""` means clear.

- [ ] **Step 5: Implement the service**

- `Create`: authorize `workspaces.RoleMember`; validate; begin tx; `LockWorkspace`; reject `suspended_at != nil`; count monitors → quota; count creations since `now−1h` → `RateLimitError{RetryAfter: oldest+1h−now}`; encrypt headers/body; insert monitor + state; insert `monitor_created` event with redacted URL; commit.
- `List`: authorize viewer; clamp limit 1–100 (default 50); query `ListMonitors`; batch `ListMonitorSummaries` for the page; decrypt headers only to produce the header-name map with `"***"` values.
- `Get`: authorize viewer; `GetMonitor` + `GetMonitorState` + `MonitorSummary` for 24h.
- `Update`: authorize member; load monitor; reject suspended; merge + validate; re-encrypt; full `UpdateMonitor`; `SetNextCheckAt(now)`; return `Get`.
- `Delete`: authorize member; reject suspended; `DeleteMonitor`; 0 rows → `ErrNotFound`.
- `SetPaused`: authorize member; reject suspended; `SetMonitorPaused`; when resuming call `ResumeMonitor`; return `Get`.
- Every mutation records a security event where design §4.5 requires one.
- Use `redactURL` (parse, clear `RawQuery`/`Fragment`, return string) for logs/events.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/monitors -count=1`
Expected: PASS (requires Docker).

- [ ] **Step 7: Commit**

```bash
git add backend/internal/monitors backend/internal/db/queries backend/internal/db/sqlc
git commit -m "feat: add monitor service with quotas and encryption"
```

---

### Task 5: Monitor HTTP API (CRUD, Pause/Resume, List)

**Files:**
- Create: `backend/internal/httpx/cursor.go`
- Create: `backend/internal/httpx/cursor_test.go`
- Create: `backend/internal/monitors/handlers.go`
- Create: `backend/internal/monitors/handlers_test.go`
- Modify: `backend/internal/httpx/problem.go` (new codes)
- Modify: `backend/internal/api/router.go` (routes + dependency)
- Modify: `backend/internal/api/integration_test.go` or create `backend/internal/api/monitors_integration_test.go`
- Modify: `backend/internal/testutil/auth.go` (session helper)

**Interfaces:**
- Produces:
  - `httpx.PageCursor{Time time.Time, ID uuid.UUID}`, `httpx.EncodeCursor(PageCursor) string`, `httpx.DecodeCursor(string) (PageCursor, error)`.
  - New codes: `CodeNotFound = "not_found"`, `CodeWorkspaceSuspended = "workspace_suspended"`, `CodeQuotaExceeded = "quota_exceeded"`, `CodeMonitorRateLimited = "monitor_rate_limited"`, `CodeInternalError = "internal_error"`.
  - Handlers: `monitors.CreateHandler`, `ListHandler`, `GetHandler`, `UpdateHandler`, `DeleteHandler`, `PauseHandler`, `ResumeHandler`.
  - `api.Dependencies.MonitorService *monitors.Service` (nil builds one from Pool/Config/WorkspaceService).
  - `testutil.SessionToken(t testing.TB, pool *pgxpool.Pool, cfg config.Config, email, password string) string`.

- [ ] **Step 1: Write the failing cursor test**

```go
func TestCursorRoundTripAndRejectsGarbage(t *testing.T) {
	c := httpx.PageCursor{Time: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC), ID: uuid.MustParse("0190a000-0000-7000-8000-000000000001")}
	enc := httpx.EncodeCursor(c)
	if strings.Contains(enc, "2026") {
		t.Fatalf("cursor leaks plaintext: %q", enc)
	}
	got, err := httpx.DecodeCursor(enc)
	if err != nil || !got.Time.Equal(c.Time) || got.ID != c.ID {
		t.Fatalf("DecodeCursor = %+v, %v", got, err)
	}
	if _, err := httpx.DecodeCursor("not-base64!"); err == nil {
		t.Fatal("DecodeCursor accepted garbage")
	}
}
```

- [ ] **Step 2: Write the failing handler/integration test**

`backend/internal/api/monitors_integration_test.go` (package `api_test`):

```go
func TestAPI_MonitorCRUDAndTenantIsolation(t *testing.T) {
	env := newMonitorAPIEnv(t) // Postgres + router + monitors service with permit-all resolver
	alice := env.login(t, "alice@example.com")
	bob := env.login(t, "bob@example.com")
	ws := alice.createWorkspace(t, "Alice")

	resp, body := alice.post("/api/v1/workspaces/"+ws+"/monitors", map[string]any{
		"name": "API", "url": "https://example.com/health",
		"method": "GET", "expected_status": "2xx",
		"interval_seconds": 60, "timeout_seconds": 10,
		"confirmation_threshold": 2, "recovery_threshold": 2,
	})
	requireStatus(t, resp, http.StatusCreated)
	created := decodeJSON[monitorJSON](t, body)
	if created.Status != "unknown" || len(created.Headers) != 0 {
		t.Fatalf("created = %+v", created)
	}

	// Another tenant cannot see or mutate it.
	resp, _ = bob.do("GET", "/api/v1/workspaces/"+ws+"/monitors/"+created.ID, nil)
	requireStatus(t, resp, http.StatusNotFound)
	resp, _ = bob.do("DELETE", "/api/v1/workspaces/"+ws+"/monitors/"+created.ID, nil)
	requireStatus(t, resp, http.StatusNotFound)

	// Pause and resume are idempotent.
	for _, action := range []string{"pause", "pause"} {
		resp, body = alice.post("/api/v1/workspaces/"+ws+"/monitors/"+created.ID+"/"+action, nil)
		requireStatus(t, resp, http.StatusOK)
		if got := decodeJSON[monitorJSON](t, body); !got.Paused || got.Status != "paused" {
			t.Fatalf("%s = %+v", action, got)
		}
	}
	resp, body = alice.post("/api/v1/workspaces/"+ws+"/monitors/"+created.ID+"/resume", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeJSON[monitorJSON](t, body); got.Paused || got.Status == "paused" {
		t.Fatalf("resume = %+v", got)
	}

	// List is tenant-scoped.
	resp, body = alice.do("GET", "/api/v1/workspaces/"+ws+"/monitors", nil)
	requireStatus(t, resp, http.StatusOK)
	list := decodeJSON[struct {
		Monitors []monitorJSON `json:"monitors"`
	}](t, body)
	if len(list.Monitors) != 1 || list.Monitors[0].ID != created.ID {
		t.Fatalf("list = %+v", list)
	}
}
```

Define `monitorJSON` in the test file with the fields asserted. Add tests for: masked header values (`"***"`), `400` validation with `fields.url`, `409 quota_exceeded`, `429 monitor_rate_limited` with `Retry-After`, and viewer mutation `403`.

- [ ] **Step 3: Run the tests and verify they fail**

Run: `go test ./internal/httpx ./internal/api -run 'TestCursor|TestAPI_MonitorCRUD' -count=1`
Expected: FAIL (cursor and routes undefined).

- [ ] **Step 4: Implement cursor and handlers**

- Cursor: JSON `{"t":RFC3339Nano,"id":uuid}` → base64url. Reject malformed UUID/time.
- Request structs exactly as design §10.3; `DisallowUnknownFields`.
- Response: design §10.1; `headers` is a name→`"***"` map or omitted when none; `last_check` and `summary_24h` nullable.
- Error mapping: `ValidationError` → 400 with fields; `ErrNotFound` → 404; `ErrForbidden` → 403; `ErrSuspended` → 403 workspace_suspended; `ErrQuotaExceeded` → 409; `RateLimitError` → 429 + `Retry-After`; default → 500.
- `limit` parsing: default 50, clamp 1–100; invalid `limit`/`cursor` → 400 with `fields`.
- Decode an incoming cursor with `httpx.DecodeCursor` and pass `&monitors.Cursor{Time: c.Time, ID: c.ID}` to the service; encode the last row back with `httpx.EncodeCursor`.
- Workspace/monitor IDs parsed with `uuid.Parse`; invalid → 400.

Router wiring: mount the seven routes under `requireSession`. Build `box`, `guard`, and `monitors.NewService` when `deps.MonitorService == nil`, `deps.Pool != nil`, and `len(cfg.EncryptionKey) == 32`; otherwise leave the service nil so DB-less router tests that never reach these handlers still construct successfully. Add the new monitor routes to `TestRouter_ProtectedRoutesRequireSession`.

`testutil.SessionToken`: build `auth.NewService(pool, sqlc.New(pool), testutil.NewFakeSender(), cfg)`, call `Login`, return the raw token. Test clients set the `o1_session` cookie.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/httpx ./internal/monitors ./internal/api -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/httpx backend/internal/monitors backend/internal/api backend/internal/testutil
git commit -m "feat: expose monitor CRUD API"
```

---

### Task 6: Results and Summary APIs

**Files:**
- Modify: `backend/internal/monitors/service.go`
- Create: `backend/internal/monitors/results.go`
- Create: `backend/internal/monitors/results_test.go`
- Create: `backend/internal/monitors/handlers_results.go`
- Modify: `backend/internal/monitors/handlers_test.go`
- Modify: `backend/internal/api/router.go` (two routes)

**Interfaces:**
- Produces:
  - `monitors.ProbeResult{ID uuid.UUID, CheckedAt time.Time, ScheduledFor time.Time, Status string, StatusCode *int, LatencyMS int, Error *string}`.
  - `(*Service).Results(ctx, actorID, workspaceID, monitorID uuid.UUID, window time.Duration, cursor *Cursor, limit int) ([]ProbeResult, error)`.
  - `(*Service).Summary(ctx, actorID, workspaceID, monitorID uuid.UUID, window time.Duration) (Summary, error)` with `Summary.Window string`.
  - Handlers `ResultsHandler`, `SummaryHandler`.
  - Accepted windows: `1h`, `6h`, `24h`, `7d` (default 24h); anything else is a validation error on `window`.

- [ ] **Step 1: Write the failing summary test**

`results_test.go`:

```go
func TestSummary_ExcludesNoDataAndComputesP95(t *testing.T) {
	env := newTestEnv(t)
	ws, id := env.createMonitor(t, "owner@example.com")
	now := time.Now().UTC()
	for i, latency := range []int{100, 200, 300, 400} {
		env.insertResult(t, id, ws, now.Add(-time.Duration(i)*time.Minute), "up", 200, latency)
	}
	env.insertResult(t, id, ws, now.Add(-10*time.Minute), "down", 500, 50)

	sum, err := env.svc.Summary(context.Background(), env.ownerID, ws, id, 24*time.Hour)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum.Checks != 5 || sum.Up != 4 || sum.Down != 1 {
		t.Fatalf("counts = %+v", sum)
	}
	if sum.Uptime == nil || math.Abs(*sum.Uptime-0.8) > 1e-9 {
		t.Fatalf("uptime = %v", sum.Uptime)
	}
	if sum.P95LatencyMS <= 300 || sum.P95LatencyMS > 400 {
		t.Fatalf("p95 = %v", sum.P95LatencyMS)
	}

	other := env.createMonitor(t, "owner@example.com")
	empty, err := env.svc.Summary(context.Background(), env.ownerID, ws, other, 24*time.Hour)
	if err != nil || empty.Checks != 0 || empty.Uptime != nil {
		t.Fatalf("empty summary = %+v, %v", empty, err)
	}
}
```

- [ ] **Step 2: Write the failing handler test**

Add to `handlers_test.go`: `GET .../results?window=24h&limit=2` returns two newest results and a `next_cursor`; following the cursor returns the rest; `window=30d` returns `400` with `fields.window`; another tenant gets `404`.

- [ ] **Step 3: Run the tests and verify they fail**

Run: `go test ./internal/monitors -run 'TestSummary|TestResultsHandler' -count=1`
Expected: FAIL (APIs undefined).

- [ ] **Step 4: Implement**

- `Summary`: authorize viewer; map the window; call `MonitorSummary`; `Uptime = up/checks` or nil when checks == 0; include `Window`.
- `Results`: authorize viewer; map window to `checked_at >= now-window`; clamp limit 1–500 (default 100); use `ListProbeResults` with the cursor.
- Convert `httpx.PageCursor` from the request into `monitors.Cursor{Time, ID}` before calling the service; encode the returned last row for `next_cursor`.
- Handlers emit:

```json
{"results":[...],"next_cursor":"..."}
```

and the summary object from design §10.2. `next_cursor` is present only when a full page was returned.
- Mount `GET .../results` and `GET .../summary`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/monitors ./internal/api -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/monitors backend/internal/api
git commit -m "feat: add monitor results and summary APIs"
```

---

### Task 7: River, Shared Runtime, and Scheduler Binary

**Files:**
- Modify: `backend/go.mod`, `backend/go.sum` (`github.com/riverqueue/river v0.51.0`, `river/riverdriver/riverpgxv5`, `river/rivermigrate`)
- Create: `backend/internal/jobs/probe.go`
- Create: `backend/internal/db/river.go`
- Modify: `backend/internal/db/migrate.go`
- Create: `backend/internal/app/app.go`
- Create: `backend/internal/app/health.go`
- Modify: `backend/cmd/api/main.go` (use `internal/app`)
- Create: `backend/internal/scheduler/scheduler.go`
- Create: `backend/internal/scheduler/scheduler_test.go`
- Create: `backend/cmd/scheduler/main.go`

**Interfaces:**
- Produces:
  - `jobs.ProbeArgs{MonitorID uuid.UUID; ScheduledFor time.Time}` with `Kind() == "probe"`.
  - `db.MigrateRiver(ctx context.Context, pool *pgxpool.Pool) error`.
  - `app.Start(ctx context.Context) (*app.Runtime, error)` where `Runtime` has `Config config.Config`, `Logger *slog.Logger`, `Pool *pgxpool.Pool`, `Close()`; it loads config, builds the logger, opens the pool, and runs `db.Migrate`.
  - `app.StartHealthServer(addr string, logger *slog.Logger, ready func(context.Context) error) *http.Server`.
  - `scheduler.New(pool *pgxpool.Pool, q *sqlc.Queries, riverClient *river.Client[pgx.Tx], cfg config.Config, logger *slog.Logger) *Scheduler`; `(*Scheduler).Tick(ctx) (int, error)`; `(*Scheduler).Run(ctx) error`.
  - `cmd/scheduler` entrypoint.

- [ ] **Step 1: Add River and write the failing scheduler test**

`scheduler_test.go` lives in package `scheduler` (not `scheduler_test`) so it can set the unexported `now`/`jitter` fields for deterministic ticks.

```go
func TestTick_EnqueuesOnlyDueActiveMonitors(t *testing.T) {
	env := newSchedulerEnv(t) // pool, migrated schema, inserted monitors
	env.addMonitor(t, "due", 60, false, false, time.Now().Add(-time.Minute))
	env.addMonitor(t, "future", 60, false, false, time.Now().Add(time.Hour))
	env.addMonitor(t, "paused", 60, true, false, time.Now().Add(-time.Minute))
	env.addMonitor(t, "suspended", 60, false, true, time.Now().Add(-time.Minute))

	n, err := env.sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n != 1 {
		t.Fatalf("Tick enqueued %d, want 1", n)
	}

	var kind string
	var count int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*), min(kind) FROM river_job WHERE kind = 'probe'`).Scan(&count, &kind); err != nil {
		t.Fatalf("read river_job: %v", err)
	}
	if count != 1 || kind != "probe" {
		t.Fatalf("jobs = %d/%q", count, kind)
	}

	var next time.Time
	if err := env.pool.QueryRow(context.Background(),
		`SELECT ms.next_check_at FROM monitor_state ms JOIN monitors m ON m.id = ms.monitor_id WHERE m.name = 'due'`).
		Scan(&next); err != nil {
		t.Fatalf("read next_check_at: %v", err)
	}
	if !next.After(time.Now().Add(50 * time.Second)) {
		t.Fatalf("next_check_at = %v, want about one interval ahead", next)
	}
}

func TestTick_DuplicateUniqueJobIsIgnored(t *testing.T) {
	env := newSchedulerEnv(t)
	env.addMonitor(t, "due", 60, false, false, time.Now().Add(-time.Minute))
	env.now = func() time.Time { return time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC) }
	env.sched.now = env.now
	env.sched.jitter = func(time.Duration) time.Duration { return 0 }

	if _, err := env.sched.Tick(context.Background()); err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	// Rewind the schedule to simulate a crash after insert but before commit visibility.
	if _, err := env.pool.Exec(context.Background(), `UPDATE monitor_state SET next_check_at = now() - interval '1 minute'`); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if _, err := env.sched.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	var count int
	if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'probe'`).Scan(&count); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if count != 1 {
		t.Fatalf("job count = %d, want 1", count)
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `go test ./internal/scheduler -run TestTick -count=1`
Expected: FAIL (package/APIs undefined).

- [ ] **Step 3: Implement River migration and shared runtime**

- `db.MigrateRiver`: `rivermigrate.New(riverpgxv5.New(pool), nil)` then `Migrate(ctx, rivermigrate.DirectionUp, &rivermigrate.MigrateOpts{})`. Call it from `db.Migrate` after goose, still inside the advisory lock.
- `app.Start`: load config, `logging.New(os.Stdout, cfg.Env)`, `db.Open`, `db.Migrate`, return runtime. `Close` closes the pool.
- `app.StartHealthServer`: mux with `GET /healthz` → 200 `{"status":"ok"}` and `GET /readyz` → call `ready(ctx)` with a 2s timeout, 503 `not_ready` problem on error. Return a configured `*http.Server`; the caller starts it and shuts it down.
- Refactor `cmd/api/main.go` to use `app.Start`, keep the existing server/graceful shutdown, and close the runtime after the server stops.

- [ ] **Step 4: Implement jobs and the scheduler tick**

`jobs/probe.go`:

```go
type ProbeArgs struct {
	MonitorID    uuid.UUID `json:"monitor_id" river:"unique"`
	ScheduledFor time.Time `json:"scheduled_for" river:"unique"`
}

func (ProbeArgs) Kind() string { return "probe" }
```

`Tick`:

1. Begin tx; `q.WithTx(tx).ListDueMonitors(ctx, now, batch)`.
2. For each row: `scheduled := now.Unix()`; compute `next := now.Add(interval + jitter)`; `riverClient.InsertTx(ctx, tx, jobs.ProbeArgs{MonitorID: id, ScheduledFor: time.Unix(scheduled, 0).UTC()}, opts)`; ignore `UniqueSkippedAsDuplicate`; `SetNextCheckAt`.
3. Commit.
4. Return the number of due rows.

`InsertOpts{MaxAttempts: 3, Queue: "probe", UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateScheduled, rivertype.JobStateRunning, rivertype.JobStatePending}}}`.

`Scheduler` fields include injectable `now func() time.Time` and `jitter func(time.Duration) time.Duration` (default random 0–10%). `Run` uses a `time.Ticker`; each tick is synchronous; returns nil on `ctx.Done()`.

- [ ] **Step 5: Implement `cmd/scheduler`**

- `app.Start`, build River client, create scheduler, start health server, run `sched.Run(ctx)`.
- On SIGINT/SIGTERM: cancel the run context, shut down health with a 5s timeout, close the runtime.
- Log `scheduler tick` with `enqueued` and `duration` at debug level; log errors at error level.
- Ensure partitions daily: the scheduler calls `db.EnsureProbePartitions` once at startup and every 24h from `Run`.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/scheduler ./internal/db ./internal/app ./cmd/... -count=1`
Expected: PASS (Docker required for scheduler/db).

- [ ] **Step 7: Commit**

```bash
git add backend/go.mod backend/go.sum backend/internal/jobs backend/internal/db backend/internal/app backend/internal/scheduler backend/cmd
git commit -m "feat: enqueue due probes with River and scheduler"
```

---

### Task 8: Probe Worker and State Machine

**Files:**
- Create: `backend/internal/worker/state.go`
- Create: `backend/internal/worker/state_test.go`
- Create: `backend/internal/worker/probe_worker.go`
- Create: `backend/internal/worker/probe_worker_test.go`
- Create: `backend/cmd/worker/main.go`
- Modify: `backend/internal/db/queries/monitors.sql` + regenerate (only if `GetMonitorForProbe` is missing fields)

**Interfaces:**
- Produces:
  - `worker.Advance(st State, success bool, confirm, recover int) State` where `State{Status string, Failures, Successes int}` and `Status` is `"unknown"|"up"|"down"`.
  - `worker.NewProbeWorker(pool *pgxpool.Pool, q *sqlc.Queries, box *secretbox.Box, prober *probe.Prober, limiter *rate.Limiter, logger *slog.Logger) *ProbeWorker`.
  - `(*ProbeWorker).Work(ctx, job *river.Job[jobs.ProbeArgs]) error` and `(*ProbeWorker).Process(ctx, args jobs.ProbeArgs) error` (tests call `Process`).
  - `cmd/worker` entrypoint.

- [ ] **Step 1: Write the failing state machine tests**

`state_test.go`:

```go
func TestAdvance(t *testing.T) {
	tests := []struct {
		name    string
		in      State
		success bool
		want    State
	}{
		{"unknown first success", State{Status: "unknown"}, true, State{Status: "up", Successes: 1}},
		{"unknown confirms down", State{Status: "unknown", Failures: 1}, false, State{Status: "down", Failures: 2}},
		{"up confirms down", State{Status: "up", Failures: 1}, false, State{Status: "down", Failures: 2}},
		{"down recovers", State{Status: "down", Successes: 1}, true, State{Status: "up", Successes: 2}},
		{"down stays down", State{Status: "down"}, false, State{Status: "down", Failures: 1}},
		{"up failure below threshold", State{Status: "up"}, false, State{Status: "up", Failures: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Advance(tc.in, tc.success, 2, 2); got != tc.want {
				t.Fatalf("Advance = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAdvance_ThresholdOne(t *testing.T) {
	if got := Advance(State{Status: "unknown"}, false, 1, 1); got.Status != "down" {
		t.Fatalf("threshold 1 = %+v", got)
	}
	if got := Advance(State{Status: "down"}, true, 1, 1); got.Status != "up" {
		t.Fatalf("recovery 1 = %+v", got)
	}
}

func TestAdvance_CapsCounters(t *testing.T) {
	got := Advance(State{Status: "down", Failures: 1000}, false, 2, 2)
	if got.Failures != 1000 {
		t.Fatalf("failures = %d, want capped at 1000", got.Failures)
	}
}
```

- [ ] **Step 2: Write the failing worker integration tests**

`probe_worker_test.go`:

```go
func TestProcess_RecordsResultAndTransitionsOnce(t *testing.T) {
	env := newWorkerEnv(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	monitorID, wsID := env.createMonitor(t, target.URL, 1, 1) // confirm=1, recover=1

	args := jobs.ProbeArgs{MonitorID: monitorID, ScheduledFor: time.Now().UTC().Truncate(time.Second)}
	if err := env.worker.Process(context.Background(), args); err != nil {
		t.Fatalf("Process: %v", err)
	}
	// Replay must be a no-op.
	if err := env.worker.Process(context.Background(), args); err != nil {
		t.Fatalf("replay: %v", err)
	}

	var results int
	var status string
	var successes int
	if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM probe_results WHERE monitor_id = $1`, monitorID).Scan(&results); err != nil {
		t.Fatalf("count results: %v", err)
	}
	if err := env.pool.QueryRow(context.Background(), `SELECT status, consecutive_successes FROM monitor_state WHERE monitor_id = $1`, monitorID).Scan(&status, &successes); err != nil {
		t.Fatalf("state: %v", err)
	}
	if results != 1 || status != "up" || successes != 1 {
		t.Fatalf("results/status/successes = %d/%s/%d", results, status, successes)
	}
	_ = wsID
}

func TestProcess_SkipsPausedAndSuspended(t *testing.T) { /* create paused and suspended monitors; assert 0 results */ }
func TestProcess_TLSFailureIsDownNotJobFailure(t *testing.T) { /* httptest.NewTLSServer with untrusted cert; err == nil, result down, error class tls */ }
```

- [ ] **Step 3: Run the tests and verify they fail**

Run: `go test ./internal/worker -run 'TestAdvance|TestProcess' -count=1`
Expected: FAIL.

- [ ] **Step 4: Implement the state machine**

`Advance` increments the matching counter, resets the other to 0, caps at 1000, then applies design §7 transitions based on the passed thresholds. Return a value copy.

- [ ] **Step 5: Implement the worker**

`Process`:

1. `GetMonitorForProbe(monitorID)`: if no rows → return nil.
2. If `suspended_at != nil` or `paused` → return nil.
3. `ProbeResultExists(monitorID, scheduled_for)` → return nil when true.
4. Wait for `limiter.Wait(ctx)`.
5. Decrypt headers (JSON into `map[string]string`) and body; a failure returns an error (job retry).
6. Build `probe.Request` with the decrypted values, parsed expected status, and monitor timeout.
7. `prober.Probe`.
8. Begin tx: `InsertProbeResult` (conflict-do-nothing). If rows affected is 0, commit and return without touching state (a replay that slipped past the existence check). Otherwise `Advance` and `UpdateMonitorState`; commit.
9. If the status changed, log `monitor_transition` with previous/new status.

Embed `river.WorkerDefaults[jobs.ProbeArgs]`; `Work` calls `Process(ctx, job.Args)`; `Timeout` returns 30s. `cmd/worker`:

- `app.Start`, build `secretbox.New(cfg.EncryptionKey)`, guard/prober from config, limiter `rate.NewLimiter(rate.Limit(cfg.ProbeGlobalRPS), int(cfg.ProbeGlobalRPS))`.
- Register the worker with `river.NewWorkers()`/`AddWorker`, build `river.NewClient(riverpgxv5.New(pool), &river.Config{Queues: map[string]river.QueueConfig{"probe": {MaxWorkers: cfg.WorkerConcurrency}}, Workers: workers, ...})`. The queue name must match the scheduler's `Queue: "probe"`.
- Start the client, health server, wait for signals, `Stop(ctx)` with a 30s timeout, close runtime.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/worker ./cmd/... -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/worker backend/cmd/worker backend/internal/db
git commit -m "feat: execute probes and advance monitor state"
```

---

### Task 9: Admin Controls and Suspension

**Files:**
- Modify: `backend/internal/auth/session.go` (`Principal.IsAdmin`, `Authenticate`)
- Modify: `backend/internal/auth/middleware.go` (`RequireAdmin`)
- Modify: `backend/internal/auth/session_test.go`, `middleware_test.go`
- Create: `backend/internal/admin/service.go`
- Create: `backend/internal/admin/handlers.go`
- Create: `backend/internal/admin/service_test.go`
- Create: `backend/internal/admin/handlers_test.go`
- Modify: `backend/internal/app/app.go` (bootstrap admin)
- Modify: `backend/internal/api/router.go` (admin routes + dependency)
- Modify: `backend/internal/monitors/validate.go` / `service.go` to enforce suspension on update/delete/pause (already required by Task 4; add tests here if not already present)

**Interfaces:**
- Produces:
  - `auth.Principal.IsAdmin bool`; `auth.RequireAdmin(next http.Handler) http.Handler`.
  - `admin.NewService(pool *pgxpool.Pool, q *sqlc.Queries) *Service`
  - `(*Service).ListWorkspaces(ctx, query string, suspended *bool, cursor *monitors.Cursor, limit int) ([]AdminWorkspace, error)`
  - `(*Service).Suspend(ctx, adminID, workspaceID uuid.UUID, reason string) error`
  - `(*Service).Unsuspend(ctx, adminID, workspaceID uuid.UUID) error`
  - `(*Service).ListMonitors(ctx, workspaceID *uuid.UUID, query string, cursor *monitors.Cursor, limit int) ([]AdminMonitor, error)`
  - `(*Service).ListSecurityEvents(ctx, kind string, workspaceID *uuid.UUID, cursor *monitors.Cursor, limit int) ([]SecurityEvent, error)`
  - Handlers `admin.WorkspacesHandler`, `SuspendHandler`, `UnsuspendHandler`, `MonitorsHandler`, `SecurityEventsHandler`.
  - `api.Dependencies.AdminService *admin.Service`.

- [ ] **Step 1: Write the failing auth tests**

- `TestAuthenticate_SetsIsAdmin`: create a verified user, `UPDATE users SET is_admin = true`, log in, call `Authenticate`, assert `Principal.IsAdmin`.
- `TestRequireAdmin`: a non-admin principal gets `403 forbidden`; an admin reaches the next handler.

- [ ] **Step 2: Write the failing admin integration test**

```go
func TestAdmin_SuspendStopsProbesAndMutations(t *testing.T) {
	env := newAdminEnv(t)
	owner, ws, monitorID := env.ownerWithMonitor(t)
	admin := env.makeAdmin(t, "admin@example.com")

	resp, body := admin.post("/api/v1/admin/workspaces/"+ws+"/suspend", map[string]string{"reason": "abuse"})
	requireStatus(t, resp, http.StatusOK)
	got := decodeJSON[workspaceJSON](t, body)
	if got.SuspendedAt == nil || got.SuspendedReason != "abuse" {
		t.Fatalf("suspended workspace = %+v", got)
	}

	resp, _ = owner.post("/api/v1/workspaces/"+ws+"/monitors/"+monitorID+"/pause", nil)
	requireStatus(t, resp, http.StatusForbidden)

	// Scheduler skips the workspace.
	if n, err := env.sched.Tick(context.Background()); err != nil || n != 0 {
		t.Fatalf("Tick = %d, %v; want 0, nil", n, err)
	}

	// Non-admin cannot suspend.
	resp, _ = owner.post("/api/v1/admin/workspaces/"+ws+"/suspend", map[string]string{"reason": "x"})
	requireStatus(t, resp, http.StatusForbidden)

	resp, _ = admin.post("/api/v1/admin/workspaces/"+ws+"/unsuspend", nil)
	requireStatus(t, resp, http.StatusOK)
}
```

Add: list workspaces returns suspension fields/counts; admin monitor list redacts the query string; security events include `workspace_suspended`.

- [ ] **Step 3: Run the tests and verify they fail**

Run: `go test ./internal/auth ./internal/admin ./internal/api -run 'TestAuthenticate_SetsIsAdmin|TestRequireAdmin|TestAdmin_' -count=1`
Expected: FAIL.

- [ ] **Step 4: Implement auth changes and admin service**

- Add `IsAdmin` to `Principal` and set it from `user.IsAdmin` in `Authenticate`.
- `RequireAdmin`: read principal; missing → 401; `!IsAdmin` → 403 `forbidden`; else next.
- Admin service validates reason 1–500 characters, authorizes by admin middleware (handler layer), and does not perform workspace membership checks (admin is cross-tenant by design).
- `Suspend`/`Unsuspend` are idempotent; suspend records `workspace_suspended` with actor/reason, unsuspend records `workspace_unsuspended`.
- `ListMonitors` uses `monitors.RedactURL` for URLs (strip query and fragment); `ListWorkspaces` returns counts from the query.
- Admin handlers decode `httpx.PageCursor` into `monitors.Cursor` exactly like the workspace monitor handlers.
- Bootstrap in `app.Start`: if `cfg.BootstrapAdminEmail != ""`, run `UPDATE users SET is_admin = true WHERE email = $1 AND email_verified_at IS NOT NULL`; log whether a row changed. Never error when the user does not exist yet.

- [ ] **Step 5: Wire the admin handlers and run tests**

Routes: `GET /api/v1/admin/workspaces`, `POST .../{workspaceID}/suspend`, `POST .../{workspaceID}/unsuspend`, `GET /api/v1/admin/monitors`, `GET /api/v1/admin/security-events`. All wrapped in `requireSession(requireAdmin(...))`.

Run: `go test ./internal/auth ./internal/admin ./internal/api ./internal/scheduler -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/auth backend/internal/admin backend/internal/app backend/internal/api
git commit -m "feat: add admin workspace suspension and security events"
```

---

### Task 10: Multi-Binary Image, Compose, and End-to-End Verification

**Files:**
- Modify: `backend/Dockerfile`
- Modify: `compose.yaml`
- Modify: `.github/workflows/ci.yml`
- Modify: `README.md` (create it at the repo root; run instructions for scheduler/worker and admin bootstrap)
- Create: `backend/internal/api/probe_flow_integration_test.go` (end-to-end probe flow through scheduler + worker)

**Interfaces:**
- Consumes: everything from Tasks 1–9.
- Produces: a runnable three-binary deployment from one image.

- [ ] **Step 1: Write the end-to-end flow test**

```go
func TestAPI_MonitorProbedBySchedulerAndWorker(t *testing.T) {
	env := newFullStackEnv(t) // Postgres, router with injected service, scheduler, worker
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	owner, ws := env.loginAndWorkspace(t, "flow@example.com")
	monitor := env.createMonitor(t, owner, ws, target.URL, 1)

	if _, err := env.sched.Tick(context.Background()); err != nil {
		t.Fatalf("scheduler Tick: %v", err)
	}
	var jobCount int
	if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'probe'`).Scan(&jobCount); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobCount != 1 {
		t.Fatalf("jobs = %d, want 1", jobCount)
	}

	// Execute the enqueued job directly (deterministic, no queue timing).
	var rawArgs []byte
	if err := env.pool.QueryRow(context.Background(), `SELECT args FROM river_job WHERE kind = 'probe'`).Scan(&rawArgs); err != nil {
		t.Fatalf("read args: %v", err)
	}
	var args jobs.ProbeArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		t.Fatalf("decode args: %v", err)
	}
	if err := env.worker.Process(context.Background(), args); err != nil {
		t.Fatalf("worker Process: %v", err)
	}

	body := env.getJSON(t, owner, "/api/v1/workspaces/"+ws+"/monitors/"+monitor.ID)
	got := decodeJSON[monitorJSON](t, body)
	if got.Status != "up" || got.LastCheck == nil || got.LastCheck.StatusCode != 200 {
		t.Fatalf("monitor after probe = %+v", got)
	}
}
```

- [ ] **Step 2: Build the multi-binary image**

Change the builder stage to:

```dockerfile
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/scheduler ./cmd/scheduler && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker
```

Copy all three into the distroless stage; keep `ENTRYPOINT ["/api"]`.

- [ ] **Step 3: Extend compose**

Add `scheduler` and `worker` services using the same build context/image, `depends_on: postgres: {condition: service_healthy}`, `environment` sharing `O1_DATABASE_URL`, `O1_SECRET_KEY`, `O1_ENV`, and `entrypoint: ["/scheduler"]` / `["/worker"]`. Also add an `api` service for the same image (`entrypoint: ["/api"]`, port 8080) so `docker compose up` runs the full stack; the current compose file only has Postgres and Mailpit. Expose health ports if desired. Do not add a migration-only service; the advisory lock makes concurrent startup safe.

- [ ] **Step 4: Update CI and docs**

- CI backend job: add `sqlc diff`? If sqlc is not installed in CI, instead add a step `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 diff` — this is slow; simpler: add `go build ./...` (already present) and a Docker build job step. Add a `docker build` step to the backend job so the multi-binary Dockerfile is exercised.
- README: document `O1_ENCRYPTION_KEY`, `O1_BOOTSTRAP_ADMIN_EMAIL`, running `docker compose up`, and the three binaries.

- [ ] **Step 5: Run the full verification**

```bash
cd backend
gofmt -l .
go vet ./...
go test -short ./...
go test -count=1 ./...
golangci-lint run
docker build -f Dockerfile .
```

Expected: `gofmt -l .` prints nothing; vet/lint/test/build all pass. Then from the repo root: `docker compose config` succeeds.

- [ ] **Step 6: Commit**

```bash
git add backend/Dockerfile compose.yaml .github/workflows/ci.yml README.md backend/internal/api
git commit -m "chore: build and run api, scheduler, and worker"
```

---

## Self-Review

**Spec coverage:**
- Monitor CRUD, validation, RBAC → Tasks 4–5.
- SSRF guard, redirects, dial-time checks, TLS failure as down → Task 3 + Task 8.
- Scheduler/worker probe loop, River idempotency → Tasks 7–8.
- State machine `unknown/up/down/paused`, thresholds, pause preservation → Tasks 4 and 8.
- Quotas, creation rate limit, global probe RPS → Tasks 4 and 8.
- Admin suspend/unsuspend, suspicious monitor visibility, security events → Task 9.
- Dashboard APIs (list/detail/results/summary) → Tasks 5–6.
- Encryption at rest, redaction, no secrets in logs → Tasks 1, 4, 5, 9.
- Monthly partitions → Task 2.
- Health/graceful shutdown for all binaries → Tasks 7–8.
- Docker/compose/CI → Task 10.
- Incidents, alerts, status pages, rollups, retention, React, metrics → explicitly out of scope; no task claims them.

**Type consistency:** `jobs.ProbeArgs`, `probe.Result`, `monitors.Monitor`, `httpx.PageCursor`, and service signatures are named identically wherever referenced. `monitors.ErrNotFound`/`ErrForbidden` alias the workspace sentinels, so handler mapping is consistent.

**Review Focus tests:** SSRF bypass → Task 3; cross-tenant access → Tasks 4–5 and 9; quota/rate race → Task 4 (workspace row lock) with concurrent test; probe replay → Task 8; secret leakage → Tasks 4, 5, 9.

**Known dependency risks to check during execution:**
- River v0.51.0 API names (`rivertype.JobState*`, `InsertTx`) — verify with `go doc` before writing the scheduler.
- sqlc `percentile_cont` and `ANY(uuid[])` generation — verify generated signatures after Task 2; if sqlc misparses a query, move that single query to a hand-written pgx function in the owning package.
- `testcontainers` Postgres must support partitioned tables (Postgres 16 does).

package rollup

import (
	"context"
	"io"
	"log/slog"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/db"
	"o1/internal/db/sqlc"
	"o1/internal/testutil"
)

// testBase is the first hour of the fixture data. The year 2026 keeps the
// expectations readable; the helper below makes sure Postgres has a partition
// covering it even if the suite runs much later.
var testBase = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

type rollupEnv struct {
	t           *testing.T
	pool        *pgxpool.Pool
	q           *sqlc.Queries
	rolluper    *Rolluper
	workspaceID uuid.UUID
	monitorID   uuid.UUID
}

// newRollupEnv creates a user, workspace, and monitor for one test.
func newRollupEnv(t *testing.T, maxHours int) *rollupEnv {
	t.Helper()
	pool, cleanup := testutil.NewPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	if err := db.EnsureProbePartitions(ctx, pool, testBase); err != nil {
		t.Fatalf("ensure probe partitions: %v", err)
	}

	userID := testutil.CreateVerifiedUser(t, pool, "rollup-"+uuid.NewString()+"@example.com", "correct horse battery staple")
	workspaceID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1,'Rollups',$2,$3)`,
		workspaceID, "rollups-"+workspaceID.String(), userID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	monitorID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO monitors (id, workspace_id, name, url, method, interval_seconds, timeout_seconds, created_by)
		 VALUES ($1,$2,'API','https://example.com','GET',60,10,$3)`,
		monitorID, workspaceID, userID); err != nil {
		t.Fatalf("insert monitor: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := sqlc.New(pool)
	return &rollupEnv{
		t:           t,
		pool:        pool,
		q:           q,
		rolluper:    New(pool, q, logger, nil, maxHours),
		workspaceID: workspaceID,
		monitorID:   monitorID,
	}
}

// at freezes the Rolluper clock.
func (e *rollupEnv) at(now time.Time) {
	e.rolluper.now = func() time.Time { return now }
}

func (e *rollupEnv) seedProbe(at time.Time, status string, latencyMS int) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO probe_results (id, monitor_id, workspace_id, scheduled_for, checked_at, status, latency_ms)
		VALUES ($1,$2,$3,$4,$4,$5,$6)`,
		uuid.New(), e.monitorID, e.workspaceID, at, status, latencyMS); err != nil {
		e.t.Fatalf("insert probe at %s: %v", at, err)
	}
}

type hourlyRow struct {
	BucketStart  time.Time
	UpCount      int64
	DownCount    int64
	Checks       int64
	AvgLatencyMS float64
	MinLatencyMS int32
	MaxLatencyMS int32
	P95LatencyMS float64
}

func (e *rollupEnv) hourlyRows() []hourlyRow {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `
		SELECT bucket_start, up_count, down_count, checks,
		       avg_latency_ms, min_latency_ms, max_latency_ms, p95_latency_ms
		FROM monitor_hourly_stats
		WHERE monitor_id = $1
		ORDER BY bucket_start`, e.monitorID)
	if err != nil {
		e.t.Fatalf("query hourly stats: %v", err)
	}
	defer rows.Close()

	var out []hourlyRow
	for rows.Next() {
		var r hourlyRow
		if err := rows.Scan(&r.BucketStart, &r.UpCount, &r.DownCount, &r.Checks,
			&r.AvgLatencyMS, &r.MinLatencyMS, &r.MaxLatencyMS, &r.P95LatencyMS); err != nil {
			e.t.Fatalf("scan hourly stats: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatalf("iterate hourly stats: %v", err)
	}
	return out
}

func (e *rollupEnv) watermark() *time.Time {
	e.t.Helper()
	wm, err := e.q.GetRollupWatermark(context.Background())
	if err != nil {
		e.t.Fatalf("get rollup watermark: %v", err)
	}
	return wm
}

func (e *rollupEnv) setWatermark(t time.Time) {
	e.t.Helper()
	if err := e.q.SetRollupWatermark(context.Background(), t); err != nil {
		e.t.Fatalf("set rollup watermark: %v", err)
	}
}

func assertWatermark(t *testing.T, e *rollupEnv, want time.Time) {
	t.Helper()
	got := e.watermark()
	if got == nil {
		t.Fatalf("watermark = nil, want %s", want)
	}
	if !got.Equal(want) {
		t.Fatalf("watermark = %s, want %s", got.UTC(), want.UTC())
	}
}

func assertWatermarkNil(t *testing.T, e *rollupEnv) {
	t.Helper()
	if got := e.watermark(); got != nil {
		t.Fatalf("watermark = %s, want nil", got.UTC())
	}
}

func nearlyEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestRun_RollsUpOneHour(t *testing.T) {
	e := newRollupEnv(t, 24)
	e.at(testBase.Add(2 * time.Hour)) // 12:00 UTC; target is 10:00
	e.seedProbe(testBase.Add(5*time.Minute), "up", 100)
	e.seedProbe(testBase.Add(10*time.Minute), "up", 200)
	e.seedProbe(testBase.Add(15*time.Minute), "up", 300)
	e.seedProbe(testBase.Add(20*time.Minute), "down", 400)

	res, err := e.rolluper.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.From.Equal(testBase) || !res.To.Equal(testBase) {
		t.Fatalf("Result range = [%s, %s], want [%s, %s]", res.From, res.To, testBase, testBase)
	}

	rows := e.hourlyRows()
	if len(rows) != 1 {
		t.Fatalf("hourly rows = %d, want 1: %+v", len(rows), rows)
	}
	got := rows[0]
	if !got.BucketStart.Equal(testBase) {
		t.Fatalf("bucket_start = %s, want %s", got.BucketStart, testBase)
	}
	if got.UpCount != 3 || got.DownCount != 1 || got.Checks != 4 {
		t.Fatalf("counts = up %d down %d checks %d, want 3/1/4", got.UpCount, got.DownCount, got.Checks)
	}
	if !nearlyEqual(got.AvgLatencyMS, 250) || got.MinLatencyMS != 100 || got.MaxLatencyMS != 400 {
		t.Fatalf("latency = avg %v min %d max %d, want 250/100/400",
			got.AvgLatencyMS, got.MinLatencyMS, got.MaxLatencyMS)
	}

	var wantP95 float64
	err = e.pool.QueryRow(context.Background(), `
		SELECT percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms)
		FROM probe_results WHERE monitor_id = $1 AND checked_at >= $2 AND checked_at < $3`,
		e.monitorID, testBase, testBase.Add(time.Hour)).Scan(&wantP95)
	if err != nil {
		t.Fatalf("reference p95: %v", err)
	}
	if !nearlyEqual(got.P95LatencyMS, wantP95) {
		t.Fatalf("p95 = %v, want %v", got.P95LatencyMS, wantP95)
	}

	assertWatermark(t, e, testBase)
}

func TestRun_IsIdempotentAndUpdatesLateRows(t *testing.T) {
	e := newRollupEnv(t, 24)
	e.at(testBase.Add(2 * time.Hour))
	e.seedProbe(testBase.Add(5*time.Minute), "up", 100)

	if _, err := e.rolluper.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if rows := e.hourlyRows(); len(rows) != 1 || rows[0].Checks != 1 {
		t.Fatalf("after first Run rows = %+v, want one row with checks=1", rows)
	}

	// A late row for the already-rolled-up hour arrives before the next pass.
	e.seedProbe(testBase.Add(35*time.Minute), "up", 300)
	if _, err := e.rolluper.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	rows := e.hourlyRows()
	if len(rows) != 1 {
		t.Fatalf("hourly rows = %d, want 1 (upsert must not duplicate): %+v", len(rows), rows)
	}
	if rows[0].Checks != 2 || rows[0].UpCount != 2 || !nearlyEqual(rows[0].AvgLatencyMS, 200) {
		t.Fatalf("row = %+v, want checks=2 up=2 avg=200", rows[0])
	}
	assertWatermark(t, e, testBase)
}

func TestRun_ResumesFromWatermark(t *testing.T) {
	e := newRollupEnv(t, 1)
	e.at(testBase.Add(3 * time.Hour)) // 13:00 UTC; target is 11:00
	e.seedProbe(testBase.Add(5*time.Minute), "up", 100)
	e.seedProbe(testBase.Add(time.Hour+5*time.Minute), "up", 200)

	if _, err := e.rolluper.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	assertWatermark(t, e, testBase)
	if rows := e.hourlyRows(); len(rows) != 1 || !rows[0].BucketStart.Equal(testBase) {
		t.Fatalf("after first Run rows = %+v, want only %s", rows, testBase)
	}

	if _, err := e.rolluper.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	assertWatermark(t, e, testBase.Add(time.Hour))

	rows := e.hourlyRows()
	if len(rows) != 2 {
		t.Fatalf("hourly rows = %d, want 2: %+v", len(rows), rows)
	}
	if !rows[0].BucketStart.Equal(testBase) || !rows[1].BucketStart.Equal(testBase.Add(time.Hour)) {
		t.Fatalf("buckets = [%s, %s], want [%s, %s]",
			rows[0].BucketStart, rows[1].BucketStart, testBase, testBase.Add(time.Hour))
	}
}

func TestRun_NeverSkipsAGap(t *testing.T) {
	e := newRollupEnv(t, 3)
	e.at(testBase.Add(6 * time.Hour)) // 16:00 UTC; target is 14:00
	e.setWatermark(testBase)          // an hour that has no raw rows
	e.seedProbe(testBase.Add(2*time.Hour+5*time.Minute), "up", 100)

	res, err := e.rolluper.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Hours 11:00 and 13:00 are empty gaps; the cap stops the pass before
	// the 14:00 target, so the watermark must advance contiguously.
	if res.Hours != 3 || res.Rows != 1 {
		t.Fatalf("Result = hours %d rows %d, want hours 3 rows 1", res.Hours, res.Rows)
	}
	assertWatermark(t, e, testBase.Add(3*time.Hour))

	rows := e.hourlyRows()
	if len(rows) != 1 || !rows[0].BucketStart.Equal(testBase.Add(2*time.Hour)) {
		t.Fatalf("rows = %+v, want only the %s bucket", rows, testBase.Add(2*time.Hour))
	}
	if rows[0].Checks != 1 {
		t.Fatalf("checks = %d, want 1", rows[0].Checks)
	}
}

func TestRun_ReRollsRecentHoursOnlyAtTheFrontier(t *testing.T) {
	e := newRollupEnv(t, 1)
	e.at(testBase.Add(4 * time.Hour)) // 14:00 UTC; target is 12:00
	e.setWatermark(testBase)

	// A row above the watermark must not be rolled up while the catch-up is
	// still behind: this pass only processes the 11:00 gap hour.
	e.seedProbe(testBase.Add(2*time.Hour+5*time.Minute), "up", 100)
	if _, err := e.rolluper.Run(context.Background()); err != nil {
		t.Fatalf("behind Run: %v", err)
	}
	if rows := e.hourlyRows(); len(rows) != 0 {
		t.Fatalf("rows while behind the frontier = %+v, want none", rows)
	}
	assertWatermark(t, e, testBase.Add(time.Hour))

	// Once caught up, the previous two hours ending at the target are
	// re-read, while older hours are outside the late-arrival contract.
	e.setWatermark(testBase.Add(2 * time.Hour))
	e.seedProbe(testBase.Add(35*time.Minute), "up", 100)               // 10:35, outside the window
	e.seedProbe(testBase.Add(2*time.Hour+35*time.Minute), "down", 200) // 12:35, inside the window

	res, err := e.rolluper.Run(context.Background())
	if err != nil {
		t.Fatalf("caught-up Run: %v", err)
	}
	if res.Hours != 2 || res.Rows != 1 {
		t.Fatalf("Result = hours %d rows %d, want hours 2 rows 1", res.Hours, res.Rows)
	}

	rows := e.hourlyRows()
	if len(rows) != 1 || !rows[0].BucketStart.Equal(testBase.Add(2*time.Hour)) {
		t.Fatalf("rows = %+v, want only the 12:00 bucket", rows)
	}
	if rows[0].Checks != 2 || rows[0].DownCount != 1 {
		t.Fatalf("row = %+v, want checks=2 down=1 from the re-read", rows[0])
	}
	assertWatermark(t, e, testBase.Add(2*time.Hour))
}

func TestRun_DeletedMonitorsCascade(t *testing.T) {
	e := newRollupEnv(t, 24)
	e.at(testBase.Add(2 * time.Hour))
	e.seedProbe(testBase.Add(5*time.Minute), "up", 100)

	if _, err := e.rolluper.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rows := e.hourlyRows(); len(rows) != 1 {
		t.Fatalf("hourly rows = %d, want 1", len(rows))
	}

	if _, err := e.pool.Exec(context.Background(),
		`DELETE FROM monitors WHERE id = $1`, e.monitorID); err != nil {
		t.Fatalf("delete monitor: %v", err)
	}

	var hourly, raw int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM monitor_hourly_stats WHERE monitor_id = $1`, e.monitorID).Scan(&hourly); err != nil {
		t.Fatalf("count hourly stats: %v", err)
	}
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM probe_results WHERE monitor_id = $1`, e.monitorID).Scan(&raw); err != nil {
		t.Fatalf("count probe results: %v", err)
	}
	if hourly != 0 || raw != 0 {
		t.Fatalf("after delete hourly=%d raw=%d, want 0/0", hourly, raw)
	}
}

type fakeMetrics struct {
	mu             sync.Mutex
	observeCalls   int
	observedHours  int
	observedRows   int64
	observedErr    error
	watermarkCalls []time.Time
}

func (f *fakeMetrics) ObserveRollup(hours int, rows int64, _ time.Duration, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observeCalls++
	f.observedHours = hours
	f.observedRows = rows
	f.observedErr = err
}

func (f *fakeMetrics) SetRollupWatermark(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watermarkCalls = append(f.watermarkCalls, t)
}

func TestRun_ReportsMetricsAndKeepsTheGaugeCurrent(t *testing.T) {
	e := newRollupEnv(t, 24)
	m := &fakeMetrics{}
	e.rolluper.metrics = m
	e.at(testBase.Add(2 * time.Hour))
	e.seedProbe(testBase.Add(5*time.Minute), "up", 100)

	if _, err := e.rolluper.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	m.mu.Lock()
	if m.observeCalls != 1 || m.observedErr != nil || m.observedHours != 1 || m.observedRows != 1 {
		t.Fatalf("ObserveRollup = calls %d hours %d rows %d err %v, want 1/1/1/nil",
			m.observeCalls, m.observedHours, m.observedRows, m.observedErr)
	}
	if len(m.watermarkCalls) != 1 || !m.watermarkCalls[0].Equal(testBase) {
		t.Fatalf("SetRollupWatermark calls = %v, want [%s]", m.watermarkCalls, testBase)
	}
	m.mu.Unlock()

	// A caught-up pass still reports the existing watermark so the gauge does
	// not go stale.
	if _, err := e.rolluper.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.observeCalls != 2 {
		t.Fatalf("ObserveRollup calls = %d, want 2", m.observeCalls)
	}
	if len(m.watermarkCalls) != 2 || !m.watermarkCalls[1].Equal(testBase) {
		t.Fatalf("SetRollupWatermark calls = %v, want the watermark repeated", m.watermarkCalls)
	}
}

func TestRun_EmptyDatabaseIsANoOp(t *testing.T) {
	e := newRollupEnv(t, 24)
	e.at(testBase.Add(2 * time.Hour))

	res, err := e.rolluper.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res != (Result{}) {
		t.Fatalf("Result = %+v, want zero value", res)
	}
	assertWatermarkNil(t, e)
}

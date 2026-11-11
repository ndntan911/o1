package retention

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/db/sqlc"
	"o1/internal/testutil"
)

// retentionNow is the frozen clock for the retention tests. It is well past
// the fixture partitions so the 90-day raw cutoff and 13-month rollup cutoff
// are unambiguous.
var retentionNow = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

type retentionEnv struct {
	t           *testing.T
	pool        *pgxpool.Pool
	q           *sqlc.Queries
	workspaceID uuid.UUID
	monitorID   uuid.UUID
}

// newRetentionEnv creates a disposable database with the user, workspace, and
// monitor that probe_results rows require.
func newRetentionEnv(t *testing.T) *retentionEnv {
	t.Helper()
	pool, cleanup := testutil.NewPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	userID := testutil.CreateVerifiedUser(t, pool, "retention-"+uuid.NewString()+"@example.com", "correct horse battery staple")
	workspaceID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1,'Retention',$2,$3)`,
		workspaceID, "retention-"+workspaceID.String(), userID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	monitorID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO monitors (id, workspace_id, name, url, method, interval_seconds, timeout_seconds, created_by)
		 VALUES ($1,$2,'API','https://example.com','GET',60,10,$3)`,
		monitorID, workspaceID, userID); err != nil {
		t.Fatalf("insert monitor: %v", err)
	}

	return &retentionEnv{t: t, pool: pool, q: sqlc.New(pool), workspaceID: workspaceID, monitorID: monitorID}
}

func (e *retentionEnv) retainer(cfg Config, repairer HourRolluper) *Retainer {
	e.t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := New(e.pool, e.q, logger, cfg, repairer)
	r.now = func() time.Time { return retentionNow }
	return r
}

func (e *retentionEnv) seedProbe(at time.Time, status string, latencyMS int) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO probe_results (id, monitor_id, workspace_id, scheduled_for, checked_at, status, latency_ms)
		VALUES ($1,$2,$3,$4,$4,$5,$6)`,
		uuid.New(), e.monitorID, e.workspaceID, at, status, latencyMS); err != nil {
		e.t.Fatalf("insert probe at %s: %v", at, err)
	}
}

// rollupHour writes the hourly summary bucket for hour.
func (e *retentionEnv) rollupHour(hour time.Time) {
	e.t.Helper()
	start := hour.UTC().Truncate(time.Hour)
	if _, err := e.q.RollupHourlyStats(context.Background(), sqlc.RollupHourlyStatsParams{
		Start: start,
		End:   start.Add(time.Hour),
	}); err != nil {
		e.t.Fatalf("roll up hour %s: %v", start, err)
	}
}

func (e *retentionEnv) setWatermark(at time.Time) {
	e.t.Helper()
	if err := e.q.SetRollupWatermark(context.Background(), at); err != nil {
		e.t.Fatalf("set rollup watermark: %v", err)
	}
}

func (e *retentionEnv) countRollupChecks(start, end time.Time) int64 {
	e.t.Helper()
	n, err := e.q.CountRollupChecks(context.Background(), sqlc.CountRollupChecksParams{Start: start, End: end})
	if err != nil {
		e.t.Fatalf("count rollup checks: %v", err)
	}
	return n
}

// fakeRepairer records each repair attempt and delegates the actual work to
// repair when set. A nil repair function leaves the database untouched, which
// models a repair that cannot reconcile the counts.
type fakeRepairer struct {
	calls  []time.Time
	repair func(ctx context.Context, hour time.Time) (int64, error)
}

func (f *fakeRepairer) RollupHour(ctx context.Context, hour time.Time) (int64, error) {
	f.calls = append(f.calls, hour)
	if f.repair != nil {
		return f.repair(ctx, hour)
	}
	return 0, nil
}

func createPartition(t *testing.T, pool *pgxpool.Pool, name string, start, end time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(), fmt.Sprintf(
		"CREATE TABLE %s PARTITION OF probe_results FOR VALUES FROM ('%s') TO ('%s')",
		pgx.Identifier{name}.Sanitize(),
		start.Format(time.RFC3339), end.Format(time.RFC3339)))
	if err != nil {
		t.Fatal(err)
	}
}

func partitionExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(),
		`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
		t.Fatalf("check partition %s: %v", name, err)
	}
	return exists
}

func testConfig() Config {
	return Config{
		RawRetentionDays:      90,
		RollupRetentionMonths: 13,
		AuditRetentionMonths:  12,
		MaxPartitionDrops:     5,
	}
}

// verifiedPartition creates a partition whose raw rows are fully rolled up and
// whose watermark covers the partition end, so it passes the retention gate.
func (e *retentionEnv) verifiedPartition(name string, start, end time.Time) {
	e.t.Helper()
	createPartition(e.t, e.pool, name, start, end)
	e.seedProbe(start.Add(10*time.Hour), "up", 50)
	e.seedProbe(start.Add(10*time.Hour+5*time.Minute), "down", 60)
	e.seedProbe(start.Add(11*time.Hour), "up", 70)
	e.rollupHour(start.Add(10 * time.Hour))
	e.rollupHour(start.Add(11 * time.Hour))
	e.setWatermark(end)
}

func TestRun_DropsVerifiedExpiredPartition(t *testing.T) {
	e := newRetentionEnv(t)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	e.verifiedPartition("probe_results_2026_08", start, end)

	repairer := &fakeRepairer{}
	res, err := e.retainer(testConfig(), repairer).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Candidates != 1 || res.PartitionsDropped != 1 {
		t.Fatalf("Result = %+v, want 1 candidate and 1 drop", res)
	}
	if res.SkippedDryRun != 0 || res.SkippedWatermark != 0 || res.SkippedMismatch != 0 {
		t.Fatalf("Result skips = dry %d watermark %d mismatch %d, want all 0",
			res.SkippedDryRun, res.SkippedWatermark, res.SkippedMismatch)
	}
	if len(repairer.calls) != 0 {
		t.Fatalf("repair calls = %d, want 0 for a verified partition", len(repairer.calls))
	}
	if partitionExists(t, e.pool, "probe_results_2026_08") {
		t.Fatal("partition probe_results_2026_08 still exists, want dropped")
	}
}

func TestRun_KeepsPartitionWithCountMismatch(t *testing.T) {
	e := newRetentionEnv(t)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	createPartition(t, e.pool, "probe_results_2026_08", start, end)
	e.seedProbe(start.Add(10*time.Hour), "up", 50)
	e.seedProbe(start.Add(11*time.Hour), "up", 70)
	e.rollupHour(start.Add(10 * time.Hour)) // 11:00 is missing from the rollups
	e.setWatermark(end)

	repairer := &fakeRepairer{} // does not fix the missing rollup
	res, err := e.retainer(testConfig(), repairer).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Candidates != 1 || res.PartitionsDropped != 0 || res.SkippedMismatch != 1 {
		t.Fatalf("Result = %+v, want 1 candidate, 0 drops, 1 mismatch skip", res)
	}
	if !partitionExists(t, e.pool, "probe_results_2026_08") {
		t.Fatal("partition probe_results_2026_08 was dropped, want it retained")
	}
	// One repair pass over every hour in the partition.
	wantHours := 31 * 24
	if len(repairer.calls) != wantHours {
		t.Fatalf("repair calls = %d, want %d", len(repairer.calls), wantHours)
	}
	if !repairer.calls[0].Equal(start) || !repairer.calls[len(repairer.calls)-1].Equal(end.Add(-time.Hour)) {
		t.Fatalf("repair call range = [%s, %s], want [%s, %s]",
			repairer.calls[0], repairer.calls[len(repairer.calls)-1], start, end.Add(-time.Hour))
	}
}

func TestRun_RepairsThenDrops(t *testing.T) {
	e := newRetentionEnv(t)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	createPartition(t, e.pool, "probe_results_2026_08", start, end)
	e.seedProbe(start.Add(10*time.Hour), "up", 50)
	e.seedProbe(start.Add(11*time.Hour), "up", 70)
	e.rollupHour(start.Add(10 * time.Hour)) // 11:00 is missing from the rollups
	e.setWatermark(end)

	repairer := &fakeRepairer{repair: func(ctx context.Context, hour time.Time) (int64, error) {
		return e.q.RollupHourlyStats(ctx, sqlc.RollupHourlyStatsParams{Start: hour, End: hour.Add(time.Hour)})
	}}
	res, err := e.retainer(testConfig(), repairer).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Candidates != 1 || res.PartitionsDropped != 1 || res.SkippedMismatch != 0 {
		t.Fatalf("Result = %+v, want 1 candidate, 1 drop, 0 mismatch skips", res)
	}
	wantHours := 31 * 24
	if len(repairer.calls) != wantHours {
		t.Fatalf("repair calls = %d, want %d", len(repairer.calls), wantHours)
	}
	if partitionExists(t, e.pool, "probe_results_2026_08") {
		t.Fatal("partition probe_results_2026_08 still exists, want dropped after repair")
	}
}

func TestRun_KeepsPartitionWithoutWatermark(t *testing.T) {
	e := newRetentionEnv(t)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	createPartition(t, e.pool, "probe_results_2026_08", start, end)
	e.seedProbe(start.Add(10*time.Hour), "up", 50)
	e.rollupHour(start.Add(10 * time.Hour)) // counts match, but the watermark is nil

	repairer := &fakeRepairer{}
	res, err := e.retainer(testConfig(), repairer).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Candidates != 1 || res.PartitionsDropped != 0 || res.SkippedWatermark != 1 {
		t.Fatalf("Result = %+v, want 1 candidate, 0 drops, 1 watermark skip", res)
	}
	if !partitionExists(t, e.pool, "probe_results_2026_08") {
		t.Fatal("partition probe_results_2026_08 was dropped without a watermark")
	}
}

func TestRun_KeepsUnexpiredPartition(t *testing.T) {
	e := newRetentionEnv(t)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	createPartition(t, e.pool, "probe_results_2026_09", start, end)

	repairer := &fakeRepairer{}
	res, err := e.retainer(testConfig(), repairer).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Candidates != 0 || res.PartitionsDropped != 0 {
		t.Fatalf("Result = %+v, want no candidates and no drops", res)
	}
	if len(repairer.calls) != 0 {
		t.Fatalf("repair calls = %d, want 0", len(repairer.calls))
	}
	if !partitionExists(t, e.pool, "probe_results_2026_09") {
		t.Fatal("unexpired partition probe_results_2026_09 was dropped")
	}
}

func TestRun_DropsBeyondRollupWindowWithoutGate(t *testing.T) {
	e := newRetentionEnv(t)
	start := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	createPartition(t, e.pool, "probe_results_2025_09", start, end)

	repairer := &fakeRepairer{}
	res, err := e.retainer(testConfig(), repairer).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Candidates != 1 || res.PartitionsDropped != 1 {
		t.Fatalf("Result = %+v, want 1 candidate and 1 drop", res)
	}
	if res.SkippedWatermark != 0 || res.SkippedMismatch != 0 {
		t.Fatalf("Result = %+v, want no gate skips beyond the rollup window", res)
	}
	if len(repairer.calls) != 0 {
		t.Fatalf("repair calls = %d, want 0 without a gate", len(repairer.calls))
	}
	if partitionExists(t, e.pool, "probe_results_2025_09") {
		t.Fatal("old partition probe_results_2025_09 still exists, want dropped without a gate")
	}
}

func TestRun_DryRunChangesNothing(t *testing.T) {
	e := newRetentionEnv(t)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	e.verifiedPartition("probe_results_2026_08", start, end)

	before := e.countRollupChecks(start, end)
	cfg := testConfig()
	cfg.DryRun = true
	repairer := &fakeRepairer{}
	res, err := e.retainer(cfg, repairer).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Candidates != 1 || res.PartitionsDropped != 0 || res.SkippedDryRun != 1 {
		t.Fatalf("Result = %+v, want 1 candidate, 0 drops, 1 dry-run skip", res)
	}
	if len(repairer.calls) != 0 {
		t.Fatalf("repair calls = %d, want 0 in dry-run", len(repairer.calls))
	}
	if !partitionExists(t, e.pool, "probe_results_2026_08") {
		t.Fatal("dry-run dropped probe_results_2026_08")
	}
	if after := e.countRollupChecks(start, end); after != before {
		t.Fatalf("rollup checks = %d after dry-run, want unchanged %d", after, before)
	}
}

func TestRun_HonorsPartitionDropCap(t *testing.T) {
	e := newRetentionEnv(t)
	julStart := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	augStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	createPartition(t, e.pool, "probe_results_2026_07", julStart, julStart.AddDate(0, 1, 0))
	createPartition(t, e.pool, "probe_results_2026_08", augStart, augStart.AddDate(0, 1, 0))
	for _, start := range []time.Time{julStart, augStart} {
		e.seedProbe(start.Add(10*time.Hour), "up", 50)
		e.rollupHour(start.Add(10 * time.Hour))
	}
	e.setWatermark(augStart.AddDate(0, 1, 0))

	cfg := testConfig()
	cfg.MaxPartitionDrops = 1
	repairer := &fakeRepairer{}
	res, err := e.retainer(cfg, repairer).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Candidates != 2 || res.PartitionsDropped != 1 {
		t.Fatalf("Result = %+v, want 2 candidates and 1 drop", res)
	}
	if partitionExists(t, e.pool, "probe_results_2026_07") {
		t.Fatal("oldest candidate probe_results_2026_07 still exists, want dropped")
	}
	if !partitionExists(t, e.pool, "probe_results_2026_08") {
		t.Fatal("probe_results_2026_08 was dropped past the cap, want it retained")
	}
}

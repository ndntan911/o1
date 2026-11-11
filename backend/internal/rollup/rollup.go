// Package rollup maintains the hourly probe summaries that back the status
// page. It rolls up raw probe results into monitor_hourly_stats, catching up
// from the oldest raw hour and advancing a contiguous watermark, then keeps
// re-reading the most recent hours so late-arriving probes are reflected.
package rollup

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/db/sqlc"
)

const (
	// lateArrivalLag is how long after an hour ends a probe may still land
	// before that hour is considered settled.
	lateArrivalLag = 15 * time.Minute

	// replayWindow is how many trailing hours are re-read once the watermark
	// has caught up. Rows older than this are repaired by retention instead.
	replayWindow = 2 * time.Hour

	// defaultMaxHoursPerRun bounds one catch-up pass when the caller does not
	// configure a cap.
	defaultMaxHoursPerRun = 24
)

// Result describes one Run pass.
type Result struct {
	// Hours is the number of hourly buckets written, counted per upsert. It
	// includes the trailing replay hours.
	Hours int
	// Rows is the number of hourly summary rows inserted or updated. A replay
	// of an existing bucket counts again because it is a write.
	Rows int64
	// From and To bound the hours written by the pass. To is also the current
	// watermark (or the previous one when the pass did no work), so callers can
	// publish it without another query.
	From, To time.Time
}

func (r *Result) add(hour time.Time, rows int64) {
	if r.Hours == 0 {
		r.From = hour
	}
	r.Hours++
	r.Rows += rows
	if hour.After(r.To) {
		r.To = hour
	}
}

// Metrics receives rollup observability. Implementations must tolerate calls
// from a single Run; a nil Metrics is safe and disables reporting.
type Metrics interface {
	ObserveRollup(hours int, rows int64, d time.Duration, err error)
	SetRollupWatermark(t time.Time)
}

// Rolluper rolls raw probe results up into UTC hourly buckets.
type Rolluper struct {
	pool    *pgxpool.Pool
	q       *sqlc.Queries
	logger  *slog.Logger
	metrics Metrics

	maxHours int

	// now is injectable for deterministic tests.
	now func() time.Time
}

// New builds a Rolluper. maxHoursPerRun defaults to 24 when not positive, and
// metrics may be nil.
func New(pool *pgxpool.Pool, q *sqlc.Queries, logger *slog.Logger, metrics Metrics, maxHoursPerRun int) *Rolluper {
	if maxHoursPerRun <= 0 {
		maxHoursPerRun = defaultMaxHoursPerRun
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Rolluper{
		pool:     pool,
		q:        q,
		logger:   logger,
		metrics:  metrics,
		maxHours: maxHoursPerRun,
		now:      time.Now,
	}
}

// RollupHour upserts the UTC hourly bucket containing hour. It is exported so
// retention can repair a partition it is about to drop. It returns the number
// of summary rows written.
func (r *Rolluper) RollupHour(ctx context.Context, hour time.Time) (int64, error) {
	return r.q.RollupHourlyStats(ctx, hourlyParams(hour))
}

// hourlyParams returns the half-open [start, end) range for hour's bucket.
func hourlyParams(hour time.Time) sqlc.RollupHourlyStatsParams {
	start := hour.UTC().Truncate(time.Hour)
	return sqlc.RollupHourlyStatsParams{
		Start: start,
		End:   start.Add(time.Hour),
	}
}

// rollupHourTx rolls up one hour and advances the watermark in a single
// transaction. Either both the bucket and the watermark commit, or neither
// does; the next pass can therefore always resume from the last committed
// hour without skipping or double-reporting a frontier.
func (r *Rolluper) rollupHourTx(ctx context.Context, hour time.Time) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		// A committed transaction returns ErrTxClosed here, which is safe to
		// ignore; a failed one is rolled back.
		_ = tx.Rollback(ctx)
	}()

	q := r.q.WithTx(tx)
	rows, err := q.RollupHourlyStats(ctx, hourlyParams(hour))
	if err != nil {
		return 0, fmt.Errorf("roll up stats: %w", err)
	}
	if err := q.SetRollupWatermark(ctx, hour); err != nil {
		return 0, fmt.Errorf("set watermark: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}
	return rows, nil
}

// Run performs one catch-up pass. It processes at most maxHours contiguous
// hours, oldest first, committing each bucket and its watermark together. When
// the pass starts with the watermark already at or past the target it replays
// the trailing two hours so late rows are folded into existing buckets. On
// error it returns the partial result.
func (r *Rolluper) Run(ctx context.Context) (res Result, err error) {
	startedAt := time.Now()
	defer func() {
		if r.metrics == nil {
			return
		}
		r.metrics.ObserveRollup(res.Hours, res.Rows, time.Since(startedAt), err)
		if !res.To.IsZero() {
			r.metrics.SetRollupWatermark(res.To)
		}
	}()

	watermark, err := r.q.GetRollupWatermark(ctx)
	if err != nil {
		return res, fmt.Errorf("get rollup watermark: %w", err)
	}
	if watermark != nil {
		// A pass that does no work still reports the existing watermark so the
		// published gauge cannot go stale.
		res.To = watermark.UTC()
	}

	var oldest *time.Time
	if watermark == nil {
		oldest, err = r.q.OldestProbeHour(ctx)
		if err != nil {
			return res, fmt.Errorf("find oldest probe hour: %w", err)
		}
		if oldest == nil {
			// No raw probes: nothing to roll up and no watermark to report.
			return res, nil
		}
	}

	target := r.now().UTC().Add(-lateArrivalLag).Add(-time.Hour).Truncate(time.Hour)

	var next time.Time
	if watermark != nil {
		next = watermark.UTC().Truncate(time.Hour).Add(time.Hour)
	} else {
		next = oldest.UTC().Truncate(time.Hour)
	}

	// Track the watermark as this pass advances it. Replay is decided from the
	// post-pass value: a pass that starts behind the target but catches up in
	// the same run must still re-read the trailing hours, otherwise long
	// intervals (for example a 1h ticker) never run replay at all.
	var lastWatermark time.Time
	watermarkSet := watermark != nil
	if watermark != nil {
		lastWatermark = watermark.UTC()
	}
	// Catch up contiguously. The cap stops the pass early; each hour commits
	// its bucket and watermark together, so the next pass resumes exactly at
	// the last committed hour.
	for hour := next; !hour.After(target) && res.Hours < r.maxHours; hour = hour.Add(time.Hour) {
		rows, err := r.rollupHourTx(ctx, hour)
		if err != nil {
			return res, fmt.Errorf("roll up hour %s: %w", hour.Format(time.RFC3339), err)
		}
		res.add(hour, rows)
		lastWatermark = hour.UTC()
		watermarkSet = true
		r.logger.Log(ctx, slog.LevelDebug, "rolled up hour", "hour", hour, "rows", rows)
	}

	if !watermarkSet || lastWatermark.UTC().Truncate(time.Hour).Before(target) {
		return res, nil
	}

	// Caught up: re-read the trailing replayWindow hours. The watermark is not
	// moved, so this can never expose a partial hour as the frontier.
	if oldest == nil {
		oldest, err = r.q.OldestProbeHour(ctx)
		if err != nil {
			return res, fmt.Errorf("find oldest probe hour: %w", err)
		}
	}
	if oldest == nil {
		return res, nil
	}
	oldestHour := oldest.UTC().Truncate(time.Hour)
	hours := int(replayWindow / time.Hour)
	for i := hours - 1; i >= 0; i-- {
		hour := target.Add(-time.Duration(i) * time.Hour)
		if hour.Before(oldestHour) {
			// Scanning hours before the oldest raw row is pointless and would
			// touch partitions nobody wrote to.
			continue
		}
		rows, err := r.RollupHour(ctx, hour)
		if err != nil {
			return res, fmt.Errorf("re-roll hour %s: %w", hour.Format(time.RFC3339), err)
		}
		res.add(hour, rows)
		r.logger.Log(ctx, slog.LevelDebug, "re-rolled hour", "hour", hour, "rows", rows)
	}
	return res, nil
}

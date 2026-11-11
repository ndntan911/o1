// Package scheduler enqueues probe jobs for monitors whose next check is due.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"o1/internal/config"
	"o1/internal/db"
	"o1/internal/db/sqlc"
	"o1/internal/incidents"
	"o1/internal/jobs"
	"o1/internal/observability"
	"o1/internal/retention"
	"o1/internal/rollup"
)

// probeQueue is the River queue the worker consumes.
const probeQueue = "probe"

// maintenanceLockID is the advisory lock that gives one scheduler process
// ownership of the rollup and retention passes.
const maintenanceLockID int64 = 0x6f310013

// RollupRunner runs one rollup maintenance pass.
type RollupRunner interface {
	Run(ctx context.Context) (rollup.Result, error)
}

// RetentionRunner runs one retention maintenance pass.
type RetentionRunner interface {
	Run(ctx context.Context) (retention.Result, error)
}

// Scheduler enqueues due probes on a fixed tick.
type Scheduler struct {
	pool    *pgxpool.Pool
	q       *sqlc.Queries
	river   *river.Client[pgx.Tx]
	cfg     config.Config
	logger  *slog.Logger
	metrics *observability.Metrics
	tracer  *observability.Tracer

	// Maintenance passes run in this process behind an advisory lock.
	rollup    RollupRunner
	retention RetentionRunner

	// Injectable for deterministic tests.
	now    func() time.Time
	jitter func(time.Duration) time.Duration
	// tickFn and reminderFn run one probe or reminder pass. They default to
	// Tick and enqueueReminders and are replaceable in tests.
	tickFn     func(context.Context) (int, error)
	reminderFn func(context.Context) (int, error)

	batch int32
	tick  time.Duration
}

// New builds a Scheduler. metrics and tracer may be nil.
func New(pool *pgxpool.Pool, q *sqlc.Queries, riverClient *river.Client[pgx.Tx], cfg config.Config, logger *slog.Logger, metrics *observability.Metrics, tracer *observability.Tracer) *Scheduler {
	tick := cfg.SchedulerTick
	if tick <= 0 {
		tick = 10 * time.Second
	}
	batch := cfg.SchedulerBatch
	if batch <= 0 {
		batch = 500
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Scheduler{
		pool:    pool,
		q:       q,
		river:   riverClient,
		cfg:     cfg,
		logger:  logger,
		metrics: metrics,
		tracer:  tracer,
		now:     time.Now,
		jitter:  randomJitter,
		batch:   int32(batch), //nolint:gosec // positive config value validated by config.Load
		tick:    tick,
	}
	s.tickFn = s.Tick
	s.reminderFn = s.enqueueReminders
	return s
}

// SetMaintenance installs the rollup and retention runners. Either may be nil
// to disable that pass. Call it before Run.
func (s *Scheduler) SetMaintenance(rollupRunner RollupRunner, retentionRunner RetentionRunner) {
	s.rollup = rollupRunner
	s.retention = retentionRunner
}

// maintenanceLoop runs one rollup+retention catch-up pass at startup, then
// rollups on the configured interval and retention once per day. Retention
// never shares the rollup ticker. It returns immediately when both runners are
// unset.
func (s *Scheduler) maintenanceLoop(ctx context.Context) {
	if s.rollup == nil && s.retention == nil {
		return
	}

	s.runMaintenance(ctx, true, true)

	interval := s.cfg.RollupInterval
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	rollupTicker := time.NewTicker(interval)
	defer rollupTicker.Stop()
	retentionTicker := time.NewTicker(24 * time.Hour)
	defer retentionTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-rollupTicker.C:
			s.runMaintenance(ctx, true, false)
		case <-retentionTicker.C:
			s.runMaintenance(ctx, false, true)
		}
	}
}

// runMaintenance runs one maintenance pass behind a recover boundary. It never
// returns an error to the tick loop: failures are logged and retried on the
// next tick.
func (s *Scheduler) runMaintenance(ctx context.Context, doRollup, doRetention bool) {
	defer func() {
		if rec := recover(); rec != nil {
			observability.CapturePanic(ctx, rec)
			s.logger.Log(ctx, slog.LevelError, "maintenance pass panicked", "panic", rec)
		}
	}()
	s.runMaintenancePass(ctx, doRollup, doRetention)
}

// runMaintenancePass takes the advisory lock for the duration of one pass so
// only one scheduler process performs maintenance at a time.
func (s *Scheduler) runMaintenancePass(ctx context.Context, doRollup, doRetention bool) {
	if s.pool == nil {
		return
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		s.logger.Log(ctx, slog.LevelError, "maintenance acquire connection failed", "error", err)
		return
	}
	defer conn.Release()

	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, maintenanceLockID).Scan(&locked); err != nil {
		s.logger.Log(ctx, slog.LevelError, "maintenance advisory lock failed", "error", err)
		return
	}
	if !locked {
		s.metrics.AddRetentionSkips("locked", 1)
		s.logger.Log(ctx, slog.LevelDebug, "maintenance skipped: advisory lock held")
		return
	}
	// Unlock before the connection returns to the pool; Release is deferred
	// first so this deferred unlock runs first. Use a fresh context because the
	// pass context may already be canceled during shutdown, and the session
	// lock must not outlive the pass on a pooled connection.
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, maintenanceLockID); err != nil {
			s.logger.Log(ctx, slog.LevelError, "maintenance advisory unlock failed", "error", err)
		}
	}()

	if doRollup && s.rollup != nil {
		start := time.Now()
		result, err := s.rollup.Run(ctx)
		elapsed := time.Since(start)
		s.metrics.ObserveRollup(result.Hours, result.Rows, elapsed, err)
		if err != nil {
			s.logger.Log(ctx, slog.LevelError, "rollup maintenance failed",
				"error", err,
				"duration", elapsed.String(),
			)
		} else {
			if !result.To.IsZero() {
				s.metrics.SetRollupWatermark(result.To)
			}
			s.logger.Log(ctx, slog.LevelInfo, "rollup maintenance",
				"hours", result.Hours,
				"rows", result.Rows,
				"from", result.From,
				"to", result.To,
				"duration", elapsed.String(),
			)
		}
	}

	if doRetention && s.retention != nil {
		start := time.Now()
		result, err := s.retention.Run(ctx)
		elapsed := time.Since(start)
		s.metrics.ObserveRetention(elapsed, err)
		if err != nil {
			s.logger.Log(ctx, slog.LevelError, "retention maintenance failed",
				"error", err,
				"duration", elapsed.String(),
			)
			return
		}
		if s.cfg.RetentionDryRun {
			s.metrics.AddRetentionCandidates("rollup", result.RollupRows)
			s.metrics.AddRetentionCandidates("audit", result.AuditRows)
		} else {
			s.metrics.AddRetentionRows("rollup", result.RollupRows)
			s.metrics.AddRetentionRows("audit", result.AuditRows)
		}
		verifiedDrops := result.PartitionsDropped - result.DroppedBeyondRollup
		s.metrics.AddPartitionsDropped("verified", verifiedDrops)
		s.metrics.AddPartitionsDropped("beyond_rollup", result.DroppedBeyondRollup)
		s.metrics.AddRetentionSkips("dry_run", result.SkippedDryRun)
		s.metrics.AddRetentionSkips("watermark_behind", result.SkippedWatermark)
		s.metrics.AddRetentionSkips("count_mismatch", result.SkippedMismatch)
		s.logger.Log(ctx, slog.LevelInfo, "retention maintenance",
			"candidates", result.Candidates,
			"partitions_dropped", result.PartitionsDropped,
			"dropped_beyond_rollup", result.DroppedBeyondRollup,
			"skipped_dry_run", result.SkippedDryRun,
			"skipped_watermark", result.SkippedWatermark,
			"skipped_mismatch", result.SkippedMismatch,
			"rollup_rows", result.RollupRows,
			"audit_rows", result.AuditRows,
			"dry_run", s.cfg.RetentionDryRun,
			"duration", elapsed.String(),
		)
	}
}

// Tick enqueues one probe per due, active monitor and advances each monitor's
// next check atomically. It returns the number of due rows processed.
func (s *Scheduler) Tick(ctx context.Context) (n int, err error) {
	ctx, span := s.tracer.Start(ctx, "scheduler.tick")
	defer span.End()

	start := time.Now()
	defer func() {
		span.SetAttributes(attribute.Int("scheduler.enqueued", n))
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		s.metrics.ObserveSchedulerTick(n, time.Since(start))
	}()

	now := s.now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin scheduler tick: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	q := s.q.WithTx(tx)
	rows, err := q.ListDueMonitors(ctx, sqlc.ListDueMonitorsParams{
		Now:   now,
		Batch: s.batch,
	})
	if err != nil {
		return 0, fmt.Errorf("list due monitors: %w", err)
	}

	scheduledFor := time.Unix(now.Unix(), 0).UTC()
	for _, row := range rows {
		next := now.Add(time.Duration(row.IntervalSeconds)*time.Second + s.jitter(time.Duration(row.IntervalSeconds)*time.Second))
		result, err := s.river.InsertTx(ctx, tx, jobs.ProbeArgs{
			MonitorID:    row.ID,
			ScheduledFor: scheduledFor,
		}, &river.InsertOpts{
			MaxAttempts: 3,
			Queue:       probeQueue,
			UniqueOpts: river.UniqueOpts{
				ByArgs: true,
				ByState: []rivertype.JobState{
					rivertype.JobStateAvailable,
					rivertype.JobStateScheduled,
					rivertype.JobStateRunning,
					rivertype.JobStatePending,
				},
			},
		})
		if err != nil {
			return 0, fmt.Errorf("insert probe job for monitor %s: %w", row.ID, err)
		}
		if result != nil && result.UniqueSkippedAsDuplicate {
			s.logger.Log(ctx, slog.LevelDebug, "probe job already enqueued", "monitor_id", row.ID)
		}
		if err := q.SetNextCheckAt(ctx, sqlc.SetNextCheckAtParams{
			MonitorID:   row.ID,
			NextCheckAt: next,
		}); err != nil {
			return 0, fmt.Errorf("advance next check for monitor %s: %w", row.ID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit scheduler tick: %w", err)
	}
	return len(rows), nil
}

// enqueueReminders sends due incident reminders. It uses its own transaction
// so a reminder failure cannot roll back or block the probe tick.
func (s *Scheduler) enqueueReminders(ctx context.Context) (int, error) {
	now := s.now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin reminder tick: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	q := s.q.WithTx(tx)
	n, err := incidents.EnqueueReminders(ctx, tx, q, s.river, now)
	if err != nil {
		return 0, fmt.Errorf("enqueue incident reminders: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit reminder tick: %w", err)
	}
	return n, nil
}

// safeTick runs the probe pass behind a recover boundary. A panicking tick is
// reported to Sentry, logged, and turned into an error so Run keeps ticking
// instead of crashing the process.
func (s *Scheduler) safeTick(ctx context.Context) (n int, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			observability.CapturePanic(ctx, rec)
			s.logger.Log(ctx, slog.LevelError, "scheduler tick panicked", "panic", rec)
			n, err = 0, fmt.Errorf("scheduler tick panic: %v", rec)
		}
	}()
	if s.tickFn == nil {
		return s.Tick(ctx)
	}
	return s.tickFn(ctx)
}

// safeReminders runs the reminder pass behind the same recover boundary as the
// probe pass.
func (s *Scheduler) safeReminders(ctx context.Context) (n int, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			observability.CapturePanic(ctx, rec)
			s.logger.Log(ctx, slog.LevelError, "scheduler reminder tick panicked", "panic", rec)
			n, err = 0, fmt.Errorf("scheduler reminder tick panic: %v", rec)
		}
	}()
	if s.reminderFn == nil {
		return s.enqueueReminders(ctx)
	}
	return s.reminderFn(ctx)
}

// Run ticks until ctx is canceled. Ticks are synchronous, so a slow tick
// delays the next one rather than stacking. It also ensures probe partitions
// at startup and once per day.
func (s *Scheduler) Run(ctx context.Context) error {
	if err := db.EnsureProbePartitions(ctx, s.pool, s.now().UTC()); err != nil {
		s.logger.Log(ctx, slog.LevelError, "ensure probe partitions", "error", err)
	}

	// Maintenance runs in its own goroutine so a slow rollup or retention pass
	// never delays the probe tick.
	go s.maintenanceLoop(ctx)

	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	partitionTicker := time.NewTicker(24 * time.Hour)
	defer partitionTicker.Stop()

	return s.tickLoop(ctx, ticker.C, partitionTicker.C)
}

// tickLoop drains tick and partition events until ctx is canceled. Each tick
// runs behind a recover boundary, so one panic never stops the loop.
func (s *Scheduler) tickLoop(ctx context.Context, ticks, partitions <-chan time.Time) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-partitions:
			if err := db.EnsureProbePartitions(ctx, s.pool, s.now().UTC()); err != nil {
				s.logger.Log(ctx, slog.LevelError, "ensure probe partitions", "error", err)
			}
		case <-ticks:
			start := time.Now()
			n, err := s.safeTick(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				s.logger.Log(ctx, slog.LevelError, "scheduler tick failed", "error", err)
				continue
			}
			s.logger.Log(ctx, slog.LevelDebug, "scheduler tick",
				"enqueued", n,
				"duration", time.Since(start).String(),
			)

			reminded, err := s.safeReminders(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				// A missed reminder is retried on the next tick, so log it and
				// keep the probe loop running.
				s.logger.Log(ctx, slog.LevelError, "scheduler reminder tick failed", "error", err)
				continue
			}
			if reminded > 0 {
				s.logger.Log(ctx, slog.LevelDebug, "scheduler reminders", "enqueued", reminded)
			}
		}
	}
}

// randomJitter returns a random duration in [0, 10% of d].
func randomJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(d)/10 + 1)) //nolint:gosec // non-cryptographic scheduling jitter
}

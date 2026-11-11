// Package retention prunes time-series data that has outlived its retention
// window. Raw probe partitions are dropped only after their hourly rollups are
// verified, so the status page keeps its history while raw rows are reclaimed.
package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/db/sqlc"
)

// defaultBatchSize bounds each delete batch in the rollup and audit pruning
// paths. It keeps long deletes from holding one transaction open indefinitely.
const defaultBatchSize = 10000

// Config holds the retention windows and destructive-maintenance limits.
// Values are expected to be validated by the caller (config.Load).
type Config struct {
	RawRetentionDays      int
	RollupRetentionMonths int
	AuditRetentionMonths  int
	MaxPartitionDrops     int
	DryRun                bool
}

// HourRolluper repairs one UTC hourly rollup bucket. Rolluper implements it.
type HourRolluper interface {
	RollupHour(ctx context.Context, hour time.Time) (int64, error)
}

// Result describes one retention Run. Candidates counts expired raw
// partitions before the drop cap is applied. DroppedBeyondRollup is the subset
// of PartitionsDropped that skipped the rollup gate because the partition was
// already beyond the rollup retention window.
type Result struct {
	Candidates          int
	PartitionsDropped   int
	DroppedBeyondRollup int
	SkippedDryRun       int
	SkippedWatermark    int
	SkippedMismatch     int
	RollupRows          int64
	AuditRows           int64
}

// Retainer prunes expired data. It is safe to call Run repeatedly; each pass
// re-evaluates the current state instead of remembering earlier decisions.
type Retainer struct {
	pool     *pgxpool.Pool
	q        *sqlc.Queries
	logger   *slog.Logger
	cfg      Config
	repairer HourRolluper

	// now is injectable for deterministic tests.
	now       func() time.Time
	batchSize int
}

// New builds a Retainer. The configuration is expected to be validated
// already; MaxPartitionDrops defaults to 1 when it is not positive so a
// misconfigured process still makes bounded progress.
func New(pool *pgxpool.Pool, q *sqlc.Queries, logger *slog.Logger, cfg Config, repairer HourRolluper) *Retainer {
	if cfg.MaxPartitionDrops <= 0 {
		cfg.MaxPartitionDrops = 1
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Retainer{
		pool:      pool,
		q:         q,
		logger:    logger,
		cfg:       cfg,
		repairer:  repairer,
		now:       time.Now,
		batchSize: defaultBatchSize,
	}
}

// Run performs one retention pass: expired raw partitions first, then rollups,
// then audit events. On error it returns the partial Result so callers can
// still report the work that completed.
func (r *Retainer) Run(ctx context.Context) (Result, error) {
	var res Result
	if err := r.runPartitions(ctx, &res); err != nil {
		return res, err
	}

	now := r.now().UTC()
	rollups, err := r.pruneRollups(ctx, now)
	res.RollupRows += rollups
	if err != nil {
		return res, err
	}

	audit, err := r.pruneAudit(ctx, now)
	res.AuditRows += audit
	if err != nil {
		return res, err
	}

	return res, nil
}

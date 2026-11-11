package retention

import (
	"context"
	"fmt"
	"time"
)

// maxPruneBatches bounds one prune pass. A large backlog is deleted across
// several runs instead of holding a single run open indefinitely.
const maxPruneBatches = 100

// pruneRollups deletes hourly rollups older than the rollup retention window.
// It returns the number of rows removed (or the number of candidates in
// dry-run mode) and the rows deleted so far when an error occurs.
func (r *Retainer) pruneRollups(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.AddDate(0, -r.cfg.RollupRetentionMonths, 0)

	if r.cfg.DryRun {
		n, err := r.q.CountOldHourlyStats(ctx, cutoff)
		if err != nil {
			return 0, fmt.Errorf("count expired hourly stats: %w", err)
		}
		r.logger.Info("rollup retention dry-run", "cutoff", cutoff, "candidates", n)
		return n, nil
	}

	var total int64
	for batch := 0; batch < maxPruneBatches; batch++ {
		rows, err := r.q.DeleteOldHourlyStats(ctx, cutoff)
		if err != nil {
			return total, fmt.Errorf("delete expired hourly stats: %w", err)
		}
		total += rows
		// A short batch means the backlog is exhausted.
		if rows < int64(r.batchSize) {
			break
		}
	}

	r.logger.Info("pruned expired rollups", "cutoff", cutoff, "rows", total)
	return total, nil
}

// pruneAudit deletes security events older than the audit retention window.
// It mirrors pruneRollups: dry-run counts candidates without deleting.
func (r *Retainer) pruneAudit(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.AddDate(0, -r.cfg.AuditRetentionMonths, 0)

	if r.cfg.DryRun {
		n, err := r.q.CountOldSecurityEvents(ctx, cutoff)
		if err != nil {
			return 0, fmt.Errorf("count expired security events: %w", err)
		}
		r.logger.Info("audit retention dry-run", "cutoff", cutoff, "candidates", n)
		return n, nil
	}

	var total int64
	for batch := 0; batch < maxPruneBatches; batch++ {
		rows, err := r.q.DeleteOldSecurityEvents(ctx, cutoff)
		if err != nil {
			return total, fmt.Errorf("delete expired security events: %w", err)
		}
		total += rows
		if rows < int64(r.batchSize) {
			break
		}
	}

	r.logger.Info("pruned expired audit events", "cutoff", cutoff, "rows", total)
	return total, nil
}

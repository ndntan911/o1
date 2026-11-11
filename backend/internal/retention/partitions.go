package retention

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"o1/internal/db/sqlc"
)

// probePartitionRE matches the monthly partition names created by
// db.EnsureProbePartitions. Anything else is left alone: retention must not
// guess at a table's range.
var probePartitionRE = regexp.MustCompile(`^probe_results_(\d{4})_(\d{2})$`)

const listProbePartitionsQuery = `
SELECT c.relname
FROM pg_inherits i
JOIN pg_class c ON c.oid = i.inhrelid
JOIN pg_class p ON p.oid = i.inhparent
WHERE p.relname = 'probe_results' AND c.relkind = 'r'
ORDER BY c.relname;`

// probePartition is one raw monthly partition and its UTC half-open bounds.
type probePartition struct {
	name  string
	start time.Time
	end   time.Time
}

// listProbePartitions returns the known monthly partitions, oldest first.
// Unrecognized children are skipped with a warning.
func (r *Retainer) listProbePartitions(ctx context.Context) ([]probePartition, error) {
	rows, err := r.pool.Query(ctx, listProbePartitionsQuery)
	if err != nil {
		return nil, fmt.Errorf("list probe partitions: %w", err)
	}
	defer rows.Close()

	var partitions []probePartition
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan probe partition: %w", err)
		}
		start, end, ok := partitionStartEnd(name)
		if !ok {
			r.logger.Warn("skipping unrecognized probe partition", "partition", name)
			continue
		}
		partitions = append(partitions, probePartition{name: name, start: start, end: end})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate probe partitions: %w", err)
	}

	sort.Slice(partitions, func(i, j int) bool {
		return partitions[i].start.Before(partitions[j].start)
	})
	return partitions, nil
}

// partitionStartEnd derives the UTC month bounds encoded in a partition name.
func partitionStartEnd(name string) (start, end time.Time, ok bool) {
	match := probePartitionRE.FindStringSubmatch(name)
	if match == nil {
		return time.Time{}, time.Time{}, false
	}
	year, err := strconv.Atoi(match[1])
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	month, err := strconv.Atoi(match[2])
	if err != nil || month < 1 || month > 12 {
		return time.Time{}, time.Time{}, false
	}
	start = time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0), true
}

// runPartitions drops expired raw partitions within the configured cap.
func (r *Retainer) runPartitions(ctx context.Context, res *Result) error {
	now := r.now().UTC()
	rawCutoff := now.AddDate(0, 0, -r.cfg.RawRetentionDays)
	rollupCutoff := now.AddDate(0, -r.cfg.RollupRetentionMonths, 0)

	partitions, err := r.listProbePartitions(ctx)
	if err != nil {
		return err
	}

	candidates := make([]probePartition, 0, len(partitions))
	for _, p := range partitions {
		if p.end.After(rawCutoff) {
			continue
		}
		candidates = append(candidates, p)
	}
	res.Candidates += len(candidates)

	for _, p := range candidates {
		if res.PartitionsDropped >= r.cfg.MaxPartitionDrops {
			break
		}

		if r.cfg.DryRun {
			raw, rolled, err := r.probeCounts(ctx, p.start, p.end)
			if err != nil {
				return err
			}
			res.SkippedDryRun++
			r.logger.Info("probe partition retention dry-run",
				"partition", p.name,
				"start", p.start,
				"end", p.end,
				"raw_checks", raw,
				"rollup_checks", rolled,
				"gate_required", p.end.After(rollupCutoff),
			)
			continue
		}

		// Beyond the rollup window there is no history left to protect, so the
		// partition drops without consulting the watermark or rollups.
		if !p.end.After(rollupCutoff) {
			if err := r.dropProbePartition(ctx, p); err != nil {
				return err
			}
			res.PartitionsDropped++
			continue
		}

		watermark, err := r.q.GetRollupWatermark(ctx)
		if err != nil {
			return fmt.Errorf("get rollup watermark: %w", err)
		}
		if watermark == nil || watermark.Before(p.end) {
			res.SkippedWatermark++
			r.logger.Warn("probe partition watermark is behind partition end",
				"partition", p.name,
				"partition_end", p.end,
				"watermark", watermark,
			)
			continue
		}

		raw, rolled, err := r.probeCounts(ctx, p.start, p.end)
		if err != nil {
			return err
		}
		if raw != rolled {
			r.logger.Warn("probe partition rollups do not match raw checks",
				"partition", p.name,
				"raw_checks", raw,
				"rollup_checks", rolled,
			)
			if r.repairer == nil {
				res.SkippedMismatch++
				continue
			}
			if err := r.repairPartition(ctx, p.start, p.end); err != nil {
				return err
			}
			raw, rolled, err = r.probeCounts(ctx, p.start, p.end)
			if err != nil {
				return err
			}
			if raw != rolled {
				res.SkippedMismatch++
				r.logger.Warn("probe partition still mismatched after repair",
					"partition", p.name,
					"raw_checks", raw,
					"rollup_checks", rolled,
				)
				continue
			}
		}

		if err := r.dropProbePartition(ctx, p); err != nil {
			return err
		}
		res.PartitionsDropped++
	}
	return nil
}

// probeCounts returns the raw check count and the summed rollup checks for a
// half-open range.
func (r *Retainer) probeCounts(ctx context.Context, start, end time.Time) (raw, rolled int64, err error) {
	raw, err = r.q.CountProbeChecks(ctx, sqlc.CountProbeChecksParams{Start: start, End: end})
	if err != nil {
		return 0, 0, fmt.Errorf("count raw probe checks [%s, %s): %w", start, end, err)
	}
	rolled, err = r.q.CountRollupChecks(ctx, sqlc.CountRollupChecksParams{Start: start, End: end})
	if err != nil {
		return 0, 0, fmt.Errorf("count rollup checks [%s, %s): %w", start, end, err)
	}
	return raw, rolled, nil
}

// repairPartition re-rolls every hour in the partition exactly once. Retention
// never repairs a second time; if the counts still disagree the partition is
// retained for a later run and an operator.
func (r *Retainer) repairPartition(ctx context.Context, start, end time.Time) error {
	hours := 0
	for hour := start.UTC().Truncate(time.Hour); hour.Before(end); hour = hour.Add(time.Hour) {
		if _, err := r.repairer.RollupHour(ctx, hour); err != nil {
			return fmt.Errorf("repair rollup for %s: %w", hour.Format(time.RFC3339), err)
		}
		hours++
	}
	r.logger.Info("repaired probe partition rollups",
		"start", start,
		"end", end,
		"hours", hours,
	)
	return nil
}

func (r *Retainer) dropProbePartition(ctx context.Context, p probePartition) error {
	// The identifier is derived from a partition name matched against
	// probePartitionRE, then quoted by pgx. DDL cannot bind table names.
	stmt := "DROP TABLE " + pgx.Identifier{p.name}.Sanitize()
	if _, err := r.pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("drop probe partition %s: %w", p.name, err)
	}
	r.logger.Info("dropped probe partition", "partition", p.name, "start", p.start, "end", p.end)
	return nil
}

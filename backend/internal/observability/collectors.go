package observability

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Refresh intervals for the background gauges. Queue depth is refreshed more
// often than the slower-moving incident count.
const (
	queueRefreshInterval    = 15 * time.Second
	incidentRefreshInterval = 30 * time.Second
)

// QueueCollector refreshes the River queue gauges from Postgres. The gauges
// are refreshed in the background rather than on every scrape so a scrape
// never queries the database. Start is intended to run in the worker process
// only; the scheduler and API expose their own process metrics but not the
// shared queue gauges.
type QueueCollector struct {
	pool    *pgxpool.Pool
	metrics *Metrics

	interval time.Duration

	mu     sync.Mutex
	jobs   map[queueState]struct{}
	oldest map[string]struct{}
}

type queueState struct {
	queue string
	state string
}

// NewQueueCollector builds a QueueCollector with the default refresh interval.
func NewQueueCollector(pool *pgxpool.Pool, metrics *Metrics) *QueueCollector {
	return &QueueCollector{
		pool:     pool,
		metrics:  metrics,
		interval: queueRefreshInterval,
		jobs:     make(map[queueState]struct{}),
		oldest:   make(map[string]struct{}),
	}
}

// Refresh queries every non-terminal River job state and the oldest runnable
// job per queue. Rows that disappear are reset to zero so a gauge cannot stay
// stuck at an old depth. On error the previously published values are kept and
// the error is returned to the caller.
func (c *QueueCollector) Refresh(ctx context.Context) error {
	if c == nil || c.pool == nil || c.metrics == nil {
		return nil
	}

	counts, oldest, err := c.load(ctx)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for key := range c.jobs {
		if _, ok := counts[key]; !ok {
			c.metrics.SetQueueJobs(key.queue, key.state, 0)
		}
	}
	nextJobs := make(map[queueState]struct{}, len(counts))
	for key, n := range counts {
		c.metrics.SetQueueJobs(key.queue, key.state, n)
		nextJobs[key] = struct{}{}
	}
	c.jobs = nextJobs

	for queue := range c.oldest {
		if _, ok := oldest[queue]; !ok {
			c.metrics.SetQueueOldestJobAge(queue, 0)
		}
	}
	now := time.Now()
	nextOldest := make(map[string]struct{}, len(oldest))
	for queue, createdAt := range oldest {
		age := now.Sub(createdAt).Seconds()
		if age < 0 {
			age = 0
		}
		c.metrics.SetQueueOldestJobAge(queue, age)
		nextOldest[queue] = struct{}{}
	}
	c.oldest = nextOldest
	return nil
}

func (c *QueueCollector) load(ctx context.Context) (map[queueState]int, map[string]time.Time, error) {
	counts := make(map[queueState]int)
	rows, err := c.pool.Query(ctx, `
		SELECT queue, state, count(*)
		FROM river_job
		WHERE state NOT IN ('completed', 'cancelled')
		GROUP BY queue, state`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var queue, state string
		var n int64
		if err := rows.Scan(&queue, &state, &n); err != nil {
			return nil, nil, err
		}
		counts[queueState{queue: queue, state: state}] = int(n) //nolint:gosec // job counts fit in int
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	oldest := make(map[string]time.Time)
	rows, err = c.pool.Query(ctx, `
		SELECT queue, min(created_at)
		FROM river_job
		WHERE state IN ('available', 'running', 'retryable', 'pending')
		  AND scheduled_at <= now()
		GROUP BY queue`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var queue string
		var createdAt time.Time
		if err := rows.Scan(&queue, &createdAt); err != nil {
			return nil, nil, err
		}
		oldest[queue] = createdAt
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return counts, oldest, nil
}

// Start refreshes immediately and then every 15 seconds until ctx is
// canceled. It blocks, so callers usually run it in a goroutine.
func (c *QueueCollector) Start(ctx context.Context) {
	if c == nil {
		return
	}
	interval := c.interval
	if interval <= 0 {
		interval = queueRefreshInterval
	}
	c.refreshAndLog(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.refreshAndLog(ctx)
		}
	}
}

func (c *QueueCollector) refreshAndLog(ctx context.Context) {
	if err := c.Refresh(ctx); err != nil && ctx.Err() == nil {
		slog.Default().WarnContext(ctx, "queue metrics refresh failed", "error", err)
	}
}

// IncidentCollector refreshes the unresolved incident gauge from Postgres.
// Like QueueCollector it is refreshed in the background and is only started in
// the worker process so every environment has exactly one refresher.
type IncidentCollector struct {
	pool     *pgxpool.Pool
	metrics  *Metrics
	interval time.Duration
}

// NewIncidentCollector builds an IncidentCollector with the default refresh
// interval.
func NewIncidentCollector(pool *pgxpool.Pool, metrics *Metrics) *IncidentCollector {
	return &IncidentCollector{
		pool:     pool,
		metrics:  metrics,
		interval: incidentRefreshInterval,
	}
}

// Refresh counts incidents without a resolved_at timestamp. On error the
// previously published value is kept and the error is returned.
func (c *IncidentCollector) Refresh(ctx context.Context) error {
	if c == nil || c.pool == nil || c.metrics == nil {
		return nil
	}
	var n int
	if err := c.pool.QueryRow(ctx,
		`SELECT count(*) FROM incidents WHERE resolved_at IS NULL`).Scan(&n); err != nil {
		return err
	}
	c.metrics.SetIncidentsOpen(n)
	return nil
}

// Start refreshes immediately and then every 30 seconds until ctx is
// canceled. It blocks, so callers usually run it in a goroutine.
func (c *IncidentCollector) Start(ctx context.Context) {
	if c == nil {
		return
	}
	interval := c.interval
	if interval <= 0 {
		interval = incidentRefreshInterval
	}
	c.refreshAndLog(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.refreshAndLog(ctx)
		}
	}
}

func (c *IncidentCollector) refreshAndLog(ctx context.Context) {
	if err := c.Refresh(ctx); err != nil && ctx.Err() == nil {
		slog.Default().WarnContext(ctx, "incident metrics refresh failed", "error", err)
	}
}

// PoolCollector exposes pgxpool statistics at scrape time. Pool statistics are
// cheap to read, so no background refresh is needed.
type PoolCollector struct {
	pool *pgxpool.Pool

	connections      *prometheus.Desc
	acquiresTotal    *prometheus.Desc
	acquireDuration  *prometheus.Desc
	emptyAcquires    *prometheus.Desc
	canceledAcquires *prometheus.Desc
}

// NewPoolCollector builds a collector for the pgxpool connection pool.
func NewPoolCollector(pool *pgxpool.Pool) prometheus.Collector {
	return &PoolCollector{
		pool: pool,
		connections: prometheus.NewDesc(
			"o1_db_pool_connections",
			"Database pool connections by state.",
			[]string{"state"}, nil,
		),
		acquiresTotal: prometheus.NewDesc(
			"o1_db_pool_acquires_total",
			"Database pool connection acquisitions.",
			nil, nil,
		),
		acquireDuration: prometheus.NewDesc(
			"o1_db_pool_acquire_duration_seconds_total",
			"Total time spent acquiring database pool connections.",
			nil, nil,
		),
		emptyAcquires: prometheus.NewDesc(
			"o1_db_pool_empty_acquires_total",
			"Database pool acquisitions that had to wait for a new connection.",
			nil, nil,
		),
		canceledAcquires: prometheus.NewDesc(
			"o1_db_pool_canceled_acquires_total",
			"Database pool acquisitions canceled by their context.",
			nil, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *PoolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.connections
	ch <- c.acquiresTotal
	ch <- c.acquireDuration
	ch <- c.emptyAcquires
	ch <- c.canceledAcquires
}

// Collect implements prometheus.Collector.
func (c *PoolCollector) Collect(ch chan<- prometheus.Metric) {
	if c == nil || c.pool == nil {
		return
	}
	stat := c.pool.Stat()
	for _, state := range []struct {
		label string
		value float64
	}{
		{"acquired", float64(stat.AcquiredConns())},
		{"idle", float64(stat.IdleConns())},
		{"total", float64(stat.TotalConns())},
		{"max", float64(stat.MaxConns())},
	} {
		ch <- prometheus.MustNewConstMetric(c.connections, prometheus.GaugeValue, state.value, state.label)
	}
	ch <- prometheus.MustNewConstMetric(c.acquiresTotal, prometheus.CounterValue, float64(stat.AcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.acquireDuration, prometheus.CounterValue, stat.AcquireDuration().Seconds())
	ch <- prometheus.MustNewConstMetric(c.emptyAcquires, prometheus.CounterValue, float64(stat.EmptyAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.canceledAcquires, prometheus.CounterValue, float64(stat.CanceledAcquireCount()))
}

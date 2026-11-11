// Package observability owns the Prometheus registry, HTTP metrics
// middleware, and (in later tasks) tracing and Sentry integration.
package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the Prometheus registry and every metric vector in the
// observability catalog. A nil *Metrics is safe to use: all methods are
// no-ops, so callers never need to guard instrumentation sites.
type Metrics struct {
	registry *prometheus.Registry
	handler  http.Handler

	httpRequestsTotal     *prometheus.CounterVec
	httpRequestDuration   *prometheus.HistogramVec
	httpInFlightRequests  prometheus.Gauge
	probeResults          *prometheus.CounterVec
	probeDuration         *prometheus.HistogramVec
	probeSkipped          *prometheus.CounterVec
	alertDeliveries       *prometheus.CounterVec
	alertDeliveryDuration *prometheus.HistogramVec
	schedulerTicks        prometheus.Counter
	schedulerEnqueued     prometheus.Counter
	schedulerTickDuration prometheus.Histogram
	incidentsOpen         prometheus.Gauge
	queueJobs             *prometheus.GaugeVec
	queueOldestJobAge     *prometheus.GaugeVec
	rollupRuns            *prometheus.CounterVec
	rollupHours           prometheus.Counter
	rollupRows            prometheus.Counter
	rollupWatermark       prometheus.Gauge
	retentionRuns         *prometheus.CounterVec
	retentionRows         *prometheus.CounterVec
	retentionPartitions   prometheus.Counter
	retentionSkipped      *prometheus.CounterVec
	maintenanceDuration   *prometheus.HistogramVec
}

// NewMetrics builds a fresh registry with Go/process collectors, build info,
// and the metric vectors used by HTTP, probes, alerts, scheduler, incidents,
// and queue collectors.
func NewMetrics(service, version string) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "o1_build_info",
		Help:        "Build information for this service.",
		ConstLabels: prometheus.Labels{"service": service, "version": version},
	})
	buildInfo.Set(1)
	reg.MustRegister(buildInfo)

	m := &Metrics{
		registry: reg,
		httpRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "o1_http_requests_total",
			Help: "Total HTTP requests by method, route pattern, and status code.",
		}, []string{"method", "route", "status"}),
		httpRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "o1_http_request_duration_seconds",
			Help:    "HTTP request duration by method and route pattern.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"method", "route"}),
		httpInFlightRequests: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "o1_http_in_flight_requests",
			Help: "HTTP requests currently being served.",
		}),
		probeResults: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "o1_probe_results_total",
			Help: "Probe results by outcome.",
		}, []string{"result"}),
		probeDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "o1_probe_duration_seconds",
			Help:    "Probe duration by outcome.",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}, []string{"result"}),
		probeSkipped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "o1_probe_skipped_total",
			Help: "Probes skipped by bounded reason.",
		}, []string{"reason"}),
		alertDeliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "o1_alert_deliveries_total",
			Help: "Alert deliveries by channel kind and status.",
		}, []string{"channel_kind", "status"}),
		alertDeliveryDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "o1_alert_delivery_duration_seconds",
			Help:    "Alert delivery duration by channel kind.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"channel_kind"}),
		schedulerTicks: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "o1_scheduler_ticks_total",
			Help: "Scheduler ticks executed.",
		}),
		schedulerEnqueued: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "o1_scheduler_enqueued_total",
			Help: "Probes enqueued by the scheduler.",
		}),
		schedulerTickDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "o1_scheduler_tick_duration_seconds",
			Help:    "Scheduler tick duration.",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}),
		incidentsOpen: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "o1_incidents_open",
			Help: "Incidents that are not resolved.",
		}),
		queueJobs: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "o1_queue_jobs",
			Help: "River jobs by queue and non-terminal state.",
		}, []string{"queue", "state"}),
		queueOldestJobAge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "o1_queue_oldest_job_age_seconds",
			Help: "Age of the oldest runnable River job by queue.",
		}, []string{"queue"}),
		rollupRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "o1_rollup_runs_total",
			Help: "Rollup maintenance runs by result.",
		}, []string{"result"}),
		rollupHours: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "o1_rollup_hours_total",
			Help: "Hourly buckets processed by rollup maintenance.",
		}),
		rollupRows: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "o1_rollup_rows_total",
			Help: "Hourly summary rows written by rollup maintenance.",
		}),
		rollupWatermark: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "o1_rollup_frontier_timestamp_seconds",
			Help: "Watermark of the last contiguous rolled-up UTC hour.",
		}),
		retentionRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "o1_retention_runs_total",
			Help: "Retention maintenance runs by result.",
		}, []string{"result"}),
		retentionRows: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "o1_retention_rows_deleted_total",
			Help: "Rows deleted by retention, by kind.",
		}, []string{"kind"}),
		retentionPartitions: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "o1_retention_partitions_dropped_total",
			Help: "Raw probe partitions dropped by retention.",
		}),
		retentionSkipped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "o1_retention_skipped_total",
			Help: "Retention skips by bounded reason.",
		}, []string{"reason"}),
		maintenanceDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "o1_maintenance_duration_seconds",
			Help:    "Maintenance job duration by job.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300},
		}, []string{"job"}),
	}

	reg.MustRegister(
		m.httpRequestsTotal,
		m.httpRequestDuration,
		m.httpInFlightRequests,
		m.probeResults,
		m.probeDuration,
		m.probeSkipped,
		m.alertDeliveries,
		m.alertDeliveryDuration,
		m.schedulerTicks,
		m.schedulerEnqueued,
		m.schedulerTickDuration,
		m.incidentsOpen,
		m.queueJobs,
		m.queueOldestJobAge,
		m.rollupRuns,
		m.rollupHours,
		m.rollupRows,
		m.rollupWatermark,
		m.retentionRuns,
		m.retentionRows,
		m.retentionPartitions,
		m.retentionSkipped,
		m.maintenanceDuration,
	)
	m.handler = promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	return m
}

// Registry returns the Prometheus registry, or nil for a nil Metrics.
func (m *Metrics) Registry() *prometheus.Registry {
	if m == nil {
		return nil
	}
	return m.registry
}

// Register adds additional collectors, such as the pgxpool collector, to the
// registry. It is a no-op for a nil Metrics.
func (m *Metrics) Register(cs ...prometheus.Collector) {
	if m == nil {
		return
	}
	m.registry.MustRegister(cs...)
}

// Handler returns the Prometheus exposition handler, or a 404 handler for a
// nil Metrics.
func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return http.NotFoundHandler()
	}
	return m.handler
}

// HTTPMiddleware records in-flight requests, counts, durations, and status
// codes. Health and metrics endpoints are excluded. The route label comes
// exclusively from resolveRoute; empty or unknown routes become "unmatched",
// so raw paths and identifiers never reach the registry.
func (m *Metrics) HTTPMiddleware(next http.Handler, resolveRoute func(*http.Request) string) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL != nil {
			switch r.URL.Path {
			case "/healthz", "/readyz", "/metrics":
				next.ServeHTTP(w, r)
				return
			}
		}

		route := "unmatched"
		if resolveRoute != nil {
			if resolved := resolveRoute(r); resolved != "" {
				route = resolved
			}
		}

		method := normalizeHTTPMethod(r.Method)
		m.httpInFlightRequests.Inc()
		defer m.httpInFlightRequests.Dec()
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		m.httpRequestsTotal.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
		m.httpRequestDuration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
	})
}

// normalizeHTTPMethod limits the method label to a bounded enum. Clients can
// send arbitrary extension methods, so anything outside the standard set
// becomes "other" instead of creating unbounded label values.
func normalizeHTTPMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodHead, http.MethodOptions:
		return method
	default:
		return "other"
	}
}

// ObserveProbe records one probe result and its duration.
func (m *Metrics) ObserveProbe(result string, d time.Duration) {
	if m == nil {
		return
	}
	m.probeResults.WithLabelValues(result).Inc()
	m.probeDuration.WithLabelValues(result).Observe(d.Seconds())
}

// ObserveProbeSkipped records a skipped probe with a bounded reason.
func (m *Metrics) ObserveProbeSkipped(reason string) {
	if m == nil {
		return
	}
	m.probeSkipped.WithLabelValues(reason).Inc()
}

// ObserveAlert records one alert delivery and its duration.
func (m *Metrics) ObserveAlert(channelKind, status string, d time.Duration) {
	if m == nil {
		return
	}
	m.alertDeliveries.WithLabelValues(channelKind, status).Inc()
	m.alertDeliveryDuration.WithLabelValues(channelKind).Observe(d.Seconds())
}

// ObserveSchedulerTick records one scheduler tick, its enqueued count, and
// its duration.
func (m *Metrics) ObserveSchedulerTick(enqueued int, d time.Duration) {
	if m == nil {
		return
	}
	m.schedulerTicks.Inc()
	m.schedulerEnqueued.Add(float64(enqueued))
	m.schedulerTickDuration.Observe(d.Seconds())
}

// SetIncidentsOpen sets the current number of unresolved incidents.
func (m *Metrics) SetIncidentsOpen(n int) {
	if m == nil {
		return
	}
	m.incidentsOpen.Set(float64(n))
}

// SetQueueJobs sets the number of jobs in a queue/state.
func (m *Metrics) SetQueueJobs(queue, state string, n int) {
	if m == nil {
		return
	}
	m.queueJobs.WithLabelValues(queue, state).Set(float64(n))
}

// SetQueueOldestJobAge sets the age of the oldest runnable job in a queue.
func (m *Metrics) SetQueueOldestJobAge(queue string, seconds float64) {
	if m == nil {
		return
	}
	m.queueOldestJobAge.WithLabelValues(queue).Set(seconds)
}

// ObserveRollup records one rollup maintenance pass: its result, hours and
// rows written, and its duration.
func (m *Metrics) ObserveRollup(hours int, rows int64, d time.Duration, err error) {
	if m == nil {
		return
	}
	result := "success"
	if err != nil {
		result = "error"
	}
	m.rollupRuns.WithLabelValues(result).Inc()
	m.rollupHours.Add(float64(hours))
	m.rollupRows.Add(float64(rows))
	m.maintenanceDuration.WithLabelValues("rollup").Observe(d.Seconds())
}

// SetRollupWatermark publishes the last contiguous rolled-up hour as a Unix
// timestamp.
func (m *Metrics) SetRollupWatermark(t time.Time) {
	if m == nil {
		return
	}
	m.rollupWatermark.Set(float64(t.Unix()))
}

// ObserveRetention records one retention pass result and its duration.
func (m *Metrics) ObserveRetention(d time.Duration, err error) {
	if m == nil {
		return
	}
	result := "success"
	if err != nil {
		result = "error"
	}
	m.retentionRuns.WithLabelValues(result).Inc()
	m.maintenanceDuration.WithLabelValues("retention").Observe(d.Seconds())
}

// AddRetentionRows adds rows deleted by retention for a kind ("rollup" or
// "audit").
func (m *Metrics) AddRetentionRows(kind string, n int64) {
	if m == nil || n == 0 {
		return
	}
	m.retentionRows.WithLabelValues(kind).Add(float64(n))
}

// AddPartitionsDropped adds dropped raw probe partitions.
func (m *Metrics) AddPartitionsDropped(n int) {
	if m == nil || n == 0 {
		return
	}
	m.retentionPartitions.Add(float64(n))
}

// ObserveRetentionSkip records a skipped retention candidate with a bounded
// reason.
func (m *Metrics) ObserveRetentionSkip(reason string) {
	if m == nil {
		return
	}
	m.retentionSkipped.WithLabelValues(reason).Inc()
}

// statusRecorder captures the response status while preserving the
// underlying writer's optional interfaces.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

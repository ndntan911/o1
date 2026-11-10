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

		method := r.Method
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

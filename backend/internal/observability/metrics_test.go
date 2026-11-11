package observability_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"o1/internal/observability"
)

// gatherText renders a registry in the Prometheus text exposition format.
// Later observability tests reuse it.
func gatherText(t *testing.T, reg *prometheus.Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "/metrics", nil),
	)
	var buf bytes.Buffer
	buf.Write(rec.Body.Bytes())
	return buf.String()
}

func TestMetrics_HTTPMiddlewareBoundedLabels(t *testing.T) {
	m := observability.NewMetrics("o1-api", "test")
	route := func(r *http.Request) string {
		if strings.HasPrefix(r.URL.Path, "/api/v1/workspaces/") {
			return "GET /api/v1/workspaces/{workspaceID}"
		}
		return "unmatched"
	}
	h := m.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}), route)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/123e4567-e89b-12d3-a456-426614174000", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := gatherText(t, m.Registry())
	if !strings.Contains(body, `o1_http_requests_total{method="GET",route="GET /api/v1/workspaces/{workspaceID}",status="418"} 1`) {
		t.Fatalf("metrics body missing bounded labels:\n%s", body)
	}
	if strings.Contains(body, "123e4567") {
		t.Fatal("raw UUID leaked into metrics")
	}
}

func TestMetrics_HTTPMiddlewareNormalizesMethod(t *testing.T) {
	m := observability.NewMetrics("o1-api", "test")
	h := m.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), func(*http.Request) string { return "BREW /api/v1/me" })

	req := httptest.NewRequest("BREW", "/api/v1/me", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	body := gatherText(t, m.Registry())
	if !strings.Contains(body, `o1_http_requests_total{method="other",route="BREW /api/v1/me",status="204"} 1`) {
		t.Fatalf("metrics body missing normalized method:\n%s", body)
	}
	if strings.Contains(body, `method="BREW"`) {
		t.Fatal("client-controlled method leaked into metrics")
	}
}

func TestMetrics_HealthAndMetricsExcluded(t *testing.T) {
	m := observability.NewMetrics("o1-api", "test")
	called := false
	h := m.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}), func(*http.Request) string { return "GET /healthz" })
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("handler did not run")
	}
	if strings.Contains(gatherText(t, m.Registry()), "o1_http_requests_total") {
		t.Fatal("health request was recorded")
	}
}

func TestMetrics_NilIsSafe(t *testing.T) {
	var m *observability.Metrics
	m.ObserveProbe("up", time.Millisecond)
	m.ObserveAlert("email", "sent", time.Millisecond)
	m.ObserveSchedulerTick(3, time.Millisecond)
	m.ObserveProbeSkipped("replay")
	m.ObserveRollup(1, 2, time.Millisecond, nil)
	m.SetRollupWatermark(time.Now())
	m.ObserveRetention(time.Millisecond, nil)
	m.AddRetentionRows("rollup", 1)
	m.AddPartitionsDropped(1)
	m.ObserveRetentionSkip("dry_run")
}

func TestMetrics_Maintenance(t *testing.T) {
	m := observability.NewMetrics("o1-scheduler", "test")
	watermark := time.Unix(1000, 0).UTC()

	m.ObserveRollup(3, 12, time.Second, nil)
	m.SetRollupWatermark(watermark)
	m.ObserveRetention(time.Second, nil)
	m.AddRetentionRows("rollup", 4)
	m.AddRetentionRows("audit", 2)
	m.AddPartitionsDropped(1)
	m.ObserveRetentionSkip("dry_run")

	body := gatherText(t, m.Registry())
	for _, want := range []string{
		`o1_rollup_runs_total{result="success"} 1`,
		"o1_rollup_hours_total 3",
		"o1_rollup_rows_total 12",
		"o1_rollup_frontier_timestamp_seconds 1000",
		`o1_retention_runs_total{result="success"} 1`,
		`o1_retention_rows_deleted_total{kind="rollup"} 4`,
		`o1_retention_rows_deleted_total{kind="audit"} 2`,
		"o1_retention_partitions_dropped_total 1",
		`o1_retention_skipped_total{reason="dry_run"} 1`,
		`o1_maintenance_duration_seconds_count{job="rollup"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("maintenance metrics missing %s:\n%s", want, body)
		}
	}
}

func TestMetrics_RegistryContainsCatalog(t *testing.T) {
	m := observability.NewMetrics("o1-api", "test")
	h := m.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), func(*http.Request) string { return "GET /api/v1/me" })
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))
	m.SetIncidentsOpen(2)
	m.SetQueueJobs("probe", "available", 3)
	m.SetQueueOldestJobAge("probe", 42)
	m.ObserveProbe("up", time.Millisecond)
	m.ObserveAlert("email", "sent", time.Millisecond)
	m.ObserveSchedulerTick(1, time.Millisecond)
	m.ObserveProbeSkipped("replay")

	body := gatherText(t, m.Registry())
	for _, name := range []string{
		"o1_build_info",
		"o1_http_requests_total",
		"o1_http_request_duration_seconds",
		"o1_http_in_flight_requests",
		"o1_probe_results_total",
		"o1_probe_duration_seconds",
		"o1_probe_skipped_total",
		"o1_alert_deliveries_total",
		"o1_alert_delivery_duration_seconds",
		"o1_scheduler_ticks_total",
		"o1_scheduler_enqueued_total",
		"o1_scheduler_tick_duration_seconds",
		"o1_incidents_open",
		"o1_queue_jobs",
		"o1_queue_oldest_job_age_seconds",
		"go_goroutines",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("registry missing %s:\n%s", name, body)
		}
	}
}

package observability

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestPrometheusRulesMatchMetricCatalog keeps the deploy-time alert rules and
// the process metrics in sync. It is intentionally lightweight so CI can run
// it without promtool or a Prometheus server.
func TestPrometheusRulesMatchMetricCatalog(t *testing.T) {
	rulesPath := filepath.Join("..", "..", "..", "deploy", "observability", "prometheus-rules.yml")
	rules, err := os.ReadFile(rulesPath) //nolint:gosec // path is a fixed repository-relative artifact
	if err != nil {
		t.Fatalf("read %s: %v", rulesPath, err)
	}

	alerts := parseAlertExpressions(t, string(rules))
	if len(alerts) == 0 {
		t.Fatal("no alert expressions parsed from the rules file")
	}

	// Process-scoped alerts must pin the one job that owns the metric.
	// O1DBPoolSaturation is deliberately absent: every process exposes pool
	// metrics and the expression already aggregates by job.
	ownedBy := map[string]string{
		"O1SchedulerStalled":      "o1-scheduler",
		"O1HighErrorRate":         "o1-api",
		"O1HighLatency":           "o1-api",
		"O1QueueBacklog":          "o1-worker",
		"O1QueueOldestJob":        "o1-worker",
		"O1JobsDiscarded":         "o1-worker",
		"O1ProbeFailureSpike":     "o1-worker",
		"O1AlertDeliveryFailures": "o1-worker",
	}
	for alert, job := range ownedBy {
		expr, ok := alerts[alert]
		if !ok {
			t.Errorf("alert %s is missing from %s", alert, rulesPath)
			continue
		}
		if !strings.Contains(expr, `job="`+job+`"`) {
			t.Errorf("alert %s must pin job=%q, got expression:\n%s", alert, job, expr)
		}
	}

	catalog := metricCatalog
	metricPattern := regexp.MustCompile(`\b(?:o1_[a-zA-Z0-9_]+|up)\b`)
	for alert, expr := range alerts {
		for _, metric := range metricPattern.FindAllString(expr, -1) {
			if _, ok := catalog[metric]; !ok {
				t.Errorf("alert %s references %s, which is not in the metric catalog", alert, metric)
			}
		}
	}
}

// parseAlertExpressions returns alert name to expression. Each rule has
// "for:" after its (possibly multi-line) expression.
func parseAlertExpressions(t *testing.T, rules string) map[string]string {
	t.Helper()
	pattern := regexp.MustCompile(`(?ms)^\s*- alert:\s*(\w+).*?^\s*expr:\s*(.*?)^\s*for:`)
	matches := pattern.FindAllStringSubmatch(rules, -1)
	alerts := make(map[string]string, len(matches))
	for _, match := range matches {
		alerts[match[1]] = match[2]
	}
	return alerts
}

// metricCatalog is the set of metrics the rules may reference: the app's
// exported catalog plus the scrape metadata metric Prometheus owns. Keeping it
// explicit means the test also catches a rule that references a metric nobody
// instruments.
var metricCatalog = map[string]struct{}{
	"up": {},

	"o1_http_requests_total":                  {},
	"o1_http_request_duration_seconds_bucket": {},
	"o1_db_pool_connections":                  {},
	"o1_queue_jobs":                           {},
	"o1_queue_oldest_job_age_seconds":         {},
	"o1_probe_results_total":                  {},
	"o1_alert_deliveries_total":               {},
	"o1_scheduler_ticks_total":                {},
}

package evidence

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"o1/internal/incidents"
	"o1/internal/monitors"
)

func strPtr(value string) *string { return &value }

func TestProjections_DoNotLeakSecrets(t *testing.T) {
	m := monitors.Monitor{
		Name:    "api",
		URL:     "https://user:pass@example.com/health?token=secret",
		Headers: map[string]string{"Authorization": "Bearer secret"},
	}
	inc := incidents.IncidentDetail{Incident: incidents.Incident{MonitorName: "api"}}
	checks := Checks(
		[]monitors.ProbeResult{{Status: "down", Error: strPtr("timeout: dial tcp 10.0.0.1:443")}},
		monitors.Summary{Checks: 1, Down: 1},
		24,
	)
	delivery := Delivery(incidents.DeliveryOutcome{
		ChannelKind: "email",
		Event:       "opened",
		Status:      "sent",
		Attempts:    1,
		CreatedAt:   time.Now(),
	})
	blob, err := json.Marshal([]any{Monitor(m), Incident(inc), SeedTimeline(inc), Timeline(inc), checks, delivery})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{
		"https://", "Authorization", "Bearer", "user:pass", "token=secret", "10.0.0.1", "dial tcp",
	} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("leaked %q: %s", secret, blob)
		}
	}
}

func TestFailureClass(t *testing.T) {
	cases := map[string]string{
		"timeout: dial tcp":            "timeout",
		"dns":                          "dns",
		"ssrf_blocked: private ip":     "ssrf_blocked",
		"tls: certificate failure":     "tls",
		"connection: refused":          "connection",
		"redirect: too many redirects": "redirect",
		"status: unexpected":           "status",
		"TIMEOUT: request timed out":   "timeout",
		"probe failed":                 "unknown",
		"":                             "unknown",
	}
	for raw, want := range cases {
		if got := FailureClass(strPtr(raw)); got != want {
			t.Errorf("FailureClass(%q) = %q, want %q", raw, got, want)
		}
	}
	if got := FailureClass(nil); got != "unknown" {
		t.Errorf("FailureClass(nil) = %q, want unknown", got)
	}
}

func TestTimeline_AllowlistsAndTruncatesDetail(t *testing.T) {
	long := strings.Repeat("a", 120)
	detail := map[string]any{
		"previous":        "up",
		"next":            "down",
		"from":            "healthy",
		"to":              "failing",
		"reason":          long,
		"status":          "open",
		"sequence":        3,
		"secret":          "token=secret",
		"previous_status": "up",
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	inc := incidents.IncidentDetail{Events: []incidents.IncidentEvent{{
		ID:        uuid.New(),
		Kind:      "opened",
		Detail:    raw,
		CreatedAt: time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC),
	}}}

	events := Timeline(inc)
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	got := events[0].Detail
	if got["previous"] != "up" || got["next"] != "down" {
		t.Fatalf("allowlisted keys missing: %+v", got)
	}
	if _, ok := got["secret"]; ok {
		t.Fatalf("unknown key survived: %+v", got)
	}
	if _, ok := got["previous_status"]; ok {
		t.Fatalf("unknown key survived: %+v", got)
	}
	if len(got["reason"]) != 80 {
		t.Fatalf("reason length = %d, want 80", len(got["reason"]))
	}
	if got["sequence"] != "3" {
		t.Fatalf("sequence = %q, want 3", got["sequence"])
	}
	if len(got) != 7 {
		t.Fatalf("detail keys = %+v", got)
	}
}

func TestTimeline_DropsRawDetailThatCannotBeDecoded(t *testing.T) {
	inc := incidents.IncidentDetail{Events: []incidents.IncidentEvent{{
		Kind:   "opened",
		Detail: json.RawMessage(`not json`),
	}}}
	events := Timeline(inc)
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	if len(events[0].Detail) != 0 {
		t.Fatalf("detail = %+v, want empty", events[0].Detail)
	}
}

func TestSeedTimeline_HasNoIDsOrDetail(t *testing.T) {
	inc := incidents.IncidentDetail{Events: []incidents.IncidentEvent{{
		ID:        uuid.New(),
		Kind:      "opened",
		Detail:    json.RawMessage(`{"next":"down","secret":"token=secret"}`),
		CreatedAt: time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC),
	}}}

	blob, err := json.Marshal(SeedTimeline(inc))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(blob)
	for _, forbidden := range []string{"id", "detail", "token=secret", "down"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("seed timeline leaked %q: %s", forbidden, text)
		}
	}
}

func TestChecks_ComputesAggregatesWithoutErrors(t *testing.T) {
	results := []monitors.ProbeResult{
		{CheckedAt: time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC), Status: "up", LatencyMS: 100},
		{CheckedAt: time.Date(2026, 11, 24, 9, 1, 0, 0, time.UTC), Status: "up", LatencyMS: 200},
		{CheckedAt: time.Date(2026, 11, 24, 9, 2, 0, 0, time.UTC), Status: "down", LatencyMS: 300, Error: strPtr("timeout: dial tcp 10.0.0.1:443")},
		{CheckedAt: time.Date(2026, 11, 24, 9, 3, 0, 0, time.UTC), Status: "down", LatencyMS: 400, Error: strPtr("dns: lookup failed")},
	}
	view := Checks(results, monitors.Summary{Checks: 999, Down: 999, P95LatencyMS: 1}, 24)

	if view.WindowHours != 24 {
		t.Fatalf("window hours = %d", view.WindowHours)
	}
	if view.Count != 4 || view.Failures != 2 {
		t.Fatalf("count/failures = %d/%d", view.Count, view.Failures)
	}
	if math.Abs(view.P95LatencyMS-385) > 0.001 {
		t.Fatalf("p95 = %v, want 385", view.P95LatencyMS)
	}
	if view.FailureClasses["timeout"] != 1 || view.FailureClasses["dns"] != 1 || len(view.FailureClasses) != 2 {
		t.Fatalf("failure classes = %+v", view.FailureClasses)
	}
	if len(view.Checks) != 4 {
		t.Fatalf("checks = %d", len(view.Checks))
	}
	if view.Checks[2].FailureClass != "timeout" {
		t.Fatalf("third failure class = %q", view.Checks[2].FailureClass)
	}

	blob, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(blob)
	for _, forbidden := range []string{"10.0.0.1", "dial tcp", "lookup failed"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("checks leaked %q: %s", forbidden, text)
		}
	}
}

func TestChecks_Empty(t *testing.T) {
	view := Checks(nil, monitors.Summary{}, 1)
	if view.Count != 0 || view.Failures != 0 || view.P95LatencyMS != 0 {
		t.Fatalf("empty view = %+v", view)
	}
	if view.Checks == nil {
		t.Fatal("checks must marshal as an array")
	}
	if view.FailureClasses == nil {
		t.Fatal("failure classes must marshal as an object")
	}
}

func TestMonitor_MapsOnlySafeFields(t *testing.T) {
	checkedAt := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	monitorID := uuid.New()
	view := Monitor(monitors.Monitor{
		ID:                    monitorID,
		Name:                  "Checkout API",
		URL:                   "https://internal.example.com/health?token=abc",
		Method:                "GET",
		Headers:               map[string]string{"Authorization": "Bearer secret"},
		ExpectedStatus:        "200",
		IntervalSeconds:       60,
		TimeoutSeconds:        10,
		ConfirmationThreshold: 3,
		RecoveryThreshold:     2,
		Paused:                true,
		Status:                "down",
		LastCheck:             &monitors.LastCheck{CheckedAt: checkedAt},
	})

	if view.ID != monitorID || view.Name != "Checkout API" || view.Status != "down" {
		t.Fatalf("view = %+v", view)
	}
	if !view.Paused || view.LastCheckAt == nil || !view.LastCheckAt.Equal(checkedAt) {
		t.Fatalf("view = %+v", view)
	}
	blob, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"https://", "Authorization", "Bearer", "token=abc"} {
		if strings.Contains(string(blob), forbidden) {
			t.Fatalf("monitor leaked %q: %s", forbidden, blob)
		}
	}
}

func TestIncident_ComputesDurationAndKeepsIdentity(t *testing.T) {
	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	resolved := started.Add(12 * time.Minute)
	detail := incidents.IncidentDetail{Incident: incidents.Incident{
		ID:            uuid.New(),
		MonitorID:     uuid.New(),
		MonitorName:   "api",
		Status:        "resolved",
		StartedAt:     started,
		ResolvedAt:    &resolved,
		ReminderCount: 2,
	}}

	view := Incident(detail)
	if view.ID != detail.Incident.ID || view.MonitorID != detail.Incident.MonitorID {
		t.Fatalf("view = %+v", view)
	}
	if view.DurationMinutes != 12 || view.ReminderCount != 2 {
		t.Fatalf("view = %+v", view)
	}
}

func TestDelivery_MapsOnlySafeFields(t *testing.T) {
	sentAt := time.Date(2026, 11, 24, 9, 5, 0, 0, time.UTC)
	createdAt := sentAt.Add(-time.Minute)
	view := Delivery(incidents.DeliveryOutcome{
		ChannelKind: "email",
		Event:       "opened",
		Status:      "sent",
		Attempts:    2,
		SentAt:      &sentAt,
		CreatedAt:   createdAt,
	})

	if view.ChannelKind != "email" || view.Event != "opened" || view.Status != "sent" || view.Attempts != 2 {
		t.Fatalf("view = %+v", view)
	}
	if view.SentAt == nil || !view.SentAt.Equal(sentAt) || !view.CreatedAt.Equal(createdAt) {
		t.Fatalf("view = %+v", view)
	}
}

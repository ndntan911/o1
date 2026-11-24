// Package evidence is the single allowlisted projection boundary used by the
// insights prompt, the copilot seed, and the MCP tool registry. Every value a
// model may see is produced here, so redaction cannot drift between callers.
//
// It imports only the domain packages (incidents and monitors); it never
// imports insights or copilot and never reads a target.
package evidence

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"o1/internal/incidents"
	"o1/internal/monitors"
)

// MonitorView deliberately excludes URL, headers, body, and query string.
type MonitorView struct {
	ID                    uuid.UUID  `json:"id"`
	Name                  string     `json:"name"`
	Method                string     `json:"method"`
	ExpectedStatus        string     `json:"expected_status"`
	IntervalSeconds       int        `json:"interval_seconds"`
	TimeoutSeconds        int        `json:"timeout_seconds"`
	ConfirmationThreshold int        `json:"confirmation_threshold"`
	RecoveryThreshold     int        `json:"recovery_threshold"`
	Paused                bool       `json:"paused"`
	Status                string     `json:"status"`
	ActiveIncidentID      *uuid.UUID `json:"active_incident_id,omitempty"`
	LastCheckAt           *time.Time `json:"last_check_at,omitempty"`
}

// IncidentView is the incident's state without notification or recipient data.
type IncidentView struct {
	ID              uuid.UUID  `json:"id"`
	MonitorID       uuid.UUID  `json:"monitor_id"`
	MonitorName     string     `json:"monitor_name"`
	Status          string     `json:"status"`
	StartedAt       time.Time  `json:"started_at"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
	DurationMinutes int        `json:"duration_minutes"`
	ReminderCount   int        `json:"reminder_count"`
}

// SeedEventView is a timeline entry for the seed: kind and timestamp only, with
// no event id and no free text.
type SeedEventView struct {
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
}

// EventView is one timeline entry with an allowlisted detail map. Keys outside
// the allowlist are dropped rather than passed through.
type EventView struct {
	ID     uuid.UUID         `json:"id"`
	Kind   string            `json:"kind"`
	At     time.Time         `json:"at"`
	Detail map[string]string `json:"detail"`
}

// CheckView is one probe result. The stored error message is never copied;
// only its bounded class survives.
type CheckView struct {
	CheckedAt    time.Time `json:"checked_at"`
	Status       string    `json:"status"`
	StatusCode   *int      `json:"status_code"`
	LatencyMS    int       `json:"latency_ms"`
	FailureClass string    `json:"failure_class"`
}

// ChecksView summarizes the requested window. Count, failures, p95, and the
// failure-class counts come from the results passed in, never from raw errors.
type ChecksView struct {
	WindowHours    int            `json:"window_hours"`
	Checks         []CheckView    `json:"checks"`
	Count          int            `json:"count"`
	Failures       int            `json:"failures"`
	P95LatencyMS   float64        `json:"p95_latency_ms"`
	FailureClasses map[string]int `json:"failure_classes"`
}

// DeliveryView is an alert outcome without recipients, channel names, or
// error text.
type DeliveryView struct {
	ChannelKind string     `json:"channel_kind"`
	Event       string     `json:"event"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	SentAt      *time.Time `json:"sent_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// allowedDetailKeys is the complete set of incident-event detail keys that may
// leave the system. Everything else is dropped.
var allowedDetailKeys = map[string]struct{}{
	"previous": {},
	"next":     {},
	"from":     {},
	"to":       {},
	"reason":   {},
	"status":   {},
	"sequence": {},
}

var failureClasses = map[string]struct{}{
	"timeout":      {},
	"dns":          {},
	"tls":          {},
	"connection":   {},
	"redirect":     {},
	"status":       {},
	"ssrf_blocked": {},
}

const detailValueLimit = 80

// FailureClass maps a stored probe error prefix onto a bounded class. The
// message after the first colon is never returned.
func FailureClass(raw *string) string {
	if raw == nil {
		return "unknown"
	}
	value := strings.ToLower(strings.TrimSpace(*raw))
	if index := strings.IndexByte(value, ':'); index >= 0 {
		value = value[:index]
	}
	value = strings.TrimSpace(value)
	if _, ok := failureClasses[value]; ok {
		return value
	}
	return "unknown"
}

// Monitor projects a monitor without its URL, headers, body, or query string.
func Monitor(m monitors.Monitor) MonitorView {
	view := MonitorView{
		ID:                    m.ID,
		Name:                  m.Name,
		Method:                m.Method,
		ExpectedStatus:        m.ExpectedStatus,
		IntervalSeconds:       m.IntervalSeconds,
		TimeoutSeconds:        m.TimeoutSeconds,
		ConfirmationThreshold: m.ConfirmationThreshold,
		RecoveryThreshold:     m.RecoveryThreshold,
		Paused:                m.Paused,
		Status:                m.Status,
	}
	if m.LastCheck != nil {
		checkedAt := m.LastCheck.CheckedAt
		view.LastCheckAt = &checkedAt
	}
	return view
}

// Incident projects an incident, computing its duration from the resolution
// time or, while it is still open, from now.
func Incident(d incidents.IncidentDetail) IncidentView {
	return incidentAt(d, time.Now())
}

func incidentAt(d incidents.IncidentDetail, now time.Time) IncidentView {
	inc := d.Incident
	end := now
	if inc.ResolvedAt != nil {
		end = *inc.ResolvedAt
	}
	duration := int(end.Sub(inc.StartedAt).Minutes())
	if duration < 0 {
		duration = 0
	}
	return IncidentView{
		ID:              inc.ID,
		MonitorID:       inc.MonitorID,
		MonitorName:     inc.MonitorName,
		Status:          inc.Status,
		StartedAt:       inc.StartedAt,
		ResolvedAt:      inc.ResolvedAt,
		DurationMinutes: duration,
		ReminderCount:   inc.ReminderCount,
	}
}

// SeedTimeline projects timeline entries as kind and timestamp only.
func SeedTimeline(d incidents.IncidentDetail) []SeedEventView {
	events := make([]SeedEventView, 0, len(d.Events))
	for _, event := range d.Events {
		events = append(events, SeedEventView{Kind: event.Kind, At: event.CreatedAt})
	}
	return events
}

// Timeline projects timeline entries with event ids and the allowlisted detail
// keys only, each value stringified and truncated.
func Timeline(d incidents.IncidentDetail) []EventView {
	events := make([]EventView, 0, len(d.Events))
	for _, event := range d.Events {
		events = append(events, EventView{
			ID:     event.ID,
			Kind:   event.Kind,
			At:     event.CreatedAt,
			Detail: allowlistedDetail(event.Detail),
		})
	}
	return events
}

func allowlistedDetail(raw json.RawMessage) map[string]string {
	out := make(map[string]string, len(allowedDetailKeys))
	if len(raw) == 0 {
		return out
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return out
	}
	for key, encoded := range decoded {
		if _, ok := allowedDetailKeys[key]; !ok {
			continue
		}
		var value any
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			continue
		}
		out[key] = truncate(detailValue(value), detailValueLimit)
	}
	return out
}

func detailValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case bool:
		return strconv.FormatBool(typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// Checks projects probe results and derives the aggregates from them. The
// summary is accepted for call-site symmetry but is not trusted for redaction:
// every reported value comes from the result slice.
func Checks(results []monitors.ProbeResult, _ monitors.Summary, hours int) ChecksView {
	view := ChecksView{
		WindowHours:    hours,
		Checks:         make([]CheckView, 0, len(results)),
		Count:          len(results),
		FailureClasses: make(map[string]int),
	}
	latencies := make([]int, 0, len(results))
	for _, result := range results {
		class := FailureClass(result.Error)
		view.Checks = append(view.Checks, CheckView{
			CheckedAt:    result.CheckedAt,
			Status:       result.Status,
			StatusCode:   result.StatusCode,
			LatencyMS:    result.LatencyMS,
			FailureClass: class,
		})
		latencies = append(latencies, result.LatencyMS)
		if result.Status == "down" {
			view.Failures++
			view.FailureClasses[class]++
		}
	}
	view.P95LatencyMS = percentile95(latencies)
	return view
}

// percentile95 matches percentile_cont(0.95): linear interpolation over the
// sorted sample.
func percentile95(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	rank := 0.95 * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper {
		return float64(sorted[lower])
	}
	fraction := rank - float64(lower)
	return float64(sorted[lower]) + fraction*(float64(sorted[upper])-float64(sorted[lower]))
}

// Delivery projects an alert outcome without recipients or error text.
func Delivery(d incidents.DeliveryOutcome) DeliveryView {
	return DeliveryView{
		ChannelKind: d.ChannelKind,
		Event:       d.Event,
		Status:      d.Status,
		Attempts:    d.Attempts,
		SentAt:      d.SentAt,
		CreatedAt:   d.CreatedAt,
	}
}

package insights

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"o1/internal/evidence"
	"o1/internal/incidents"
	"o1/internal/monitors"
)

// Payload is everything the model is allowed to see. Every field here is
// copied from the incident's own records; nothing is fetched from the target.
type Payload struct {
	Monitor    MonitorView    `json:"monitor"`
	Incident   IncidentView   `json:"incident"`
	Timeline   []EventView    `json:"timeline"`
	Deliveries []DeliveryView `json:"deliveries"`
}

// MonitorView deliberately excludes URL, headers and body.
type MonitorView struct {
	Name                  string `json:"name"` // untrusted user text
	Method                string `json:"method"`
	ExpectedStatus        string `json:"expected_status"`
	IntervalSeconds       int    `json:"interval_seconds"`
	TimeoutSeconds        int    `json:"timeout_seconds"`
	ConfirmationThreshold int    `json:"confirmation_threshold"`
	RecoveryThreshold     int    `json:"recovery_threshold"`
}

// IncidentView is the incident's state without identity or notification data.
type IncidentView struct {
	Status        string     `json:"status"`
	StartedAt     time.Time  `json:"started_at"`
	ResolvedAt    *time.Time `json:"resolved_at,omitempty"`
	ReminderCount int        `json:"reminder_count"`
	DurationMin   int        `json:"duration_minutes"`
}

// EventView is one timeline entry.
type EventView struct {
	ID     uuid.UUID `json:"id"`
	Kind   string    `json:"kind"`
	At     time.Time `json:"at"`
	Detail string    `json:"detail,omitempty"`
}

// DeliveryView is an alert outcome; recipients and channel names are excluded.
type DeliveryView struct {
	ChannelKind string `json:"channel_kind"`
	Event       string `json:"event"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
}

// BuildPayload redacts incident records into the model's input. Monitor
// configuration is optional; when absent only the incident's own fields are
// used.
func BuildPayload(
	detail incidents.IncidentDetail,
	deliveries []incidents.Delivery,
	monitor *monitors.Monitor,
	now time.Time,
) Payload {
	inc := detail.Incident

	source := monitors.Monitor{Name: inc.MonitorName}
	if monitor != nil {
		source = *monitor
	}
	projectedMonitor := evidence.Monitor(source)
	view := MonitorView{
		Name:                  projectedMonitor.Name,
		Method:                projectedMonitor.Method,
		ExpectedStatus:        projectedMonitor.ExpectedStatus,
		IntervalSeconds:       projectedMonitor.IntervalSeconds,
		TimeoutSeconds:        projectedMonitor.TimeoutSeconds,
		ConfirmationThreshold: projectedMonitor.ConfirmationThreshold,
		RecoveryThreshold:     projectedMonitor.RecoveryThreshold,
	}

	projectedIncident := evidence.Incident(detail)
	// Duration is the only field that depends on the caller's clock. Keep the
	// injected time so the payload hash stays deterministic in tests.
	end := now
	if inc.ResolvedAt != nil {
		end = *inc.ResolvedAt
	}
	duration := int(end.Sub(inc.StartedAt).Minutes())
	if duration < 0 {
		duration = 0
	}
	projectedIncident.DurationMinutes = duration

	projectedEvents := evidence.Timeline(detail)
	events := make([]EventView, 0, len(projectedEvents))
	for _, event := range projectedEvents {
		detailText := "{}"
		if encoded, err := json.Marshal(event.Detail); err == nil {
			detailText = string(encoded)
		}
		events = append(events, EventView{
			ID:     event.ID,
			Kind:   event.Kind,
			At:     event.At,
			Detail: detailText,
		})
	}

	dels := make([]DeliveryView, 0, len(deliveries))
	for _, delivery := range deliveries {
		projectedDelivery := evidence.Delivery(incidents.DeliveryOutcome{
			ChannelKind: delivery.ChannelKind,
			Event:       delivery.Event,
			Status:      delivery.Status,
			Attempts:    delivery.Attempts,
			SentAt:      delivery.SentAt,
			CreatedAt:   delivery.CreatedAt,
		})
		dels = append(dels, DeliveryView{
			ChannelKind: projectedDelivery.ChannelKind,
			Event:       projectedDelivery.Event,
			Status:      projectedDelivery.Status,
			Attempts:    projectedDelivery.Attempts,
		})
	}

	return Payload{
		Monitor: view,
		Incident: IncidentView{
			Status:        projectedIncident.Status,
			StartedAt:     projectedIncident.StartedAt,
			ResolvedAt:    projectedIncident.ResolvedAt,
			ReminderCount: projectedIncident.ReminderCount,
			DurationMin:   projectedIncident.DurationMinutes,
		},
		Timeline:   events,
		Deliveries: dels,
	}
}

// Hash fingerprints the payload together with the prompt version, so a rerun
// over unchanged data can be recognised and skipped.
func Hash(payload Payload, promptVersion string) string {
	encoded, err := json.Marshal(struct {
		Version string  `json:"version"`
		Payload Payload `json:"payload"`
	}{Version: promptVersion, Payload: payload})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// EventIDs returns the set of timeline ids a citation may point at.
func EventIDs(detail incidents.IncidentDetail) map[uuid.UUID]struct{} {
	ids := make(map[uuid.UUID]struct{}, len(detail.Events))
	for _, event := range detail.Events {
		ids[event.ID] = struct{}{}
	}
	return ids
}

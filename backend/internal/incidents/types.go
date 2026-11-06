// Package incidents implements the incident lifecycle: opening and resolving
// incidents from probe state transitions, timeline events, and alert delivery
// enqueueing.
package incidents

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// Transition describes one monitor state change produced by a probe.
type Transition struct {
	WorkspaceID uuid.UUID
	MonitorID   uuid.UUID
	Previous    string
	Next        string
	At          time.Time
}

// JobInserter inserts River jobs inside the caller's transaction.
// river.Client[pgx.Tx] satisfies this interface.
type JobInserter interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// AlertEmail carries the values needed to render one incident alert. MonitorURL
// must already be redacted, and Recipient is the single delivery recipient.
type AlertEmail struct {
	WorkspaceName string
	WorkspaceSlug string
	MonitorName   string
	MonitorURL    string // already redacted
	Event         string // opened, reminder, resolved
	Sequence      int
	IncidentID    uuid.UUID
	StartedAt     time.Time
	BaseURL       string
	Recipient     string
}

// EmailConfig is the alert channel configuration shape for email channels.
type EmailConfig struct {
	Recipients []string `json:"recipients"`
}

// AlertChannel is the API-facing view of a workspace alert channel. Recipients
// are decoded from the channel config and must only be returned to admins.
type AlertChannel struct {
	ID         uuid.UUID `json:"id"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	Enabled    bool      `json:"enabled"`
	Recipients []string  `json:"recipients"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// MonitorAlertState is a monitor's per-channel alert switch. Available is
// false when the workspace has no email channel; recipients are never exposed
// through this view.
type MonitorAlertState struct {
	Available      bool `json:"available"`
	ChannelEnabled bool `json:"channel_enabled"`
	Enabled        bool `json:"enabled"`
}

// Incident is the API-facing view of an incident. It deliberately omits
// monitor secrets such as headers, bodies, and query strings.
type Incident struct {
	ID             uuid.UUID  `json:"id"`
	WorkspaceID    uuid.UUID  `json:"workspace_id"`
	MonitorID      uuid.UUID  `json:"monitor_id"`
	MonitorName    string     `json:"monitor_name"`
	Status         string     `json:"status"`
	StartedAt      time.Time  `json:"started_at"`
	AcknowledgedAt *time.Time `json:"acknowledged_at"`
	AcknowledgedBy *uuid.UUID `json:"acknowledged_by"`
	ResolvedAt     *time.Time `json:"resolved_at"`
	LastNotifiedAt *time.Time `json:"last_notified_at"`
	ReminderCount  int        `json:"reminder_count"`
}

// IncidentEvent is one entry in an incident timeline. Detail carries only
// non-secret context such as previous and new probe status.
type IncidentEvent struct {
	ID          uuid.UUID       `json:"id"`
	Kind        string          `json:"kind"`
	ActorUserID *uuid.UUID      `json:"actor_user_id"`
	Detail      json.RawMessage `json:"detail"`
	CreatedAt   time.Time       `json:"created_at"`
}

// IncidentDetail is the incident plus its timeline, ordered oldest first.
type IncidentDetail struct {
	Incident Incident        `json:"incident"`
	Events   []IncidentEvent `json:"events"`
}

// Delivery is the API-facing view of one alert delivery. It exposes delivery
// state to workspace admins without leaking monitor secrets.
type Delivery struct {
	ID          uuid.UUID  `json:"id"`
	ChannelID   uuid.UUID  `json:"channel_id"`
	ChannelKind string     `json:"channel_kind"`
	ChannelName string     `json:"channel_name"`
	Event       string     `json:"event"`
	Sequence    int        `json:"sequence"`
	Recipient   string     `json:"recipient"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	LastError   *string    `json:"last_error"`
	SentAt      *time.Time `json:"sent_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// IncidentFilter selects a page of incidents. Status accepts active, open,
// acknowledged, resolved, or all; an empty status means active.
type IncidentFilter struct {
	Status    string
	MonitorID *uuid.UUID
}

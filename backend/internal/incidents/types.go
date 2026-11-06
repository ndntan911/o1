// Package incidents implements the incident lifecycle: opening and resolving
// incidents from probe state transitions, timeline events, and alert delivery
// enqueueing.
package incidents

import (
	"context"
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

// EmailConfig is the alert channel configuration shape for email channels.
type EmailConfig struct {
	Recipients []string `json:"recipients"`
}

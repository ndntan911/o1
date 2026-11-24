package jobs

import (
	"github.com/google/uuid"
)

// InvestigationArgs identifies one copilot investigation to run. The
// investigation id is unique, so a retried insert cannot enqueue the same run
// twice while the original job is still pending, scheduled, running, or
// available.
type InvestigationArgs struct {
	InvestigationID uuid.UUID `json:"investigation_id" river:"unique"`
}

// Kind is the River job kind.
func (InvestigationArgs) Kind() string { return "investigation" }

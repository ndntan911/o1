// Package jobs defines River job payloads.
package jobs

import (
	"time"

	"github.com/google/uuid"
)

// ProbeArgs identifies one scheduled probe of a monitor. The monitor ID and
// scheduled time together make the job unique, so a retried scheduler tick
// cannot enqueue the same probe twice.
type ProbeArgs struct {
	MonitorID    uuid.UUID `json:"monitor_id" river:"unique"`
	ScheduledFor time.Time `json:"scheduled_for" river:"unique"`
}

// Kind is the River job kind.
func (ProbeArgs) Kind() string { return "probe" }

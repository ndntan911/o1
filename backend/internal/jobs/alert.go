// Package jobs defines River job payloads.
package jobs

import (
	"github.com/google/uuid"
)

// AlertArgs identifies one alert delivery email to send. The delivery ID is
// unique, so retried inserts cannot enqueue the same email twice while the
// original job is still pending, scheduled, running, or available.
type AlertArgs struct {
	DeliveryID uuid.UUID `json:"delivery_id" river:"unique"`
}

// Kind is the River job kind.
func (AlertArgs) Kind() string { return "alert" }

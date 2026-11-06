package incidents

import (
	"errors"

	"o1/internal/workspaces"
)

// Sentinel errors returned by the service. Handlers map them onto HTTP
// responses; errors.Is callers can branch on them.
var (
	// ErrInvalidChannelConfig identifies malformed alert channel configuration.
	// The probe lifecycle skips malformed channels instead of failing the probe
	// transaction; API validation can use this sentinel when rejecting config.
	ErrInvalidChannelConfig = errors.New("invalid alert channel config")
	// ErrValidation is returned for invalid input.
	ErrValidation = workspaces.ErrValidation
	// ErrNotFound is returned when the workspace, incident, or target resource
	// does not exist, including for workspaces the caller cannot see.
	ErrNotFound = workspaces.ErrNotFound
	// ErrForbidden is returned when the caller is a member but lacks the
	// required role.
	ErrForbidden = workspaces.ErrForbidden
	// ErrSuspended is returned when a mutation targets a suspended workspace.
	// Reads remain available.
	ErrSuspended = errors.New("workspace suspended")
	// ErrIncidentResolved is returned when an acknowledgement targets a
	// resolved incident.
	ErrIncidentResolved = errors.New("incident resolved")
	// ErrAlertChannelMissing is returned when a workspace has no matching
	// alert channel.
	ErrAlertChannelMissing = errors.New("alert channel missing")
)

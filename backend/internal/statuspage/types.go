// Package statuspage implements the public status page: admin configuration,
// public payload aggregation, an in-process response cache, and HTTP handlers.
package statuspage

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"o1/internal/workspaces"
)

// Sentinel errors returned by the service. Handlers map them onto HTTP
// responses; errors.Is callers can branch on them.
var (
	// ErrNotFound is returned for pages or workspaces the caller cannot see.
	// Cross-tenant lookups must not reveal whether a resource exists.
	ErrNotFound = workspaces.ErrNotFound
	// ErrForbidden is returned when the caller is a member but lacks the
	// required role.
	ErrForbidden = workspaces.ErrForbidden
	// ErrValidation is returned for invalid input. Validation failures carry
	// a *ValidationError with per-field messages.
	ErrValidation = workspaces.ErrValidation
	// ErrSuspended is returned when a mutation targets a suspended workspace.
	ErrSuspended = errors.New("workspace suspended")
)

// ValidationError reports invalid input with one message per JSON field.
type ValidationError struct {
	Fields map[string]string
}

// Error renders the field errors in a stable order.
func (e *ValidationError) Error() string {
	fields := make([]string, 0, len(e.Fields))
	for field := range e.Fields {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, field+": "+e.Fields[field])
	}
	return "validation error: " + strings.Join(parts, "; ")
}

// Is reports ValidationError as workspaces.ErrValidation so errors.Is works.
func (e *ValidationError) Is(target error) bool {
	return target == workspaces.ErrValidation
}

// Config is the saved status page configuration returned to admins.
// MonitorIDs is always a non-nil slice so it serializes as [] rather than
// null when the page has no monitors.
type Config struct {
	Enabled     bool        `json:"enabled"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	MonitorIDs  []uuid.UUID `json:"monitor_ids"`
}

// ConfigInput is the request body for saving a status page configuration.
type ConfigInput struct {
	Enabled     bool        `json:"enabled"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	MonitorIDs  []uuid.UUID `json:"monitor_ids"`
}

// PublicWorkspace identifies the workspace a public page belongs to without
// exposing its internal ID.
type PublicWorkspace struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// PublicPageInfo is the configured page content and last update time.
type PublicPageInfo struct {
	Title       string    `json:"title"`
	Description string    `json:"description"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PublicDay is one UTC day of uptime for a monitor. Uptime is nil when the
// day has no checks, which serializes as null.
type PublicDay struct {
	Date   string   `json:"date"`
	Status string   `json:"status"`
	Uptime *float64 `json:"uptime"`
}

// PublicMonitor is one publicly selected monitor with its current status and
// daily uptime buckets. Uptime is nil when the monitor has no checks.
type PublicMonitor struct {
	ID     uuid.UUID   `json:"id"`
	Name   string      `json:"name"`
	Status string      `json:"status"`
	Uptime *float64    `json:"uptime"`
	Days   []PublicDay `json:"days"`
}

// PublicIncident is an active or recently resolved incident on a selected
// monitor. It omits acknowledgement, identity, and delivery details.
type PublicIncident struct {
	ID          uuid.UUID  `json:"id"`
	MonitorID   uuid.UUID  `json:"monitor_id"`
	MonitorName string     `json:"monitor_name"`
	Status      string     `json:"status"`
	StartedAt   time.Time  `json:"started_at"`
	ResolvedAt  *time.Time `json:"resolved_at"`
}

// PublicIncidents groups ongoing and recently resolved incidents.
type PublicIncidents struct {
	Active []PublicIncident `json:"active"`
	Recent []PublicIncident `json:"recent"`
}

// PublicPage is the full public status payload for one workspace.
type PublicPage struct {
	Workspace PublicWorkspace `json:"workspace"`
	Page      PublicPageInfo  `json:"page"`
	Monitors  []PublicMonitor `json:"monitors"`
	Incidents PublicIncidents `json:"incidents"`
}

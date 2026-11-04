// Package monitors implements the monitor domain: CRUD, per-workspace
// quotas, creation rate limiting, and encryption of secrets at rest.
package monitors

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"o1/internal/workspaces"
)

// Sentinel errors returned by the service. Handlers map them onto HTTP
// responses; errors.Is callers can branch on them.
var (
	// ErrValidation is returned for invalid input. Validation failures carry
	// a *ValidationError with per-field messages.
	ErrValidation = workspaces.ErrValidation
	// ErrNotFound is returned when the workspace or monitor does not exist,
	// including for workspaces the caller cannot see. Cross-tenant lookups
	// must not reveal whether a resource exists.
	ErrNotFound = workspaces.ErrNotFound
	// ErrForbidden is returned when the caller is a member but lacks the
	// required role.
	ErrForbidden = workspaces.ErrForbidden
	// ErrSuspended is returned when a mutation targets a suspended
	// workspace. Reads remain available.
	ErrSuspended = errors.New("workspace suspended")
	// ErrQuotaExceeded is returned when the workspace already has
	// monitor_limit monitors.
	ErrQuotaExceeded = errors.New("quota exceeded")
)

// ValidationError reports invalid input with one message per JSON field.
type ValidationError struct {
	Fields map[string]string

	// cause preserves the underlying validation failure (for example
	// probe.ErrBlocked) without exposing it to clients.
	cause error
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

// Is reports ValidationError as ErrValidation so errors.Is works.
func (e *ValidationError) Is(target error) bool {
	return target == ErrValidation
}

// Unwrap exposes the underlying cause, if any.
func (e *ValidationError) Unwrap() error {
	return e.cause
}

// RateLimitError reports that the per-workspace creation rate limit was hit.
// RetryAfter is the time until the oldest creation in the window leaves it.
type RateLimitError struct {
	RetryAfter time.Duration
}

// Error implements error.
func (e *RateLimitError) Error() string {
	return fmt.Sprintf("monitor creation rate limit exceeded; retry after %s", e.RetryAfter.Round(time.Second))
}

// Package apikeys implements workspace-scoped API key management: creating,
// listing, and revoking keys for admins and owners.
package apikeys

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
	// ErrSuspended is returned when a mutation targets a suspended workspace.
	ErrSuspended = errors.New("workspace suspended")
	// ErrNameTaken is returned when an active key already uses the name in the
	// workspace.
	ErrNameTaken = errors.New("api key name already taken")
	// ErrLimit is returned when a workspace has reached the active key limit.
	ErrLimit = errors.New("api key limit reached")
	// ErrNotFound is returned for keys or workspaces the caller cannot see.
	// Cross-tenant lookups must not reveal whether a resource exists.
	ErrNotFound = workspaces.ErrNotFound
	// ErrForbidden is returned when the caller lacks the required role.
	ErrForbidden = workspaces.ErrForbidden
	// ErrValidation is returned for invalid input. Validation failures carry a
	// *ValidationError with per-field messages.
	ErrValidation = workspaces.ErrValidation
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

// Key is the client-facing view of an API key. It never carries a secret or
// the stored token hash. Prefix is rendered as o1_<stored prefix>.
type Key struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Role       string     `json:"role"`
	CreatedBy  uuid.UUID  `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// CreatedKey is returned exactly once, from Create, with the plaintext secret.
type CreatedKey struct {
	Key    Key    `json:"key"`
	Secret string `json:"secret"`
}

// CreateInput is the request body for creating an API key.
type CreateInput struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

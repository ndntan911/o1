// Package admin implements cross-tenant administrator controls: workspace
// suspension and an audit view of monitors and security events.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/db/sqlc"
	"o1/internal/monitors"
	"o1/internal/workspaces"
)

// Sentinel errors mirror the workspace service so handlers can map them.
var (
	ErrValidation = workspaces.ErrValidation
	ErrNotFound   = workspaces.ErrNotFound
)

const (
	maxReasonLen   = 500
	defaultLimit   = 50
	maxLimit       = 100
	eventSuspended = "workspace_suspended"
	eventUnsuspend = "workspace_unsuspended"
)

// Service exposes administrator operations. Authorization is performed by the
// requireAdmin middleware; these methods are cross-tenant by design.
type Service struct {
	pool *pgxpool.Pool
	q    *sqlc.Queries
}

// NewService builds a Service.
func NewService(pool *pgxpool.Pool, q *sqlc.Queries) *Service {
	return &Service{pool: pool, q: q}
}

// AdminWorkspace is an administrator-facing workspace row.
type AdminWorkspace struct {
	ID              uuid.UUID  `json:"id"`
	Name            string     `json:"name"`
	Slug            string     `json:"slug"`
	CreatedBy       uuid.UUID  `json:"created_by"`
	CreatedAt       time.Time  `json:"created_at"`
	MonitorLimit    int        `json:"monitor_limit"`
	SuspendedAt     *time.Time `json:"suspended_at"`
	SuspendedBy     *uuid.UUID `json:"suspended_by"`
	SuspendedReason *string    `json:"suspended_reason"`
	MemberCount     int64      `json:"member_count"`
	MonitorCount    int64      `json:"monitor_count"`
}

// AdminMonitor is an administrator-facing monitor row. The URL has its query
// string and fragment removed.
type AdminMonitor struct {
	ID             uuid.UUID  `json:"id"`
	WorkspaceID    uuid.UUID  `json:"workspace_id"`
	WorkspaceName  string     `json:"workspace_name"`
	WorkspaceSlug  string     `json:"workspace_slug"`
	Name           string     `json:"name"`
	URL            string     `json:"url"`
	Method         string     `json:"method"`
	ExpectedStatus string     `json:"expected_status"`
	IntervalSecond int        `json:"interval_seconds"`
	TimeoutSeconds int        `json:"timeout_seconds"`
	Paused         bool       `json:"paused"`
	Status         string     `json:"status"`
	LastCheckedAt  *time.Time `json:"last_checked_at"`
	LastStatusCode *int       `json:"last_status_code"`
	LastLatencyMS  *int       `json:"last_latency_ms"`
	LastError      *string    `json:"last_error"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// SecurityEvent is an abuse signal audit row.
type SecurityEvent struct {
	ID          uuid.UUID       `json:"id"`
	WorkspaceID *uuid.UUID      `json:"workspace_id"`
	ActorUserID *uuid.UUID      `json:"actor_user_id"`
	Kind        string          `json:"kind"`
	Detail      json.RawMessage `json:"detail"`
	CreatedAt   time.Time       `json:"created_at"`
}

// ListWorkspaces returns workspaces newest first.
func (s *Service) ListWorkspaces(ctx context.Context, query string, suspended *bool, cursor *monitors.Cursor, limit int) ([]AdminWorkspace, error) {
	limit = clampLimit(limit)
	params := sqlc.ListWorkspacesForAdminParams{
		Suspended: suspended,
		PageLimit: int32(limit), //nolint:gosec // clamped to 100
	}
	if q := strings.TrimSpace(query); q != "" {
		params.Query = &q
	}
	if cursor != nil {
		t := cursor.Time
		params.AfterTime = &t
		params.AfterID = cursor.ID
	}
	rows, err := s.q.ListWorkspacesForAdmin(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]AdminWorkspace, 0, len(rows))
	for _, row := range rows {
		out = append(out, adminWorkspace(row))
	}
	return out, nil
}

// GetWorkspace returns one workspace by ID.
func (s *Service) GetWorkspace(ctx context.Context, id uuid.UUID) (AdminWorkspace, error) {
	ws, err := s.q.GetWorkspaceByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AdminWorkspace{}, ErrNotFound
		}
		return AdminWorkspace{}, fmt.Errorf("load workspace: %w", err)
	}
	return AdminWorkspace{
		ID:              ws.ID,
		Name:            ws.Name,
		Slug:            ws.Slug,
		CreatedBy:       ws.CreatedBy,
		CreatedAt:       ws.CreatedAt,
		MonitorLimit:    int(ws.MonitorLimit),
		SuspendedAt:     ws.SuspendedAt,
		SuspendedBy:     uuidPtr(ws.SuspendedBy),
		SuspendedReason: ws.SuspendedReason,
	}, nil
}

// Suspend suspends a workspace and records an audit event. Repeated calls
// succeed and record another event.
func (s *Service) Suspend(ctx context.Context, adminID, workspaceID uuid.UUID, reason string) error {
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n < 1 || n > maxReasonLen {
		return fmt.Errorf("%w: reason must be between 1 and %d characters", ErrValidation, maxReasonLen)
	}
	if _, err := s.q.GetWorkspaceByID(ctx, workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load workspace: %w", err)
	}
	if err := s.q.SuspendWorkspace(ctx, sqlc.SuspendWorkspaceParams{
		ID:              workspaceID,
		SuspendedBy:     pgtype.UUID{Bytes: adminID, Valid: true},
		SuspendedReason: &reason,
	}); err != nil {
		return fmt.Errorf("suspend workspace: %w", err)
	}
	return s.recordEvent(ctx, workspaceID, &adminID, eventSuspended, map[string]any{"reason": reason})
}

// Unsuspend clears a workspace suspension and records an audit event.
func (s *Service) Unsuspend(ctx context.Context, adminID, workspaceID uuid.UUID) error {
	if _, err := s.q.GetWorkspaceByID(ctx, workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load workspace: %w", err)
	}
	if err := s.q.UnsuspendWorkspace(ctx, workspaceID); err != nil {
		return fmt.Errorf("unsuspend workspace: %w", err)
	}
	return s.recordEvent(ctx, workspaceID, &adminID, eventUnsuspend, map[string]any{})
}

// ListMonitors returns monitors across tenants, newest first, with URLs
// redacted.
func (s *Service) ListMonitors(ctx context.Context, workspaceID *uuid.UUID, query string, cursor *monitors.Cursor, limit int) ([]AdminMonitor, error) {
	limit = clampLimit(limit)
	params := sqlc.ListMonitorsForAdminParams{PageLimit: int32(limit)} //nolint:gosec // clamped to 100
	if workspaceID != nil {
		params.WorkspaceID = pgtype.UUID{Bytes: *workspaceID, Valid: true}
	}
	if q := strings.TrimSpace(query); q != "" {
		params.Query = &q
	}
	if cursor != nil {
		t := cursor.Time
		params.AfterTime = &t
		params.AfterID = cursor.ID
	}
	rows, err := s.q.ListMonitorsForAdmin(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]AdminMonitor, 0, len(rows))
	for _, row := range rows {
		status := ""
		if row.Status != nil {
			status = *row.Status
		}
		if row.Paused {
			status = "paused"
		}
		out = append(out, AdminMonitor{
			ID:             row.ID,
			WorkspaceID:    row.WorkspaceID,
			WorkspaceName:  row.WorkspaceName,
			WorkspaceSlug:  row.WorkspaceSlug,
			Name:           row.Name,
			URL:            monitors.RedactURL(row.Url),
			Method:         row.Method,
			ExpectedStatus: row.ExpectedStatus,
			IntervalSecond: int(row.IntervalSeconds),
			TimeoutSeconds: int(row.TimeoutSeconds),
			Paused:         row.Paused,
			Status:         status,
			LastCheckedAt:  row.LastCheckedAt,
			LastStatusCode: intPtr(row.LastStatusCode),
			LastLatencyMS:  intPtr(row.LastLatencyMs),
			LastError:      row.LastError,
			CreatedAt:      row.CreatedAt,
			UpdatedAt:      row.UpdatedAt,
		})
	}
	return out, nil
}

// ListSecurityEvents returns audit events newest first.
func (s *Service) ListSecurityEvents(ctx context.Context, kind string, workspaceID *uuid.UUID, cursor *monitors.Cursor, limit int) ([]SecurityEvent, error) {
	limit = clampLimit(limit)
	params := sqlc.ListSecurityEventsParams{PageLimit: int32(limit)} //nolint:gosec // clamped to 100
	if k := strings.TrimSpace(kind); k != "" {
		params.Kind = &k
	}
	if workspaceID != nil {
		params.WorkspaceID = pgtype.UUID{Bytes: *workspaceID, Valid: true}
	}
	if cursor != nil {
		t := cursor.Time
		params.AfterTime = &t
		params.AfterID = cursor.ID
	}
	rows, err := s.q.ListSecurityEvents(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]SecurityEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, SecurityEvent{
			ID:          row.ID,
			WorkspaceID: uuidPtr(row.WorkspaceID),
			ActorUserID: uuidPtr(row.ActorUserID),
			Kind:        row.Kind,
			Detail:      json.RawMessage(row.Detail),
			CreatedAt:   row.CreatedAt,
		})
	}
	return out, nil
}

func (s *Service) recordEvent(ctx context.Context, workspaceID uuid.UUID, actorID *uuid.UUID, kind string, detail map[string]any) error {
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("new security event id: %w", err)
	}
	payload, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode security event: %w", err)
	}
	params := sqlc.CreateSecurityEventParams{
		ID:          id,
		WorkspaceID: pgtype.UUID{Bytes: workspaceID, Valid: true},
		Kind:        kind,
		Detail:      payload,
	}
	if actorID != nil {
		params.ActorUserID = pgtype.UUID{Bytes: *actorID, Valid: true}
	}
	if err := s.q.CreateSecurityEvent(ctx, params); err != nil {
		return fmt.Errorf("record security event: %w", err)
	}
	return nil
}

func adminWorkspace(row sqlc.ListWorkspacesForAdminRow) AdminWorkspace {
	return AdminWorkspace{
		ID:              row.ID,
		Name:            row.Name,
		Slug:            row.Slug,
		CreatedBy:       row.CreatedBy,
		CreatedAt:       row.CreatedAt,
		MonitorLimit:    int(row.MonitorLimit),
		SuspendedAt:     row.SuspendedAt,
		SuspendedBy:     uuidPtr(row.SuspendedBy),
		SuspendedReason: row.SuspendedReason,
		MemberCount:     row.MemberCount,
		MonitorCount:    row.MonitorCount,
	}
}

func uuidPtr(v pgtype.UUID) *uuid.UUID {
	if !v.Valid {
		return nil
	}
	id := uuid.UUID(v.Bytes)
	return &id
}

func intPtr(v *int32) *int {
	if v == nil {
		return nil
	}
	n := int(*v)
	return &n
}

func clampLimit(limit int) int {
	if limit < 1 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

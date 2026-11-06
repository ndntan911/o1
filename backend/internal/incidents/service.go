package incidents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/db/sqlc"
	"o1/internal/httpx"
	"o1/internal/workspaces"
)

const (
	defaultListLimit = 50
	maxListLimit     = 100
)

// Service implements the incident HTTP domain on top of the incident schema
// and workspace authorization.
type Service struct {
	pool *pgxpool.Pool
	q    *sqlc.Queries
	ws   *workspaces.Service
}

// NewService wires a Service together.
func NewService(pool *pgxpool.Pool, q *sqlc.Queries, ws *workspaces.Service) *Service {
	return &Service{pool: pool, q: q, ws: ws}
}

// List returns one page of incidents newest first. It authorizes a viewer and
// maps the API status filter onto the active-only flag and status column.
func (s *Service) List(ctx context.Context, actorID, workspaceID uuid.UUID, filter IncidentFilter, cursor *httpx.PageCursor, limit int) ([]Incident, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleViewer); err != nil {
		return nil, err
	}
	activeOnly, status, err := incidentStatusFilter(filter.Status)
	if err != nil {
		return nil, err
	}

	params := sqlc.ListIncidentsParams{
		WorkspaceID: workspaceID,
		ActiveOnly:  activeOnly,
		Status:      status,
		PageLimit:   int32(clampIncidentLimit(limit)),
	}
	if filter.MonitorID != nil {
		params.MonitorID = pgtype.UUID{Bytes: *filter.MonitorID, Valid: true}
	}
	if cursor != nil {
		params.AfterTime = &cursor.Time
		params.AfterID = cursor.ID
	}

	rows, err := s.q.ListIncidents(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list incidents: %w", err)
	}
	out := make([]Incident, 0, len(rows))
	for _, row := range rows {
		out = append(out, Incident{
			ID:             row.ID,
			WorkspaceID:    row.WorkspaceID,
			MonitorID:      row.MonitorID,
			MonitorName:    row.MonitorName,
			Status:         row.Status,
			StartedAt:      row.StartedAt,
			AcknowledgedAt: row.AcknowledgedAt,
			AcknowledgedBy: uuidPointer(row.AcknowledgedBy),
			ResolvedAt:     row.ResolvedAt,
			LastNotifiedAt: row.LastNotifiedAt,
			ReminderCount:  int(row.ReminderCount),
		})
	}
	return out, nil
}

// Get returns one incident and its timeline. An incident outside the
// workspace is reported as not found.
func (s *Service) Get(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) (IncidentDetail, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleViewer); err != nil {
		return IncidentDetail{}, err
	}
	row, err := s.q.GetIncident(ctx, sqlc.GetIncidentParams{ID: incidentID, WorkspaceID: workspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IncidentDetail{}, ErrNotFound
		}
		return IncidentDetail{}, fmt.Errorf("load incident: %w", err)
	}
	events, err := s.q.ListIncidentEvents(ctx, incidentID)
	if err != nil {
		return IncidentDetail{}, fmt.Errorf("list incident events: %w", err)
	}
	out := make([]IncidentEvent, 0, len(events))
	for _, event := range events {
		out = append(out, IncidentEvent{
			ID:          event.ID,
			Kind:        event.Kind,
			ActorUserID: uuidPointer(event.ActorUserID),
			Detail:      json.RawMessage(event.Detail),
			CreatedAt:   event.CreatedAt,
		})
	}
	return IncidentDetail{
		Incident: Incident{
			ID:             row.ID,
			WorkspaceID:    row.WorkspaceID,
			MonitorID:      row.MonitorID,
			MonitorName:    row.MonitorName,
			Status:         row.Status,
			StartedAt:      row.StartedAt,
			AcknowledgedAt: row.AcknowledgedAt,
			AcknowledgedBy: uuidPointer(row.AcknowledgedBy),
			ResolvedAt:     row.ResolvedAt,
			LastNotifiedAt: row.LastNotifiedAt,
			ReminderCount:  int(row.ReminderCount),
		},
		Events: out,
	}, nil
}

// Acknowledge moves an open incident to acknowledged. It is idempotent for an
// already acknowledged incident and rejects resolved incidents. The workspace
// row lock serializes concurrent acknowledgements so only the first inserts an
// event.
func (s *Service) Acknowledge(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) error {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleMember); err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	locked, err := qtx.LockWorkspace(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock workspace: %w", err)
	}
	if locked.SuspendedAt != nil {
		return ErrSuspended
	}

	incident, err := qtx.GetIncident(ctx, sqlc.GetIncidentParams{ID: incidentID, WorkspaceID: workspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load incident: %w", err)
	}
	switch {
	case incident.ResolvedAt != nil || incident.Status == "resolved":
		return ErrIncidentResolved
	case incident.AcknowledgedAt != nil:
		// Already acknowledged: keep the first actor and event untouched.
		return nil
	}

	now := time.Now().UTC()
	if err := qtx.AcknowledgeIncident(ctx, sqlc.AcknowledgeIncidentParams{
		ID:             incidentID,
		AcknowledgedAt: &now,
		AcknowledgedBy: pgtype.UUID{Bytes: actorID, Valid: true},
	}); err != nil {
		return fmt.Errorf("acknowledge incident: %w", err)
	}

	detail, err := json.Marshal(map[string]string{
		"previous_status": incident.Status,
		"new_status":      "acknowledged",
	})
	if err != nil {
		return fmt.Errorf("encode acknowledged event: %w", err)
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("new incident event id: %w", err)
	}
	if err := qtx.CreateIncidentEvent(ctx, sqlc.CreateIncidentEventParams{
		ID:          eventID,
		IncidentID:  incidentID,
		Kind:        "acknowledged",
		ActorUserID: pgtype.UUID{Bytes: actorID, Valid: true},
		Detail:      detail,
	}); err != nil {
		return fmt.Errorf("create acknowledged incident event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit acknowledgement: %w", err)
	}
	return nil
}

// Deliveries returns the alert delivery rows for an incident. Only admins may
// see recipients and failure details.
func (s *Service) Deliveries(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) ([]Delivery, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleAdmin); err != nil {
		return nil, err
	}
	if _, err := s.q.GetIncident(ctx, sqlc.GetIncidentParams{ID: incidentID, WorkspaceID: workspaceID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load incident: %w", err)
	}
	rows, err := s.q.ListIncidentDeliveries(ctx, incidentID)
	if err != nil {
		return nil, fmt.Errorf("list incident deliveries: %w", err)
	}
	out := make([]Delivery, 0, len(rows))
	for _, row := range rows {
		out = append(out, Delivery{
			ID:        row.ID,
			Event:     row.Event,
			Sequence:  int(row.Sequence),
			Recipient: row.Recipient,
			Status:    row.Status,
			Attempts:  int(row.Attempts),
			LastError: row.LastError,
			SentAt:    row.SentAt,
			CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}

// incidentStatusFilter maps an API status to the query's active-only flag and
// exact status filter. An empty status means active.
func incidentStatusFilter(status string) (activeOnly bool, dbStatus *string, err error) {
	switch status {
	case "", "active":
		return true, nil, nil
	case "open", "acknowledged", "resolved":
		value := status
		return false, &value, nil
	case "all":
		return false, nil, nil
	default:
		return false, nil, fmt.Errorf("%w: invalid status", ErrValidation)
	}
}

// clampIncidentLimit applies the list default and maximum.
func clampIncidentLimit(limit int) int {
	if limit <= 0 {
		return defaultListLimit
	}
	if limit > maxListLimit {
		return maxListLimit
	}
	return limit
}

// uuidPointer converts a nullable database UUID to an optional UUID.
func uuidPointer(value pgtype.UUID) *uuid.UUID {
	if !value.Valid {
		return nil
	}
	id := uuid.UUID(value.Bytes)
	return &id
}

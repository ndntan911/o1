package incidents

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"o1/internal/db/sqlc"
	"o1/internal/workspaces"
)

// DeliveryOutcomes returns the member-safe delivery outcomes for an incident.
// It authorizes a member, verifies the incident belongs to the workspace, and
// maps only outcome fields. Recipients, channel names, error text, and channel
// configuration are never selected, so they cannot leak to a member.
func (s *Service) DeliveryOutcomes(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) ([]DeliveryOutcome, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleMember); err != nil {
		return nil, err
	}
	if _, err := s.q.GetIncident(ctx, sqlc.GetIncidentParams{ID: incidentID, WorkspaceID: workspaceID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load incident: %w", err)
	}
	rows, err := s.q.ListIncidentDeliveryOutcomes(ctx, incidentID)
	if err != nil {
		return nil, fmt.Errorf("list incident delivery outcomes: %w", err)
	}
	out := make([]DeliveryOutcome, 0, len(rows))
	for _, row := range rows {
		out = append(out, DeliveryOutcome{
			ChannelKind: row.ChannelKind,
			Event:       row.Event,
			Status:      row.Status,
			Attempts:    int(row.Attempts),
			SentAt:      row.SentAt,
			CreatedAt:   row.CreatedAt,
		})
	}
	return out, nil
}

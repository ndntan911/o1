package incidents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"o1/internal/db/sqlc"
	"o1/internal/jobs"
)

const (
	alertQueue       = "alert"
	alertMaxAttempts = 5
)

// ApplyProbeTransition opens or resolves the monitor's incident to match a
// probe state transition. It must run inside the probe result transaction so
// the state update, incident, timeline events, deliveries, and alert jobs
// commit atomically. All other transitions are no-ops.
func ApplyProbeTransition(ctx context.Context, tx pgx.Tx, q *sqlc.Queries, inserter JobInserter, tr Transition) error {
	switch {
	case tr.Previous != "down" && tr.Next == "down":
		return openIncident(ctx, tx, q, inserter, tr)
	case tr.Previous == "down" && tr.Next == "up":
		return resolveIncident(ctx, tx, q, inserter, tr)
	default:
		return nil
	}
}

func openIncident(ctx context.Context, tx pgx.Tx, q *sqlc.Queries, inserter JobInserter, tr Transition) error {
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("new incident id: %w", err)
	}
	incident, err := q.CreateIncident(ctx, sqlc.CreateIncidentParams{
		ID:          id,
		WorkspaceID: tr.WorkspaceID,
		MonitorID:   tr.MonitorID,
		StartedAt:   tr.At,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The partial unique index says an incident is already active. A
		// retried transition must not add another event or delivery set.
		return nil
	}
	if err != nil {
		return fmt.Errorf("create incident: %w", err)
	}
	if err := createEvent(ctx, q, incident.ID, "opened", tr); err != nil {
		return err
	}
	return enqueueDeliveries(ctx, tx, q, inserter, incident, "opened")
}

func resolveIncident(ctx context.Context, tx pgx.Tx, q *sqlc.Queries, inserter JobInserter, tr Transition) error {
	incident, err := q.GetActiveIncidentForMonitor(ctx, tr.MonitorID)
	if errors.Is(err, pgx.ErrNoRows) {
		// A monitor that was already down before incidents existed, or whose
		// incident was removed, must not send a recovery email.
		return nil
	}
	if err != nil {
		return fmt.Errorf("get active incident: %w", err)
	}

	resolvedAt := tr.At
	if err := q.ResolveIncident(ctx, sqlc.ResolveIncidentParams{
		ID:         incident.ID,
		ResolvedAt: &resolvedAt,
	}); err != nil {
		return fmt.Errorf("resolve incident: %w", err)
	}
	if err := createEvent(ctx, q, incident.ID, "resolved", tr); err != nil {
		return err
	}
	return enqueueDeliveries(ctx, tx, q, inserter, incident, "resolved")
}

func createEvent(ctx context.Context, q *sqlc.Queries, incidentID uuid.UUID, kind string, tr Transition) error {
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("new incident event id: %w", err)
	}
	detail, err := json.Marshal(map[string]string{
		"previous_status": tr.Previous,
		"new_status":      tr.Next,
	})
	if err != nil {
		return fmt.Errorf("encode incident event: %w", err)
	}
	if err := q.CreateIncidentEvent(ctx, sqlc.CreateIncidentEventParams{
		ID:          id,
		IncidentID:  incidentID,
		Kind:        kind,
		ActorUserID: pgtype.UUID{}, // system actor, null
		Detail:      detail,
	}); err != nil {
		return fmt.Errorf("create %s incident event: %w", kind, err)
	}
	return nil
}

func enqueueDeliveries(ctx context.Context, tx pgx.Tx, q *sqlc.Queries, inserter JobInserter, incident sqlc.Incident, event string) error {
	channels, err := q.ListEnabledAlertChannelsForMonitor(ctx, incident.MonitorID)
	if err != nil {
		return fmt.Errorf("list enabled alert channels: %w", err)
	}

	for _, channel := range channels {
		// Plan 4 supports email alert channels only.
		if channel.Kind != "email" {
			continue
		}
		var config EmailConfig
		if err := json.Unmarshal(channel.Config, &config); err != nil {
			// A malformed channel must not fail monitoring. Skip its deliveries
			// and continue with the remaining channels.
			continue
		}
		for _, rawRecipient := range config.Recipients {
			recipient := strings.TrimSpace(rawRecipient)
			if recipient == "" {
				continue
			}
			deliveryID, err := uuid.NewV7()
			if err != nil {
				return fmt.Errorf("new alert delivery id: %w", err)
			}
			delivery, err := q.CreateAlertDelivery(ctx, sqlc.CreateAlertDeliveryParams{
				ID:         deliveryID,
				IncidentID: incident.ID,
				ChannelID:  channel.ID,
				Event:      event,
				Sequence:   0,
				Recipient:  recipient,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				// The delivery unique key already exists; do not enqueue a
				// second job for the same recipient and event.
				continue
			}
			if err != nil {
				return fmt.Errorf("create %s alert delivery: %w", event, err)
			}
			if err := insertAlertJob(ctx, tx, inserter, delivery.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertAlertJob(ctx context.Context, tx pgx.Tx, inserter JobInserter, deliveryID uuid.UUID) error {
	if inserter == nil {
		return errors.New("incidents: nil alert job inserter")
	}
	if _, err := inserter.InsertTx(ctx, tx, jobs.AlertArgs{DeliveryID: deliveryID}, &river.InsertOpts{
		MaxAttempts: alertMaxAttempts,
		Queue:       alertQueue,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStateScheduled,
				rivertype.JobStateRunning,
				rivertype.JobStatePending,
			},
		},
	}); err != nil {
		return fmt.Errorf("insert alert job for delivery %s: %w", deliveryID, err)
	}
	return nil
}

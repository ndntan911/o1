package incidents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"o1/internal/db/sqlc"
)

const (
	// reminderInterval is how long an open, unacknowledged incident waits
	// between reminder emails.
	reminderInterval = 30 * time.Minute
	// reminderMaxCount caps the reminders sent for one incident.
	reminderMaxCount = 10
	// reminderBatch bounds how many incidents one tick processes.
	reminderBatch = 100
)

// EnqueueReminders sends reminder alerts for open, unacknowledged incidents
// whose last notification is older than the reminder interval. It must run
// inside a transaction so the delivery rows, River jobs, and the incident's
// reminder bookkeeping commit atomically. It returns the number of incidents
// that produced at least one delivery.
func EnqueueReminders(ctx context.Context, tx pgx.Tx, q *sqlc.Queries, inserter JobInserter, now time.Time) (int, error) {
	rows, err := q.ListIncidentsNeedingReminder(ctx, sqlc.ListIncidentsNeedingReminderParams{
		MaxReminders: reminderMaxCount,
		Before:       now.Add(-reminderInterval),
		Batch:        reminderBatch,
	})
	if err != nil {
		return 0, fmt.Errorf("list incidents needing reminder: %w", err)
	}

	reminded := 0
	for _, incident := range rows {
		inserted, err := enqueueReminderDeliveries(ctx, tx, q, inserter, incident)
		if err != nil {
			return reminded, err
		}
		if inserted == 0 {
			// No enabled channel or recipient means there is nothing to send.
			// Do not advance the incident so a later fix can still remind.
			continue
		}
		if err := q.UpdateIncidentReminder(ctx, sqlc.UpdateIncidentReminderParams{
			ID:             incident.ID,
			LastNotifiedAt: &now,
		}); err != nil {
			return reminded, fmt.Errorf("update incident reminder %s: %w", incident.ID, err)
		}
		reminded++
	}
	return reminded, nil
}

// enqueueReminderDeliveries inserts one reminder delivery and alert job per
// recipient and returns how many deliveries were actually inserted. Malformed
// channel configuration is skipped rather than failing the whole tick.
func enqueueReminderDeliveries(ctx context.Context, tx pgx.Tx, q *sqlc.Queries, inserter JobInserter, incident sqlc.ListIncidentsNeedingReminderRow) (int, error) {
	channels, err := q.ListEnabledAlertChannelsForMonitor(ctx, incident.MonitorID)
	if err != nil {
		return 0, fmt.Errorf("list enabled alert channels: %w", err)
	}

	sequence := incident.ReminderCount + 1
	inserted := 0
	for _, channel := range channels {
		// Plan 4 supports email alert channels only.
		if channel.Kind != "email" {
			continue
		}
		var config EmailConfig
		if err := json.Unmarshal(channel.Config, &config); err != nil {
			// A malformed channel must not fail the reminder tick. Skip its
			// deliveries and continue with the remaining channels.
			continue
		}
		for _, rawRecipient := range config.Recipients {
			recipient := strings.TrimSpace(rawRecipient)
			if recipient == "" {
				continue
			}
			deliveryID, err := uuid.NewV7()
			if err != nil {
				return inserted, fmt.Errorf("new alert delivery id: %w", err)
			}
			delivery, err := q.CreateAlertDelivery(ctx, sqlc.CreateAlertDeliveryParams{
				ID:         deliveryID,
				IncidentID: incident.ID,
				ChannelID:  channel.ID,
				Event:      "reminder",
				Sequence:   sequence,
				Recipient:  recipient,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				// This reminder sequence was already inserted; do not enqueue
				// a second job for the same recipient.
				continue
			}
			if err != nil {
				return inserted, fmt.Errorf("create reminder alert delivery: %w", err)
			}
			if err := insertAlertJob(ctx, tx, inserter, delivery.ID); err != nil {
				return inserted, err
			}
			inserted++
		}
	}
	return inserted, nil
}

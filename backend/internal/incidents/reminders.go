package incidents

import (
	"context"
	"fmt"
	"time"

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
// reminder bookkeeping commit atomically. Incidents that produce no delivery
// are still touched so they rotate out of the front of the batch without
// consuming their reminder budget. It returns the number of incidents that
// produced at least one delivery.
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
			// Still advance last_notified_at so the incident rotates to the
			// back of the reminder batch instead of starving deliverable
			// incidents forever. The reminder budget is not consumed.
			if err := q.TouchIncidentReminder(ctx, sqlc.TouchIncidentReminderParams{
				ID:             incident.ID,
				LastNotifiedAt: &now,
			}); err != nil {
				return reminded, fmt.Errorf("touch incident reminder %s: %w", incident.ID, err)
			}
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
	sequence := int(incident.ReminderCount) + 1
	return enqueueAlertDeliveries(ctx, tx, q, inserter, incident.ID, incident.MonitorID, "reminder", sequence)
}

package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"o1/internal/config"
	"o1/internal/db/sqlc"
	"o1/internal/email"
	"o1/internal/incidents"
	"o1/internal/jobs"
	"o1/internal/monitors"
)

const (
	// alertReminderStaleAfter is twice the reminder interval. A reminder older
	// than this is dropped rather than sent in a burst after worker downtime.
	alertReminderStaleAfter = 60 * time.Minute
	// alertJobTimeout bounds one alert job, including the SMTP send.
	alertJobTimeout = 60 * time.Second
)

// AlertWorker sends one incident alert email per delivery.
type AlertWorker struct {
	river.WorkerDefaults[jobs.AlertArgs]

	q      *sqlc.Queries
	sender email.Sender
	cfg    config.Config
	logger *slog.Logger
}

// NewAlertWorker builds an AlertWorker. The pool is accepted for symmetry with
// the other workers and for future transactional re-checks.
func NewAlertWorker(pool *pgxpool.Pool, q *sqlc.Queries, sender email.Sender, cfg config.Config, logger *slog.Logger) *AlertWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &AlertWorker{q: q, sender: sender, cfg: cfg, logger: logger}
}

// Timeout bounds each job run.
func (w *AlertWorker) Timeout(*river.Job[jobs.AlertArgs]) time.Duration { return alertJobTimeout }

// Work sends one alert delivery. Deliveries in a terminal state are no-ops.
// Reminders are re-checked because they are only useful while the incident is
// still open, unacknowledged, and the monitor and workspace are active.
func (w *AlertWorker) Work(ctx context.Context, job *river.Job[jobs.AlertArgs]) error {
	delivery, err := w.q.GetAlertDelivery(ctx, job.Args.DeliveryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load alert delivery %s: %w", job.Args.DeliveryID, err)
	}

	switch delivery.Status {
	case "sent", "failed", "skipped":
		return nil
	}

	if delivery.Event == "reminder" {
		if reason := reminderSkipReason(delivery, time.Now().UTC()); reason != "" {
			if err := w.markDelivery(ctx, delivery.ID, "skipped", reason); err != nil {
				return err
			}
			w.logger.Log(ctx, slog.LevelInfo, "alert_delivery_skipped",
				"delivery_id", delivery.ID,
				"incident_id", delivery.IncidentID,
				"reason", reason,
			)
			return nil
		}
	}

	message := incidents.BuildAlertMessage(incidents.AlertEmail{
		WorkspaceName: delivery.WorkspaceName,
		WorkspaceSlug: delivery.WorkspaceSlug,
		MonitorName:   delivery.MonitorName,
		MonitorURL:    monitors.RedactURL(delivery.MonitorUrl),
		Event:         delivery.Event,
		Sequence:      int(delivery.Sequence),
		IncidentID:    delivery.IncidentID,
		StartedAt:     delivery.StartedAt,
		BaseURL:       w.cfg.AppBaseURL,
		Recipient:     delivery.Recipient,
	})
	if err := w.sender.Send(ctx, message); err != nil {
		lastError := err.Error()
		if job.Attempt >= job.MaxAttempts {
			if markErr := w.markDelivery(ctx, delivery.ID, "failed", lastError); markErr != nil {
				return markErr
			}
			w.logger.Log(ctx, slog.LevelError, "alert_delivery_failed",
				"delivery_id", delivery.ID,
				"incident_id", delivery.IncidentID,
				"attempts", job.Attempt,
				"error", lastError,
			)
			return nil
		}
		if markErr := w.markDelivery(ctx, delivery.ID, "pending", lastError); markErr != nil {
			return markErr
		}
		return err
	}
	if err := w.q.MarkAlertDeliverySent(ctx, delivery.ID); err != nil {
		return fmt.Errorf("mark alert delivery sent: %w", err)
	}
	return nil
}

// reminderSkipReason returns why a reminder is no longer worth sending, or an
// empty string when it should still go out.
func reminderSkipReason(delivery sqlc.GetAlertDeliveryRow, now time.Time) string {
	switch {
	case delivery.IncidentStatus == "resolved" || delivery.ResolvedAt != nil:
		return "incident resolved"
	case delivery.IncidentStatus == "acknowledged" || delivery.AcknowledgedAt != nil:
		return "incident acknowledged"
	case delivery.MonitorPaused:
		return "monitor paused"
	case delivery.SuspendedAt != nil:
		return "workspace suspended"
	case now.Sub(delivery.CreatedAt) > alertReminderStaleAfter:
		return "reminder older than 60 minutes"
	default:
		return ""
	}
}

func (w *AlertWorker) markDelivery(ctx context.Context, id uuid.UUID, status, reason string) error {
	if err := w.q.MarkAlertDeliveryFailed(ctx, sqlc.MarkAlertDeliveryFailedParams{
		ID:        id,
		Status:    status,
		LastError: &reason,
	}); err != nil {
		return fmt.Errorf("mark alert delivery %s: %w", status, err)
	}
	return nil
}

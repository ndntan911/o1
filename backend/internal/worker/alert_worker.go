package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"o1/internal/alerts"
	"o1/internal/config"
	"o1/internal/db/sqlc"
	"o1/internal/email"
	"o1/internal/incidents"
	"o1/internal/jobs"
	"o1/internal/monitors"
	"o1/internal/observability"
	"o1/internal/secretbox"
)

const (
	// alertReminderStaleAfter is twice the reminder interval. A reminder older
	// than this is dropped rather than sent in a burst after worker downtime.
	alertReminderStaleAfter = 60 * time.Minute
	// alertJobTimeout bounds one alert job, including the outbound send.
	alertJobTimeout = 60 * time.Second
)

// AlertWorker sends one incident alert delivery per job, using email or the
// guarded webhook/Slack/Telegram notifier.
type AlertWorker struct {
	river.WorkerDefaults[jobs.AlertArgs]

	q        *sqlc.Queries
	sender   email.Sender
	box      *secretbox.Box
	notifier alerts.NotificationSender
	cfg      config.Config
	logger   *slog.Logger
	metrics  *observability.Metrics
}

// NewAlertWorker builds an AlertWorker. The pool is accepted for symmetry with
// the other workers and for future transactional re-checks. metrics may be
// nil.
func NewAlertWorker(
	pool *pgxpool.Pool,
	q *sqlc.Queries,
	sender email.Sender,
	box *secretbox.Box,
	notifier alerts.NotificationSender,
	cfg config.Config,
	logger *slog.Logger,
	metrics *observability.Metrics,
) *AlertWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &AlertWorker{
		q: q, sender: sender, box: box, notifier: notifier, cfg: cfg, logger: logger, metrics: metrics,
	}
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

	if delivery.ChannelDeletedAt != nil {
		start := time.Now()
		if err := w.markDelivery(ctx, delivery.ID, "skipped", "channel deleted"); err != nil {
			return err
		}
		w.observeDelivery(ctx, delivery.ChannelKind, "skipped", start)
		w.logger.Log(ctx, slog.LevelInfo, "alert_delivery_skipped",
			"delivery_id", delivery.ID,
			"incident_id", delivery.IncidentID,
			"channel_id", delivery.ChannelID,
			"channel_kind", delivery.ChannelKind,
			"reason", "channel deleted",
		)
		return nil
	}

	if delivery.Event == "reminder" {
		if reason := reminderSkipReason(delivery, time.Now().UTC()); reason != "" {
			start := time.Now()
			if err := w.markDelivery(ctx, delivery.ID, "skipped", reason); err != nil {
				return err
			}
			w.observeDelivery(ctx, delivery.ChannelKind, "skipped", start)
			w.logger.Log(ctx, slog.LevelInfo, "alert_delivery_skipped",
				"delivery_id", delivery.ID,
				"incident_id", delivery.IncidentID,
				"reason", reason,
			)
			return nil
		}
	}

	switch delivery.ChannelKind {
	case "email":
		return w.sendEmail(ctx, job, delivery)
	case "webhook", "slack", "telegram":
		return w.sendNotifier(ctx, job, delivery)
	default:
		return w.failWithoutRetry(ctx, delivery, "unsupported alert channel kind", time.Now())
	}
}

func (w *AlertWorker) sendEmail(ctx context.Context, job *river.Job[jobs.AlertArgs], delivery sqlc.GetAlertDeliveryRow) error {
	start := time.Now()
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
		return w.handleSendError(ctx, job, delivery, err, start)
	}
	if err := w.q.MarkAlertDeliverySent(ctx, delivery.ID); err != nil {
		return fmt.Errorf("mark alert delivery sent: %w", err)
	}
	w.observeDelivery(ctx, delivery.ChannelKind, "sent", start)
	return nil
}

func (w *AlertWorker) sendNotifier(ctx context.Context, job *river.Job[jobs.AlertArgs], delivery sqlc.GetAlertDeliveryRow) error {
	start := time.Now()
	if w.box == nil {
		return w.failWithoutRetry(ctx, delivery, "invalid alert channel configuration", start)
	}
	target, err := alerts.OpenTarget(w.box, delivery.ChannelKind, delivery.ChannelConfig)
	if err != nil {
		return w.failWithoutRetry(ctx, delivery, "invalid alert channel configuration", start)
	}
	if w.notifier == nil {
		return fmt.Errorf("alert notifier is not configured")
	}
	event := alerts.Event{
		Kind:           delivery.Event,
		Sequence:       int(delivery.Sequence),
		WorkspaceName:  delivery.WorkspaceName,
		WorkspaceSlug:  delivery.WorkspaceSlug,
		MonitorID:      delivery.MonitorID,
		MonitorName:    delivery.MonitorName,
		MonitorURL:     monitors.RedactURL(delivery.MonitorUrl),
		IncidentID:     delivery.IncidentID,
		StartedAt:      delivery.StartedAt,
		AcknowledgedAt: delivery.AcknowledgedAt,
		ResolvedAt:     delivery.ResolvedAt,
		Link:           incidentLink(w.cfg.AppBaseURL, delivery),
	}
	if err := w.notifier.Send(ctx, target, event); err != nil {
		if alerts.IsPermanent(err) {
			return w.failWithoutRetry(ctx, delivery, err.Error(), start)
		}
		return w.handleSendError(ctx, job, delivery, err, start)
	}
	if err := w.q.MarkAlertDeliverySent(ctx, delivery.ID); err != nil {
		return fmt.Errorf("mark alert delivery sent: %w", err)
	}
	w.observeDelivery(ctx, delivery.ChannelKind, "sent", start)
	return nil
}

// handleSendError applies the shared retry budget to a failed send.
func (w *AlertWorker) handleSendError(ctx context.Context, job *river.Job[jobs.AlertArgs], delivery sqlc.GetAlertDeliveryRow, sendErr error, start time.Time) error {
	lastError := sendErr.Error()
	if job.Attempt >= job.MaxAttempts {
		if markErr := w.markDelivery(ctx, delivery.ID, "failed", lastError); markErr != nil {
			return markErr
		}
		w.observeDelivery(ctx, delivery.ChannelKind, "failed", start)
		w.logger.Log(ctx, slog.LevelError, "alert_delivery_failed",
			"delivery_id", delivery.ID,
			"incident_id", delivery.IncidentID,
			"channel_id", delivery.ChannelID,
			"channel_kind", delivery.ChannelKind,
			"attempts", job.Attempt,
			"error", lastError,
		)
		return nil
	}
	if markErr := w.markDelivery(ctx, delivery.ID, "pending", lastError); markErr != nil {
		return markErr
	}
	return sendErr
}

func (w *AlertWorker) failWithoutRetry(ctx context.Context, delivery sqlc.GetAlertDeliveryRow, reason string, start time.Time) error {
	if err := w.markDelivery(ctx, delivery.ID, "failed", reason); err != nil {
		return err
	}
	w.observeDelivery(ctx, delivery.ChannelKind, "failed", start)
	w.logger.Log(ctx, slog.LevelError, "alert_delivery_failed",
		"delivery_id", delivery.ID,
		"incident_id", delivery.IncidentID,
		"channel_id", delivery.ChannelID,
		"channel_kind", delivery.ChannelKind,
		"error", reason,
	)
	return nil
}

// observeDelivery records a terminal delivery outcome. Unknown channel kinds
// are folded into "other" so the label remains bounded.
func (w *AlertWorker) observeDelivery(ctx context.Context, channelKind, status string, start time.Time) {
	kind := boundedChannelKind(channelKind)
	w.metrics.ObserveAlert(kind, status, time.Since(start))
	trace.SpanFromContext(ctx).SetAttributes(
		attribute.String("alert.channel_kind", kind),
		attribute.String("alert.status", status),
	)
}

func boundedChannelKind(kind string) string {
	switch kind {
	case "email", "webhook", "slack", "telegram":
		return kind
	default:
		return "other"
	}
}

func incidentLink(baseURL string, delivery sqlc.GetAlertDeliveryRow) string {
	return strings.TrimRight(baseURL, "/") + "/w/" + url.PathEscape(delivery.WorkspaceSlug) +
		"/incidents/" + delivery.IncidentID.String()
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

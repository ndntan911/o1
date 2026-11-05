package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"golang.org/x/time/rate"

	"o1/internal/db/sqlc"
	"o1/internal/jobs"
	"o1/internal/probe"
	"o1/internal/secretbox"
)

// jobTimeout bounds one probe job, including decryption and database writes.
const jobTimeout = 30 * time.Second

// ProbeWorker executes probe jobs.
type ProbeWorker struct {
	river.WorkerDefaults[jobs.ProbeArgs]

	pool    *pgxpool.Pool
	q       *sqlc.Queries
	box     *secretbox.Box
	prober  *probe.Prober
	limiter *rate.Limiter
	logger  *slog.Logger
}

// NewProbeWorker builds a ProbeWorker.
func NewProbeWorker(pool *pgxpool.Pool, q *sqlc.Queries, box *secretbox.Box, prober *probe.Prober, limiter *rate.Limiter, logger *slog.Logger) *ProbeWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &ProbeWorker{pool: pool, q: q, box: box, prober: prober, limiter: limiter, logger: logger}
}

// Timeout bounds each job run.
func (w *ProbeWorker) Timeout(*river.Job[jobs.ProbeArgs]) time.Duration { return jobTimeout }

// Work executes one River probe job.
func (w *ProbeWorker) Work(ctx context.Context, job *river.Job[jobs.ProbeArgs]) error {
	return w.Process(ctx, job.Args)
}

// Process probes one monitor and records the result exactly once. Replays,
// paused monitors, suspended workspaces, and missing monitors are no-ops.
func (w *ProbeWorker) Process(ctx context.Context, args jobs.ProbeArgs) error {
	monitor, err := w.q.GetMonitorForProbe(ctx, args.MonitorID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("load monitor for probe: %w", err)
	}
	if monitor.SuspendedAt != nil || monitor.Paused {
		return nil
	}

	scheduledFor := args.ScheduledFor.UTC()
	exists, err := w.q.ProbeResultExists(ctx, sqlc.ProbeResultExistsParams{
		MonitorID:    monitor.ID,
		ScheduledFor: scheduledFor,
	})
	if err != nil {
		return fmt.Errorf("check probe result: %w", err)
	}
	if exists {
		return nil
	}

	if w.limiter != nil {
		if err := w.limiter.Wait(ctx); err != nil {
			return err
		}
	}

	headers, err := w.decryptHeaders(monitor.HeadersEnc)
	if err != nil {
		return err
	}
	body, err := w.decryptBody(monitor.BodyEnc)
	if err != nil {
		return err
	}
	expected, err := probe.ParseExpectedStatus(monitor.ExpectedStatus)
	if err != nil {
		return fmt.Errorf("parse expected status: %w", err)
	}
	request := probe.Request{
		URL:      monitor.Url,
		Method:   monitor.Method,
		Headers:  headers,
		Expected: expected,
		Timeout:  time.Duration(monitor.TimeoutSeconds) * time.Second,
	}
	if body != nil {
		request.Body = []byte(*body)
	}
	if monitor.ContentType != nil {
		request.ContentType = *monitor.ContentType
	}

	result := w.prober.Probe(ctx, request)
	checkedAt := time.Now().UTC()
	return w.record(ctx, monitor, scheduledFor, checkedAt, result)
}

func (w *ProbeWorker) record(ctx context.Context, monitor sqlc.GetMonitorForProbeRow, scheduledFor, checkedAt time.Time, result probe.Result) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin probe result tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	q := w.q.WithTx(tx)

	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("new probe result id: %w", err)
	}

	status := "down"
	if result.Up {
		status = "up"
	}
	var statusCode *int32
	if result.StatusCode > 0 {
		code := int32(result.StatusCode) //nolint:gosec // HTTP status codes are 100-599
		statusCode = &code
	}
	latency := result.LatencyMS
	if latency < 0 {
		latency = 0
	}
	latency32 := int32(latency) //nolint:gosec // probe durations are far below int32 range
	errorText := probeErrorText(result)

	rows, err := q.InsertProbeResult(ctx, sqlc.InsertProbeResultParams{
		ID:           id,
		MonitorID:    monitor.ID,
		WorkspaceID:  monitor.WorkspaceID,
		ScheduledFor: scheduledFor,
		CheckedAt:    checkedAt,
		Status:       status,
		StatusCode:   statusCode,
		LatencyMs:    latency32,
		Error:        errorText,
	})
	if err != nil {
		return fmt.Errorf("insert probe result: %w", err)
	}
	if rows == 0 {
		// Another worker recorded this probe between the existence check and
		// the insert; do not advance state a second time.
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit replayed probe: %w", err)
		}
		return nil
	}

	previous := monitor.Status
	next := Advance(State{
		Status:    previous,
		Failures:  int(monitor.ConsecutiveFailures),
		Successes: int(monitor.ConsecutiveSuccesses),
	}, result.Up, int(monitor.ConfirmationThreshold), int(monitor.RecoveryThreshold))

	if err := q.UpdateMonitorState(ctx, sqlc.UpdateMonitorStateParams{
		MonitorID:            monitor.ID,
		Status:               next.Status,
		ConsecutiveFailures:  int32(next.Failures),  //nolint:gosec // capped at 1000
		ConsecutiveSuccesses: int32(next.Successes), //nolint:gosec // capped at 1000
		LastCheckedAt:        &checkedAt,
		LastStatusCode:       statusCode,
		LastLatencyMs:        &latency32,
		LastError:            errorText,
	}); err != nil {
		return fmt.Errorf("update monitor state: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit probe result: %w", err)
	}

	if previous != next.Status {
		w.logger.Log(ctx, slog.LevelInfo, "monitor_transition",
			"monitor_id", monitor.ID,
			"workspace_id", monitor.WorkspaceID,
			"previous_status", previous,
			"new_status", next.Status,
		)
	}
	return nil
}

func (w *ProbeWorker) decryptHeaders(enc *string) (map[string]string, error) {
	if enc == nil || *enc == "" {
		return nil, nil
	}
	plain, err := w.box.Open(*enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt headers: %w", err)
	}
	var headers map[string]string
	if err := json.Unmarshal(plain, &headers); err != nil {
		return nil, fmt.Errorf("parse headers: %w", err)
	}
	return headers, nil
}

func (w *ProbeWorker) decryptBody(enc *string) (*string, error) {
	if enc == nil || *enc == "" {
		return nil, nil
	}
	plain, err := w.box.Open(*enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt body: %w", err)
	}
	body := string(plain)
	return &body, nil
}

func probeErrorText(result probe.Result) *string {
	if result.Up {
		return nil
	}
	message := result.ErrorMessage
	switch {
	case result.ErrorClass == "" && message == "":
		message = "probe failed"
	case result.ErrorClass == "":
	case message == "":
		message = result.ErrorClass
	default:
		message = result.ErrorClass + ": " + message
	}
	return &message
}

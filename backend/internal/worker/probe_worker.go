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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"golang.org/x/time/rate"

	"o1/internal/db/sqlc"
	"o1/internal/incidents"
	"o1/internal/jobs"
	"o1/internal/monitors"
	"o1/internal/probe"
	"o1/internal/secretbox"
)

// jobTimeout bounds one probe job, including decryption and database writes.
const jobTimeout = 30 * time.Second

// ProbeWorker executes probe jobs.
type ProbeWorker struct {
	river.WorkerDefaults[jobs.ProbeArgs]

	pool     *pgxpool.Pool
	q        *sqlc.Queries
	box      *secretbox.Box
	prober   *probe.Prober
	limiter  *rate.Limiter
	logger   *slog.Logger
	inserter incidents.JobInserter
}

// NewProbeWorker builds a ProbeWorker.
func NewProbeWorker(pool *pgxpool.Pool, q *sqlc.Queries, box *secretbox.Box, prober *probe.Prober, limiter *rate.Limiter, logger *slog.Logger) *ProbeWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &ProbeWorker{pool: pool, q: q, box: box, prober: prober, limiter: limiter, logger: logger}
}

// SetJobInserter wires the River client used to enqueue alert jobs. The client
// needs its workers at construction, so this is called after NewClient and
// before Start.
func (w *ProbeWorker) SetJobInserter(inserter incidents.JobInserter) {
	w.inserter = inserter
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

	// Cheap pre-probe replay check: a duplicate delivery for an already
	// recorded schedule must not perform another outbound request. This check
	// is advisory only; the authoritative check happens under the state lock
	// in record so racing workers still record exactly once.
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

	// Follow the workspace -> monitor_state lock order used by monitor
	// update/delete/resume. The probe_results foreign keys would otherwise take
	// a key-share lock on workspaces after monitor_state, which can deadlock
	// with a mutation holding the workspace lock.
	if _, err := q.LockWorkspaceForProbe(ctx, monitor.WorkspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The workspace disappeared; the monitor is gone with it.
			return nil
		}
		return fmt.Errorf("lock workspace for probe: %w", err)
	}

	// Locking the state row serializes concurrent jobs for this monitor. The
	// existence check must happen after the lock so two workers racing on the
	// same scheduled probe cannot both advance state.
	state, err := q.GetMonitorStateForUpdate(ctx, monitor.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The monitor was deleted after the probe ran.
			return nil
		}
		return fmt.Errorf("lock monitor state: %w", err)
	}
	exists, err := q.ProbeResultExists(ctx, sqlc.ProbeResultExistsParams{
		MonitorID:    monitor.ID,
		ScheduledFor: scheduledFor,
	})
	if err != nil {
		return fmt.Errorf("check probe result: %w", err)
	}
	if exists {
		return nil
	}

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
		// The result already exists; the state lock makes this unreachable in
		// practice, but never advance counters twice.
		return nil
	}

	// A pause can commit while the probe is in flight. Keep the result for
	// audit but do not advance state or open/resolve an incident.
	paused, err := q.GetMonitorPaused(ctx, monitor.ID)
	if err != nil {
		return fmt.Errorf("check monitor paused: %w", err)
	}
	if paused {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit probe result: %w", err)
		}
		return nil
	}

	previous := state.Status
	next := Advance(State{
		Status:    previous,
		Failures:  int(state.ConsecutiveFailures),
		Successes: int(state.ConsecutiveSuccesses),
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
	if err := incidents.ApplyProbeTransition(ctx, tx, q, w.inserter, incidents.Transition{
		WorkspaceID: monitor.WorkspaceID,
		MonitorID:   monitor.ID,
		Previous:    previous,
		Next:        next.Status,
		At:          checkedAt,
	}); err != nil {
		return fmt.Errorf("apply incident transition: %w", err)
	}
	if result.ErrorClass == "ssrf_blocked" {
		if err := w.recordSSRFEvent(ctx, q, monitor, result); err != nil {
			return err
		}
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

// recordSSRFEvent persists a dial-time or redirect-time block in the same
// transaction as the probe result so administrators can audit abuse attempts.
func (w *ProbeWorker) recordSSRFEvent(ctx context.Context, q *sqlc.Queries, monitor sqlc.GetMonitorForProbeRow, result probe.Result) error {
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("new security event id: %w", err)
	}
	detail, err := json.Marshal(map[string]any{
		"monitor_id":   monitor.ID,
		"workspace_id": monitor.WorkspaceID,
		"url":          monitors.RedactURL(monitor.Url),
		"reason":       result.ErrorMessage,
	})
	if err != nil {
		return fmt.Errorf("encode ssrf security event: %w", err)
	}
	if err := q.CreateSecurityEvent(ctx, sqlc.CreateSecurityEventParams{
		ID:          id,
		WorkspaceID: pgtype.UUID{Bytes: monitor.WorkspaceID, Valid: true},
		Kind:        "ssrf_blocked",
		Detail:      detail,
	}); err != nil {
		return fmt.Errorf("record ssrf security event: %w", err)
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

package copilot

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
	"github.com/riverqueue/river/rivertype"

	"o1/internal/ai"
	"o1/internal/audit"
	"o1/internal/auth"
	"o1/internal/copilot/tools"
	"o1/internal/db/sqlc"
	"o1/internal/incidents"
	"o1/internal/monitors"
	"o1/internal/observability"
	"o1/internal/workspaces"
)

const (
	// errorCodeProviderUnavailable is the terminal code for a provider that
	// stayed transient through the last allowed attempt.
	errorCodeProviderUnavailable = "provider_unavailable"
	// errorCodeActorRemoved is the terminal code for a run whose initiator is
	// gone or is no longer a workspace member.
	errorCodeActorRemoved = "actor_removed"
)

// auditKindInvestigationCompleted is the security event written for every
// terminal run. Failed runs carry their error code in the detail.
const auditKindInvestigationCompleted = "investigation_completed"

// JobInserter inserts River jobs inside the caller's transaction.
// river.Client[pgx.Tx] satisfies this interface.
type JobInserter interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// Options wires the investigation service. Provider may be nil, which leaves
// the copilot off. Now, Logger, and Metrics are optional.
type Options struct {
	Store      *Store
	Incidents  *incidents.Service
	Monitors   *monitors.Service
	Workspaces *workspaces.Service
	Provider   ai.ToolProvider
	Inserter   JobInserter
	Pool       *pgxpool.Pool
	Queries    *sqlc.Queries
	Model      string
	Limits     Limits
	Now        func() time.Time
	Logger     *slog.Logger
	Metrics    *observability.Metrics
}

// Service runs investigations and owns the read surface used by the API. The
// worker calls Run; the API handlers call Start, Latest, and Get.
type Service struct {
	store      *Store
	incidents  *incidents.Service
	monitors   *monitors.Service
	workspaces *workspaces.Service
	provider   ai.ToolProvider
	inserter   JobInserter
	pool       *pgxpool.Pool
	queries    *sqlc.Queries
	model      string
	limits     Limits
	runner     *Runner
	now        func() time.Time
	logger     *slog.Logger
	metrics    *observability.Metrics
}

// NewService builds the tool registry and the bounded runner once. A nil
// provider leaves the copilot disabled: Enabled reports false and the worker
// turns every job into a no-op.
func NewService(opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	limits := opts.Limits
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}

	svc := &Service{
		store:      opts.Store,
		incidents:  opts.Incidents,
		monitors:   opts.Monitors,
		workspaces: opts.Workspaces,
		provider:   opts.Provider,
		inserter:   opts.Inserter,
		pool:       opts.Pool,
		queries:    opts.Queries,
		model:      opts.Model,
		limits:     limits,
		now:        now,
		logger:     logger,
		metrics:    opts.Metrics,
	}
	if opts.Provider != nil {
		registry := tools.NewRegistry(opts.Incidents, opts.Monitors, now)
		svc.runner = NewRunner(opts.Provider, registry, opts.Model, limits, now)
	}
	return svc
}

// Enabled reports whether the service can run investigations. A nil service is
// disabled, so the worker can be built before the copilot is configured.
func (s *Service) Enabled() bool {
	return s != nil && s.provider != nil && s.runner != nil
}

// Run executes one River attempt for an investigation. It loads the row so a
// terminal status is a no-op, rebuilds the redacted seed as the initiating
// user, marks the row running, runs the agent, and records exactly one terminal
// outcome.
//
// A transient provider error is saved as progress and returned to River while
// attempts remain; the last attempt terminalizes the row as failed with
// provider_unavailable. If MarkRunning, SaveProgress, or Complete reports
// ErrNotActive, a reaper or another worker already finished the row, so Run
// returns nil without reviving it.
func (s *Service) Run(ctx context.Context, investigationID uuid.UUID, attempt, maxAttempts int) error {
	if !s.Enabled() {
		return nil
	}
	if attempt < 1 {
		attempt = 1
	}
	if maxAttempts < attempt {
		maxAttempts = attempt
	}

	inv, err := s.store.GetByID(ctx, investigationID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("copilot: load investigation %s: %w", investigationID, err)
	}
	if inv.Status != StatusQueued && inv.Status != StatusRunning {
		return nil
	}

	started := s.now()

	if inv.CreatedBy == nil {
		return s.finishFailure(ctx, inv, errorCodeActorRemoved, Trace{}, 0, 0, started)
	}
	actorID := *inv.CreatedBy
	actorCtx := auth.ContextWithPrincipal(ctx, auth.Principal{UserID: actorID})
	if _, err := s.workspaces.Authorize(ctx, actorID, inv.WorkspaceID, workspaces.RoleMember); err != nil {
		if errors.Is(err, workspaces.ErrNotFound) || errors.Is(err, workspaces.ErrForbidden) {
			return s.finishFailure(ctx, inv, errorCodeActorRemoved, Trace{}, 0, 0, started)
		}
		return fmt.Errorf("copilot: authorize actor for investigation %s: %w", investigationID, err)
	}

	seed, err := BuildSeed(actorCtx, s.incidents, s.monitors, actorID, inv.WorkspaceID, inv.IncidentID, s.now())
	if err != nil {
		s.logger.Log(ctx, slog.LevelError, "investigation seed failed",
			"investigation_id", inv.ID,
			"incident_id", inv.IncidentID,
			"error", err,
		)
		return s.finishFailure(ctx, inv, errorCodeInternal, Trace{}, 0, 0, started)
	}
	seedHash := HashSeed(seed, PromptVersion)

	if err := s.store.MarkRunning(ctx, inv.ID, attempt, seedHash); err != nil {
		if errors.Is(err, ErrNotActive) {
			return nil
		}
		return fmt.Errorf("copilot: mark investigation %s running: %w", inv.ID, err)
	}

	outcome := s.runner.Run(actorCtx, inv.WorkspaceID, seed)
	duration := s.now().Sub(started)
	toolCalls := len(outcome.Trace.Calls)

	if outcome.Err != nil {
		if attempt < maxAttempts {
			if err := s.store.SaveProgress(ctx, inv.ID, outcome.Trace, attempt, toolCalls, outcome.InputTokens, outcome.OutputTokens); err != nil {
				if errors.Is(err, ErrNotActive) {
					return nil
				}
				return fmt.Errorf("copilot: save investigation %s progress: %w", inv.ID, err)
			}
			s.logger.Log(ctx, slog.LevelWarn, "investigation provider error; retrying",
				"investigation_id", inv.ID,
				"incident_id", inv.IncidentID,
				"attempt", attempt,
				"max_attempts", maxAttempts,
				"error_type", fmt.Sprintf("%T", outcome.Err),
			)
			return outcome.Err
		}
		inv.Status = StatusFailed
		inv.ErrorCode = errorCodeProviderUnavailable
		inv.Result = nil
		inv.Trace = outcome.Trace
		inv.InputTokens = outcome.InputTokens
		inv.OutputTokens = outcome.OutputTokens
		return s.complete(ctx, inv, duration, toolCalls, 0)
	}

	inv.Status = outcome.Status
	inv.Result = outcome.Result
	inv.Trace = outcome.Trace
	inv.InputTokens = outcome.InputTokens
	inv.OutputTokens = outcome.OutputTokens
	inv.ErrorCode = outcome.ErrorCode
	return s.complete(actorCtx, inv, duration, toolCalls, hypothesisCount(outcome.Result))
}

// finishFailure terminalizes a row that failed before the runner started. No
// timeline event is appended; the audit row carries the error code.
func (s *Service) finishFailure(ctx context.Context, inv Investigation, code string, trace Trace, toolCalls, hypotheses int, started time.Time) error {
	inv.Status = StatusFailed
	inv.ErrorCode = code
	inv.Result = nil
	inv.Trace = trace
	inv.ToolCalls = toolCalls
	inv.InputTokens = 0
	inv.OutputTokens = 0
	return s.complete(ctx, inv, s.now().Sub(started), toolCalls, hypotheses)
}

// complete writes the terminal row, the optional timeline event, the audit
// row, and the metrics. ErrNotActive means another writer already finished the
// row: the stale error code wins and nothing else is written.
func (s *Service) complete(ctx context.Context, inv Investigation, duration time.Duration, toolCalls, hypotheses int) error {
	inv.DurationMS = durationMillis(duration)
	inv.ToolCalls = toolCalls

	if err := s.store.Complete(ctx, inv); err != nil {
		if errors.Is(err, ErrNotActive) {
			return nil
		}
		return fmt.Errorf("copilot: complete investigation %s: %w", inv.ID, err)
	}

	if inv.Status == StatusOK || inv.Status == StatusInsufficient {
		if err := s.incidents.RecordInvestigationEvent(
			ctx,
			inv.WorkspaceID,
			inv.IncidentID,
			inv.ID,
			string(inv.Status),
			s.modelName(inv),
			toolCalls,
			hypotheses,
		); err != nil {
			s.logger.Log(ctx, slog.LevelWarn, "investigation timeline event failed",
				"investigation_id", inv.ID,
				"incident_id", inv.IncidentID,
				"error", err,
			)
		}
	}

	s.recordAudit(ctx, inv, toolCalls, hypotheses)
	s.metrics.ObserveInvestigation(string(inv.Status), duration, toolCalls, inv.InputTokens, inv.OutputTokens)
	s.logger.Log(ctx, slog.LevelInfo, "investigation completed",
		"investigation_id", inv.ID,
		"incident_id", inv.IncidentID,
		"status", inv.Status,
		"error_code", inv.ErrorCode,
		"tool_calls", toolCalls,
		"hypotheses", hypotheses,
		"input_tokens", inv.InputTokens,
		"output_tokens", inv.OutputTokens,
		"duration_ms", inv.DurationMS,
	)
	return nil
}

// recordAudit writes the bounded investigation_completed security event.
// Audit is best-effort, so a failure does not change the run result.
func (s *Service) recordAudit(ctx context.Context, inv Investigation, toolCalls, hypotheses int) {
	if s.queries == nil {
		return
	}
	detail := map[string]any{
		"investigation_id": inv.ID.String(),
		"incident_id":      inv.IncidentID.String(),
		"model":            s.modelName(inv),
		"status":           string(inv.Status),
		"tool_calls":       toolCalls,
		"hypotheses":       hypotheses,
		"input_tokens":     inv.InputTokens,
		"output_tokens":    inv.OutputTokens,
		"duration_ms":      inv.DurationMS,
	}
	if inv.ErrorCode != "" {
		detail["error_code"] = inv.ErrorCode
	}
	audit.RecordBestEffort(ctx, s.queries, audit.Event{
		WorkspaceID: &inv.WorkspaceID,
		ActorUserID: inv.CreatedBy,
		Kind:        auditKindInvestigationCompleted,
		Detail:      detail,
	})
}

// modelName returns the model the run actually used, falling back to the row's
// stored model for a service built without an explicit one.
func (s *Service) modelName(inv Investigation) string {
	if s.model != "" {
		return s.model
	}
	return inv.Model
}

// hypothesisCount reports how many causes the stored result carries.
func hypothesisCount(result *Result) int {
	if result == nil {
		return 0
	}
	return len(result.Hypotheses)
}

// durationMillis converts an interval to non-negative milliseconds.
func durationMillis(d time.Duration) int {
	if d < 0 {
		return 0
	}
	return int(d / time.Millisecond)
}

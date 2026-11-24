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
	"o1/internal/jobs"
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

// auditKindInvestigationStarted is the security event written when a member
// enqueues a run. It is best-effort and never blocks the start.
const auditKindInvestigationStarted = "investigation_started"

const (
	// defaultPerIncidentPerHour and defaultPerWorkspacePerDay bound how many
	// runs one incident or workspace may start. Failed runs count, so a broken
	// provider cannot be hammered for free.
	defaultPerIncidentPerHour = 3
	defaultPerWorkspacePerDay = 20
	// staleAfter is how old a queued or running row must be before a new start
	// treats it as abandoned and fails it.
	staleAfter = 5 * time.Minute
	// investigationQueue is the River queue the worker consumes.
	investigationQueue = "ai"
	// investigationMaxAttempts matches the worker's retry budget.
	investigationMaxAttempts = 3
)

// ErrLimit means the incident's hourly or the workspace's daily start cap has
// been reached.
var ErrLimit = errors.New("copilot: investigation limit reached")

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
	// PerIncidentPerHour and PerWorkspacePerDay bound Start. Values below one
	// fall back to the production defaults.
	PerIncidentPerHour int
	PerWorkspacePerDay int
	Now                func() time.Time
	Logger             *slog.Logger
	Metrics            *observability.Metrics
}

// Service runs investigations and owns the read surface used by the API. The
// worker calls Run; the API handlers call Start, Latest, and Get.
type Service struct {
	store              *Store
	incidents          *incidents.Service
	monitors           *monitors.Service
	workspaces         *workspaces.Service
	provider           ai.ToolProvider
	inserter           JobInserter
	pool               *pgxpool.Pool
	queries            *sqlc.Queries
	model              string
	limits             Limits
	perIncidentPerHour int
	perWorkspacePerDay int
	runner             *Runner
	now                func() time.Time
	logger             *slog.Logger
	metrics            *observability.Metrics
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
	perIncident := opts.PerIncidentPerHour
	if perIncident < 1 {
		perIncident = defaultPerIncidentPerHour
	}
	perWorkspace := opts.PerWorkspacePerDay
	if perWorkspace < 1 {
		perWorkspace = defaultPerWorkspacePerDay
	}

	svc := &Service{
		store:              opts.Store,
		incidents:          opts.Incidents,
		monitors:           opts.Monitors,
		workspaces:         opts.Workspaces,
		provider:           opts.Provider,
		inserter:           opts.Inserter,
		pool:               opts.Pool,
		queries:            opts.Queries,
		model:              opts.Model,
		limits:             limits,
		perIncidentPerHour: perIncident,
		perWorkspacePerDay: perWorkspace,
		now:                now,
		logger:             logger,
		metrics:            opts.Metrics,
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

// Start authorizes a member, verifies that the incident belongs to the path
// workspace, reaps abandoned rows, enforces the per-incident and per-workspace
// start caps, and writes the queued row together with its River job in one
// transaction. A nil provider does not matter here: Start only enqueues work.
//
// ErrActive means the incident already has an active run; the returned
// investigation is that run. ErrLimit means a cap was reached. Authorization
// failures are passed through from workspaces and incidents, so a viewer gets
// workspaces.ErrForbidden and an incident outside the workspace gets
// workspaces.ErrNotFound.
func (s *Service) Start(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) (Investigation, error) {
	if s == nil || s.store == nil || s.incidents == nil || s.workspaces == nil || s.inserter == nil {
		return Investigation{}, errors.New("copilot: investigation service is not configured")
	}
	if _, err := s.workspaces.Authorize(ctx, actorID, workspaceID, workspaces.RoleMember); err != nil {
		return Investigation{}, err
	}
	// Authorizing the member above is not enough: the incident id must belong
	// to this workspace, and incidents.Get reports a cross-workspace id as not
	// found.
	if _, err := s.incidents.Get(ctx, actorID, workspaceID, incidentID); err != nil {
		return Investigation{}, err
	}

	pool := s.pool
	if pool == nil {
		pool = s.store.pool
	}
	if pool == nil {
		return Investigation{}, errors.New("copilot: start investigation: no database pool")
	}
	queries := s.queries
	if queries == nil {
		queries = sqlc.New(pool)
	}

	now := s.now().UTC()

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Investigation{}, fmt.Errorf("copilot: begin investigation start: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	qtx := queries.WithTx(tx)
	if _, err := qtx.ReapStaleInvestigations(ctx, sqlc.ReapStaleInvestigationsParams{
		IncidentID: incidentID,
		CreatedAt:  now.Add(-staleAfter),
	}); err != nil {
		return Investigation{}, fmt.Errorf("copilot: reap stale investigations: %w", err)
	}

	incidentCount, err := qtx.CountInvestigationsForIncidentSince(ctx, sqlc.CountInvestigationsForIncidentSinceParams{
		IncidentID: incidentID,
		CreatedAt:  now.Add(-time.Hour),
	})
	if err != nil {
		return Investigation{}, fmt.Errorf("copilot: count investigations for incident: %w", err)
	}
	if incidentCount >= int64(s.perIncidentPerHour) {
		return Investigation{}, ErrLimit
	}

	workspaceCount, err := qtx.CountInvestigationsForWorkspaceSince(ctx, sqlc.CountInvestigationsForWorkspaceSinceParams{
		WorkspaceID: workspaceID,
		CreatedAt:   now.Add(-24 * time.Hour),
	})
	if err != nil {
		return Investigation{}, fmt.Errorf("copilot: count investigations for workspace: %w", err)
	}
	if workspaceCount >= int64(s.perWorkspacePerDay) {
		return Investigation{}, ErrLimit
	}

	model := s.model
	if model == "" {
		model = "unknown"
	}
	created, err := s.store.CreateTx(ctx, tx, Investigation{
		WorkspaceID:   workspaceID,
		IncidentID:    incidentID,
		Status:        StatusQueued,
		Model:         model,
		PromptVersion: PromptVersion,
		CreatedBy:     &actorID,
		CreatedAt:     now,
	})
	if err != nil {
		if errors.Is(err, ErrActive) {
			// The failed insert aborted this transaction, so the active row
			// must be read after rolling it back.
			_ = tx.Rollback(ctx)
			active, lookupErr := s.store.Latest(ctx, workspaceID, incidentID)
			if lookupErr != nil || active == nil {
				return Investigation{}, ErrActive
			}
			return *active, ErrActive
		}
		return Investigation{}, err
	}

	if _, err := s.inserter.InsertTx(ctx, tx, jobs.InvestigationArgs{InvestigationID: created.ID}, &river.InsertOpts{
		Queue:       investigationQueue,
		MaxAttempts: investigationMaxAttempts,
	}); err != nil {
		return Investigation{}, fmt.Errorf("copilot: enqueue investigation %s: %w", created.ID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Investigation{}, fmt.Errorf("copilot: commit investigation start: %w", err)
	}

	audit.RecordBestEffort(ctx, queries, audit.Event{
		WorkspaceID: &workspaceID,
		ActorUserID: &actorID,
		Kind:        auditKindInvestigationStarted,
		Detail: map[string]any{
			"investigation_id": created.ID.String(),
			"incident_id":      incidentID.String(),
			"model":            created.Model,
			"status":           string(created.Status),
		},
	})
	return created, nil
}

// Latest returns the most recent investigation for an incident, or nil when
// none exists. It authorizes a viewer against the path workspace before the
// scoped lookup, so a key scoped to another workspace cannot read the row.
func (s *Service) Latest(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) (*Investigation, error) {
	if s == nil || s.store == nil || s.workspaces == nil {
		return nil, errors.New("copilot: investigation service is not configured")
	}
	if _, err := s.workspaces.Authorize(ctx, actorID, workspaceID, workspaces.RoleViewer); err != nil {
		return nil, err
	}
	return s.store.Latest(ctx, workspaceID, incidentID)
}

// Get returns one investigation scoped to its workspace and incident. It
// authorizes a viewer against the path workspace first, so a key scoped to
// another workspace gets ErrNotFound rather than another tenant's data.
func (s *Service) Get(ctx context.Context, actorID, workspaceID, incidentID, id uuid.UUID) (Investigation, error) {
	if s == nil || s.store == nil || s.workspaces == nil {
		return Investigation{}, errors.New("copilot: investigation service is not configured")
	}
	if _, err := s.workspaces.Authorize(ctx, actorID, workspaceID, workspaces.RoleViewer); err != nil {
		return Investigation{}, err
	}
	return s.store.Get(ctx, workspaceID, incidentID, id)
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

// complete writes the terminal row and, for a successful run, its timeline
// event in one transaction. A failed event insert rolls the status back so River
// can retry; the row must never be terminal without the event it promised.
// ErrNotActive means another writer already finished the row: the stale error
// code wins and nothing else is written. Audit and metrics happen only after the
// transaction commits.
func (s *Service) complete(ctx context.Context, inv Investigation, duration time.Duration, toolCalls, hypotheses int) error {
	inv.DurationMS = durationMillis(duration)
	inv.ToolCalls = toolCalls
	wantsEvent := inv.Status == StatusOK || inv.Status == StatusInsufficient

	pool := s.pool
	if pool == nil && s.store != nil {
		pool = s.store.pool
	}
	if pool == nil {
		return fmt.Errorf("copilot: complete investigation %s: no database pool", inv.ID)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("copilot: begin investigation %s completion: %w", inv.ID, err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if err := s.store.CompleteTx(ctx, tx, inv); err != nil {
		if errors.Is(err, ErrNotActive) {
			return nil
		}
		return fmt.Errorf("copilot: complete investigation %s: %w", inv.ID, err)
	}
	if wantsEvent {
		if err := s.incidents.RecordInvestigationEventTx(
			ctx,
			tx,
			inv.WorkspaceID,
			inv.IncidentID,
			inv.ID,
			string(inv.Status),
			s.modelName(inv),
			toolCalls,
			hypotheses,
		); err != nil {
			return fmt.Errorf("copilot: record investigation %s event: %w", inv.ID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("copilot: commit investigation %s completion: %w", inv.ID, err)
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

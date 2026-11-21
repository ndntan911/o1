package insights

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"o1/internal/ai"
	"o1/internal/audit"
	"o1/internal/db/sqlc"
	"o1/internal/incidents"
	"o1/internal/monitors"
)

// Errors callers can distinguish.
var (
	// ErrDisabled means no provider is configured; the API hides the feature.
	ErrDisabled = errors.New("insights: not configured")
	// ErrDuplicate means the latest analysis already covers this exact payload.
	ErrDuplicate = errors.New("insights: already generated for this data")
)

// IncidentSource is the slice of the incidents service this package needs.
type IncidentSource interface {
	Get(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) (incidents.IncidentDetail, error)
	Deliveries(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) ([]incidents.Delivery, error)
}

// MonitorSource is the slice of the monitors service this package needs.
type MonitorSource interface {
	Get(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID) (monitors.Monitor, error)
}

// Service generates and stores incident analyses.
type Service struct {
	store     Store
	provider  ai.Provider
	model     string
	incidents IncidentSource
	monitors  MonitorSource
	queries   *sqlc.Queries
	now       func() time.Time
	timeout   time.Duration
	maxTokens int
}

// Options configures a Service. A nil provider disables the feature.
type Options struct {
	Store     Store
	Provider  ai.Provider
	Model     string
	Incidents IncidentSource
	Monitors  MonitorSource
	Queries   *sqlc.Queries
	Now       func() time.Time
	Timeout   time.Duration
	MaxTokens int
}

// NewService builds a Service.
func NewService(opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 700
	}
	return &Service{
		store:     opts.Store,
		provider:  opts.Provider,
		model:     opts.Model,
		incidents: opts.Incidents,
		monitors:  opts.Monitors,
		queries:   opts.Queries,
		now:       now,
		timeout:   timeout,
		maxTokens: maxTokens,
	}
}

// Enabled reports whether the feature can run at all.
func (s *Service) Enabled() bool {
	return s != nil && s.provider != nil && s.store != nil && s.incidents != nil
}

// Latest returns the stored analysis for an incident, or nil.
func (s *Service) Latest(ctx context.Context, workspaceID, incidentID uuid.UUID) (*Insight, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	return s.store.Latest(ctx, workspaceID, incidentID)
}

// Generate builds the payload, asks the provider, validates the answer and
// stores it. A duplicate is only refused when force is false and the latest
// analysis was produced from the same data and prompt version.
func (s *Service) Generate(
	ctx context.Context,
	actorID, workspaceID, incidentID uuid.UUID,
	force bool,
) (Insight, error) {
	if !s.Enabled() {
		return Insight{}, ErrDisabled
	}

	detail, err := s.incidents.Get(ctx, actorID, workspaceID, incidentID)
	if err != nil {
		return Insight{}, err
	}
	// Deliveries are context, not the incident itself. A viewer cannot read
	// them (they name recipients) and a delivery table can be empty; neither is
	// a reason to refuse an analysis.
	deliveries, err := s.incidents.Deliveries(ctx, actorID, workspaceID, incidentID)
	if err != nil {
		deliveries = nil
	}

	var monitor *monitors.Monitor
	if s.monitors != nil {
		found, err := s.monitors.Get(ctx, actorID, workspaceID, detail.Incident.MonitorID)
		switch {
		case err == nil:
			monitor = &found
		case errors.Is(err, monitors.ErrNotFound):
			monitor = nil
		default:
			return Insight{}, err
		}
	}

	payload := BuildPayload(detail, deliveries, monitor, s.now())
	hash := Hash(payload, PromptVersion)

	if !force {
		latest, err := s.store.Latest(ctx, workspaceID, incidentID)
		if err != nil {
			return Insight{}, err
		}
		if latest != nil && latest.PayloadHash == hash && latest.Status != StatusFailed {
			return *latest, fmt.Errorf("%w", ErrDuplicate)
		}
	}

	prompt, err := BuildPrompt(payload)
	if err != nil {
		return Insight{}, err
	}

	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	response, err := s.provider.Complete(callCtx, ai.Request{
		Model:     s.model,
		System:    SystemPrompt,
		User:      prompt,
		MaxTokens: s.maxTokens,
	})

	model := s.model
	if response.Model != "" {
		model = response.Model
	}

	insight := Insight{
		WorkspaceID:   workspaceID,
		IncidentID:    incidentID,
		Model:         model,
		PromptVersion: PromptVersion,
		PayloadHash:   hash,
		InputTokens:   response.InputTokens,
		OutputTokens:  response.OutputTokens,
		CreatedBy:     &actorID,
		CreatedAt:     s.now(),
	}

	if err != nil {
		insight.Status = StatusFailed
		insight.Result = emptyResult("The model call failed. Nothing was analysed.")
	} else {
		result, parseErr := Parse(response.Text, EventIDs(detail))
		switch {
		case parseErr != nil:
			insight.Status = StatusFailed
			insight.Result = emptyResult("The model returned an answer that could not be read, so it is not shown.")
		case !result.Sufficient || len(result.Hypotheses) == 0:
			insight.Status = StatusInsufficient
			insight.Result = result
		default:
			insight.Status = StatusOK
			insight.Result = result
		}
	}

	stored, storeErr := s.store.Insert(ctx, insight)
	if storeErr != nil {
		return Insight{}, storeErr
	}

	s.recordAudit(ctx, actorID, workspaceID, incidentID, stored, err)

	if err != nil {
		return stored, fmt.Errorf("insights: model call failed: %w", err)
	}
	return stored, nil
}

// recordAudit writes the generation attempt. Failures to audit never change
// the response.
func (s *Service) recordAudit(
	ctx context.Context,
	actorID, workspaceID, incidentID uuid.UUID,
	insight Insight,
	modelErr error,
) {
	if s.queries == nil {
		return
	}
	detail := map[string]any{
		"incident_id":    incidentID.String(),
		"insight_id":     insight.ID.String(),
		"model":          insight.Model,
		"prompt_version": insight.PromptVersion,
		"status":         insight.Status,
		"input_tokens":   insight.InputTokens,
		"output_tokens":  insight.OutputTokens,
	}
	if modelErr != nil {
		detail["error"] = "model_call_failed"
	}
	audit.RecordBestEffort(ctx, s.queries, audit.Event{
		WorkspaceID: &workspaceID,
		ActorUserID: &actorID,
		Kind:        "incident.insight_generated",
		Detail:      detail,
	})
}

// emptyResult keeps the response shape stable: consumers range over
// hypotheses, and a nil slice marshals to null.
func emptyResult(assessment string) Result {
	return Result{Assessment: assessment, Hypotheses: []Hypothesis{}}
}

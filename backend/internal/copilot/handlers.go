package copilot

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"o1/internal/auth"
	"o1/internal/httpx"
	"o1/internal/workspaces"
)

// investigationResponse is the public shape of a stored run. It deliberately
// omits workspace_id, created_by, prompt_version, seed_hash, duration_ms, and
// the trace's raw calls and model calls.
type investigationResponse struct {
	ID           uuid.UUID          `json:"id"`
	IncidentID   uuid.UUID          `json:"incident_id"`
	Status       Status             `json:"status"`
	Model        string             `json:"model"`
	Result       *Result            `json:"result"`
	Trace        investigationTrace `json:"trace"`
	Attempts     int                `json:"attempts"`
	ToolCalls    int                `json:"tool_calls"`
	InputTokens  int                `json:"input_tokens"`
	OutputTokens int                `json:"output_tokens"`
	ErrorCode    string             `json:"error_code"`
	CreatedAt    time.Time          `json:"created_at"`
	CompletedAt  *time.Time         `json:"completed_at"`
}

// investigationTrace exposes only the model-facing steps of a run.
type investigationTrace struct {
	Steps []Step `json:"steps"`
}

// StartHandler returns
// POST /api/v1/workspaces/{workspaceID}/incidents/{incidentID}/investigations.
// The route is session-only; the service enforces the member role and returns
// 202 because the run happens asynchronously.
func StartHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !investigationsEnabled(w, svc) {
			return
		}
		actorID, workspaceID, incidentID, ok := investigationIDs(w, r)
		if !ok {
			return
		}

		created, err := svc.Start(r.Context(), actorID, workspaceID, incidentID)
		if err != nil {
			switch {
			case errors.Is(err, ErrActive):
				if created.ID != uuid.Nil {
					httpx.WriteJSON(w, http.StatusConflict, newInvestigationResponse(created))
					return
				}
				investigationProblem(w, http.StatusConflict, httpx.CodeQuotaExceeded, "Investigation Already Running")
			case errors.Is(err, ErrLimit):
				investigationProblem(w, http.StatusTooManyRequests, httpx.CodeQuotaExceeded, "Investigation Limit Reached")
			case errors.Is(err, workspaces.ErrNotFound):
				investigationProblem(w, http.StatusNotFound, httpx.CodeNotFound, "Not Found")
			case errors.Is(err, workspaces.ErrForbidden):
				investigationProblem(w, http.StatusForbidden, httpx.CodeForbidden, "Forbidden")
			default:
				investigationProblem(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal Server Error")
			}
			return
		}
		httpx.WriteJSON(w, http.StatusAccepted, newInvestigationResponse(created))
	}
}

// LatestHandler returns
// GET /api/v1/workspaces/{workspaceID}/incidents/{incidentID}/investigations/latest.
// It accepts a session or an API key; the service authorizes a viewer against
// the path workspace.
func LatestHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !investigationsEnabled(w, svc) {
			return
		}
		actorID, workspaceID, incidentID, ok := investigationIDs(w, r)
		if !ok {
			return
		}

		inv, err := svc.Latest(r.Context(), actorID, workspaceID, incidentID)
		if err != nil {
			writeInvestigationReadError(w, err)
			return
		}
		if inv == nil {
			investigationProblem(w, http.StatusNotFound, httpx.CodeNotFound, "No investigation for this incident")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, newInvestigationResponse(*inv))
	}
}

// GetHandler returns
// GET /api/v1/workspaces/{workspaceID}/incidents/{incidentID}/investigations/{investigationID}.
func GetHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !investigationsEnabled(w, svc) {
			return
		}
		actorID, workspaceID, incidentID, ok := investigationIDs(w, r)
		if !ok {
			return
		}
		investigationID, err := uuid.Parse(r.PathValue("investigationID"))
		if err != nil {
			investigationProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid investigation id")
			return
		}

		inv, err := svc.Get(r.Context(), actorID, workspaceID, incidentID, investigationID)
		if err != nil {
			writeInvestigationReadError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, newInvestigationResponse(inv))
	}
}

// investigationsEnabled reports whether the router has a configured service.
// A nil service is the deployment-wide off switch and answers 501.
func investigationsEnabled(w http.ResponseWriter, svc *Service) bool {
	if svc == nil || !svc.Enabled() {
		investigationProblem(w, http.StatusNotImplemented, httpx.CodeNotImplemented, "Copilot is not configured")
		return false
	}
	return true
}

// investigationIDs reads the principal and the workspace/incident path ids.
func investigationIDs(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, uuid.UUID, bool) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		investigationProblem(w, http.StatusUnauthorized, httpx.CodeUnauthenticated, "Authentication required")
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	workspaceID, err := uuid.Parse(r.PathValue("workspaceID"))
	if err != nil {
		investigationProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid workspace id")
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	incidentID, err := uuid.Parse(r.PathValue("incidentID"))
	if err != nil {
		investigationProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid incident id")
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	return principal.UserID, workspaceID, incidentID, true
}

// writeInvestigationReadError maps read-service sentinels onto problem
// responses. Non-members and cross-tenant reads both look like 404.
func writeInvestigationReadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, workspaces.ErrNotFound):
		investigationProblem(w, http.StatusNotFound, httpx.CodeNotFound, "Not Found")
	case errors.Is(err, workspaces.ErrForbidden):
		investigationProblem(w, http.StatusForbidden, httpx.CodeForbidden, "Forbidden")
	default:
		investigationProblem(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal Server Error")
	}
}

// newInvestigationResponse projects a stored run onto the public shape. The
// trace keeps only steps and steps is always an array.
func newInvestigationResponse(inv Investigation) investigationResponse {
	steps := inv.Trace.Steps
	if steps == nil {
		steps = []Step{}
	}
	return investigationResponse{
		ID:           inv.ID,
		IncidentID:   inv.IncidentID,
		Status:       inv.Status,
		Model:        inv.Model,
		Result:       inv.Result,
		Trace:        investigationTrace{Steps: steps},
		Attempts:     inv.Attempts,
		ToolCalls:    inv.ToolCalls,
		InputTokens:  inv.InputTokens,
		OutputTokens: inv.OutputTokens,
		ErrorCode:    inv.ErrorCode,
		CreatedAt:    inv.CreatedAt,
		CompletedAt:  inv.CompletedAt,
	}
}

// investigationProblem emits a problem response without field details.
func investigationProblem(w http.ResponseWriter, status int, code, title string) {
	httpx.WriteProblem(w, httpx.Problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Code:   code,
	})
}

package insights

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"o1/internal/auth"
	"o1/internal/httpx"
)

// GenerateHandler returns
// POST /api/v1/workspaces/{workspaceID}/incidents/{incidentID}/insights.
func GenerateHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actorID, workspaceID, incidentID, ok := insightIDs(w, r)
		if !ok {
			return
		}
		if !svc.Enabled() {
			insightProblem(w, http.StatusNotFound, httpx.CodeNotFound, "Insights are not available")
			return
		}

		force := r.URL.Query().Get("force") == "true"
		insight, err := svc.Generate(r.Context(), actorID, workspaceID, incidentID, force)

		switch {
		case err == nil:
			httpx.WriteJSON(w, http.StatusOK, insight)
			return
		case errors.Is(err, ErrDuplicate):
			httpx.WriteJSON(w, http.StatusConflict, insight)
			return
		case errors.Is(err, ErrDisabled):
			insightProblem(w, http.StatusNotFound, httpx.CodeNotFound, "Insights are not available")
			return
		case insight.ID != uuid.Nil:
			// The call failed but the attempt was stored; the body says so.
			httpx.WriteJSON(w, http.StatusOK, insight)
			return
		default:
			insightProblem(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal Server Error")
			return
		}
	}
}

// LatestHandler returns
// GET /api/v1/workspaces/{workspaceID}/incidents/{incidentID}/insights/latest.
func LatestHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, workspaceID, incidentID, ok := insightIDs(w, r)
		if !ok {
			return
		}
		if !svc.Enabled() {
			insightProblem(w, http.StatusNotFound, httpx.CodeNotFound, "Insights are not available")
			return
		}

		insight, err := svc.Latest(r.Context(), workspaceID, incidentID)
		if err != nil {
			insightProblem(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal Server Error")
			return
		}
		if insight == nil {
			insightProblem(w, http.StatusNotFound, httpx.CodeNotFound, "No analysis for this incident")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, insight)
	}
}

// insightIDs reads the principal and both path ids, writing 401/400 on failure.
func insightIDs(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, uuid.UUID, bool) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		insightProblem(w, http.StatusUnauthorized, httpx.CodeUnauthenticated, "Authentication required")
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	workspaceID, err := uuid.Parse(r.PathValue("workspaceID"))
	if err != nil {
		insightProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid workspace id")
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	incidentID, err := uuid.Parse(r.PathValue("incidentID"))
	if err != nil {
		insightProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid incident id")
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	return principal.UserID, workspaceID, incidentID, true
}

// insightProblem writes a problem response without field details.
func insightProblem(w http.ResponseWriter, status int, code, title string) {
	httpx.WriteProblem(w, httpx.Problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Code:   code,
	})
}

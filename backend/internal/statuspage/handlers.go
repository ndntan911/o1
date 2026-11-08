package statuspage

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"o1/internal/auth"
	"o1/internal/httpx"
)

// GetConfigHandler returns GET /workspaces/{workspaceID}/status-page.
func GetConfigHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !statusPageServiceAvailable(w, svc) {
			return
		}
		principal, ok := statusPagePrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, ok := statusPageWorkspaceID(w, r)
		if !ok {
			return
		}
		cfg, err := svc.GetConfig(r.Context(), principal.UserID, workspaceID)
		if err != nil {
			statusPageServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, cfg)
	}
}

// UpdateConfigHandler returns PUT /workspaces/{workspaceID}/status-page.
func UpdateConfigHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !statusPageServiceAvailable(w, svc) {
			return
		}
		principal, ok := statusPagePrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, ok := statusPageWorkspaceID(w, r)
		if !ok {
			return
		}
		var in ConfigInput
		if !decodeStatusPageBody(w, r, &in) {
			return
		}
		cfg, err := svc.UpdateConfig(r.Context(), principal.UserID, workspaceID, in)
		if err != nil {
			statusPageServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, cfg)
	}
}

// PublicHandler returns GET /public/status/{slug}. Successful responses may be
// cached briefly by shared caches; 404s must never be cached so enabling a page
// takes effect immediately.
func PublicHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !statusPageServiceAvailable(w, svc) {
			return
		}
		slug := strings.TrimSpace(r.PathValue("slug"))
		if slug == "" {
			statusPageProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Validation Error", "", map[string]string{
				"slug": "slug is required",
			})
			return
		}
		page, err := svc.PublicPage(r.Context(), slug)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				w.Header().Set("Cache-Control", "no-store")
				statusPageProblem(w, http.StatusNotFound, httpx.CodeNotFound, "Not Found", "", nil)
				return
			}
			statusPageProblem(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal Server Error", "", nil)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=15, stale-while-revalidate=15")
		httpx.WriteJSON(w, http.StatusOK, page)
	}
}

func statusPageServiceAvailable(w http.ResponseWriter, svc *Service) bool {
	if svc != nil {
		return true
	}
	statusPageProblem(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal Server Error", "", nil)
	return false
}

func statusPagePrincipal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		statusPageProblem(w, http.StatusUnauthorized, httpx.CodeUnauthenticated, "Authentication required", "", nil)
		return auth.Principal{}, false
	}
	return principal, true
}

func statusPageWorkspaceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	workspaceID, err := uuid.Parse(r.PathValue("workspaceID"))
	if err != nil {
		statusPageProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid workspace id", "", nil)
		return uuid.Nil, false
	}
	return workspaceID, true
}

func decodeStatusPageBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		statusPageProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid request body", "", nil)
		return false
	}
	return true
}

func statusPageServiceError(w http.ResponseWriter, err error) {
	var validation *ValidationError
	switch {
	case errors.As(err, &validation):
		statusPageProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Validation Error", "", validation.Fields)
	case errors.Is(err, ErrSuspended):
		statusPageProblem(w, http.StatusForbidden, httpx.CodeWorkspaceSuspended, "Workspace Suspended", "", nil)
	case errors.Is(err, ErrNotFound):
		statusPageProblem(w, http.StatusNotFound, httpx.CodeNotFound, "Not Found", "", nil)
	case errors.Is(err, ErrForbidden):
		statusPageProblem(w, http.StatusForbidden, httpx.CodeForbidden, "Forbidden", "", nil)
	default:
		statusPageProblem(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal Server Error", "", nil)
	}
}

func statusPageProblem(w http.ResponseWriter, status int, code, title, detail string, fields map[string]string) {
	httpx.WriteProblem(w, httpx.Problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Code:   code,
		Detail: detail,
		Fields: fields,
	})
}

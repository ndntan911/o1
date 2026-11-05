package monitors

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"o1/internal/auth"
	"o1/internal/httpx"
)

// defaultListLimit and maxListLimit bound GET /monitors pages.
const (
	defaultListLimit = 50
	maxListLimit     = 100
)

// monitorListResponse wraps a page of monitors.
type monitorListResponse struct {
	Monitors   []Monitor `json:"monitors"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

// CreateHandler returns POST /workspaces/{workspaceID}/monitors.
func CreateHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serviceAvailable(w, svc) {
			return
		}
		principal, ok := requirePrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, ok := requireWorkspaceID(w, r)
		if !ok {
			return
		}
		var in Input
		if !decodeJSON(w, r, &in) {
			return
		}
		monitor, err := svc.Create(r.Context(), principal.UserID, workspaceID, in)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, monitor)
	}
}

// ListHandler returns GET /workspaces/{workspaceID}/monitors.
func ListHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serviceAvailable(w, svc) {
			return
		}
		principal, ok := requirePrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, ok := requireWorkspaceID(w, r)
		if !ok {
			return
		}
		limit, ok := parseLimit(w, r, defaultListLimit, maxListLimit)
		if !ok {
			return
		}
		cursor, ok := parseCursor(w, r)
		if !ok {
			return
		}
		list, err := svc.List(r.Context(), principal.UserID, workspaceID, cursor, limit)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		resp := monitorListResponse{Monitors: list}
		if len(list) == limit {
			last := list[len(list)-1]
			resp.NextCursor = httpx.EncodeCursor(httpx.PageCursor{Time: last.CreatedAt, ID: last.ID})
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	}
}

// GetHandler returns GET /workspaces/{workspaceID}/monitors/{monitorID}.
func GetHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serviceAvailable(w, svc) {
			return
		}
		principal, workspaceID, monitorID, ok := requireIDs(w, r)
		if !ok {
			return
		}
		monitor, err := svc.Get(r.Context(), principal.UserID, workspaceID, monitorID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, monitor)
	}
}

// UpdateHandler returns PATCH /workspaces/{workspaceID}/monitors/{monitorID}.
func UpdateHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serviceAvailable(w, svc) {
			return
		}
		principal, workspaceID, monitorID, ok := requireIDs(w, r)
		if !ok {
			return
		}
		var in UpdateInput
		if !decodeJSON(w, r, &in) {
			return
		}
		monitor, err := svc.Update(r.Context(), principal.UserID, workspaceID, monitorID, in)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, monitor)
	}
}

// DeleteHandler returns DELETE /workspaces/{workspaceID}/monitors/{monitorID}.
func DeleteHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serviceAvailable(w, svc) {
			return
		}
		principal, workspaceID, monitorID, ok := requireIDs(w, r)
		if !ok {
			return
		}
		if err := svc.Delete(r.Context(), principal.UserID, workspaceID, monitorID); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// PauseHandler returns POST /workspaces/{workspaceID}/monitors/{monitorID}/pause.
func PauseHandler(svc *Service) http.HandlerFunc {
	return setPausedHandler(svc, true)
}

// ResumeHandler returns POST /workspaces/{workspaceID}/monitors/{monitorID}/resume.
func ResumeHandler(svc *Service) http.HandlerFunc {
	return setPausedHandler(svc, false)
}

func setPausedHandler(svc *Service, paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serviceAvailable(w, svc) {
			return
		}
		principal, workspaceID, monitorID, ok := requireIDs(w, r)
		if !ok {
			return
		}
		monitor, err := svc.SetPaused(r.Context(), principal.UserID, workspaceID, monitorID, paused)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, monitor)
	}
}

// serviceAvailable guards handlers when the router has no database-backed
// monitor service. Authenticated requests to such a router get an opaque 500.
func serviceAvailable(w http.ResponseWriter, svc *Service) bool {
	if svc == nil {
		writeProblem(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal Server Error")
		return false
	}
	return true
}

// requirePrincipal reads the authenticated principal, writing 401 when absent.
func requirePrincipal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, httpx.CodeUnauthenticated, "Authentication required")
		return auth.Principal{}, false
	}
	return principal, true
}

// requireWorkspaceID parses the workspaceID path value.
func requireWorkspaceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("workspaceID"))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid workspace id")
		return uuid.Nil, false
	}
	return id, true
}

// requireIDs parses both path IDs and the authenticated principal.
func requireIDs(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, uuid.UUID, bool) {
	principal, ok := requirePrincipal(w, r)
	if !ok {
		return auth.Principal{}, uuid.Nil, uuid.Nil, false
	}
	workspaceID, ok := requireWorkspaceID(w, r)
	if !ok {
		return auth.Principal{}, uuid.Nil, uuid.Nil, false
	}
	monitorID, err := uuid.Parse(r.PathValue("monitorID"))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid monitor id")
		return auth.Principal{}, uuid.Nil, uuid.Nil, false
	}
	return principal, workspaceID, monitorID, true
}

// decodeJSON decodes a strict JSON body, writing a 400 problem on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid request body")
		return false
	}
	return true
}

// parseLimit parses and validates the limit query parameter.
func parseLimit(w http.ResponseWriter, r *http.Request, fallback, max int) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return fallback, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		writeFieldsProblem(w, map[string]string{"limit": "limit must be a positive integer"})
		return 0, false
	}
	if n > max {
		n = max
	}
	return n, true
}

// parseCursor decodes the cursor query parameter when present.
func parseCursor(w http.ResponseWriter, r *http.Request) (*Cursor, bool) {
	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return nil, true
	}
	c, err := httpx.DecodeCursor(raw)
	if err != nil {
		writeFieldsProblem(w, map[string]string{"cursor": "invalid cursor"})
		return nil, false
	}
	return &Cursor{Time: c.Time, ID: c.ID}, true
}

// writeServiceError maps service errors onto problem responses.
func writeServiceError(w http.ResponseWriter, err error) {
	var validation *ValidationError
	var rateLimit *RateLimitError

	switch {
	case errors.As(err, &validation):
		writeFieldsProblem(w, validation.Fields)
	case errors.As(err, &rateLimit):
		retry := int(math.Ceil(rateLimit.RetryAfter.Seconds()))
		if retry < 1 {
			retry = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		httpx.WriteProblem(w, httpx.Problem{
			Type:   "about:blank",
			Title:  "Too Many Requests",
			Status: http.StatusTooManyRequests,
			Code:   httpx.CodeMonitorRateLimited,
		})
	case errors.Is(err, ErrSuspended):
		httpx.WriteProblem(w, httpx.Problem{
			Type:   "about:blank",
			Title:  "Workspace Suspended",
			Status: http.StatusForbidden,
			Detail: "This workspace is suspended. Reads remain available.",
			Code:   httpx.CodeWorkspaceSuspended,
		})
	case errors.Is(err, ErrQuotaExceeded):
		httpx.WriteProblem(w, httpx.Problem{
			Type:   "about:blank",
			Title:  "Quota Exceeded",
			Status: http.StatusConflict,
			Code:   httpx.CodeQuotaExceeded,
		})
	case errors.Is(err, ErrNotFound):
		writeProblem(w, http.StatusNotFound, httpx.CodeNotFound, "Not Found")
	case errors.Is(err, ErrForbidden):
		writeProblem(w, http.StatusForbidden, httpx.CodeForbidden, "Forbidden")
	case errors.Is(err, ErrValidation):
		writeProblem(w, http.StatusBadRequest, httpx.CodeValidationError, err.Error())
	default:
		writeProblem(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal Server Error")
	}
}

// writeFieldsProblem responds 400 with per-field messages.
func writeFieldsProblem(w http.ResponseWriter, fields map[string]string) {
	httpx.WriteProblem(w, httpx.Problem{
		Type:   "about:blank",
		Title:  "Validation Error",
		Status: http.StatusBadRequest,
		Code:   httpx.CodeValidationError,
		Fields: fields,
	})
}

// writeProblem emits a problem response without fields.
func writeProblem(w http.ResponseWriter, status int, code, title string) {
	httpx.WriteProblem(w, httpx.Problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Code:   code,
	})
}

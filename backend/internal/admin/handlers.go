package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"o1/internal/auth"
	"o1/internal/httpx"
	"o1/internal/monitors"
)

const (
	defaultPageLimit = 50
	maxPageLimit     = 100
)

// workspaceListResponse wraps a page of administration workspaces.
type workspaceListResponse struct {
	Workspaces []AdminWorkspace `json:"workspaces"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// monitorListResponse wraps a page of administration monitors.
type monitorListResponse struct {
	Monitors   []AdminMonitor `json:"monitors"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// securityEventListResponse wraps a page of security events.
type securityEventListResponse struct {
	Events     []SecurityEvent `json:"events"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// suspendRequest is the JSON body of a suspension.
type suspendRequest struct {
	Reason string `json:"reason"`
}

// WorkspacesHandler returns GET /admin/workspaces.
func WorkspacesHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serviceAvailable(w, svc) {
			return
		}
		suspended, ok := parseSuspended(w, r)
		if !ok {
			return
		}
		cursor, ok := parseCursor(w, r)
		if !ok {
			return
		}
		limit, ok := parseLimit(w, r)
		if !ok {
			return
		}
		workspaces, err := svc.ListWorkspaces(r.Context(), r.URL.Query().Get("q"), suspended, cursor, limit)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		resp := workspaceListResponse{Workspaces: workspaces}
		if len(workspaces) == limit {
			last := workspaces[len(workspaces)-1]
			resp.NextCursor = httpx.EncodeCursor(httpx.PageCursor{Time: last.CreatedAt, ID: last.ID})
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	}
}

// GetWorkspaceHandler returns GET /admin/workspaces/{workspaceID}.
func GetWorkspaceHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serviceAvailable(w, svc) {
			return
		}
		workspaceID, ok := parseWorkspaceID(w, r)
		if !ok {
			return
		}
		workspace, err := svc.GetWorkspace(r.Context(), workspaceID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, workspace)
	}
}

// SuspendHandler returns POST /admin/workspaces/{workspaceID}/suspend.
func SuspendHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serviceAvailable(w, svc) {
			return
		}
		adminID, ok := requireAdminID(w, r)
		if !ok {
			return
		}
		workspaceID, ok := parseWorkspaceID(w, r)
		if !ok {
			return
		}
		var req suspendRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if err := svc.Suspend(r.Context(), adminID, workspaceID, req.Reason); err != nil {
			writeServiceError(w, err)
			return
		}
		workspace, err := svc.GetWorkspace(r.Context(), workspaceID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, workspace)
	}
}

// UnsuspendHandler returns POST /admin/workspaces/{workspaceID}/unsuspend.
func UnsuspendHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serviceAvailable(w, svc) {
			return
		}
		adminID, ok := requireAdminID(w, r)
		if !ok {
			return
		}
		workspaceID, ok := parseWorkspaceID(w, r)
		if !ok {
			return
		}
		if err := svc.Unsuspend(r.Context(), adminID, workspaceID); err != nil {
			writeServiceError(w, err)
			return
		}
		workspace, err := svc.GetWorkspace(r.Context(), workspaceID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, workspace)
	}
}

// MonitorsHandler returns GET /admin/monitors.
func MonitorsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serviceAvailable(w, svc) {
			return
		}
		workspaceID, ok := parseOptionalWorkspaceID(w, r)
		if !ok {
			return
		}
		cursor, ok := parseCursor(w, r)
		if !ok {
			return
		}
		limit, ok := parseLimit(w, r)
		if !ok {
			return
		}
		list, err := svc.ListMonitors(r.Context(), workspaceID, r.URL.Query().Get("q"), cursor, limit)
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

// SecurityEventsHandler returns GET /admin/security-events.
func SecurityEventsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serviceAvailable(w, svc) {
			return
		}
		workspaceID, ok := parseOptionalWorkspaceID(w, r)
		if !ok {
			return
		}
		cursor, ok := parseCursor(w, r)
		if !ok {
			return
		}
		limit, ok := parseLimit(w, r)
		if !ok {
			return
		}
		events, err := svc.ListSecurityEvents(r.Context(), r.URL.Query().Get("kind"), workspaceID, cursor, limit)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		resp := securityEventListResponse{Events: events}
		if len(events) == limit {
			last := events[len(events)-1]
			resp.NextCursor = httpx.EncodeCursor(httpx.PageCursor{Time: last.CreatedAt, ID: last.ID})
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	}
}

// serviceAvailable guards handlers when the router has no admin service.
func serviceAvailable(w http.ResponseWriter, svc *Service) bool {
	if svc == nil {
		writeProblem(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal Server Error")
		return false
	}
	return true
}

// requireAdminID reads the authenticated administrator's ID.
func requireAdminID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, httpx.CodeUnauthenticated, "Authentication required")
		return uuid.Nil, false
	}
	return principal.UserID, true
}

// parseWorkspaceID parses the workspaceID path value.
func parseWorkspaceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("workspaceID"))
	if err != nil {
		writeFieldsProblem(w, map[string]string{"workspaceID": "invalid workspace id"})
		return uuid.Nil, false
	}
	return id, true
}

// parseOptionalWorkspaceID parses the optional workspace_id query parameter.
func parseOptionalWorkspaceID(w http.ResponseWriter, r *http.Request) (*uuid.UUID, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
	if raw == "" {
		return nil, true
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		writeFieldsProblem(w, map[string]string{"workspace_id": "invalid workspace id"})
		return nil, false
	}
	return &id, true
}

// parseSuspended parses the optional suspended query parameter.
func parseSuspended(w http.ResponseWriter, r *http.Request) (*bool, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("suspended"))
	if raw == "" {
		return nil, true
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		writeFieldsProblem(w, map[string]string{"suspended": "suspended must be true or false"})
		return nil, false
	}
	return &value, true
}

// parseCursor decodes the cursor query parameter when present.
func parseCursor(w http.ResponseWriter, r *http.Request) (*monitors.Cursor, bool) {
	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return nil, true
	}
	c, err := httpx.DecodeCursor(raw)
	if err != nil {
		writeFieldsProblem(w, map[string]string{"cursor": "invalid cursor"})
		return nil, false
	}
	return &monitors.Cursor{Time: c.Time, ID: c.ID}, true
}

// parseLimit parses and bounds the limit query parameter.
func parseLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultPageLimit, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		writeFieldsProblem(w, map[string]string{"limit": "limit must be a positive integer"})
		return 0, false
	}
	if n > maxPageLimit {
		n = maxPageLimit
	}
	return n, true
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

// writeServiceError maps admin service errors onto problem responses.
func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		writeProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Validation Error")
	case errors.Is(err, ErrNotFound):
		writeProblem(w, http.StatusNotFound, httpx.CodeNotFound, "Not Found")
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

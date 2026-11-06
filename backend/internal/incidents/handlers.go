package incidents

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"o1/internal/auth"
	"o1/internal/httpx"
)

// incidentListResponse wraps a page of incidents.
type incidentListResponse struct {
	Incidents  []Incident `json:"incidents"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

// deliveryListResponse wraps an incident's alert deliveries.
type deliveryListResponse struct {
	Deliveries []Delivery `json:"deliveries"`
}

// alertChannelListResponse wraps a workspace's alert channels.
type alertChannelListResponse struct {
	Channels []AlertChannel `json:"channels"`
}

// alertChannelPutRequest is the PUT /alert-channels/email body.
type alertChannelPutRequest struct {
	Recipients []string `json:"recipients"`
	Enabled    bool     `json:"enabled"`
}

// monitorAlertRequest is the PATCH /monitors/{monitorID}/alert-channel body.
type monitorAlertRequest struct {
	Enabled *bool `json:"enabled"`
}

// ListHandler returns GET /workspaces/{workspaceID}/incidents.
func ListHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !incidentServiceAvailable(w, svc) {
			return
		}
		principal, ok := incidentPrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, ok := incidentWorkspaceID(w, r)
		if !ok {
			return
		}
		limit, ok := incidentLimit(w, r)
		if !ok {
			return
		}
		cursor, ok := incidentCursor(w, r)
		if !ok {
			return
		}
		filter := IncidentFilter{Status: r.URL.Query().Get("status")}
		if raw := r.URL.Query().Get("monitor_id"); raw != "" {
			monitorID, err := uuid.Parse(raw)
			if err != nil {
				incidentFieldsProblem(w, map[string]string{"monitor_id": "monitor_id must be a UUID"})
				return
			}
			filter.MonitorID = &monitorID
		}

		list, err := svc.List(r.Context(), principal.UserID, workspaceID, filter, cursor, limit)
		if err != nil {
			incidentServiceError(w, err)
			return
		}
		resp := incidentListResponse{Incidents: list}
		if len(list) == limit {
			last := list[len(list)-1]
			resp.NextCursor = httpx.EncodeCursor(httpx.PageCursor{Time: last.StartedAt, ID: last.ID})
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	}
}

// GetHandler returns GET /workspaces/{workspaceID}/incidents/{incidentID}.
func GetHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !incidentServiceAvailable(w, svc) {
			return
		}
		principal, workspaceID, incidentID, ok := incidentIDs(w, r)
		if !ok {
			return
		}
		detail, err := svc.Get(r.Context(), principal.UserID, workspaceID, incidentID)
		if err != nil {
			incidentServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, detail)
	}
}

// AcknowledgeHandler returns POST /workspaces/{workspaceID}/incidents/{incidentID}/acknowledge.
func AcknowledgeHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !incidentServiceAvailable(w, svc) {
			return
		}
		principal, workspaceID, incidentID, ok := incidentIDs(w, r)
		if !ok {
			return
		}
		if err := svc.Acknowledge(r.Context(), principal.UserID, workspaceID, incidentID); err != nil {
			incidentServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// DeliveriesHandler returns GET /workspaces/{workspaceID}/incidents/{incidentID}/deliveries.
func DeliveriesHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !incidentServiceAvailable(w, svc) {
			return
		}
		principal, workspaceID, incidentID, ok := incidentIDs(w, r)
		if !ok {
			return
		}
		deliveries, err := svc.Deliveries(r.Context(), principal.UserID, workspaceID, incidentID)
		if err != nil {
			incidentServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, deliveryListResponse{Deliveries: deliveries})
	}
}

// AlertChannelsHandler returns GET /workspaces/{workspaceID}/alert-channels.
func AlertChannelsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !incidentServiceAvailable(w, svc) {
			return
		}
		principal, ok := incidentPrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, ok := incidentWorkspaceID(w, r)
		if !ok {
			return
		}
		channels, err := svc.ListAlertChannels(r.Context(), principal.UserID, workspaceID)
		if err != nil {
			incidentServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, alertChannelListResponse{Channels: channels})
	}
}

// PutEmailChannelHandler returns PUT /workspaces/{workspaceID}/alert-channels/email.
func PutEmailChannelHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !incidentServiceAvailable(w, svc) {
			return
		}
		principal, ok := incidentPrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, ok := incidentWorkspaceID(w, r)
		if !ok {
			return
		}
		var in alertChannelPutRequest
		if !decodeIncidentBody(w, r, &in) {
			return
		}
		if _, err := svc.PutEmailChannel(r.Context(), principal.UserID, workspaceID, in.Recipients, in.Enabled); err != nil {
			incidentServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// MonitorAlertChannelHandler returns
// GET /workspaces/{workspaceID}/monitors/{monitorID}/alert-channel.
func MonitorAlertChannelHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !incidentServiceAvailable(w, svc) {
			return
		}
		principal, workspaceID, monitorID, ok := monitorAlertIDs(w, r)
		if !ok {
			return
		}
		state, err := svc.GetMonitorAlertState(r.Context(), principal.UserID, workspaceID, monitorID)
		if err != nil {
			incidentServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, state)
	}
}

// SetMonitorAlertChannelHandler returns
// PATCH /workspaces/{workspaceID}/monitors/{monitorID}/alert-channel.
func SetMonitorAlertChannelHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !incidentServiceAvailable(w, svc) {
			return
		}
		principal, workspaceID, monitorID, ok := monitorAlertIDs(w, r)
		if !ok {
			return
		}
		var in monitorAlertRequest
		if !decodeIncidentBody(w, r, &in) {
			return
		}
		if in.Enabled == nil {
			incidentFieldsProblem(w, map[string]string{"enabled": "enabled is required"})
			return
		}
		if err := svc.SetMonitorAlert(r.Context(), principal.UserID, workspaceID, monitorID, *in.Enabled); err != nil {
			incidentServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// incidentServiceAvailable guards handlers when the router has no
// database-backed incident service.
func incidentServiceAvailable(w http.ResponseWriter, svc *Service) bool {
	if svc == nil {
		incidentProblem(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal Server Error")
		return false
	}
	return true
}

// incidentPrincipal reads the authenticated principal, writing 401 when absent.
func incidentPrincipal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		incidentProblem(w, http.StatusUnauthorized, httpx.CodeUnauthenticated, "Authentication required")
		return auth.Principal{}, false
	}
	return principal, true
}

// incidentWorkspaceID parses the workspaceID path value.
func incidentWorkspaceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("workspaceID"))
	if err != nil {
		incidentProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid workspace id")
		return uuid.Nil, false
	}
	return id, true
}

// incidentIDs parses both path IDs and the authenticated principal.
func incidentIDs(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, uuid.UUID, bool) {
	principal, ok := incidentPrincipal(w, r)
	if !ok {
		return auth.Principal{}, uuid.Nil, uuid.Nil, false
	}
	workspaceID, ok := incidentWorkspaceID(w, r)
	if !ok {
		return auth.Principal{}, uuid.Nil, uuid.Nil, false
	}
	incidentID, err := uuid.Parse(r.PathValue("incidentID"))
	if err != nil {
		incidentProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid incident id")
		return auth.Principal{}, uuid.Nil, uuid.Nil, false
	}
	return principal, workspaceID, incidentID, true
}

// monitorAlertIDs parses the workspace and monitor path IDs plus principal.
func monitorAlertIDs(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, uuid.UUID, bool) {
	principal, ok := incidentPrincipal(w, r)
	if !ok {
		return auth.Principal{}, uuid.Nil, uuid.Nil, false
	}
	workspaceID, ok := incidentWorkspaceID(w, r)
	if !ok {
		return auth.Principal{}, uuid.Nil, uuid.Nil, false
	}
	monitorID, err := uuid.Parse(r.PathValue("monitorID"))
	if err != nil {
		incidentProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid monitor id")
		return auth.Principal{}, uuid.Nil, uuid.Nil, false
	}
	return principal, workspaceID, monitorID, true
}

// decodeIncidentBody decodes one JSON request body, rejecting unknown fields.
func decodeIncidentBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		incidentProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid request body")
		return false
	}
	return true
}

// incidentLimit parses and validates the limit query parameter.
func incidentLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultListLimit, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		incidentFieldsProblem(w, map[string]string{"limit": "limit must be a positive integer"})
		return 0, false
	}
	if n > maxListLimit {
		n = maxListLimit
	}
	return n, true
}

// incidentCursor decodes the cursor query parameter when present.
func incidentCursor(w http.ResponseWriter, r *http.Request) (*httpx.PageCursor, bool) {
	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return nil, true
	}
	cursor, err := httpx.DecodeCursor(raw)
	if err != nil {
		incidentFieldsProblem(w, map[string]string{"cursor": "invalid cursor"})
		return nil, false
	}
	return &cursor, true
}

// incidentServiceError maps service errors onto problem responses.
func incidentServiceError(w http.ResponseWriter, err error) {
	var validation *ValidationError

	switch {
	case errors.As(err, &validation):
		incidentFieldsProblem(w, validation.Fields)
	case errors.Is(err, ErrSuspended):
		httpx.WriteProblem(w, httpx.Problem{
			Type:   "about:blank",
			Title:  "Workspace Suspended",
			Status: http.StatusForbidden,
			Detail: "This workspace is suspended. Reads remain available.",
			Code:   httpx.CodeWorkspaceSuspended,
		})
	case errors.Is(err, ErrIncidentResolved):
		httpx.WriteProblem(w, httpx.Problem{
			Type:   "about:blank",
			Title:  "Incident Resolved",
			Status: http.StatusConflict,
			Code:   httpx.CodeIncidentResolved,
		})
	case errors.Is(err, ErrAlertChannelMissing):
		incidentProblem(w, http.StatusNotFound, "alert_channel_missing", "Alert Channel Missing")
	case errors.Is(err, ErrNotFound):
		incidentProblem(w, http.StatusNotFound, httpx.CodeNotFound, "Not Found")
	case errors.Is(err, ErrForbidden):
		incidentProblem(w, http.StatusForbidden, httpx.CodeForbidden, "Forbidden")
	case errors.Is(err, ErrValidation):
		incidentProblem(w, http.StatusBadRequest, httpx.CodeValidationError, err.Error())
	default:
		incidentProblem(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal Server Error")
	}
}

// incidentFieldsProblem responds 400 with per-field messages.
func incidentFieldsProblem(w http.ResponseWriter, fields map[string]string) {
	httpx.WriteProblem(w, httpx.Problem{
		Type:   "about:blank",
		Title:  "Validation Error",
		Status: http.StatusBadRequest,
		Code:   httpx.CodeValidationError,
		Fields: fields,
	})
}

// incidentProblem emits a problem response without fields.
func incidentProblem(w http.ResponseWriter, status int, code, title string) {
	httpx.WriteProblem(w, httpx.Problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Code:   code,
	})
}

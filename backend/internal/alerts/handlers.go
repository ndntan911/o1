package alerts

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"o1/internal/auth"
	"o1/internal/httpx"
)

// channelListResponse wraps a workspace's alert channels.
type channelListResponse struct {
	Channels []Channel `json:"channels"`
}

// ListChannelsHandler returns GET /workspaces/{workspaceID}/alert-channels.
func ListChannelsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !alertServiceAvailable(w, svc) {
			return
		}
		principal, ok := alertPrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, ok := alertWorkspaceID(w, r)
		if !ok {
			return
		}
		channels, err := svc.List(r.Context(), principal.UserID, workspaceID)
		if err != nil {
			alertServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, channelListResponse{Channels: channels})
	}
}

// CreateChannelHandler returns POST /workspaces/{workspaceID}/alert-channels.
func CreateChannelHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !alertServiceAvailable(w, svc) {
			return
		}
		principal, ok := alertPrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, ok := alertWorkspaceID(w, r)
		if !ok {
			return
		}
		var in CreateInput
		if !decodeAlertBody(w, r, &in) {
			return
		}
		channel, err := svc.Create(r.Context(), principal.UserID, workspaceID, in)
		if err != nil {
			alertServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, channel)
	}
}

// UpdateChannelHandler returns PATCH /workspaces/{workspaceID}/alert-channels/{channelID}.
func UpdateChannelHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !alertServiceAvailable(w, svc) {
			return
		}
		principal, ok := alertPrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, channelID, ok := alertChannelIDs(w, r)
		if !ok {
			return
		}
		var in UpdateInput
		if !decodeAlertBody(w, r, &in) {
			return
		}
		channel, err := svc.Update(r.Context(), principal.UserID, workspaceID, channelID, in)
		if err != nil {
			alertServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, channel)
	}
}

// DeleteChannelHandler returns DELETE /workspaces/{workspaceID}/alert-channels/{channelID}.
func DeleteChannelHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !alertServiceAvailable(w, svc) {
			return
		}
		principal, ok := alertPrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, channelID, ok := alertChannelIDs(w, r)
		if !ok {
			return
		}
		if err := svc.Delete(r.Context(), principal.UserID, workspaceID, channelID); err != nil {
			alertServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// monitorChannelListResponse wraps a monitor's alert channel switches.
type monitorChannelListResponse struct {
	Channels []MonitorChannel `json:"channels"`
}

// monitorAlertSwitchRequest is the PATCH monitor channel body.
type monitorAlertSwitchRequest struct {
	Enabled *bool `json:"enabled"`
}

// MonitorChannelsHandler returns
// GET /workspaces/{workspaceID}/monitors/{monitorID}/alert-channels.
func MonitorChannelsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !alertServiceAvailable(w, svc) {
			return
		}
		principal, ok := alertPrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, monitorID, ok := alertMonitorIDs(w, r)
		if !ok {
			return
		}
		channels, err := svc.ListMonitorChannels(r.Context(), principal.UserID, workspaceID, monitorID)
		if err != nil {
			alertServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, monitorChannelListResponse{Channels: channels})
	}
}

// SetMonitorChannelHandler returns
// PATCH /workspaces/{workspaceID}/monitors/{monitorID}/alert-channels/{channelID}.
func SetMonitorChannelHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !alertServiceAvailable(w, svc) {
			return
		}
		principal, ok := alertPrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, monitorID, ok := alertMonitorIDs(w, r)
		if !ok {
			return
		}
		channelID, err := uuid.Parse(r.PathValue("channelID"))
		if err != nil {
			alertProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid channel id", "", nil)
			return
		}
		var in monitorAlertSwitchRequest
		if !decodeAlertBody(w, r, &in) {
			return
		}
		if in.Enabled == nil {
			alertProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Validation Error", "", map[string]string{
				"enabled": "enabled is required",
			})
			return
		}
		if err := svc.SetMonitorChannel(r.Context(), principal.UserID, workspaceID, monitorID, channelID, *in.Enabled); err != nil {
			alertServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// TestChannelHandler returns POST /workspaces/{workspaceID}/alert-channels/{channelID}/test.
func TestChannelHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !alertServiceAvailable(w, svc) {
			return
		}
		principal, ok := alertPrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, channelID, ok := alertChannelIDs(w, r)
		if !ok {
			return
		}
		if err := svc.SendTest(r.Context(), principal.UserID, workspaceID, channelID); err != nil {
			var deliveryErr *DeliveryError
			switch {
			case errors.Is(err, ErrInvalidChannelConfig):
				alertProblem(w, http.StatusUnprocessableEntity, "test_send_failed", "Test Send Failed",
					"Alert channel configuration is incomplete.", nil)
			case errors.As(err, &deliveryErr):
				alertProblem(w, http.StatusUnprocessableEntity, "test_send_failed", "Test Send Failed", deliveryErr.Error(), nil)
			default:
				alertServiceError(w, err)
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func alertMonitorIDs(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	workspaceID, ok := alertWorkspaceID(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	monitorID, err := uuid.Parse(r.PathValue("monitorID"))
	if err != nil {
		alertProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid monitor id", "", nil)
		return uuid.Nil, uuid.Nil, false
	}
	return workspaceID, monitorID, true
}

func alertServiceAvailable(w http.ResponseWriter, svc *Service) bool {
	if svc != nil {
		return true
	}
	alertProblem(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal Server Error", "", nil)
	return false
}

func alertPrincipal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		alertProblem(w, http.StatusUnauthorized, httpx.CodeUnauthenticated, "Authentication required", "", nil)
		return auth.Principal{}, false
	}
	return principal, true
}

func alertWorkspaceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	workspaceID, err := uuid.Parse(r.PathValue("workspaceID"))
	if err != nil {
		alertProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid workspace id", "", nil)
		return uuid.Nil, false
	}
	return workspaceID, true
}

func alertChannelIDs(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	workspaceID, ok := alertWorkspaceID(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	channelID, err := uuid.Parse(r.PathValue("channelID"))
	if err != nil {
		alertProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid channel id", "", nil)
		return uuid.Nil, uuid.Nil, false
	}
	return workspaceID, channelID, true
}

func decodeAlertBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		alertProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid request body", "", nil)
		return false
	}
	return true
}

func alertServiceError(w http.ResponseWriter, err error) {
	var validation *ValidationError
	switch {
	case errors.As(err, &validation):
		alertProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Validation Error", "", validation.Fields)
	case errors.Is(err, ErrNameTaken):
		alertProblem(w, http.StatusConflict, "channel_name_taken", "Channel Name Taken", "", nil)
	case errors.Is(err, ErrLimit):
		alertProblem(w, http.StatusUnprocessableEntity, "channel_limit", "Channel Limit Reached", "", nil)
	case errors.Is(err, ErrEmailChannel):
		alertProblem(w, http.StatusUnprocessableEntity, "email_channel_managed_elsewhere", "Email Channel Managed Elsewhere", "", nil)
	case errors.Is(err, ErrInvalidChannelConfig):
		alertProblem(w, http.StatusUnprocessableEntity, "invalid_channel_config", "Invalid Channel Configuration", "", nil)
	case errors.Is(err, ErrSuspended):
		alertProblem(w, http.StatusForbidden, httpx.CodeWorkspaceSuspended, "Workspace Suspended", "", nil)
	case errors.Is(err, ErrNotFound):
		alertProblem(w, http.StatusNotFound, httpx.CodeNotFound, "Not Found", "", nil)
	case errors.Is(err, ErrForbidden):
		alertProblem(w, http.StatusForbidden, httpx.CodeForbidden, "Forbidden", "", nil)
	default:
		alertProblem(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal Server Error", "", nil)
	}
}

func alertProblem(w http.ResponseWriter, status int, code, title, detail string, fields map[string]string) {
	httpx.WriteProblem(w, httpx.Problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Code:   code,
		Detail: detail,
		Fields: fields,
	})
}

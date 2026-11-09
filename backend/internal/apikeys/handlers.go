package apikeys

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"o1/internal/auth"
	"o1/internal/httpx"
)

// keyListResponse wraps a workspace's active API keys.
type keyListResponse struct {
	Keys []Key `json:"keys"`
}

// ListHandler returns GET /workspaces/{workspaceID}/api-keys.
func ListHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !keyServiceAvailable(w, svc) {
			return
		}
		principal, ok := keyPrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, ok := keyWorkspaceID(w, r)
		if !ok {
			return
		}
		keys, err := svc.List(r.Context(), principal.UserID, workspaceID)
		if err != nil {
			keyServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, keyListResponse{Keys: keys})
	}
}

// CreateHandler returns POST /workspaces/{workspaceID}/api-keys. The response
// carries the plaintext secret exactly once.
func CreateHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !keyServiceAvailable(w, svc) {
			return
		}
		principal, ok := keyPrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, ok := keyWorkspaceID(w, r)
		if !ok {
			return
		}
		var in CreateInput
		if !decodeKeyBody(w, r, &in) {
			return
		}
		created, err := svc.Create(r.Context(), principal.UserID, workspaceID, in)
		if err != nil {
			keyServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, created)
	}
}

// RevokeHandler returns DELETE /workspaces/{workspaceID}/api-keys/{keyID}.
func RevokeHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !keyServiceAvailable(w, svc) {
			return
		}
		principal, ok := keyPrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, ok := keyWorkspaceID(w, r)
		if !ok {
			return
		}
		keyID, err := uuid.Parse(r.PathValue("keyID"))
		if err != nil {
			keyProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid key id", "", nil)
			return
		}
		if err := svc.Revoke(r.Context(), principal.UserID, workspaceID, keyID); err != nil {
			keyServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func keyServiceAvailable(w http.ResponseWriter, svc *Service) bool {
	if svc != nil {
		return true
	}
	keyProblem(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal Server Error", "", nil)
	return false
}

func keyPrincipal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		keyProblem(w, http.StatusUnauthorized, httpx.CodeUnauthenticated, "Authentication required", "", nil)
		return auth.Principal{}, false
	}
	return principal, true
}

func keyWorkspaceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	workspaceID, err := uuid.Parse(r.PathValue("workspaceID"))
	if err != nil {
		keyProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid workspace id", "", nil)
		return uuid.Nil, false
	}
	return workspaceID, true
}

func decodeKeyBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		keyProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid request body", "", nil)
		return false
	}
	return true
}

func keyServiceError(w http.ResponseWriter, err error) {
	var validation *ValidationError
	switch {
	case errors.As(err, &validation):
		keyProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Validation Error", "", validation.Fields)
	case errors.Is(err, ErrNameTaken):
		keyProblem(w, http.StatusConflict, "api_key_name_taken", "API Key Name Taken", "", nil)
	case errors.Is(err, ErrLimit):
		keyProblem(w, http.StatusUnprocessableEntity, "api_key_limit", "API Key Limit Reached", "", nil)
	case errors.Is(err, ErrSuspended):
		keyProblem(w, http.StatusForbidden, httpx.CodeWorkspaceSuspended, "Workspace Suspended", "", nil)
	case errors.Is(err, ErrNotFound):
		keyProblem(w, http.StatusNotFound, httpx.CodeNotFound, "Not Found", "", nil)
	case errors.Is(err, ErrForbidden):
		keyProblem(w, http.StatusForbidden, httpx.CodeForbidden, "Forbidden", "", nil)
	default:
		keyProblem(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal Server Error", "", nil)
	}
}

func keyProblem(w http.ResponseWriter, status int, code, title, detail string, fields map[string]string) {
	httpx.WriteProblem(w, httpx.Problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Code:   code,
		Detail: detail,
		Fields: fields,
	})
}

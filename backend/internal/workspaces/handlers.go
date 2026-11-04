package workspaces

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"o1/internal/auth"
	"o1/internal/httpx"
)

// workspaceResponse is the public representation of a workspace.
type workspaceResponse struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Slug string    `json:"slug"`
	Role string    `json:"role"`
}

// workspaceListResponse wraps the caller's workspaces.
type workspaceListResponse struct {
	Workspaces []workspaceResponse `json:"workspaces"`
}

// memberResponse is the public representation of a workspace membership.
type memberResponse struct {
	UserID uuid.UUID `json:"user_id"`
	Email  string    `json:"email"`
	Role   string    `json:"role"`
}

// memberListResponse wraps a workspace's members.
type memberListResponse struct {
	Members []memberResponse `json:"members"`
}

// createWorkspaceRequest is the JSON body of POST /workspaces.
type createWorkspaceRequest struct {
	Name string `json:"name"`
}

// addMemberRequest is the JSON body of POST /workspaces/{workspaceID}/members.
type addMemberRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// ListHandler returns GET /workspaces: every workspace the authenticated user
// belongs to, each annotated with their role.
func ListHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requirePrincipal(w, r)
		if !ok {
			return
		}
		list, err := svc.ListForUser(r.Context(), principal.UserID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		out := make([]workspaceResponse, 0, len(list))
		for _, ws := range list {
			out = append(out, toWorkspaceResponse(ws))
		}
		httpx.WriteJSON(w, http.StatusOK, workspaceListResponse{Workspaces: out})
	}
}

// CreateHandler returns POST /workspaces: it creates a workspace with the
// authenticated user as owner and answers 201 with the workspace.
func CreateHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requirePrincipal(w, r)
		if !ok {
			return
		}
		var req createWorkspaceRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeValidationProblem(w, "Invalid request body")
			return
		}
		ws, err := svc.Create(r.Context(), principal.UserID, req.Name)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, toWorkspaceResponse(ws))
	}
}

// GetHandler returns GET /workspaces/{workspaceID}: one workspace with the
// caller's role. Non-members get 404.
func GetHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requirePrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, ok := requireWorkspaceID(w, r)
		if !ok {
			return
		}
		ws, err := svc.Get(r.Context(), principal.UserID, workspaceID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, toWorkspaceResponse(ws))
	}
}

// MembersHandler returns GET /workspaces/{workspaceID}/members: the workspace
// membership list. Non-members get 404.
func MembersHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requirePrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, ok := requireWorkspaceID(w, r)
		if !ok {
			return
		}
		members, err := svc.ListMembers(r.Context(), principal.UserID, workspaceID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		out := make([]memberResponse, 0, len(members))
		for _, m := range members {
			out = append(out, memberResponse{UserID: m.UserID, Email: m.Email, Role: m.Role.String()})
		}
		httpx.WriteJSON(w, http.StatusOK, memberListResponse{Members: out})
	}
}

// AddMemberHandler returns POST /workspaces/{workspaceID}/members: it adds or
// updates a membership and answers 204.
func AddMemberHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requirePrincipal(w, r)
		if !ok {
			return
		}
		workspaceID, ok := requireWorkspaceID(w, r)
		if !ok {
			return
		}
		var req addMemberRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeValidationProblem(w, "Invalid request body")
			return
		}
		role, err := ParseRole(req.Role)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		if err := svc.AddMember(r.Context(), principal.UserID, workspaceID, req.Email, role); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// requirePrincipal reads the authenticated principal placed in the context by
// auth.RequireSession, writing 401 when it is missing.
func requirePrincipal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, httpx.CodeUnauthenticated, "Authentication required")
		return auth.Principal{}, false
	}
	return principal, true
}

// requireWorkspaceID parses the workspaceID path value, writing 400 when it is
// not a valid UUID.
func requireWorkspaceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("workspaceID"))
	if err != nil {
		writeValidationProblem(w, "Invalid workspace id")
		return uuid.Nil, false
	}
	return id, true
}

// toWorkspaceResponse converts a domain workspace to its API representation.
func toWorkspaceResponse(ws Workspace) workspaceResponse {
	return workspaceResponse{ID: ws.ID, Name: ws.Name, Slug: ws.Slug, Role: ws.Role.String()}
}

// writeServiceError maps service sentinels onto problem responses. Unknown
// errors become an opaque 500 so internals never leak.
func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "Not Found")
	case errors.Is(err, ErrForbidden):
		writeProblem(w, http.StatusForbidden, "forbidden", "Forbidden")
	case errors.Is(err, ErrValidation):
		writeProblem(w, http.StatusBadRequest, httpx.CodeValidationError, err.Error())
	default:
		writeProblem(w, http.StatusInternalServerError, "internal_error", "Internal Server Error")
	}
}

// writeValidationProblem responds 400 with code validation_error.
func writeValidationProblem(w http.ResponseWriter, title string) {
	writeProblem(w, http.StatusBadRequest, httpx.CodeValidationError, title)
}

// writeProblem emits an application/problem+json response.
func writeProblem(w http.ResponseWriter, status int, code, title string) {
	httpx.WriteProblem(w, httpx.Problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Code:   code,
	})
}

package billing

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"o1/internal/auth"
	"o1/internal/httpx"
	"o1/internal/workspaces"
)

// API adapts the billing service to HTTP with workspace authorization.
type API struct {
	svc *Service
	ws  *workspaces.Service
}

// NewAPI wires the billing handlers.
func NewAPI(svc *Service, ws *workspaces.Service) *API {
	return &API{svc: svc, ws: ws}
}

func writeProblem(w http.ResponseWriter, status int, title, code string) {
	httpx.WriteProblem(w, httpx.Problem{
		Type: "about:blank", Title: title, Status: status, Code: code,
	})
}

// Plans is the public pricing list.
func (a *API) Plans(w http.ResponseWriter, r *http.Request) {
	plans, err := a.svc.q.ListPlans(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Could not load plans", "billing_failed")
		return
	}
	out := make([]map[string]any, 0, len(plans))
	for _, plan := range plans {
		out = append(out, map[string]any{
			"key": plan.Key, "name": plan.Name, "monitors": plan.MonitorLimit,
			"channels": plan.ChannelLimit, "api_keys": plan.APIKeyLimit,
			"price_cents": plan.PriceCents, "trial_days": plan.TrialDays,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"plans": out})
}

func (a *API) workspace(w http.ResponseWriter, r *http.Request, min workspaces.Role) (uuid.UUID, auth.Principal, bool) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Authentication required", "unauthenticated")
		return uuid.Nil, principal, false
	}
	workspaceID, err := uuid.Parse(r.PathValue("workspaceID"))
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Workspace not found", "not_found")
		return uuid.Nil, principal, false
	}
	if _, err := a.ws.Authorize(r.Context(), principal.UserID, workspaceID, min); err != nil {
		if errors.Is(err, workspaces.ErrForbidden) {
			writeProblem(w, http.StatusForbidden, "Forbidden", "forbidden")
		} else {
			writeProblem(w, http.StatusNotFound, "Workspace not found", "not_found")
		}
		return uuid.Nil, principal, false
	}
	return workspaceID, principal, true
}

// Get returns the workspace plan and usage limits.
func (a *API) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID, _, ok := a.workspace(w, r, workspaces.RoleViewer)
	if !ok {
		return
	}
	view, err := a.svc.Get(r.Context(), workspaceID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Could not load billing", "billing_failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"plan": map[string]any{
			"key": view.Plan.Key, "name": view.Plan.Name,
			"monitors": view.Plan.MonitorLimit, "channels": view.Plan.ChannelLimit,
			"api_keys": view.Plan.APIKeyLimit, "price_cents": view.Plan.PriceCents,
		},
		"status": view.Status, "provider": view.Provider,
		"current_period_end":   view.CurrentPeriodEnd,
		"trial_end":            view.TrialEnd,
		"cancel_at_period_end": view.CancelAtPeriodEnd,
		"mode":                 a.svc.ProviderName(),
	})
}

type checkoutRequest struct {
	Plan string `json:"plan"`
}

// Checkout starts a sandbox checkout for a paid plan.
func (a *API) Checkout(w http.ResponseWriter, r *http.Request) {
	workspaceID, _, ok := a.workspace(w, r, workspaces.RoleAdmin)
	if !ok {
		return
	}
	var in checkoutRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Plan == "" {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "validation_error")
		return
	}
	session, err := a.svc.Checkout(r.Context(), workspaceID, in.Plan)
	switch {
	case errors.Is(err, ErrAlreadySubscribed):
		writeProblem(w, http.StatusConflict, "Workspace already has a paid plan", "billing_already_subscribed")
		return
	case errors.Is(err, ErrInvalidPlan):
		writeProblem(w, http.StatusBadRequest, "Unknown plan", "billing_plan_invalid")
		return
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "Could not start checkout", "billing_failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"url": session.URL})
}

// Cancel marks the subscription to end at the current period.
func (a *API) Cancel(w http.ResponseWriter, r *http.Request) {
	workspaceID, principal, ok := a.workspace(w, r, workspaces.RoleAdmin)
	if !ok {
		return
	}
	if err := a.svc.Cancel(r.Context(), principal.UserID, workspaceID); err != nil {
		writeProblem(w, http.StatusBadRequest, "Could not cancel", "billing_cancel_failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type sandboxCompleteRequest struct {
	Token   string `json:"token"`
	Outcome string `json:"outcome"`
}

// SandboxComplete simulates the provider outcome for the demo checkout.
func (a *API) SandboxComplete(w http.ResponseWriter, r *http.Request) {
	if a.svc.ProviderName() != "sandbox" {
		writeProblem(w, http.StatusNotFound, "Not found", "not_found")
		return
	}
	var in sandboxCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "validation_error")
		return
	}
	sandbox, ok := a.svc.provider.(*Sandbox)
	if !ok {
		writeProblem(w, http.StatusNotFound, "Not found", "not_found")
		return
	}
	checkout, err := sandbox.ParseToken(in.Token)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid checkout token", "billing_token_invalid")
		return
	}
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Authentication required", "unauthenticated")
		return
	}
	if _, err := a.ws.Authorize(r.Context(), principal.UserID, checkout.WorkspaceID, workspaces.RoleAdmin); err != nil {
		writeProblem(w, http.StatusForbidden, "Forbidden", "forbidden")
		return
	}

	now := a.svc.now()
	end := now.AddDate(0, 0, 30)
	event := Event{
		ID: uuid.NewString(), WorkspaceID: checkout.WorkspaceID.String(),
		PlanKey: checkout.PlanKey, ProviderSubscriptionID: "sandbox_" + checkout.WorkspaceID.String(),
		ProviderCustomerID: "sandbox_" + checkout.WorkspaceID.String(),
	}
	switch in.Outcome {
	case "success":
		event.Kind = "subscription.started"
		event.CurrentPeriodEnd = &end
	case "failure":
		event.Kind = "subscription.payment_failed"
	case "cancel":
		event.Kind = "subscription.canceled"
		event.CurrentPeriodEnd = &end
	default:
		writeProblem(w, http.StatusBadRequest, "Invalid outcome", "validation_error")
		return
	}
	if err := a.svc.ApplyEvent(r.Context(), "sandbox", event); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Could not complete checkout", "billing_failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type adminPlanRequest struct {
	Plan string `json:"plan"`
}

// AdminSetPlan is the audited support override.
func (a *API) AdminSetPlan(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Authentication required", "unauthenticated")
		return
	}
	workspaceID, err := uuid.Parse(r.PathValue("workspaceID"))
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Workspace not found", "not_found")
		return
	}
	var in adminPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Plan == "" {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "validation_error")
		return
	}
	if err := a.svc.AdminSetPlan(r.Context(), principal.UserID, workspaceID, in.Plan); err != nil {
		if errors.Is(err, ErrInvalidPlan) {
			writeProblem(w, http.StatusBadRequest, "Unknown plan", "billing_plan_invalid")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Could not change plan", "billing_failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

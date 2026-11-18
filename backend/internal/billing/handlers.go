package billing

import (
	"errors"
	"net/http"

	"o1/internal/httpx"
)

// Webhook handles provider callbacks. The signature is the only authentication,
// so an invalid or unparsable payload is rejected before any state change.
func (s *Service) Webhook(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if provider != s.provider.Name() {
		httpx.WriteProblem(w, httpx.Problem{
			Type:   "about:blank",
			Title:  "Unknown billing provider",
			Status: http.StatusNotFound,
			Code:   "billing_provider_unknown",
		})
		return
	}
	event, err := s.provider.ParseWebhook(r)
	if err != nil {
		httpx.WriteProblem(w, httpx.Problem{
			Type:   "about:blank",
			Title:  "Invalid webhook signature",
			Status: http.StatusBadRequest,
			Code:   "billing_signature_invalid",
		})
		return
	}
	if err := s.ApplyEvent(r.Context(), provider, event); err != nil {
		if errors.Is(err, ErrInvalidPlan) {
			httpx.WriteProblem(w, httpx.Problem{
				Type:   "about:blank",
				Title:  "Invalid plan in event",
				Status: http.StatusBadRequest,
				Code:   "billing_plan_invalid",
			})
			return
		}
		httpx.WriteProblem(w, httpx.Problem{
			Type:   "about:blank",
			Title:  "Could not process webhook",
			Status: http.StatusInternalServerError,
			Code:   "billing_webhook_failed",
		})
		return
	}
	w.WriteHeader(http.StatusOK)
}

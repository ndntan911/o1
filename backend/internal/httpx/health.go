package httpx

import "net/http"

type healthResponse struct {
	Status string `json:"status"`
}

// HandleHealthz serves GET /healthz, a liveness endpoint for load balancers.
func HandleHealthz(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

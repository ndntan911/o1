package httpx

import (
	"encoding/json"
	"net/http"
)

// WriteJSON serializes v as application/json with the given HTTP status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

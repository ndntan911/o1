package httpx

import (
	"encoding/json"
	"net/http"
)

// Problem is an RFC 9457 problem-details style response body. Every API
// error is serialized as application/problem+json.
type Problem struct {
	Type   string            `json:"type"`
	Title  string            `json:"title"`
	Status int               `json:"status"`
	Detail string            `json:"detail,omitempty"`
	Code   string            `json:"code"`
	Fields map[string]string `json:"fields,omitempty"`
}

// WriteProblem serializes p as application/problem+json with its status.
func WriteProblem(w http.ResponseWriter, p Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

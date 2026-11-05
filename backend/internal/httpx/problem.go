package httpx

import (
	"encoding/json"
	"net/http"
)

// Error codes shared by API problem responses. Authentication endpoints use
// the 401/403 codes below so clients can branch on a stable identifier.
const (
	CodeValidationError    = "validation_error"
	CodeUnauthenticated    = "unauthenticated"
	CodeInvalidCredentials = "invalid_credentials" //nolint:gosec // stable public error code, not a credential
	CodeEmailNotVerified   = "email_not_verified"
	CodeTokenInvalid       = "token_invalid"
	CodeForbidden          = "forbidden"
	CodeRateLimited        = "rate_limited"
	CodeNotFound           = "not_found"
	CodeWorkspaceSuspended = "workspace_suspended"
	CodeQuotaExceeded      = "quota_exceeded"
	CodeMonitorRateLimited = "monitor_rate_limited"
	CodeInternalError      = "internal_error"
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

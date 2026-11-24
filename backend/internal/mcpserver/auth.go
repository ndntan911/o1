package mcpserver

import (
	"net/http"
	"strings"

	"o1/internal/httpx"
)

// ratePerSecond and rateBurst bound MCP calls per API key: 60 calls per
// minute sustained with a burst of 10, matching the documented MCP limit.
const (
	ratePerSecond = 1.0
	rateBurst     = 10
)

// bearerToken extracts the token from an Authorization header. Only a Bearer
// credential is accepted; the MCP endpoint never falls back to a session
// cookie. The scheme name is compared case-insensitively, as RFC 7235 requires.
func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// keyPrefixForLog extracts the public prefix from a raw key for failure logs.
// It never inspects or logs the secret half, and it returns "" when the shape
// is invalid.
func keyPrefixForLog(raw string) string {
	parts := strings.SplitN(raw, "_", 3)
	if len(parts) != 3 || parts[0] != "o1" || len(parts[1]) != 12 {
		return ""
	}
	return parts[1]
}

// allowedOrigin normalizes an Origin header or configured origin for exact
// comparison.
func allowedOrigin(origin string) string {
	return strings.TrimRight(strings.TrimSpace(origin), "/")
}

// originAllowed reports whether the request may proceed. Requests without an
// Origin header are allowed: browsers always send Origin on cross-origin
// requests, and non-browser MCP clients have no origin to forge.
func (s *Server) originAllowed(r *http.Request) bool {
	origin := allowedOrigin(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	_, ok := s.allowedOrigins[origin]
	return ok
}

// writeUnauthorized emits a 401 that advertises the Bearer scheme.
func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	httpx.WriteProblem(w, httpx.Problem{
		Type:   "about:blank",
		Title:  "Unauthorized",
		Status: http.StatusUnauthorized,
		Code:   httpx.CodeUnauthenticated,
	})
}

// writeForbidden emits the disallowed-Origin response.
func writeForbidden(w http.ResponseWriter) {
	httpx.WriteProblem(w, httpx.Problem{
		Type:   "about:blank",
		Title:  "Forbidden",
		Status: http.StatusForbidden,
		Code:   httpx.CodeForbidden,
	})
}

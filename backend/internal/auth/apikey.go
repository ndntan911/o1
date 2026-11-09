package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// apiKeyVersionPrefix is the fixed scheme marker on every API key.
const apiKeyVersionPrefix = "o1"

// apiKeyPrefixLen is the exact length of the stored, publicly visible prefix.
const apiKeyPrefixLen = 12

// APIKeyPrincipal is the key-scoped identity stored in the request context
// alongside the session Principal. Role is kept as a string so this package
// does not depend on workspaces' role type; workspaces.Authorize parses it.
type APIKeyPrincipal struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	Name        string
	Prefix      string
	Role        string
	CreatedBy   uuid.UUID
}

// apiKeyContextKey is the context key carrying an APIKeyPrincipal.
type apiKeyContextKey struct{}

// ContextWithAPIKey returns a copy of ctx carrying key.
func ContextWithAPIKey(ctx context.Context, key APIKeyPrincipal) context.Context {
	return context.WithValue(ctx, apiKeyContextKey{}, key)
}

// APIKeyFrom returns the APIKeyPrincipal stored by RequireAuth, if present.
func APIKeyFrom(ctx context.Context) (APIKeyPrincipal, bool) {
	key, ok := ctx.Value(apiKeyContextKey{}).(APIKeyPrincipal)
	return key, ok
}

// APIKeyIDFrom returns a pointer to the API key ID present in ctx, or nil when
// the request was authenticated by a session rather than a key.
func APIKeyIDFrom(ctx context.Context) *uuid.UUID {
	key, ok := APIKeyFrom(ctx)
	if !ok {
		return nil
	}
	id := key.ID
	return &id
}

// APIKeyRateKey returns the per-key rate-limit identity for r. Session
// requests return the empty string, which KeyedRateLimit always allows.
func APIKeyRateKey(r *http.Request) string {
	key, ok := APIKeyFrom(r.Context())
	if !ok {
		return ""
	}
	return key.ID.String()
}

// parseAPIKeyPrefix validates the o1_<prefix>_<secret> shape and returns the
// prefix. The secret is not inspected beyond being non-empty; base64url
// secrets may themselves contain underscores, so only the first two
// separators are significant.
func parseAPIKeyPrefix(raw string) (string, error) {
	parts := strings.SplitN(raw, "_", 3)
	if len(parts) != 3 || parts[0] != apiKeyVersionPrefix || len(parts[1]) != apiKeyPrefixLen || parts[2] == "" {
		return "", ErrUnauthenticated
	}
	for _, c := range parts[1] {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return "", ErrUnauthenticated
		}
	}
	return parts[1], nil
}

// AuthenticateAPIKey resolves a raw API key to its user and key principals.
// The prefix selects a single active key row; the full raw key is then
// compared to the stored hash in constant time. Malformed, unknown, and
// revoked keys all yield ErrUnauthenticated. A successful lookup touches
// last_used_at on a best-effort basis.
func (s *Service) AuthenticateAPIKey(ctx context.Context, raw string) (Principal, APIKeyPrincipal, error) {
	prefix, err := parseAPIKeyPrefix(raw)
	if err != nil {
		return Principal{}, APIKeyPrincipal{}, err
	}

	key, err := s.q.GetActiveAPIKeyByPrefix(ctx, prefix)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Principal{}, APIKeyPrincipal{}, ErrUnauthenticated
		}
		return Principal{}, APIKeyPrincipal{}, fmt.Errorf("load api key: %w", err)
	}
	if subtle.ConstantTimeCompare(HashToken(raw), key.TokenHash) != 1 {
		return Principal{}, APIKeyPrincipal{}, ErrUnauthenticated
	}

	_ = s.q.TouchAPIKeyLastUsed(ctx, key.ID)

	principal := Principal{UserID: key.CreatedBy, IsAdmin: false}
	apiKey := APIKeyPrincipal{
		ID:          key.ID,
		WorkspaceID: key.WorkspaceID,
		Name:        key.Name,
		Prefix:      key.Prefix,
		Role:        key.Role,
		CreatedBy:   key.CreatedBy,
	}
	return principal, apiKey, nil
}

// RequireAuth authenticates either a Bearer API key or a session cookie. When
// an Authorization header is present only Bearer credentials are considered;
// a bad Bearer token never falls back to the cookie. Without the header the
// request follows the same cookie-only path as RequireSession.
func RequireAuth(svc *Service) func(http.Handler) http.Handler {
	sessionAuth := RequireSession(svc)
	return func(next http.Handler) http.Handler {
		sessionHandler := sessionAuth(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(r.Header.Values("Authorization")) == 0 {
				sessionHandler.ServeHTTP(w, r)
				return
			}

			raw, ok := bearerToken(r.Header.Get("Authorization"))
			if !ok {
				writeBearerUnauthenticated(w)
				return
			}
			principal, key, err := svc.AuthenticateAPIKey(r.Context(), raw)
			if err != nil {
				writeBearerUnauthenticated(w)
				return
			}
			ctx := context.WithValue(r.Context(), principalKey{}, principal)
			ctx = ContextWithAPIKey(ctx, key)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// bearerToken extracts the token from a Bearer Authorization header. The
// scheme name is compared case-insensitively, as RFC 7235 requires.
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

func writeBearerUnauthenticated(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeUnauthenticated(w)
}

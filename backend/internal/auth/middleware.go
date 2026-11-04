package auth

import (
	"context"
	"net/http"

	"o1/internal/httpx"
)

// RequireSession authenticates the session cookie, stores the resulting
// Principal in the request context, and calls the next handler. Missing,
// invalid, and expired sessions get a 401 unauthenticated problem response.
func RequireSession(svc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(sessionCookieName(svc.cfg))
			if err != nil || cookie.Value == "" {
				writeUnauthenticated(w)
				return
			}
			principal, err := svc.Authenticate(r.Context(), cookie.Value)
			if err != nil {
				writeUnauthenticated(w)
				return
			}
			ctx := context.WithValue(r.Context(), principalKey{}, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func writeUnauthenticated(w http.ResponseWriter) {
	writeProblem(w, http.StatusUnauthorized, httpx.CodeUnauthenticated, "Authentication required")
}

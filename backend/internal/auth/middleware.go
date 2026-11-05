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

// RequireAdmin permits only administrators. It must be mounted inside
// RequireSession; a missing principal still gets 401 so the route does not
// change shape when mounted incorrectly.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			writeUnauthenticated(w)
			return
		}
		if !principal.IsAdmin {
			writeProblem(w, http.StatusForbidden, httpx.CodeForbidden, "Forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeUnauthenticated(w http.ResponseWriter) {
	writeProblem(w, http.StatusUnauthorized, httpx.CodeUnauthenticated, "Authentication required")
}

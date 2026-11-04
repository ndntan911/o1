package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"o1/internal/auth"
	"o1/internal/testutil"
)

func TestRequireSession_RejectsMissingCookie(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)

	called := false
	h := auth.RequireSession(svc)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))

	if called {
		t.Fatal("protected handler ran without a session")
	}
	body := rec.Body.String()
	if code := problemCode(t, rec.Code, rec.Header().Get("Content-Type"), body); rec.Code != http.StatusUnauthorized || code != "unauthenticated" {
		t.Fatalf("status = %d, code = %q, body = %s", rec.Code, code, body)
	}
}

func TestRequireSession_RejectsInvalidCookie(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)

	h := auth.RequireSession(svc)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: "o1_session", Value: "not-a-session"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if code := problemCode(t, rec.Code, rec.Header().Get("Content-Type"), body); rec.Code != http.StatusUnauthorized || code != "unauthenticated" {
		t.Fatalf("status = %d, code = %q, body = %s", rec.Code, code, body)
	}
}

func TestRequireSession_RejectsExpiredCookie(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	raw, u, err := svc.Login(context.Background(), testEmail, testPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		"UPDATE sessions SET expires_at = now() - interval '1 minute' WHERE user_id=$1", u.ID); err != nil {
		t.Fatal(err)
	}

	h := auth.RequireSession(svc)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: "o1_session", Value: raw})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRequireSession_PutsPrincipalInContext(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	raw, u, err := svc.Login(context.Background(), testEmail, testPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}

	var got auth.Principal
	var ok bool
	h := auth.RequireSession(svc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = auth.PrincipalFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: "o1_session", Value: raw})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !ok {
		t.Fatal("principal missing from context")
	}
	if got.UserID != u.ID || got.SessionID == uuid.Nil || got.Email != testEmail {
		t.Fatalf("principal = %+v", got)
	}
}

func TestPrincipalFrom_NoValue(t *testing.T) {
	if _, ok := auth.PrincipalFrom(context.Background()); ok {
		t.Fatal("PrincipalFrom returned a principal from an empty context")
	}
}

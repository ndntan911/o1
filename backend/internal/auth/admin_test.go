package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"o1/internal/auth"
	"o1/internal/testutil"
)

func TestAuthenticate_SetsIsAdmin(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()

	raw, user, err := svc.Login(ctx, testEmail, testPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := svc.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if principal.IsAdmin {
		t.Fatal("fresh user reported as admin")
	}

	if _, err := pool.Exec(ctx, "UPDATE users SET is_admin = true WHERE id = $1", user.ID); err != nil {
		t.Fatal(err)
	}
	principal, err = svc.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !principal.IsAdmin {
		t.Fatal("promoted user not reported as admin")
	}
}

func TestRequireAdmin(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()

	raw, user, err := svc.Login(ctx, testEmail, testPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}

	called := false
	h := auth.RequireSession(svc)(auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/workspaces", nil)
	req.AddCookie(&http.Cookie{Name: "o1_session", Value: raw})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if called {
		t.Fatal("non-admin reached the handler")
	}
	body := rec.Body.String()
	if code := problemCode(t, rec.Code, rec.Header().Get("Content-Type"), body); rec.Code != http.StatusForbidden || code != "forbidden" {
		t.Fatalf("status = %d, code = %q, body = %s", rec.Code, code, body)
	}

	if _, err := pool.Exec(ctx, "UPDATE users SET is_admin = true WHERE id = $1", user.ID); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("admin status = %d, called = %v, body = %s", rec.Code, called, rec.Body.String())
	}
}

func TestRequireAdmin_RejectsMissingPrincipal(t *testing.T) {
	called := false
	h := auth.RequireAdmin(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/workspaces", nil))
	if called {
		t.Fatal("handler ran without a principal")
	}
	if code := problemCode(t, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String()); rec.Code != http.StatusUnauthorized || code != "unauthenticated" {
		t.Fatalf("status = %d, code = %q", rec.Code, code)
	}
}

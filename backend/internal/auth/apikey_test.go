package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/auth"
	"o1/internal/testutil"
)

func insertTestKey(t *testing.T, pool *pgxpool.Pool, userID, workspaceID uuid.UUID, role, prefix, raw string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO api_keys (id, workspace_id, name, prefix, token_hash, role, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		id, workspaceID, "Test key "+prefix, prefix, auth.HashToken(raw), role, userID); err != nil {
		t.Fatal(err)
	}
	return id
}

func createTestWorkspace(t *testing.T, pool *pgxpool.Pool, owner uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO workspaces (id, name, slug, created_by)
		VALUES ($1, 'API key test', $2, $3)`,
		id, "api-key-"+id.String()[:8], owner); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAuthenticateAPIKey_SuccessAndRevoked(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	u := testutil.CreateVerifiedUser(t, pool, "apikey-auth@example.com", testPassword)
	ws := createTestWorkspace(t, pool, u)
	raw := "o1_abcdefghijkl_" + strings.Repeat("a", 43)
	keyID := insertTestKey(t, pool, u, ws, "member", "abcdefghijkl", raw)

	principal, key, err := svc.AuthenticateAPIKey(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if principal.UserID != u || principal.IsAdmin || principal.SessionID != uuid.Nil {
		t.Fatalf("principal = %+v", principal)
	}
	if key.ID != keyID || key.WorkspaceID != ws || key.Role != "member" || key.Prefix != "abcdefghijkl" {
		t.Fatalf("key = %+v", key)
	}

	if _, err := pool.Exec(context.Background(), `UPDATE api_keys SET revoked_at=now() WHERE id=$1`, keyID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.AuthenticateAPIKey(context.Background(), raw); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("revoked error = %v", err)
	}
	if _, _, err := svc.AuthenticateAPIKey(context.Background(), "not-a-key"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("malformed error = %v", err)
	}
}

func TestAuthenticateAPIKey_TouchesLastUsedOnce(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	u := testutil.CreateVerifiedUser(t, pool, "apikey-touch@example.com", testPassword)
	ws := createTestWorkspace(t, pool, u)
	raw := "o1_abcdefghijkl_" + strings.Repeat("c", 43)
	keyID := insertTestKey(t, pool, u, ws, "viewer", "abcdefghijkl", raw)

	if _, _, err := svc.AuthenticateAPIKey(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	var first time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT last_used_at FROM api_keys WHERE id=$1`, keyID).Scan(&first); err != nil {
		t.Fatal(err)
	}

	if _, _, err := svc.AuthenticateAPIKey(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	var second time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT last_used_at FROM api_keys WHERE id=$1`, keyID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if !second.Equal(first) {
		t.Fatalf("last_used_at changed from %v to %v", first, second)
	}
}

func TestRequireAuth_BearerAndNoCookieFallback(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	u := testutil.CreateVerifiedUser(t, pool, "apikey-middleware@example.com", testPassword)
	ws := createTestWorkspace(t, pool, u)
	raw := "o1_abcdefghijkl_" + strings.Repeat("b", 43)
	insertTestKey(t, pool, u, ws, "viewer", "abcdefghijkl", raw)
	session, _, err := svc.Login(context.Background(), "apikey-middleware@example.com", testPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}

	h := auth.RequireAuth(svc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := auth.APIKeyFrom(r.Context())
		if !ok || key.WorkspaceID != ws {
			t.Fatalf("key context = %+v, %v", key, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+ws.String()+"/monitors", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("valid key status = %d", rec.Code)
	}

	// A bad Bearer token must not fall back to a valid session cookie.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+ws.String()+"/monitors", nil)
	req.Header.Set("Authorization", "Bearer garbage")
	req.AddCookie(&http.Cookie{Name: "o1_session", Value: session})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("bad bearer status = %d, header = %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}

func TestRequireAuth_APIKeyBypassesTOTP(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	user, _, _ := enableTOTPForTest(t, svc, pool)
	ws := createTestWorkspace(t, pool, user.ID)
	raw := "o1_abcdefghijkl_" + strings.Repeat("e", 43)
	insertTestKey(t, pool, user.ID, ws, "viewer", "abcdefghijkl", raw)

	// Password login now requires a second factor, so the API key is the only
	// way to authenticate without one.
	start, err := svc.BeginLogin(context.Background(), testEmail, testPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !start.MFARequired || start.Token != "" {
		t.Fatalf("password login = %+v, want an MFA challenge", start)
	}

	h := auth.RequireAuth(svc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := auth.APIKeyFrom(r.Context())
		if !ok || key.WorkspaceID != ws {
			t.Fatalf("key context = %+v, %v", key, ok)
		}
		principal, ok := auth.PrincipalFrom(r.Context())
		if !ok || principal.UserID != user.ID || principal.SessionID != uuid.Nil {
			t.Fatalf("principal = %+v, %v", principal, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+ws.String()+"/monitors", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("TOTP-enabled user with API key status = %d", rec.Code)
	}
}

func TestRequireAuth_SessionStillWorks(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	session, _, err := svc.Login(context.Background(), testEmail, testPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}

	called := false
	h := auth.RequireAuth(svc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		principal, ok := auth.PrincipalFrom(r.Context())
		if !ok || principal.UserID == uuid.Nil {
			t.Fatalf("principal = %+v, %v", principal, ok)
		}
		if _, ok := auth.APIKeyFrom(r.Context()); ok {
			t.Fatal("session request unexpectedly has an API key principal")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: "o1_session", Value: session})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("status = %d, called = %v", rec.Code, called)
	}
}

func TestRequireAuth_KeyPrincipalIsNotAdmin(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	u := testutil.CreateVerifiedUser(t, pool, "apikey-admin@example.com", testPassword)
	ws := createTestWorkspace(t, pool, u)
	raw := "o1_abcdefghijkl_" + strings.Repeat("d", 43)
	insertTestKey(t, pool, u, ws, "member", "abcdefghijkl", raw)

	h := auth.RequireAuth(svc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFrom(r.Context())
		if !ok {
			t.Fatal("missing principal")
		}
		if principal.IsAdmin {
			t.Fatal("API key principal must not be an admin")
		}
		if principal.SessionID != uuid.Nil {
			t.Fatalf("session id = %v, want nil", principal.SessionID)
		}
		keyID := auth.APIKeyIDFrom(r.Context())
		if keyID == nil || *keyID == uuid.Nil {
			t.Fatalf("key id = %v", keyID)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
}

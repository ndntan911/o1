package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"o1/internal/auth"
	"o1/internal/db/sqlc"
	"o1/internal/httpx"
	"o1/internal/testutil"
)

// postJSON posts body as a JSON request to h and returns the response
// status, content type, and drained body.
func postJSON(t *testing.T, h http.HandlerFunc, body string) (int, string, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	resp := rec.Result()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close response: %v", err)
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(raw)
}

// postJSONRec posts body as a JSON request with optional cookies and returns
// the response recorder so tests can inspect headers and cookies.
func postJSONRec(t *testing.T, h http.HandlerFunc, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// sessionCookie returns the response's o1_session cookie, failing the test if
// it is absent.
func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == "o1_session" {
			return c
		}
	}
	t.Fatalf("no o1_session cookie in response headers: %v", rec.Header())
	return nil
}

// problemCode asserts the response is a problem+json document and returns
// its error code.
func problemCode(t *testing.T, status int, ctype, body string) string {
	t.Helper()
	if ctype != "application/problem+json" {
		t.Fatalf("status = %d, content type = %q, body = %s", status, ctype, body)
	}
	var p httpx.Problem
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("decode problem body %s: %v", body, err)
	}
	return p.Code
}

func TestRegisterHandler_CreatesUser(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	fs := testutil.NewFakeSender()
	svc := auth.NewService(pool, sqlc.New(pool), fs, testutil.TestConfig(t))

	status, _, body := postJSON(t, auth.RegisterHandler(svc),
		`{"email":"handler@example.com","password":"correct horse battery staple"}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	var created struct {
		ID    string
		Email string
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode body %s: %v", body, err)
	}
	if created.Email != "handler@example.com" || created.ID == "" {
		t.Fatalf("body = %s", body)
	}
	if len(fs.Msgs) != 1 {
		t.Fatalf("sent = %d", len(fs.Msgs))
	}
}

func TestRegisterHandler_DuplicateConflict(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := auth.NewService(pool, sqlc.New(pool), testutil.NewFakeSender(), testutil.TestConfig(t))

	status, _, _ := postJSON(t, auth.RegisterHandler(svc),
		`{"email":"dup@example.com","password":"correct horse battery staple"}`)
	if status != http.StatusCreated {
		t.Fatalf("first status = %d", status)
	}

	status, ctype, body := postJSON(t, auth.RegisterHandler(svc),
		`{"email":"DUP@example.com","password":"correct horse battery staple"}`)
	if code := problemCode(t, status, ctype, body); status != http.StatusConflict || code != "email_taken" {
		t.Fatalf("status = %d, code = %q", status, code)
	}
}

func TestRegisterHandler_ValidationErrors(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := auth.NewService(pool, sqlc.New(pool), testutil.NewFakeSender(), testutil.TestConfig(t))
	h := auth.RegisterHandler(svc)

	for name, body := range map[string]string{ //nolint:gosec // fixture payloads, not credentials
		"bad json":     "{not json",
		"unknown":      `{"email":"u@example.com","password":"correct horse battery staple","extra":1}`,
		"short pw":     `{"email":"u@example.com","password":"short"}`,
		"bad email":    `{"email":"nope","password":"correct horse battery staple"}`,
		"missing both": `{}`,
	} {
		status, ctype, respBody := postJSON(t, h, body)
		if code := problemCode(t, status, ctype, respBody); status != http.StatusBadRequest || code != "validation_error" {
			t.Errorf("%s: status = %d, code = %q", name, status, code)
		}
	}
}

func TestVerifyEmailHandler_ConsumesToken(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	fs := testutil.NewFakeSender()
	svc := auth.NewService(pool, sqlc.New(pool), fs, testutil.TestConfig(t))

	status, _, _ := postJSON(t, auth.RegisterHandler(svc),
		`{"email":"verify@example.com","password":"correct horse battery staple"}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d", status)
	}
	token := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(fs.Msgs[0].Text)[1]

	status, _, body := postJSON(t, auth.VerifyEmailHandler(svc), `{"token":"`+token+`"}`)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s, want 204", status, body)
	}

	reused, ctype, body := postJSON(t, auth.VerifyEmailHandler(svc), `{"token":"`+token+`"}`)
	if code := problemCode(t, reused, ctype, body); reused != http.StatusBadRequest || code != "token_invalid" {
		t.Fatalf("reused: status = %d, code = %q", reused, code)
	}
}

func TestVerifyEmailHandler_InvalidAndBadJSON(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := auth.NewService(pool, sqlc.New(pool), testutil.NewFakeSender(), testutil.TestConfig(t))
	h := auth.VerifyEmailHandler(svc)

	status, ctype, body := postJSON(t, h, `{"token":"unknown-token-value-000"}`)
	if code := problemCode(t, status, ctype, body); status != http.StatusBadRequest || code != "token_invalid" {
		t.Fatalf("status = %d, code = %q", status, code)
	}
	status, ctype, body = postJSON(t, h, `{"unknown":"field"}`)
	if code := problemCode(t, status, ctype, body); status != http.StatusBadRequest || code != "validation_error" {
		t.Fatalf("unknown field: status = %d, code = %q", status, code)
	}
}

func TestLoginHandler_SetsSecureCookieFlags(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	testutil.CreateVerifiedUser(t, pool, testEmail, testPassword)
	cfg := testutil.TestConfig(t)
	cfg.Env = "production"
	svc := auth.NewService(pool, sqlc.New(pool), testutil.NewFakeSender(), cfg)

	rec := postJSONRec(t, auth.LoginHandler(svc, cfg),
		`{"email":"`+testEmail+`","password":"`+testPassword+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		User struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp.User.ID == "" || resp.User.Email != testEmail {
		t.Fatalf("body = %s", rec.Body.String())
	}

	c := sessionCookie(t, rec)
	if c.Value == "" {
		t.Fatal("empty session cookie value")
	}
	if !c.HttpOnly {
		t.Fatal("session cookie is not HttpOnly")
	}
	if !c.Secure {
		t.Fatal("session cookie is not Secure in production")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite = %v, want Lax", c.SameSite)
	}
	if c.Path != "/" {
		t.Fatalf("Path = %q, want /", c.Path)
	}
	if c.MaxAge != int(cfg.SessionTTL.Seconds()) {
		t.Fatalf("MaxAge = %d, want %d", c.MaxAge, int(cfg.SessionTTL.Seconds()))
	}
}

func TestLoginHandler_NonProductionCookieNotSecure(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	testutil.CreateVerifiedUser(t, pool, testEmail, testPassword)
	cfg := testutil.TestConfig(t)
	svc := auth.NewService(pool, sqlc.New(pool), testutil.NewFakeSender(), cfg)

	rec := postJSONRec(t, auth.LoginHandler(svc, cfg),
		`{"email":"`+testEmail+`","password":"`+testPassword+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if c := sessionCookie(t, rec); c.Secure {
		t.Fatal("session cookie is Secure outside production")
	}
}

func TestLoginHandler_ErrorMapping(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	testutil.CreateVerifiedUser(t, pool, testEmail, testPassword)
	fs := testutil.NewFakeSender()
	svc := auth.NewService(pool, sqlc.New(pool), fs, testutil.TestConfig(t))
	h := auth.LoginHandler(svc, testutil.TestConfig(t))

	status, ctype, body := postJSON(t, h, `{"email":"`+testEmail+`","password":"wrong horse battery staple"}`)
	if code := problemCode(t, status, ctype, body); status != http.StatusUnauthorized || code != "invalid_credentials" {
		t.Fatalf("wrong password: status = %d, code = %q", status, code)
	}
	status, ctype, body = postJSON(t, h, `{"email":"nobody@example.com","password":"`+testPassword+`"}`)
	if code := problemCode(t, status, ctype, body); status != http.StatusUnauthorized || code != "invalid_credentials" {
		t.Fatalf("unknown email: status = %d, code = %q", status, code)
	}

	if _, err := svc.Register(context.Background(), "unverified@example.com", testPassword); err != nil {
		t.Fatal(err)
	}
	status, ctype, body = postJSON(t, h, `{"email":"unverified@example.com","password":"`+testPassword+`"}`)
	if code := problemCode(t, status, ctype, body); status != http.StatusForbidden || code != "email_not_verified" {
		t.Fatalf("unverified: status = %d, code = %q", status, code)
	}

	status, ctype, body = postJSON(t, h, `{not json`)
	if code := problemCode(t, status, ctype, body); status != http.StatusBadRequest || code != "validation_error" {
		t.Fatalf("bad json: status = %d, code = %q", status, code)
	}
}

func TestLogoutHandler_ClearsCookie(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	cfg := testutil.TestConfig(t)

	login := postJSONRec(t, auth.LoginHandler(svc, cfg),
		`{"email":"`+testEmail+`","password":"`+testPassword+`"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d", login.Code)
	}
	c := sessionCookie(t, login)

	rec := postJSONRec(t, auth.LogoutHandler(svc, cfg), "", c)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cleared := sessionCookie(t, rec)
	if cleared.Value != "" {
		t.Fatalf("cleared cookie value = %q, want empty", cleared.Value)
	}
	if cleared.MaxAge >= 0 {
		t.Fatalf("cleared cookie MaxAge = %d, want negative", cleared.MaxAge)
	}
	if !cleared.HttpOnly || cleared.Path != "/" || cleared.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cleared cookie attributes = %+v", cleared)
	}
	if _, err := svc.Authenticate(context.Background(), c.Value); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("session survived logout: %v", err)
	}

	// Logging out without a cookie is still a successful no-op.
	if rec := postJSONRec(t, auth.LogoutHandler(svc, cfg), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("cookie-less logout status = %d", rec.Code)
	}
}

func TestForgotPassword_UnknownEmailStill204(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	fs := testutil.NewFakeSender()
	svc := auth.NewService(pool, sqlc.New(pool), fs, testutil.TestConfig(t))
	h := auth.ForgotPasswordHandler(svc)

	status, _, body := postJSON(t, h, `{"email":"nobody@example.com"}`)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s, want 204", status, body)
	}
	if len(fs.Msgs) != 0 {
		t.Fatalf("unknown email sent %d messages", len(fs.Msgs))
	}

	testutil.CreateVerifiedUser(t, pool, testEmail, testPassword)
	status, _, body = postJSON(t, h, `{"email":"`+testEmail+`"}`)
	if status != http.StatusNoContent {
		t.Fatalf("known email status = %d, body = %s", status, body)
	}
	if len(fs.Msgs) != 1 {
		t.Fatalf("known email sent %d messages, want 1", len(fs.Msgs))
	}
}

func TestResetPasswordHandler_ResetsPassword(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	fs := testutil.NewFakeSender()
	svc := auth.NewService(pool, sqlc.New(pool), fs, testutil.TestConfig(t))
	testutil.CreateVerifiedUser(t, pool, testEmail, testPassword)
	h := auth.ResetPasswordHandler(svc)

	if err := svc.RequestPasswordReset(context.Background(), testEmail); err != nil {
		t.Fatal(err)
	}
	token := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(fs.Msgs[len(fs.Msgs)-1].Text)[1]

	status, _, body := postJSON(t, h, `{"token":"`+token+`","new_password":"new correct horse battery"}`)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s, want 204", status, body)
	}
	if _, _, err := svc.Login(context.Background(), testEmail, "new correct horse battery", "", ""); err != nil {
		t.Fatalf("login with new password: %v", err)
	}

	status, ctype, body := postJSON(t, h, `{"token":"`+token+`","new_password":"another correct horse battery"}`)
	if code := problemCode(t, status, ctype, body); status != http.StatusBadRequest || code != "token_invalid" {
		t.Fatalf("reuse: status = %d, code = %q", status, code)
	}

	if err := svc.RequestPasswordReset(context.Background(), testEmail); err != nil {
		t.Fatal(err)
	}
	shortToken := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(fs.Msgs[len(fs.Msgs)-1].Text)[1]
	status, ctype, body = postJSON(t, h, `{"token":"`+shortToken+`","new_password":"short"}`)
	if code := problemCode(t, status, ctype, body); status != http.StatusBadRequest || code != "validation_error" {
		t.Fatalf("short password: status = %d, code = %q", status, code)
	}
}

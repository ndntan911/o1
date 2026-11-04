package auth_test

import (
	"encoding/json"
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

package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"o1/internal/api"
	"o1/internal/httpx"
	"o1/internal/testutil"
)

// apiClient is a small HTTP client that applies the configured Origin header
// to unsafe requests and decodes JSON responses.
type apiClient struct {
	t      *testing.T
	base   string
	origin string
	client *http.Client
}

func newAPIClient(t *testing.T, base, origin string) *apiClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}
	return &apiClient{
		t:      t,
		base:   base,
		origin: origin,
		client: &http.Client{Jar: jar},
	}
}

func (c *apiClient) do(method, path string, body any) (*http.Response, []byte) {
	c.t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("encode request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		req.Header.Set("Origin", c.origin)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("read response body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		c.t.Fatalf("close response body: %v", err)
	}
	return resp, data
}

func decodeJSON[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode JSON %q: %v", string(data), err)
	}
	return v
}

func requireStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("%s %s = %d, want %d", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, want)
	}
}

var tokenPattern = regexp.MustCompile(`token=([A-Za-z0-9_-]+)`)

func verificationToken(t *testing.T, text string) string {
	t.Helper()
	match := tokenPattern.FindStringSubmatch(text)
	if len(match) != 2 {
		t.Fatalf("verification message contains no token: %q", text)
	}
	return match[1]
}

func TestAPI_RegisterVerifyLoginCreateWorkspace(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()

	fs := testutil.NewFakeSender()
	cfg := testutil.TestConfig(t)
	router := api.NewRouter(api.Dependencies{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Pool:        pool,
		Config:      cfg,
		EmailSender: fs,
	})
	server := httptest.NewServer(router)
	defer server.Close()

	c := newAPIClient(t, server.URL, cfg.AppBaseURL)

	// 1. Register.
	resp, body := c.do(http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email":    "alice@example.com",
		"password": "correct horse battery staple",
	})
	requireStatus(t, resp, http.StatusCreated)
	registered := decodeJSON[struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}](t, body)
	if registered.ID == "" || registered.Email != "alice@example.com" {
		t.Fatalf("register response = %+v", registered)
	}

	// 2. Extract the verification token from the fake sender and verify.
	messages := fs.WaitForMessages(t, 1)
	token := verificationToken(t, messages[0].Text)
	resp, _ = c.do(http.MethodPost, "/api/v1/auth/verify-email", map[string]string{"token": token})
	requireStatus(t, resp, http.StatusNoContent)

	// 3. Log in; the cookie jar keeps the session cookie.
	resp, body = c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email":    "alice@example.com",
		"password": "correct horse battery staple",
	})
	requireStatus(t, resp, http.StatusOK)
	loggedIn := decodeJSON[struct {
		User struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
	}](t, body)
	if loggedIn.User.ID != registered.ID || loggedIn.User.Email != "alice@example.com" {
		t.Fatalf("login user = %+v", loggedIn.User)
	}

	// 4. /me returns the user and no workspaces yet.
	resp, body = c.do(http.MethodGet, "/api/v1/me", nil)
	requireStatus(t, resp, http.StatusOK)
	me := decodeJSON[struct {
		User struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
		Workspaces []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
			Role string `json:"role"`
		} `json:"workspaces"`
	}](t, body)
	if me.User.ID != registered.ID || me.User.Email != "alice@example.com" {
		t.Fatalf("me user = %+v", me.User)
	}
	if len(me.Workspaces) != 0 {
		t.Fatalf("me workspaces = %+v, want none", me.Workspaces)
	}

	// 5. Create a workspace.
	resp, body = c.do(http.MethodPost, "/api/v1/workspaces", map[string]string{"name": "Acme Uptime"})
	requireStatus(t, resp, http.StatusCreated)
	created := decodeJSON[struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		Role string `json:"role"`
	}](t, body)
	if created.ID == "" || created.Name != "Acme Uptime" || created.Role != "owner" {
		t.Fatalf("created workspace = %+v", created)
	}

	// 6. List workspaces: exactly the one just created.
	resp, body = c.do(http.MethodGet, "/api/v1/workspaces", nil)
	requireStatus(t, resp, http.StatusOK)
	list := decodeJSON[struct {
		Workspaces []struct {
			ID string `json:"id"`
		} `json:"workspaces"`
	}](t, body)
	if len(list.Workspaces) != 1 || list.Workspaces[0].ID != created.ID {
		t.Fatalf("workspace list = %+v, want [%s]", list.Workspaces, created.ID)
	}

	// 7. Log out, then /me is unauthenticated.
	resp, _ = c.do(http.MethodPost, "/api/v1/auth/logout", nil)
	requireStatus(t, resp, http.StatusNoContent)

	resp, body = c.do(http.MethodGet, "/api/v1/me", nil)
	requireStatus(t, resp, http.StatusUnauthorized)
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("unauthenticated content type = %q", ct)
	}
	problem := decodeJSON[httpx.Problem](t, body)
	if problem.Code != httpx.CodeUnauthenticated || problem.Status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated problem = %+v", problem)
	}
	if strings.Contains(string(body), "alice@example.com") {
		t.Fatal("unauthenticated response leaked the user's email")
	}
}

package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"o1/internal/api"
	"o1/internal/config"
	"o1/internal/testutil"
)

func TestMCP_DisabledReturns501(t *testing.T) {
	router := api.NewRouter(api.Dependencies{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: config.Config{
			AllowedOrigins: []string{"https://app.example.com"},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("POST /mcp = %d, want 501; body = %s", rec.Code, rec.Body.String())
	}
	problem := decodeJSON[struct {
		Code string `json:"code"`
	}](t, rec.Body.Bytes())
	if problem.Code != "not_implemented" {
		t.Fatalf("problem code = %q, want not_implemented", problem.Code)
	}
}

func TestMCP_EnabledWithoutDependenciesReturns501(t *testing.T) {
	router := api.NewRouter(api.Dependencies{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: config.Config{
			MCPEnabled:     true,
			AllowedOrigins: []string{"https://app.example.com"},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("POST /mcp = %d, want 501; body = %s", rec.Code, rec.Body.String())
	}
}

func TestMCP_EnabledWiresRealHandler(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()

	cfg := testutil.TestConfig(t)
	cfg.MCPEnabled = true
	router := api.NewRouter(api.Dependencies{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Pool:   pool,
		Config: cfg,
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /mcp without a key = %d, want 401; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
	}
}

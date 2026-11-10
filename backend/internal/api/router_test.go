package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"o1/internal/api"
	"o1/internal/config"
	"o1/internal/httpx"
	"o1/internal/observability"
)

// newTestRouter builds a router without a database. Only routes that do not
// reach the services are exercised here.
func newTestRouter(t *testing.T) http.Handler {
	t.Helper()
	return api.NewRouter(api.Dependencies{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: config.Config{AllowedOrigins: []string{"https://app.example.com"}},
	})
}

func serve(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRouter_HealthStaysAtRoot(t *testing.T) {
	h := newTestRouter(t)

	rec := serve(t, h, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want %d", rec.Code, http.StatusOK)
	}

	rec = serve(t, h, httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/healthz = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestRouter_ReadyzReportsDatabaseUnavailable(t *testing.T) {
	h := newTestRouter(t)
	rec := serve(t, h, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if p.Code != "not_ready" {
		t.Fatalf("code = %q, want not_ready", p.Code)
	}
}

func TestRouter_ProtectedRoutesRequireSession(t *testing.T) {
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/me"},
		{http.MethodPost, "/api/v1/auth/logout"},
		{http.MethodGet, "/api/v1/workspaces"},
		{http.MethodPost, "/api/v1/workspaces"},
		{http.MethodGet, "/api/v1/workspaces/01234567-89ab-cdef-0123-456789abcdef"},
		{http.MethodGet, "/api/v1/workspaces/01234567-89ab-cdef-0123-456789abcdef/members"},
		{http.MethodPost, "/api/v1/workspaces/01234567-89ab-cdef-0123-456789abcdef/members"},
		{http.MethodPost, "/api/v1/workspaces/01234567-89ab-cdef-0123-456789abcdef/monitors"},
		{http.MethodGet, "/api/v1/workspaces/01234567-89ab-cdef-0123-456789abcdef/monitors"},
		{http.MethodGet, "/api/v1/workspaces/01234567-89ab-cdef-0123-456789abcdef/monitors/01234567-89ab-cdef-0123-456789abcdef"},
		{http.MethodPatch, "/api/v1/workspaces/01234567-89ab-cdef-0123-456789abcdef/monitors/01234567-89ab-cdef-0123-456789abcdef"},
		{http.MethodDelete, "/api/v1/workspaces/01234567-89ab-cdef-0123-456789abcdef/monitors/01234567-89ab-cdef-0123-456789abcdef"},
		{http.MethodPost, "/api/v1/workspaces/01234567-89ab-cdef-0123-456789abcdef/monitors/01234567-89ab-cdef-0123-456789abcdef/pause"},
		{http.MethodPost, "/api/v1/workspaces/01234567-89ab-cdef-0123-456789abcdef/monitors/01234567-89ab-cdef-0123-456789abcdef/resume"},
		{http.MethodGet, "/api/v1/workspaces/01234567-89ab-cdef-0123-456789abcdef/monitors/01234567-89ab-cdef-0123-456789abcdef/results"},
		{http.MethodGet, "/api/v1/workspaces/01234567-89ab-cdef-0123-456789abcdef/monitors/01234567-89ab-cdef-0123-456789abcdef/summary"},
		{http.MethodGet, "/api/v1/admin/workspaces"},
		{http.MethodPost, "/api/v1/admin/workspaces/01234567-89ab-cdef-0123-456789abcdef/suspend"},
		{http.MethodPost, "/api/v1/admin/workspaces/01234567-89ab-cdef-0123-456789abcdef/unsuspend"},
		{http.MethodGet, "/api/v1/admin/monitors"},
		{http.MethodGet, "/api/v1/admin/security-events"},
		{http.MethodGet, "/api/v1/auth/2fa"},
		{http.MethodPost, "/api/v1/auth/2fa/setup"},
		{http.MethodPost, "/api/v1/auth/2fa/enable"},
		{http.MethodPost, "/api/v1/auth/2fa/disable"},
		{http.MethodPost, "/api/v1/auth/2fa/recovery-codes"},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			h := newTestRouter(t)
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("Origin", "https://app.example.com")
			rec := serve(t, h, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestRouter_CSRFRejectsForeignOrigin(t *testing.T) {
	h := newTestRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := serve(t, h, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if p.Code != httpx.CodeForbidden {
		t.Fatalf("code = %q, want %q", p.Code, httpx.CodeForbidden)
	}
}

func TestRouter_RegisterRejectsMalformedBody(t *testing.T) {
	h := newTestRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := serve(t, h, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestRouter_AuthRateLimiterCoversTOTPRoutes(t *testing.T) {
	for _, path := range []string{
		"/api/v1/auth/login/totp",
		"/api/v1/auth/2fa/setup",
	} {
		t.Run(path, func(t *testing.T) {
			h := newTestRouter(t)
			var rec *httptest.ResponseRecorder
			for i := 0; i < 11; i++ {
				rec = serve(t, h, httptest.NewRequest(http.MethodPost, path, nil))
			}
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("request 11 = %d, want %d", rec.Code, http.StatusTooManyRequests)
			}
		})
	}
}

func TestRouterMetrics_PublicMetricsNotServed(t *testing.T) {
	h := newTestRouter(t)
	rec := serve(t, h, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /metrics = %d, want 404 or 405", rec.Code)
	}
}

func TestRouterMetrics_RecordsBoundedRoutePattern(t *testing.T) {
	m := observability.NewMetrics("o1-api", "test")
	h := api.NewRouter(api.Dependencies{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config:  config.Config{AllowedOrigins: []string{"https://app.example.com"}},
		Metrics: m,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/01234567-89ab-cdef-0123-456789abcdef/monitors", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := serve(t, h, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	metricsRec := httptest.NewRecorder()
	m.Handler().ServeHTTP(metricsRec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metricsRec.Body.String()
	want := `o1_http_requests_total{method="GET",route="GET /api/v1/workspaces/{workspaceID}/monitors",status="401"} 1`
	if !strings.Contains(body, want) {
		t.Fatalf("metrics body missing bounded route labels:\n%s", body)
	}
	if strings.Contains(body, "01234567") {
		t.Fatal("raw UUID leaked into metrics")
	}
}

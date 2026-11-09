package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"o1/internal/httpx"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

func TestCSRF_RejectsForeignOrigin(t *testing.T) {
	h := httpx.CSRF([]string{"https://app.example.com"})(okHandler())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type = %q, want application/problem+json", ct)
	}
}

func TestCSRF_AllowsConfiguredOrigin(t *testing.T) {
	h := httpx.CSRF([]string{"https://app.example.com"})(okHandler())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestCSRF_AllowsMissingOrigin(t *testing.T) {
	h := httpx.CSRF([]string{"https://app.example.com"})(okHandler())
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/workspaces/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestCSRF_IgnoresSafeMethods(t *testing.T) {
	h := httpx.CSRF([]string{"https://app.example.com"})(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestRateLimit_Returns429AfterBurst(t *testing.T) {
	h := httpx.RateLimit(1, 2, true)(okHandler())
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		req.RemoteAddr = "203.0.113.10:1234"
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("request %d = %d, want %d", i, rec.Code, http.StatusNoContent)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Fatal("missing Retry-After header")
	}
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if p.Code != "rate_limited" || p.Status != http.StatusTooManyRequests {
		t.Fatalf("problem = %+v, want rate_limited/429", p)
	}
}

func TestRateLimit_IsolatesIPs(t *testing.T) {
	h := httpx.RateLimit(1, 1, true)(okHandler())

	request := func(remoteAddr string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		req.RemoteAddr = remoteAddr
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := request("203.0.113.10:1234"); got != http.StatusNoContent {
		t.Fatalf("first IP request = %d, want %d", got, http.StatusNoContent)
	}
	if got := request("203.0.113.11:1234"); got != http.StatusNoContent {
		t.Fatalf("second IP request = %d, want %d", got, http.StatusNoContent)
	}
	if got := request("203.0.113.10:5678"); got != http.StatusTooManyRequests {
		t.Fatalf("first IP second request = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func TestRateLimit_UsesForwardedForBehindPrivateNetwork(t *testing.T) {
	h := httpx.RateLimit(1, 1, true)(okHandler())

	request := func(remoteAddr, forwardedFor string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		req.RemoteAddr = remoteAddr
		req.Header.Set("X-Forwarded-For", forwardedFor)
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := request("10.0.0.5:1234", "203.0.113.10"); got != http.StatusNoContent {
		t.Fatalf("first forwarded request = %d, want %d", got, http.StatusNoContent)
	}
	if got := request("10.0.0.5:5678", "203.0.113.10"); got != http.StatusTooManyRequests {
		t.Fatalf("repeat forwarded request = %d, want %d", got, http.StatusTooManyRequests)
	}
	if got := request("10.0.0.5:1234", "203.0.113.11"); got != http.StatusNoContent {
		t.Fatalf("different forwarded request = %d, want %d", got, http.StatusNoContent)
	}
}

func TestRateLimit_IgnoresForwardedForFromPublicClient(t *testing.T) {
	h := httpx.RateLimit(1, 1, true)(okHandler())

	request := func(remoteAddr, forwardedFor string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		req.RemoteAddr = remoteAddr
		req.Header.Set("X-Forwarded-For", forwardedFor)
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := request("203.0.113.10:1234", "198.51.100.1"); got != http.StatusNoContent {
		t.Fatalf("first public request = %d, want %d", got, http.StatusNoContent)
	}
	if got := request("203.0.113.10:5678", "198.51.100.2"); got != http.StatusTooManyRequests {
		t.Fatalf("spoofed forwarded request = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func TestRateLimit_GlobalBucket(t *testing.T) {
	h := httpx.RateLimit(1, 1, false)(okHandler())

	request := func(remoteAddr string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		req.RemoteAddr = remoteAddr
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := request("203.0.113.10:1234"); got != http.StatusNoContent {
		t.Fatalf("first request = %d, want %d", got, http.StatusNoContent)
	}
	if got := request("203.0.113.11:1234"); got != http.StatusTooManyRequests {
		t.Fatalf("second request from another IP = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func TestKeyedRateLimit_IsolatesKeys(t *testing.T) {
	h := httpx.KeyedRateLimit(func(r *http.Request) string {
		return r.Header.Get("X-Test-Key")
	}, 1, 2)(okHandler())

	// okHandler passes allowed requests through with 204.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Test-Key", "a")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("key a request %d status = %d", i, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Test-Key", "a")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("key a third status = %d, want 429", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Test-Key", "b")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("key b status = %d, want 204", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("empty key status = %d, want 204", rec.Code)
	}
}

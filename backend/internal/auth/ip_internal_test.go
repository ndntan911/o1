package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"o1/internal/httpx"
)

func TestRequestIP_MatchesHTTPXClientIP(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		forwarded  string
		want       string
	}{
		{
			name:       "loopback peer trusts forwarded header",
			remoteAddr: "127.0.0.1:1234",
			forwarded:  "203.0.113.10",
			want:       "203.0.113.10",
		},
		{
			name:       "private peer trusts forwarded header",
			remoteAddr: "10.0.0.5:1234",
			forwarded:  "203.0.113.11",
			want:       "203.0.113.11",
		},
		{
			name:       "public peer ignores forwarded header",
			remoteAddr: "203.0.113.10:1234",
			forwarded:  "198.51.100.1",
			want:       "203.0.113.10",
		},
		{
			name:       "no forwarded header uses peer",
			remoteAddr: "10.0.0.5:1234",
			want:       "10.0.0.5",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			got := requestIP(req)
			if got != httpx.ClientIP(req) {
				t.Fatalf("requestIP = %q, httpx.ClientIP = %q", got, httpx.ClientIP(req))
			}
			if got != tc.want {
				t.Fatalf("requestIP = %q, want %q", got, tc.want)
			}
		})
	}
}

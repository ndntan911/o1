package monitors

import (
	"context"
	"errors"
	"net"
	"testing"

	"o1/internal/probe"
)

// staticResolver makes URL validation deterministic: every hostname resolves
// to the configured IP.
type staticResolver struct {
	ip string
}

func (r staticResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if r.ip == "" {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return []net.IPAddr{{IP: net.ParseIP(r.ip)}}, nil
}

func testGuard() *probe.Guard {
	return probe.NewGuardWithResolver(probe.Policy{
		AllowedPorts:    map[int]struct{}{80: {}, 443: {}},
		MaxRedirects:    5,
		MaxResponseBody: 1 << 20,
	}, staticResolver{ip: "93.184.216.34"})
}

func TestValidateInput(t *testing.T) {
	base := Input{
		Name: "API", URL: "https://example.com/health", Method: "GET",
		ExpectedStatus: "2xx", IntervalSeconds: 60, TimeoutSeconds: 10,
		ConfirmationThreshold: 2, RecoveryThreshold: 2,
	}
	guard := testGuard()
	if _, err := validate(base, guard); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Input)
		field  string
	}{
		{"empty name", func(in *Input) { in.Name = "  " }, "name"},
		{"bad method", func(in *Input) { in.Method = "TRACE" }, "method"},
		{"short interval", func(in *Input) { in.IntervalSeconds = 30 }, "interval_seconds"},
		{"long timeout", func(in *Input) { in.TimeoutSeconds = 60 }, "timeout_seconds"},
		{"bad threshold", func(in *Input) { in.ConfirmationThreshold = 6 }, "confirmation_threshold"},
		{"bad expected", func(in *Input) { in.ExpectedStatus = "nope" }, "expected_status"},
		{"host header", func(in *Input) { in.Headers = map[string]string{"Host": "evil"} }, "headers"},
		{"get body", func(in *Input) { b := "x"; in.Body = &b }, "body"},
		{"bad url", func(in *Input) { in.URL = "ftp://example.com/health" }, "url"},
		{"bad port", func(in *Input) { in.URL = "https://example.com:8443/health" }, "url"},
		{"content type without body", func(in *Input) { ct := "application/json"; in.ContentType = &ct }, "content_type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.mutate(&in)
			_, err := validate(in, guard)
			var verr *ValidationError
			if !errors.As(err, &verr) || verr.Fields[tc.field] == "" {
				t.Fatalf("err = %v, want field %q", err, tc.field)
			}
		})
	}
}

func TestValidateInput_Normalizes(t *testing.T) {
	in := Input{
		Name: "  API  ", URL: "  https://example.com/health  ", Method: "post",
		ExpectedStatus: "200-299", IntervalSeconds: 60, TimeoutSeconds: 10,
		ConfirmationThreshold: 2, RecoveryThreshold: 2,
	}
	got, err := validate(in, testGuard())
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got.Name != "API" {
		t.Fatalf("name = %q", got.Name)
	}
	if got.Method != "POST" {
		t.Fatalf("method = %q", got.Method)
	}
	if got.URL != "https://example.com/health" {
		t.Fatalf("url = %q", got.URL)
	}
	if got.ExpectedStatus != "200-299" {
		t.Fatalf("expected status = %q", got.ExpectedStatus)
	}
}

func TestValidateInput_DefaultsExpectedStatus(t *testing.T) {
	in := Input{
		Name: "API", URL: "https://example.com/health", Method: "GET",
		IntervalSeconds: 60, TimeoutSeconds: 10,
		ConfirmationThreshold: 2, RecoveryThreshold: 2,
	}
	got, err := validate(in, testGuard())
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got.ExpectedStatus != "200-299" {
		t.Fatalf("expected status = %q, want 200-299", got.ExpectedStatus)
	}
}

func TestValidateInput_SSRFBlockedIsWrapped(t *testing.T) {
	in := Input{
		Name: "API", URL: "http://127.0.0.1/health", Method: "GET",
		ExpectedStatus: "2xx", IntervalSeconds: 60, TimeoutSeconds: 10,
		ConfirmationThreshold: 2, RecoveryThreshold: 2,
	}
	_, err := validate(in, testGuard())
	var verr *ValidationError
	if !errors.As(err, &verr) || verr.Fields["url"] == "" {
		t.Fatalf("err = %v, want url field", err)
	}
	if !errors.Is(err, probe.ErrBlocked) {
		t.Fatalf("err = %v, want wrapped probe.ErrBlocked", err)
	}
}

func TestRedactURL(t *testing.T) {
	cases := map[string]string{ //nolint:gosec // redaction fixtures, not credentials
		"https://example.com/health?token=secret#frag": "https://example.com/health",
		"https://example.com/health":                   "https://example.com/health",
		"https://example.com/?a=1":                     "https://example.com/",
		"not a url?token=secret":                       "not a url",
	}
	for raw, want := range cases {
		if got := RedactURL(raw); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

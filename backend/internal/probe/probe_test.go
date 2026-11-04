package probe

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func mustStatus(t *testing.T, raw string) ExpectedStatus {
	t.Helper()
	st, err := ParseExpectedStatus(raw)
	if err != nil {
		t.Fatalf("ParseExpectedStatus(%q): %v", raw, err)
	}
	return st
}

func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port from %q: %v", rawURL, err)
	}
	return port
}

func policyForPorts(ports ...int) Policy {
	allowed := make(map[int]struct{}, len(ports))
	for _, port := range ports {
		allowed[port] = struct{}{}
	}
	return Policy{
		AllowedPorts:         allowed,
		MaxRedirects:         5,
		MaxResponseBody:      1 << 20,
		ConnectTimeout:       time.Second,
		TLSHandshakeTimeout:  time.Second,
		AllowPrivateNetworks: true,
	}
}

func newPermissiveProberForPorts(t *testing.T, maxRedirects int, ports ...int) *Prober {
	t.Helper()
	p := policyForPorts(ports...)
	p.MaxRedirects = maxRedirects
	return NewProber(NewGuardWithResolver(p, staticResolver{ips: []string{"127.0.0.1"}}))
}

func newPermissiveProber(t *testing.T, rawURL string) *Prober {
	t.Helper()
	return newPermissiveProberForPorts(t, 5, portOf(t, rawURL))
}

func TestProber_SuccessStatusAndTimeout(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Host") != "" {
			t.Error("probe sent a user Host header")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ok.Close()

	p := newPermissiveProber(t, ok.URL) // AllowPrivateNetworks=true, port from the URL
	res := p.Probe(context.Background(), Request{
		URL: ok.URL, Method: http.MethodGet,
		Expected: mustStatus(t, "2xx"), Timeout: 2 * time.Second,
	})
	if !res.Up || res.StatusCode != http.StatusNoContent || res.ErrorClass != "" {
		t.Fatalf("success result = %+v", res)
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer slow.Close()
	res = p.Probe(context.Background(), Request{
		URL: slow.URL, Method: http.MethodGet,
		Expected: mustStatus(t, "2xx"), Timeout: 50 * time.Millisecond,
	})
	if res.Up || res.ErrorClass != "timeout" {
		t.Fatalf("timeout result = %+v", res)
	}
}

func TestProber_WrongStatusIsDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	res := newPermissiveProber(t, srv.URL).Probe(context.Background(), Request{
		URL: srv.URL, Method: http.MethodGet,
		Expected: mustStatus(t, "2xx"), Timeout: 2 * time.Second,
	})
	if res.Up {
		t.Fatalf("wrong status result = %+v, want down", res)
	}
	if res.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", res.StatusCode)
	}
	if res.LatencyMS < 0 {
		t.Errorf("LatencyMS = %d", res.LatencyMS)
	}
}

func TestProber_BlocksPrivateTargetWithoutEscapeHatch(t *testing.T) {
	g := NewGuardWithResolver(
		Policy{AllowedPorts: map[int]struct{}{80: {}}, MaxRedirects: 5, MaxResponseBody: 1 << 20},
		staticResolver{ips: []string{"93.184.216.34"}},
	)
	res := NewProber(g).Probe(context.Background(), Request{
		URL: "http://127.0.0.1/", Method: http.MethodGet,
		Expected: mustStatus(t, "2xx"), Timeout: time.Second,
	})
	if res.Up || res.ErrorClass != "ssrf_blocked" {
		t.Fatalf("private target result = %+v, want ssrf_blocked", res)
	}
}

func TestProber_TLSVerificationFailure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := newPermissiveProber(t, srv.URL).Probe(context.Background(), Request{
		URL: srv.URL, Method: http.MethodGet,
		Expected: mustStatus(t, "2xx"), Timeout: 2 * time.Second,
	})
	if res.Up || res.ErrorClass != "tls" {
		t.Fatalf("TLS result = %+v, want tls", res)
	}
}

func TestProber_RedirectLimit(t *testing.T) {
	const n = 6
	servers := make([]*httptest.Server, n)
	for i := range servers {
		i := i
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, servers[(i+1)%n].URL, http.StatusFound)
		}))
	}
	for _, srv := range servers {
		defer srv.Close()
	}

	ports := make([]int, 0, len(servers))
	for _, srv := range servers {
		ports = append(ports, portOf(t, srv.URL))
	}
	p := newPermissiveProberForPorts(t, 5, ports...)
	res := p.Probe(context.Background(), Request{
		URL: servers[0].URL, Method: http.MethodGet,
		Expected: mustStatus(t, "2xx"), Timeout: 2 * time.Second,
	})
	if res.Up || res.ErrorClass != "redirect" {
		t.Fatalf("redirect limit result = %+v, want redirect", res)
	}
}

func TestGuardCheckRedirect_RejectsPrivateTarget(t *testing.T) {
	g := NewGuardWithResolver(
		Policy{AllowedPorts: map[int]struct{}{80: {}, 443: {}}, MaxRedirects: 5},
		staticResolver{ips: []string{"93.184.216.34"}},
	)
	prev, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	next, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.checkRedirect(next, []*http.Request{prev}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("checkRedirect error = %v, want ErrBlocked", err)
	}
}

func TestGuardCheckRedirect_StripsHeadersCrossHost(t *testing.T) {
	g := NewGuardWithResolver(
		Policy{AllowedPorts: map[int]struct{}{80: {}, 443: {}}, MaxRedirects: 5},
		staticResolver{ips: []string{"93.184.216.34"}},
	)
	prev, err := http.NewRequest(http.MethodGet, "https://example.com/a", nil)
	if err != nil {
		t.Fatal(err)
	}

	same, err := http.NewRequest(http.MethodGet, "https://example.com/b", nil)
	if err != nil {
		t.Fatal(err)
	}
	same.Header.Set("Authorization", "Bearer secret")
	same.Header.Set("X-Custom", "1")
	same.Header.Set("Host", "evil.example")
	if err := g.checkRedirect(same, []*http.Request{prev}); err != nil {
		t.Fatalf("same-host checkRedirect: %v", err)
	}
	if same.Header.Get("Authorization") != "Bearer secret" {
		t.Error("same-host redirect dropped user Authorization")
	}
	if same.Header.Get("X-Custom") != "1" {
		t.Error("same-host redirect dropped user header")
	}
	if same.Header.Get("Host") != "" {
		t.Error("same-host redirect kept Host header")
	}

	cross, err := http.NewRequest(http.MethodGet, "https://other.example/b", nil)
	if err != nil {
		t.Fatal(err)
	}
	cross.Header.Set("Authorization", "Bearer secret")
	cross.Header.Set("X-Custom", "1")
	cross.Header.Set("Accept", "application/json")
	cross.Header.Set("User-Agent", "kept")
	if err := g.checkRedirect(cross, []*http.Request{prev}); err != nil {
		t.Fatalf("cross-host checkRedirect: %v", err)
	}
	if cross.Header.Get("Authorization") != "" {
		t.Error("cross-host redirect kept Authorization")
	}
	if cross.Header.Get("X-Custom") != "" {
		t.Error("cross-host redirect kept user header")
	}
	if cross.Header.Get("Accept") != "application/json" {
		t.Error("cross-host redirect dropped Accept")
	}
	if cross.Header.Get("User-Agent") != "kept" {
		t.Error("cross-host redirect dropped User-Agent")
	}
}

func TestGuardCheckRedirect_RejectsTooMany(t *testing.T) {
	g := NewGuardWithResolver(
		Policy{AllowedPorts: map[int]struct{}{80: {}, 443: {}}, MaxRedirects: 1},
		staticResolver{ips: []string{"93.184.216.34"}},
	)
	req, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	via := []*http.Request{req, req}
	if err := g.checkRedirect(req, via); !errors.Is(err, errTooManyRedirects) {
		t.Fatalf("checkRedirect error = %v, want errTooManyRedirects", err)
	}
}

func TestValidateHeaders(t *testing.T) {
	if err := ValidateHeaders(nil); err != nil {
		t.Errorf("ValidateHeaders(nil) = %v", err)
	}
	if err := ValidateHeaders(map[string]string{"Authorization": "Bearer secret", "Accept": "application/json"}); err != nil {
		t.Errorf("ValidateHeaders(valid) = %v", err)
	}
	for _, name := range []string{"Host", "hOsT", "Content-Length", "Connection", "Transfer-Encoding", "Proxy-Connection", "Upgrade", "TE"} {
		if err := ValidateHeaders(map[string]string{name: "x"}); err == nil {
			t.Errorf("ValidateHeaders accepted blocked header %q", name)
		}
	}
	if err := ValidateHeaders(map[string]string{"": "x"}); err == nil {
		t.Error("ValidateHeaders accepted empty name")
	}
	if err := ValidateHeaders(map[string]string{"Bad Name": "x"}); err == nil {
		t.Error("ValidateHeaders accepted invalid name")
	}
	if err := ValidateHeaders(map[string]string{strings.Repeat("a", 65): "x"}); err == nil {
		t.Error("ValidateHeaders accepted 65-character name")
	}
	if err := ValidateHeaders(map[string]string{"X-Test": strings.Repeat("a", 1025)}); err == nil {
		t.Error("ValidateHeaders accepted 1025-character value")
	}
	if err := ValidateHeaders(map[string]string{"X-Test": "line\r\nbreak"}); err == nil {
		t.Error("ValidateHeaders accepted CRLF value")
	}
	if err := ValidateHeaders(map[string]string{"X-Test": "line\nbreak"}); err == nil {
		t.Error("ValidateHeaders accepted LF value")
	}

	many := make(map[string]string, 21)
	for i := 0; i < 21; i++ {
		many["X-Test-"+strconv.Itoa(i)] = "x"
	}
	if err := ValidateHeaders(many); err == nil {
		t.Error("ValidateHeaders accepted 21 headers")
	}

	total := make(map[string]string, 9)
	for i := 0; i < 9; i++ {
		total["X-Test-"+strconv.Itoa(i)] = strings.Repeat("a", 1024)
	}
	if err := ValidateHeaders(total); err == nil {
		t.Error("ValidateHeaders accepted more than 8 KiB of headers")
	}
}

func TestValidateBody(t *testing.T) {
	empty := ""
	body := "hello"
	large := strings.Repeat("a", 16*1024)
	tooLarge := strings.Repeat("a", 16*1024+1)
	longType := strings.Repeat("a", 201)

	if err := ValidateBody(http.MethodGet, nil, nil); err != nil {
		t.Errorf("ValidateBody(GET, nil) = %v", err)
	}
	if err := ValidateBody(http.MethodGet, &body, nil); err == nil {
		t.Error("ValidateBody accepted GET body")
	}
	if err := ValidateBody(http.MethodHead, &body, nil); err == nil {
		t.Error("ValidateBody accepted HEAD body")
	}
	if err := ValidateBody(http.MethodGet, &empty, nil); err != nil {
		t.Errorf("ValidateBody(GET, empty body) = %v, want nil (cleared body)", err)
	}
	if err := ValidateBody(http.MethodPost, &body, nil); err != nil {
		t.Errorf("ValidateBody(POST, body) = %v", err)
	}
	if err := ValidateBody(http.MethodPost, &large, nil); err != nil {
		t.Errorf("ValidateBody(POST, 16 KiB body) = %v", err)
	}
	if err := ValidateBody(http.MethodPost, &tooLarge, nil); err == nil {
		t.Error("ValidateBody accepted 16 KiB + 1 body")
	}
	ct := "application/json"
	if err := ValidateBody(http.MethodPost, nil, &ct); err == nil {
		t.Error("ValidateBody accepted Content-Type without body")
	}
	if err := ValidateBody(http.MethodPost, &body, &ct); err != nil {
		t.Errorf("ValidateBody(POST, body, content type) = %v", err)
	}
	if err := ValidateBody(http.MethodPost, &body, &longType); err == nil {
		t.Error("ValidateBody accepted 201-character content type")
	}
}

func TestProber_DNSError(t *testing.T) {
	g := NewGuardWithResolver(
		Policy{AllowedPorts: map[int]struct{}{80: {}}, MaxRedirects: 5, MaxResponseBody: 1 << 20},
		staticResolver{err: &net.DNSError{Err: "no such host", Name: "nonexistent.example", IsNotFound: true}},
	)
	res := NewProber(g).Probe(context.Background(), Request{
		URL: "http://nonexistent.example/health?token=secret", Method: http.MethodGet,
		Expected: mustStatus(t, "2xx"), Timeout: time.Second,
	})
	if res.Up || res.ErrorClass != "dns" {
		t.Fatalf("DNS result = %+v, want dns", res)
	}
	if strings.Contains(res.ErrorMessage, "secret") {
		t.Errorf("ErrorMessage leaked query: %q", res.ErrorMessage)
	}
}

func TestProber_ConnectionErrorSanitized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rawURL := srv.URL + "/health?token=super-secret"
	p := newPermissiveProber(t, srv.URL)
	srv.Close() // force a connection failure

	res := p.Probe(context.Background(), Request{
		URL: rawURL, Method: http.MethodGet,
		Expected: mustStatus(t, "2xx"), Timeout: time.Second,
	})
	if res.Up || res.ErrorClass != "connection" {
		t.Fatalf("connection result = %+v, want connection", res)
	}
	if strings.Contains(res.ErrorMessage, "secret") || strings.Contains(res.ErrorMessage, "?") {
		t.Errorf("ErrorMessage leaked URL/query: %q", res.ErrorMessage)
	}
}

func TestSanitizeErrorMessage(t *testing.T) {
	raw := &url.Error{
		Op:  "Get",
		URL: "https://example.com/health?token=super-secret",
		Err: errors.New("boom"),
	}
	msg := sanitizeErrorMessage(raw)
	if strings.Contains(msg, "token") || strings.Contains(msg, "super-secret") {
		t.Errorf("sanitizeErrorMessage leaked query: %q", msg)
	}
	long := errors.New(strings.Repeat("x", 1000))
	if msg := sanitizeErrorMessage(long); len(msg) > 256 {
		t.Errorf("sanitizeErrorMessage not truncated: len = %d", len(msg))
	}
}

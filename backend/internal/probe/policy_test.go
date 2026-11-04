package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"o1/internal/config"
)

// staticResolver is a deterministic Resolver for tests.
type staticResolver struct {
	ips []string
	err error
}

func (r staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	if r.err != nil {
		return nil, r.err
	}
	addrs := make([]net.IPAddr, 0, len(r.ips))
	for _, raw := range r.ips {
		ip := net.ParseIP(raw)
		if ip == nil {
			panic(fmt.Sprintf("staticResolver: invalid IP %q", raw))
		}
		addrs = append(addrs, net.IPAddr{IP: ip})
	}
	return addrs, nil
}

func newTestGuard(t *testing.T, p Policy) *Guard {
	t.Helper()
	return NewGuardWithResolver(p, staticResolver{ips: []string{"93.184.216.34"}})
}

func TestValidateURL_BlocksDangerousTargets(t *testing.T) {
	g := newTestGuard(t, Policy{
		AllowedPorts: map[int]struct{}{80: {}, 443: {}},
		MaxRedirects: 5, MaxResponseBody: 1 << 20,
	})
	cases := []string{
		"http://127.0.0.1/", "http://10.0.0.1/", "http://169.254.1.1/",
		"http://[::1]/", "http://[fc00::1]/", "http://[fe80::1]/",
		"http://[::ffff:127.0.0.1]/", "http://100.64.0.1/",
		"http://192.0.0.1/", "http://198.18.0.1/", "http://224.0.0.1/",
		"http://0.0.0.0/", "http://example.com:8080/",
		"ftp://example.com/", "http://user:pass@example.com/",
		"http://example.com/#fragment",
	}
	for _, raw := range cases {
		if _, err := g.ValidateURL(context.Background(), raw); err == nil {
			t.Errorf("ValidateURL(%q) succeeded, want rejection", raw)
		}
	}
}

func TestValidateURL_AcceptsPublicHTTP(t *testing.T) {
	g := newTestGuard(t, Policy{AllowedPorts: map[int]struct{}{80: {}, 443: {}}, MaxRedirects: 5})
	for _, raw := range []string{"http://example.com/health", "https://example.com:443/x?y=1", "https://93.184.216.34/"} {
		if _, err := g.ValidateURL(context.Background(), raw); err != nil {
			t.Errorf("ValidateURL(%q): %v", raw, err)
		}
	}
}

func TestValidateURL_BlocksResolvedPrivateAddress(t *testing.T) {
	g := NewGuardWithResolver(
		Policy{AllowedPorts: map[int]struct{}{80: {}, 443: {}}, MaxRedirects: 5},
		staticResolver{ips: []string{"10.0.0.5"}},
	)
	_, err := g.ValidateURL(context.Background(), "http://example.com/health")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("ValidateURL error = %v, want ErrBlocked", err)
	}
}

func TestValidateURL_BlocksIfAnyResolvedAddressIsPrivate(t *testing.T) {
	g := NewGuardWithResolver(
		Policy{AllowedPorts: map[int]struct{}{80: {}, 443: {}}, MaxRedirects: 5},
		staticResolver{ips: []string{"93.184.216.34", "10.0.0.5"}},
	)
	if _, err := g.ValidateURL(context.Background(), "http://example.com/"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("ValidateURL error = %v, want ErrBlocked", err)
	}
}

func TestValidateURL_AcceptsDNSFailure(t *testing.T) {
	g := NewGuardWithResolver(
		Policy{AllowedPorts: map[int]struct{}{80: {}, 443: {}}, MaxRedirects: 5},
		staticResolver{err: &net.DNSError{Err: "no such host", Name: "example.com", IsNotFound: true}},
	)
	if _, err := g.ValidateURL(context.Background(), "http://example.com/health"); err != nil {
		t.Fatalf("ValidateURL with DNS failure = %v, want nil", err)
	}
}

func TestValidateURL_AllowsPrivateWhenConfigured(t *testing.T) {
	g := NewGuardWithResolver(
		Policy{AllowedPorts: map[int]struct{}{80: {}}, MaxRedirects: 5, AllowPrivateNetworks: true},
		staticResolver{ips: []string{"127.0.0.1"}},
	)
	if _, err := g.ValidateURL(context.Background(), "http://example.com/health"); err != nil {
		t.Fatalf("ValidateURL with AllowPrivateNetworks = %v", err)
	}
}

func TestIsPublicIP(t *testing.T) {
	cases := []struct {
		ip     string
		public bool
	}{
		{"8.8.8.8", true},
		{"93.184.216.34", true},
		{"2606:4700:4700::1111", true},
		{"::ffff:8.8.8.8", true},
		{"127.0.0.1", false},
		{"127.255.255.254", false},
		{"10.0.0.1", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"169.254.1.1", false},
		{"100.64.0.1", false},
		{"192.0.0.1", false},
		{"198.18.0.1", false},
		{"198.19.255.255", false},
		{"224.0.0.1", false},
		{"240.0.0.1", false},
		{"255.255.255.255", false},
		{"0.0.0.0", false},
		{"0.1.2.3", false}, // inside 0.0.0.0/8
		{"::1", false},
		{"fc00::1", false},
		{"fe80::1", false},
		{"ff02::1", false},
		{"::", false},
		{"2001:db8::1", false},
		{"::ffff:127.0.0.1", false},
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("ParseIP(%q) = nil", tc.ip)
		}
		if got := IsPublicIP(ip); got != tc.public {
			t.Errorf("IsPublicIP(%q) = %v, want %v", tc.ip, got, tc.public)
		}
	}
	if IsPublicIP(nil) {
		t.Error("IsPublicIP(nil) = true, want false")
	}
}

func TestPolicyFromConfig(t *testing.T) {
	cfg := config.Config{
		ProbeAllowedPorts: []int{80, 8443},
		ProbeMaxRedirects: 3,
		ProbeMaxBodyBytes: 4096,
	}
	p := PolicyFromConfig(cfg)
	if _, ok := p.AllowedPorts[8443]; !ok {
		t.Errorf("AllowedPorts = %v, want 8443", p.AllowedPorts)
	}
	if len(p.AllowedPorts) != 2 {
		t.Errorf("AllowedPorts = %v, want 2 entries", p.AllowedPorts)
	}
	if p.MaxRedirects != 3 {
		t.Errorf("MaxRedirects = %d, want 3", p.MaxRedirects)
	}
	if p.MaxResponseBody != 4096 {
		t.Errorf("MaxResponseBody = %d, want 4096", p.MaxResponseBody)
	}
	if p.ConnectTimeout <= 0 || p.TLSHandshakeTimeout <= 0 {
		t.Errorf("timeouts = %v / %v, want positive", p.ConnectTimeout, p.TLSHandshakeTimeout)
	}
	if p.AllowPrivateNetworks {
		t.Error("PolicyFromConfig enabled AllowPrivateNetworks")
	}
}

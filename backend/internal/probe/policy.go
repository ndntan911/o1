// Package probe implements SSRF-safe HTTP probing.
package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"o1/internal/config"
)

var (
	// ErrBlocked is returned when a target is disallowed by policy. Callers can
	// use errors.Is to distinguish policy rejections from malformed URLs.
	ErrBlocked = errors.New("probe: target blocked")
	// ErrInvalidURL is returned when a URL is malformed or uses a disallowed
	// scheme, userinfo, or fragment.
	ErrInvalidURL = errors.New("probe: invalid url")

	errTooManyRedirects = errors.New("probe: too many redirects")
)

// Policy controls which targets a Guard permits.
type Policy struct {
	AllowedPorts         map[int]struct{}
	MaxRedirects         int
	MaxResponseBody      int64
	ConnectTimeout       time.Duration
	TLSHandshakeTimeout  time.Duration
	AllowPrivateNetworks bool
}

// Resolver is the subset of *net.Resolver used by the guard. Tests supply a
// deterministic implementation.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Guard validates URLs and dial addresses against a Policy.
type Guard struct {
	policy   Policy
	resolver Resolver
}

// PolicyFromConfig builds a production Policy. AllowPrivateNetworks is never
// enabled from configuration.
func PolicyFromConfig(cfg config.Config) Policy {
	ports := make(map[int]struct{}, len(cfg.ProbeAllowedPorts))
	for _, port := range cfg.ProbeAllowedPorts {
		ports[port] = struct{}{}
	}
	return Policy{
		AllowedPorts:        ports,
		MaxRedirects:        cfg.ProbeMaxRedirects,
		MaxResponseBody:     cfg.ProbeMaxBodyBytes,
		ConnectTimeout:      5 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
	}
}

// NewGuard returns a Guard that resolves names with the system resolver.
func NewGuard(p Policy) *Guard {
	return NewGuardWithResolver(p, net.DefaultResolver)
}

// NewGuardWithResolver returns a Guard that resolves names with r. A nil
// resolver uses the system resolver. An empty AllowedPorts falls back to the
// default 80 and 443.
func NewGuardWithResolver(p Policy, r Resolver) *Guard {
	ports := make(map[int]struct{}, len(p.AllowedPorts))
	for port := range p.AllowedPorts {
		ports[port] = struct{}{}
	}
	if len(ports) == 0 {
		ports[80] = struct{}{}
		ports[443] = struct{}{}
	}
	p.AllowedPorts = ports
	if r == nil {
		r = net.DefaultResolver
	}
	return &Guard{policy: p, resolver: r}
}

// ValidateURL parses raw and checks it against the policy. A DNS failure at
// validation time is accepted: the probe dialer still classifies every
// resolved address before connecting.
func (g *Guard) ValidateURL(ctx context.Context, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed url", ErrInvalidURL)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("%w: unsupported scheme", ErrInvalidURL)
	}
	if u.Opaque != "" {
		return nil, fmt.Errorf("%w: opaque url", ErrInvalidURL)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: userinfo is not allowed", ErrInvalidURL)
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return nil, fmt.Errorf("%w: fragment is not allowed", ErrInvalidURL)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("%w: host is required", ErrInvalidURL)
	}
	if len(host) > 253 {
		return nil, fmt.Errorf("%w: host is too long", ErrInvalidURL)
	}
	if strings.Contains(host, "%") {
		return nil, fmt.Errorf("%w: zone identifiers are not allowed", ErrInvalidURL)
	}
	port, err := effectivePort(u)
	if err != nil {
		return nil, err
	}
	if !g.portAllowed(port) {
		return nil, fmt.Errorf("%w: port is not allowed", ErrBlocked)
	}

	// Private targets are still probed when the test escape hatch is set.
	if g.policy.AllowPrivateNetworks {
		return u, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if !IsPublicIP(ip) {
			return nil, fmt.Errorf("%w: address is not public", ErrBlocked)
		}
		return u, nil
	}
	addrs, err := g.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		// Transient DNS failure is not a URL validation failure. The probe
		// records the DNS error when it dials.
		return u, nil
	}
	for _, addr := range addrs {
		if !IsPublicIP(addr.IP) {
			return nil, fmt.Errorf("%w: address is not public", ErrBlocked)
		}
	}
	return u, nil
}

// checkRedirect enforces the redirect policy: a hop limit, full URL
// revalidation, and cross-host header stripping.
func (g *Guard) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > g.policy.MaxRedirects {
		return fmt.Errorf("%w: limit is %d", errTooManyRedirects, g.policy.MaxRedirects)
	}
	if _, err := g.ValidateURL(req.Context(), req.URL.String()); err != nil {
		return err
	}
	if len(via) == 0 {
		return nil
	}
	previous := via[len(via)-1]
	if strings.EqualFold(previous.URL.Hostname(), req.URL.Hostname()) {
		req.Header.Del("Host")
		return nil
	}

	// Cross-host redirect: keep only the headers the prober itself controls.
	keep := map[string]struct{}{
		"User-Agent":      {},
		"Accept":          {},
		"Accept-Encoding": {},
	}
	for name := range req.Header {
		if _, ok := keep[http.CanonicalHeaderKey(name)]; !ok {
			delete(req.Header, name)
		}
	}
	req.Header.Del("Host")
	return nil
}

// dialContext resolves and classifies the destination on every connection
// attempt, then dials the numeric address that passed the check. This defeats
// DNS rebinding between validation and connect.
func (g *Guard) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, portRaw, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid dial address", ErrBlocked)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || (!g.policy.AllowPrivateNetworks && !g.portAllowed(port)) {
		return nil, fmt.Errorf("%w: port is not allowed", ErrBlocked)
	}
	ips, err := g.resolveForDial(ctx, host)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: g.connectTimeout()}
	var lastErr error
	for _, ip := range ips {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), portRaw))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		return nil, fmt.Errorf("%w: no dialable address", ErrBlocked)
	}
	return nil, lastErr
}

func (g *Guard) resolveForDial(ctx context.Context, host string) ([]net.IP, error) {
	if strings.Contains(host, "%") {
		return nil, fmt.Errorf("%w: zone identifiers are not allowed", ErrBlocked)
	}
	if ip := net.ParseIP(host); ip != nil {
		if !g.policy.AllowPrivateNetworks && !IsPublicIP(ip) {
			return nil, fmt.Errorf("%w: address is not public", ErrBlocked)
		}
		return []net.IP{ip}, nil
	}
	addrs, err := g.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if g.policy.AllowPrivateNetworks || IsPublicIP(addr.IP) {
			ips = append(ips, addr.IP)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: no public addresses", ErrBlocked)
	}
	return ips, nil
}

func (g *Guard) portAllowed(port int) bool {
	_, ok := g.policy.AllowedPorts[port]
	return ok
}

func (g *Guard) connectTimeout() time.Duration {
	if g.policy.ConnectTimeout > 0 {
		return g.policy.ConnectTimeout
	}
	return 5 * time.Second
}

func (g *Guard) tlsHandshakeTimeout() time.Duration {
	if g.policy.TLSHandshakeTimeout > 0 {
		return g.policy.TLSHandshakeTimeout
	}
	return 5 * time.Second
}

func effectivePort(u *url.URL) (int, error) {
	if raw := u.Port(); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return 0, fmt.Errorf("%w: invalid port", ErrInvalidURL)
		}
		return port, nil
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return 80, nil
	case "https":
		return 443, nil
	default:
		return 0, fmt.Errorf("%w: unsupported scheme", ErrInvalidURL)
	}
}

var blockedCIDRs = mustParseCIDRs(
	"0.0.0.0/8",
	"100.64.0.0/10",
	"192.0.0.0/24",
	"198.18.0.0/15",
	"240.0.0.0/4",
	"255.255.255.255/32",
	"fc00::/7",
	"fe80::/10",
	"ff00::/8",
	"::/128",
	"2001:db8::/32",
)

func mustParseCIDRs(raw ...string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(raw))
	for _, cidr := range raw {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("probe: invalid CIDR %q: %v", cidr, err))
		}
		nets = append(nets, network)
	}
	return nets
}

// IsPublicIP reports whether ip is a routable public address under the SSRF
// policy. IPv4-mapped IPv6 addresses are unwrapped before classification.
func IsPublicIP(ip net.IP) bool {
	if ip == nil || ip.To16() == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	for _, network := range blockedCIDRs {
		if network.Contains(ip) {
			return false
		}
	}
	return true
}

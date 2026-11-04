package probe

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	probeUserAgent               = "o1-probe/1.0"
	defaultRequestTimeout        = 10 * time.Second
	defaultMaxResponseBody       = 1 << 20
	maxBodyBytes                 = 16 * 1024
	maxHeaderCount               = 20
	maxHeaderNameLength          = 64
	maxHeaderValueLength         = 1024
	maxHeaderTotalBytes          = 8 * 1024
	maxContentTypeLength         = 200
	maxErrorMessageLength        = 256
	maxLatencyMillis       int64 = 1<<31 - 1
)

var blockedHeaderNames = map[string]struct{}{
	"host":              {},
	"content-length":    {},
	"connection":        {},
	"transfer-encoding": {},
	"proxy-connection":  {},
	"upgrade":           {},
	"te":                {},
}

// Request describes one probe.
type Request struct {
	URL         string
	Method      string
	Headers     map[string]string
	Body        []byte
	ContentType string
	Expected    ExpectedStatus
	Timeout     time.Duration
}

// Result is the outcome of one probe. ErrorClass is empty on success.
type Result struct {
	Up           bool
	StatusCode   int
	LatencyMS    int
	ErrorClass   string
	ErrorMessage string
}

// Prober executes HTTP probes through a Guard.
type Prober struct {
	client  *http.Client
	maxBody int64
}

// NewProber builds a Prober with a single shared transport. Proxies are
// disabled and keep-alives are off so every probe uses a fresh, guarded
// connection.
func NewProber(g *Guard) *Prober {
	if g == nil {
		g = NewGuard(Policy{})
	}
	maxBody := g.policy.MaxResponseBody
	if maxBody <= 0 {
		maxBody = defaultMaxResponseBody
	}
	transport := &http.Transport{
		Proxy:               nil,
		DisableKeepAlives:   true,
		DialContext:         g.dialContext,
		TLSHandshakeTimeout: g.tlsHandshakeTimeout(),
		// Response-header timeouts are enforced per request by the context
		// deadline in Probe, so the shared transport needs no static value.
	}
	return &Prober{
		client: &http.Client{
			Transport:     transport,
			CheckRedirect: g.checkRedirect,
		},
		maxBody: maxBody,
	}
}

// Probe executes req with an SSRF-safe dialer and redirect policy, returning a
// classified result. Probe failures are results, not errors. The caller is
// responsible for validating the URL and header/body limits at write time;
// dial-time and redirect-time checks still enforce address policy.
func (p *Prober) Probe(ctx context.Context, req Request) Result {
	start := time.Now()
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	out, err := http.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		return Result{LatencyMS: elapsedMillis(start), ErrorClass: "connection", ErrorMessage: "invalid request"}
	}
	for name, value := range req.Headers {
		if isBlockedHeader(name) {
			continue
		}
		out.Header.Set(name, value)
	}
	if req.ContentType != "" {
		out.Header.Set("Content-Type", req.ContentType)
	}
	out.Header.Set("User-Agent", probeUserAgent)

	resp, err := p.client.Do(out)
	latency := elapsedMillis(start)
	if err != nil {
		class, message := classifyError(err)
		return Result{LatencyMS: latency, ErrorClass: class, ErrorMessage: message}
	}
	defer func() { _ = resp.Body.Close() }()

	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, p.maxBody)); err != nil {
		class, message := classifyError(err)
		if class == "" {
			class = "connection"
		}
		if message == "" {
			message = "response body read failed"
		}
		return Result{
			StatusCode:   resp.StatusCode,
			LatencyMS:    latency,
			ErrorClass:   class,
			ErrorMessage: message,
		}
	}

	if !req.Expected.Matches(resp.StatusCode) {
		return Result{
			StatusCode:   resp.StatusCode,
			LatencyMS:    latency,
			ErrorClass:   "status",
			ErrorMessage: fmt.Sprintf("unexpected status code %d", resp.StatusCode),
		}
	}
	return Result{Up: true, StatusCode: resp.StatusCode, LatencyMS: latency}
}

// ValidateHeaders enforces the header count, name, value, and total-size
// limits, and rejects hop-by-hop headers the prober must control itself.
func ValidateHeaders(headers map[string]string) error {
	if len(headers) > maxHeaderCount {
		return fmt.Errorf("probe: too many headers (maximum %d)", maxHeaderCount)
	}
	total := 0
	for name, value := range headers {
		if name == "" {
			return fmt.Errorf("probe: header name must not be empty")
		}
		if len(name) > maxHeaderNameLength {
			return fmt.Errorf("probe: header name is too long (maximum %d)", maxHeaderNameLength)
		}
		if !isHeaderToken(name) {
			return fmt.Errorf("probe: invalid header name")
		}
		if isBlockedHeader(name) {
			return fmt.Errorf("probe: header %q is not allowed", name)
		}
		if len(value) > maxHeaderValueLength {
			return fmt.Errorf("probe: header value is too long (maximum %d)", maxHeaderValueLength)
		}
		if hasInvalidHeaderValue(value) {
			return fmt.Errorf("probe: header value contains invalid characters")
		}
		total += len(name) + len(value)
	}
	if total > maxHeaderTotalBytes {
		return fmt.Errorf("probe: headers exceed %d bytes", maxHeaderTotalBytes)
	}
	return nil
}

// ValidateBody enforces the body size limit, rejects bodies on GET/HEAD, and
// allows Content-Type only when a body is present.
func ValidateBody(method string, body *string, contentType *string) error {
	bodyPresent := body != nil && *body != ""
	if bodyPresent && len(*body) > maxBodyBytes {
		return fmt.Errorf("probe: body exceeds %d bytes", maxBodyBytes)
	}
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead:
		if bodyPresent {
			return fmt.Errorf("probe: %s requests must not have a body", strings.ToUpper(strings.TrimSpace(method)))
		}
	}
	if contentType != nil {
		if len(*contentType) > maxContentTypeLength {
			return fmt.Errorf("probe: content type exceeds %d characters", maxContentTypeLength)
		}
		if strings.ContainsAny(*contentType, "\r\n") {
			return fmt.Errorf("probe: content type contains invalid characters")
		}
		if !bodyPresent {
			return fmt.Errorf("probe: content type requires a body")
		}
	}
	return nil
}

func isBlockedHeader(name string) bool {
	_, blocked := blockedHeaderNames[strings.ToLower(name)]
	return blocked
}

func isHeaderToken(name string) bool {
	const punctuation = "!#$%&'*+-.^_`|~"
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case strings.ContainsRune(punctuation, rune(c)):
		default:
			return false
		}
	}
	return true
}

func hasInvalidHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '\r' || c == '\n' || c == 0x7f {
			return true
		}
		if c < 0x20 && c != '\t' {
			return true
		}
	}
	return false
}

func classifyError(err error) (string, string) {
	switch {
	case errors.Is(err, ErrBlocked):
		return "ssrf_blocked", "target address is blocked"
	case errors.Is(err, errTooManyRedirects):
		return "redirect", "too many redirects"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout", "request timed out"
	}

	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	var certificateInvalid x509.CertificateInvalidError
	var recordHeaderError tls.RecordHeaderError
	var certificateVerification *tls.CertificateVerificationError
	if errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostnameError) ||
		errors.As(err, &certificateInvalid) ||
		errors.As(err, &recordHeaderError) ||
		errors.As(err, &certificateVerification) {
		return "tls", "tls verification failed"
	}

	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return "dns", sanitizeErrorMessage(err)
	}

	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return "timeout", "request timed out"
	}
	return "connection", sanitizeErrorMessage(err)
}

// sanitizeErrorMessage unwraps URL-bearing errors so query strings can never
// reach logs or stored results, strips control characters, and truncates.
func sanitizeErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	for {
		switch typed := err.(type) {
		case *url.Error:
			if typed.Err == nil {
				return ""
			}
			err = typed.Err
		case *net.OpError:
			if typed.Err == nil {
				return ""
			}
			err = typed.Err
		default:
			goto done
		}
	}
done:
	message := strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		default:
			return r
		}
	}, err.Error())
	if len(message) > maxErrorMessageLength {
		message = message[:maxErrorMessageLength]
	}
	return message
}

func elapsedMillis(start time.Time) int {
	millis := time.Since(start).Milliseconds()
	if millis < 0 {
		return 0
	}
	if millis > maxLatencyMillis {
		return int(maxLatencyMillis)
	}
	return int(millis)
}

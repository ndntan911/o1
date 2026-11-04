package monitors

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"o1/internal/probe"
)

const (
	maxNameLen = 100
	maxURLLen  = 2048

	defaultMinInterval = 60 * time.Second
	defaultMaxInterval = 24 * time.Hour

	minTimeoutSeconds = 1
	maxTimeoutSeconds = 30

	minThreshold = 1
	maxThreshold = 5
)

var allowedMethods = map[string]struct{}{
	"GET":    {},
	"HEAD":   {},
	"POST":   {},
	"PUT":    {},
	"PATCH":  {},
	"DELETE": {},
}

// validate normalizes and checks in against the production defaults. It is
// the package-level entry point used by unit tests; the service uses
// validateContext with the configured interval bounds.
func validate(in Input, guard *probe.Guard) (Input, error) {
	return validateContext(context.Background(), in, guard, defaultMinInterval, defaultMaxInterval)
}

// validateContext normalizes in and returns a *ValidationError listing every
// invalid field. The returned Input is only meaningful when err is nil.
func validateContext(ctx context.Context, in Input, guard *probe.Guard, minInterval, maxInterval time.Duration) (Input, error) {
	if minInterval <= 0 {
		minInterval = defaultMinInterval
	}
	if maxInterval <= 0 {
		maxInterval = defaultMaxInterval
	}

	fields := make(map[string]string)
	var cause error

	in.Name = strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > maxNameLen {
		fields["name"] = fmt.Sprintf("name must be between 1 and %d characters", maxNameLen)
	}

	in.URL = strings.TrimSpace(in.URL)
	switch {
	case in.URL == "":
		fields["url"] = "url is required"
	case utf8.RuneCountInString(in.URL) > maxURLLen:
		fields["url"] = fmt.Sprintf("url must be at most %d characters", maxURLLen)
	default:
		if _, err := guard.ValidateURL(ctx, in.URL); err != nil {
			cause = err
			if errors.Is(err, probe.ErrBlocked) {
				fields["url"] = "url is not allowed"
			} else {
				fields["url"] = "url is invalid"
			}
		}
	}

	in.Method = strings.ToUpper(strings.TrimSpace(in.Method))
	if _, ok := allowedMethods[in.Method]; !ok {
		fields["method"] = "method must be one of GET, HEAD, POST, PUT, PATCH, DELETE"
	}

	if in.ContentType != nil {
		trimmed := strings.TrimSpace(*in.ContentType)
		if trimmed == "" {
			in.ContentType = nil
		} else {
			in.ContentType = &trimmed
		}
	}

	// An empty body is the same as no body.
	if in.Body != nil && *in.Body == "" {
		in.Body = nil
	}

	if err := probe.ValidateHeaders(in.Headers); err != nil {
		fields["headers"] = strings.TrimPrefix(err.Error(), "probe: ")
	}

	if err := probe.ValidateBody(in.Method, in.Body, in.ContentType); err != nil {
		field := "body"
		if strings.HasPrefix(err.Error(), "probe: content type") {
			field = "content_type"
		}
		fields[field] = strings.TrimPrefix(err.Error(), "probe: ")
	}

	in.ExpectedStatus = strings.TrimSpace(in.ExpectedStatus)
	if in.ExpectedStatus == "" {
		in.ExpectedStatus = "2xx"
	}
	if status, err := probe.ParseExpectedStatus(in.ExpectedStatus); err != nil {
		fields["expected_status"] = strings.TrimPrefix(err.Error(), "probe: ")
	} else {
		in.ExpectedStatus = status.String()
	}

	interval := time.Duration(in.IntervalSeconds) * time.Second
	if in.IntervalSeconds <= 0 || interval < minInterval || interval > maxInterval {
		fields["interval_seconds"] = fmt.Sprintf(
			"interval must be between %d and %d seconds",
			int(minInterval/time.Second), int(maxInterval/time.Second),
		)
	}

	if in.TimeoutSeconds < minTimeoutSeconds || in.TimeoutSeconds > maxTimeoutSeconds {
		fields["timeout_seconds"] = fmt.Sprintf(
			"timeout must be between %d and %d seconds", minTimeoutSeconds, maxTimeoutSeconds,
		)
	}

	if in.ConfirmationThreshold < minThreshold || in.ConfirmationThreshold > maxThreshold {
		fields["confirmation_threshold"] = fmt.Sprintf(
			"confirmation threshold must be between %d and %d", minThreshold, maxThreshold,
		)
	}

	if in.RecoveryThreshold < minThreshold || in.RecoveryThreshold > maxThreshold {
		fields["recovery_threshold"] = fmt.Sprintf(
			"recovery threshold must be between %d and %d", minThreshold, maxThreshold,
		)
	}

	if len(fields) > 0 {
		return Input{}, &ValidationError{Fields: fields, cause: cause}
	}
	return in, nil
}

// RedactURL removes the query string and fragment from raw so it is safe to
// log or store in a security event. Malformed URLs are truncated at the first
// query or fragment delimiter.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return redactFallback(raw)
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.RawFragment = ""
	u.ForceQuery = false
	return u.String()
}

// redactURL is the unexported spelling used inside the package.
func redactURL(raw string) string {
	return RedactURL(raw)
}

// redactFallback strips a query string or fragment from an unparseable value.
func redactFallback(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		return raw[:i]
	}
	return raw
}

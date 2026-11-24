package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// HTTPError is a non-2xx answer from a provider. The status code drives the
// retry decision; Body is a bounded snippet kept for logs.
type HTTPError struct {
	StatusCode int
	Body       string
}

// Error keeps the text the transport returned before the typed error existed,
// so existing logs and tests read the same.
func (e *HTTPError) Error() string {
	if e == nil {
		return "ai: provider returned an error"
	}
	return fmt.Sprintf("ai: provider returned %d: %s", e.StatusCode, e.Body)
}

// providerHTTPError drains a bounded suffix of the response body.
func providerHTTPError(resp *http.Response) *HTTPError {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	return &HTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(snippet))}
}

// IsTransient reports whether retrying the same request can succeed. Network
// failures, timeouts, 408, 425, 429 and 5xx are transient; other 4xx answers
// and a cancelled context are not.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	// A cancelled request is often wrapped in *url.Error, which satisfies
	// net.Error; decide it before the generic network branch.
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr != nil {
		switch httpErr.StatusCode {
		case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
			return true
		}
		return httpErr.StatusCode >= 500 && httpErr.StatusCode <= 599
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return false
}

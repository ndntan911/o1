package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newResendTestSender(t *testing.T, url string) *ResendSender {
	t.Helper()
	return NewResendSender("test-key", "o1 <noreply@example.com>", "", url, 1000, 10)
}

func TestResendSender_SendsRequest(t *testing.T) {
	type captured struct {
		method string
		path   string
		header http.Header
		body   map[string]any
	}
	got := make(chan captured, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		got <- captured{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := NewResendSender("test-key", "o1 <noreply@example.com>", "support@example.com", srv.URL, 1000, 10)
	msg := Message{
		To:             "user@example.com",
		Subject:        "Verify your email",
		Text:           "line one",
		HTML:           "<p>line one</p>",
		IdempotencyKey: "delivery-123",
	}
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	req := <-got
	if req.method != http.MethodPost {
		t.Errorf("method = %q, want POST", req.method)
	}
	if req.path != "/emails" {
		t.Errorf("path = %q, want /emails", req.path)
	}
	if auth := req.header.Get("Authorization"); auth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", auth)
	}
	if ct := req.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if ua := req.header.Get("User-Agent"); ua != "o1-email/1.0" {
		t.Errorf("User-Agent = %q, want o1-email/1.0", ua)
	}
	if key := req.header.Get("Idempotency-Key"); key != "delivery-123" {
		t.Errorf("Idempotency-Key = %q, want delivery-123", key)
	}

	want := map[string]any{
		"from":     "o1 <noreply@example.com>",
		"subject":  "Verify your email",
		"text":     "line one",
		"html":     "<p>line one</p>",
		"reply_to": "support@example.com",
	}
	for field, value := range want {
		if req.body[field] != value {
			t.Errorf("body[%q] = %v, want %q", field, req.body[field], value)
		}
	}
	to, ok := req.body["to"].([]any)
	if !ok || len(to) != 1 || to[0] != "user@example.com" {
		t.Errorf("body[to] = %#v, want [user@example.com]", req.body["to"])
	}
}

func TestResendSender_OmitsOptionalFields(t *testing.T) {
	type captured struct {
		header http.Header
		body   map[string]any
	}
	got := make(chan captured, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		got <- captured{header: r.Header.Clone(), body: body}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	sender := newResendTestSender(t, srv.URL)
	if err := sender.Send(context.Background(), Message{
		To:      "user@example.com",
		Subject: "hello",
		Text:    "x",
		HTML:    "<p>x</p>",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	req := <-got
	if _, ok := req.body["reply_to"]; ok {
		t.Errorf("reply_to present in body: %#v", req.body["reply_to"])
	}
	if key := req.header.Get("Idempotency-Key"); key != "" {
		t.Errorf("Idempotency-Key = %q, want omitted", key)
	}
}

func TestResendSender_SuccessStatuses(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusNoContent} {
		t.Run(fmt.Sprintf("%d", status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer srv.Close()

			sender := newResendTestSender(t, srv.URL)
			if err := sender.Send(context.Background(), Message{
				To:      "user@example.com",
				Subject: "hello",
				Text:    "x",
				HTML:    "<p>x</p>",
			}); err != nil {
				t.Fatalf("Send with HTTP %d: %v", status, err)
			}
		})
	}
}

func TestResendSender_ClassifiesStatusCodes(t *testing.T) {
	permanent := []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity}
	transient := []int{http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable}

	for _, status := range permanent {
		t.Run(fmt.Sprintf("%d", status), func(t *testing.T) {
			assertResendStatus(t, status, true)
		})
	}
	for _, status := range transient {
		t.Run(fmt.Sprintf("%d", status), func(t *testing.T) {
			assertResendStatus(t, status, false)
		})
	}
}

func assertResendStatus(t *testing.T, status int, wantPermanent bool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"message":"provider-secret-body"}`)
	}))
	defer srv.Close()

	sender := newResendTestSender(t, srv.URL)
	err := sender.Send(context.Background(), Message{
		To:      "user@example.com",
		Subject: "hello",
		Text:    "x",
		HTML:    "<p>x</p>",
	})
	if err == nil {
		t.Fatalf("Send with HTTP %d returned nil", status)
	}
	if got := IsPermanent(err); got != wantPermanent {
		t.Fatalf("IsPermanent(HTTP %d) = %v, want %v (err %v)", status, got, wantPermanent, err)
	}
	var deliveryErr *DeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("error %T is not a *DeliveryError", err)
	}
	if want := fmt.Sprintf("resend returned HTTP %d", status); deliveryErr.Message != want {
		t.Errorf("DeliveryError.Message = %q, want %q", deliveryErr.Message, want)
	}
	for _, banned := range []string{"test-key", "user@example.com", "provider-secret-body", "Authorization"} {
		if strings.Contains(err.Error(), banned) {
			t.Errorf("error leaks %q: %v", banned, err)
		}
	}
}

func TestResendSender_DoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/emails", http.StatusFound)
	}))
	defer srv.Close()

	sender := newResendTestSender(t, srv.URL)
	err := sender.Send(context.Background(), Message{
		To:      "user@example.com",
		Subject: "hello",
		Text:    "x",
		HTML:    "<p>x</p>",
	})
	if err == nil {
		t.Fatal("Send followed a redirect")
	}
	if !IsPermanent(err) {
		t.Fatalf("redirect error = %v, want permanent", err)
	}
	if followed.Load() != 0 {
		t.Fatalf("redirect target was contacted %d times", followed.Load())
	}
}

func TestResendSender_TimeoutIsTransient(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer srv.Close()

	sender := newResendTestSender(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := sender.Send(ctx, Message{
		To:      "user@example.com",
		Subject: "hello",
		Text:    "x",
		HTML:    "<p>x</p>",
	})
	close(release)
	if err == nil {
		t.Fatal("Send succeeded despite the timeout")
	}
	if IsPermanent(err) {
		t.Fatalf("timeout classified permanent: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the request")
	}
}

func TestResendSender_DrainsLargeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("a"), 128<<10))
	}))
	defer srv.Close()

	sender := newResendTestSender(t, srv.URL)
	done := make(chan error, 1)
	go func() {
		done <- sender.Send(context.Background(), Message{
			To:      "user@example.com",
			Subject: "hello",
			Text:    "x",
			HTML:    "<p>x</p>",
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send hung on a large response body")
	}
}

func TestResendSender_RejectsInvalidMessagesBeforeIO(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := newResendTestSender(t, srv.URL)

	tests := map[string]Message{
		"empty recipient":      {Subject: "hello", Text: "x", HTML: "<p>x</p>"},
		"invalid recipient":    {To: "not-an-email", Subject: "hello", Text: "x", HTML: "<p>x</p>"},
		"recipient CRLF":       {To: "user@example.com\r\nBcc: evil@example.com", Subject: "hello", Text: "x", HTML: "<p>x</p>"},
		"subject CRLF":         {To: "user@example.com", Subject: "hello\r\nBcc: evil@example.com", Text: "x", HTML: "<p>x</p>"},
		"subject too long":     {To: "user@example.com", Subject: strings.Repeat("a", maxSubjectLength+1), Text: "x", HTML: "<p>x</p>"},
		"idempotency key CRLF": {To: "user@example.com", Subject: "hello", Text: "x", HTML: "<p>x</p>", IdempotencyKey: "a\r\nb"},
	}
	for name, msg := range tests {
		t.Run(name, func(t *testing.T) {
			err := sender.Send(context.Background(), msg)
			if err == nil {
				t.Fatalf("Send accepted invalid message %+v", msg)
			}
			if !IsPermanent(err) {
				t.Errorf("validation error = %v, want permanent", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("server was contacted %d times before validation", calls.Load())
	}
}

func TestResendSender_RateLimitSpacesSends(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := NewResendSender("test-key", "o1@example.com", "", srv.URL, 1, 1)
	msg := Message{To: "user@example.com", Subject: "hello", Text: "x", HTML: "<p>x</p>"}
	for i := 0; i < 2; i++ {
		if err := sender.Send(context.Background(), msg); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(times) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(times))
	}
	if gap := times[1].Sub(times[0]); gap < 800*time.Millisecond {
		t.Fatalf("sends spaced by %v, want at least ~1s under rps=1", gap)
	}
}

func TestResendSender_RateLimitHonorsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// burst=1 lets the first send through instantly; rps=0.01 makes the
	// second wait far longer than the test context allows.
	sender := NewResendSender("test-key", "o1@example.com", "", srv.URL, 0.01, 1)
	msg := Message{To: "user@example.com", Subject: "hello", Text: "x", HTML: "<p>x</p>"}
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Fatalf("first Send: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := sender.Send(ctx, msg)
	if err == nil {
		t.Fatal("second Send ignored the rate limiter")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("second Send took %v, want context-aware wait", elapsed)
	}
	if IsPermanent(err) {
		t.Fatalf("rate-limit wait error classified permanent: %v", err)
	}
}

func TestDeliveryError(t *testing.T) {
	cause := errors.New("boom")
	err := &DeliveryError{Message: "resend request failed", Cause: cause}
	if got := err.Error(); got != "resend request failed" {
		t.Errorf("Error() = %q, want only the sanitized message", got)
	}
	if !errors.Is(err, cause) {
		t.Error("Unwrap did not expose the cause")
	}
	if IsPermanent(err) {
		t.Error("zero-value DeliveryError should be transient")
	}
	if !IsPermanent(&DeliveryError{Message: "resend returned HTTP 422", Permanent: true}) {
		t.Error("permanent DeliveryError not detected")
	}
	if IsPermanent(cause) {
		t.Error("plain error classified permanent")
	}
}

func TestMailpitSender_IgnoresIdempotencyKey(t *testing.T) {
	sender := NewMailpitSender("localhost:1025", "o1@localhost")
	msg := Message{
		To:             "user@example.com",
		Subject:        "Verify your email",
		Text:           "line one",
		HTML:           "<p>line one</p>",
		IdempotencyKey: "delivery-123",
	}
	raw := string(sender.render(msg))
	if strings.Contains(raw, "delivery-123") {
		t.Fatalf("idempotency key leaked into the rendered message:\n%s", raw)
	}
}

package email

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// scriptedSender returns one scripted error per Send call, repeating the last
// error once the script is exhausted, and records every delivered message.
type scriptedSender struct {
	mu     sync.Mutex
	calls  []Message
	errs   []error
	onSend func()
}

func (s *scriptedSender) Send(_ context.Context, msg Message) error {
	s.mu.Lock()
	index := len(s.calls)
	s.calls = append(s.calls, msg)
	var err error
	switch {
	case index < len(s.errs):
		err = s.errs[index]
	case len(s.errs) > 0:
		err = s.errs[len(s.errs)-1]
	}
	onSend := s.onSend
	s.mu.Unlock()

	if onSend != nil {
		onSend()
	}
	return err
}

func (s *scriptedSender) messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.calls...)
}

func transientError() error {
	return &DeliveryError{Message: "resend returned HTTP 500"}
}

func permanentError() error {
	return &DeliveryError{Message: "resend returned HTTP 422", Permanent: true}
}

func testMessage() Message {
	return Message{
		To:             "user@example.com",
		Subject:        "Verify your email",
		Text:           "line one",
		HTML:           "<p>line one</p>",
		IdempotencyKey: "token-uuid",
	}
}

func TestRetrySender_RetriesTransientThreeTimes(t *testing.T) {
	inner := &scriptedSender{errs: []error{transientError()}}
	sender := NewRetrySender(inner, 3, time.Millisecond)

	err := sender.Send(context.Background(), testMessage())
	if err == nil {
		t.Fatal("Send returned nil for a persistently failing sender")
	}
	if got := len(inner.messages()); got != 3 {
		t.Fatalf("inner sender called %d times, want 3", got)
	}
	var deliveryErr *DeliveryError
	if !errors.As(err, &deliveryErr) || deliveryErr.Message != "resend returned HTTP 500" {
		t.Fatalf("err = %v, want the last transient error", err)
	}
}

func TestRetrySender_DoesNotRetryPermanent(t *testing.T) {
	inner := &scriptedSender{errs: []error{permanentError()}}
	sender := NewRetrySender(inner, 3, time.Millisecond)

	err := sender.Send(context.Background(), testMessage())
	if err == nil {
		t.Fatal("Send returned nil for a permanent error")
	}
	if !IsPermanent(err) {
		t.Fatalf("err = %v, want the permanent error", err)
	}
	if got := len(inner.messages()); got != 1 {
		t.Fatalf("inner sender called %d times, want 1", got)
	}
}

func TestRetrySender_SucceedsOnSecondAttempt(t *testing.T) {
	inner := &scriptedSender{errs: []error{transientError(), nil}}
	sender := NewRetrySender(inner, 3, time.Millisecond)

	if err := sender.Send(context.Background(), testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := len(inner.messages()); got != 2 {
		t.Fatalf("inner sender called %d times, want 2", got)
	}
}

func TestRetrySender_StopsWhenContextCanceledDuringBackoff(t *testing.T) {
	inner := &scriptedSender{errs: []error{transientError()}}
	sender := NewRetrySender(inner, 3, 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := sender.Send(ctx, testMessage())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Send returned nil after context cancellation")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > time.Second {
		t.Fatalf("Send took %v, want it to stop when the context expired", elapsed)
	}
	if got := len(inner.messages()); got != 1 {
		t.Fatalf("inner sender called %d times, want 1", got)
	}
}

func TestRetrySender_PreservesMessage(t *testing.T) {
	inner := &scriptedSender{errs: []error{transientError(), nil}}
	sender := NewRetrySender(inner, 3, time.Millisecond)

	msg := testMessage()
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	calls := inner.messages()
	if len(calls) != 2 {
		t.Fatalf("inner sender called %d times, want 2", len(calls))
	}
	for i, call := range calls {
		if call != msg {
			t.Fatalf("attempt %d received %+v, want %+v", i+1, call, msg)
		}
	}
	if calls[0].IdempotencyKey != "token-uuid" {
		t.Fatalf("idempotency key = %q, want preserved", calls[0].IdempotencyKey)
	}
}

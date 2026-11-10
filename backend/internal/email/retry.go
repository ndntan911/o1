package email

import (
	"context"
	"time"
)

const (
	// defaultRetryBackoff is the first wait when the caller supplies a
	// non-positive backoff.
	defaultRetryBackoff = 250 * time.Millisecond
	// maxRetryBackoff caps a single wait, however many attempts are configured.
	maxRetryBackoff = 10 * time.Second
)

// RetrySender retries transient delivery failures with context-aware backoff.
// Permanent failures are returned immediately.
type RetrySender struct {
	inner    Sender
	attempts int
	backoff  time.Duration
}

// NewRetrySender wraps inner with at most attempts total sends. backoff is the
// first wait; subsequent waits grow fourfold, so the default 250ms schedule is
// 250ms then 1s for a three-attempt sender.
func NewRetrySender(inner Sender, attempts int, backoff time.Duration) *RetrySender {
	if attempts < 1 {
		attempts = 1
	}
	return &RetrySender{inner: inner, attempts: attempts, backoff: backoff}
}

// Send delivers msg, retrying only failures that are not permanent. Every wait
// respects ctx cancellation and returns ctx.Err() promptly.
func (r *RetrySender) Send(ctx context.Context, msg Message) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if r.inner == nil {
		return &DeliveryError{
			Message:   "email: no sender configured",
			Permanent: true,
		}
	}

	var err error
	for attempt := 0; attempt < r.attempts; attempt++ {
		if attempt > 0 {
			if waitErr := waitBackoff(ctx, r.delay(attempt-1)); waitErr != nil {
				return waitErr
			}
		}

		err = r.inner.Send(ctx, msg)
		if err == nil {
			return nil
		}
		if IsPermanent(err) {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	return err
}

// delay returns the wait before retry number retry (0-based).
func (r *RetrySender) delay(retry int) time.Duration {
	delay := r.backoff
	if delay <= 0 {
		delay = defaultRetryBackoff
	}
	if delay > maxRetryBackoff {
		return maxRetryBackoff
	}
	for i := 0; i < retry; i++ {
		if delay > maxRetryBackoff/4 {
			return maxRetryBackoff
		}
		delay *= 4
	}
	return delay
}

// waitBackoff sleeps for delay unless ctx is done first.
func waitBackoff(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

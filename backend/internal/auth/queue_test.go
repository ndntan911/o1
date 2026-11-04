package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"o1/internal/auth"
	"o1/internal/db/sqlc"
	"o1/internal/email"
	"o1/internal/testutil"
)

// blockingSender signals when a send starts and blocks until released, so a
// test can prove the handler does not wait for email delivery.
type blockingSender struct {
	startOnce   sync.Once
	releaseOnce sync.Once
	started     chan struct{}
	release     chan struct{}

	mu   sync.Mutex
	msgs []email.Message
}

func newBlockingSender() *blockingSender {
	return &blockingSender{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *blockingSender) Send(ctx context.Context, msg email.Message) error {
	b.startOnce.Do(func() { close(b.started) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	b.mu.Lock()
	b.msgs = append(b.msgs, msg)
	b.mu.Unlock()
	return nil
}

func (b *blockingSender) releaseAll() {
	b.releaseOnce.Do(func() { close(b.release) })
}

func (b *blockingSender) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.msgs)
}

func TestQueuePasswordReset_UsesDetachedContext(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	fs := testutil.NewFakeSender()
	svc := auth.NewService(pool, sqlc.New(pool), fs, testutil.TestConfig(t))
	defer svc.Close()
	testutil.CreateVerifiedUser(t, pool, testEmail, testPassword)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.QueuePasswordReset(ctx, testEmail)

	fs.WaitForMessages(t, 1)
	svc.Close()
	if fs.Len() != 1 {
		t.Fatalf("sent = %d, want 1 despite canceled request context", fs.Len())
	}
}

func TestForgotPasswordHandler_ReturnsBeforeAsyncSend(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	sender := newBlockingSender()
	svc := auth.NewService(pool, sqlc.New(pool), sender, testutil.TestConfig(t))
	defer svc.Close()
	testutil.CreateVerifiedUser(t, pool, testEmail, testPassword)
	handler := auth.ForgotPasswordHandler(svc)

	done := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"email":"`+testEmail+`"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler(rec, req)
		done <- rec.Code
	}()

	var status int
	select {
	case status = <-done:
	case <-time.After(2 * time.Second):
		sender.releaseAll()
		t.Fatal("handler blocked on background reset work")
	}
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", status)
	}

	select {
	case <-sender.started:
	case <-time.After(2 * time.Second):
		sender.releaseAll()
		t.Fatal("queued reset never reached the sender")
	}
	if sender.Len() != 0 {
		t.Fatal("sender completed before the handler returned")
	}

	sender.releaseAll()
	svc.Close()
	if sender.Len() != 1 {
		t.Fatalf("sent = %d after release, want 1", sender.Len())
	}
}

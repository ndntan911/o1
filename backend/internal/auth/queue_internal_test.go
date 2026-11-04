package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestForgotPasswordHandler_QueueFullStill204(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	q := newPasswordResetQueue(1, 1, func(context.Context, string) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
	})
	defer q.close()
	defer close(release)

	if !q.enqueue(passwordResetJob{ctx: context.Background(), email: "first@example.com"}) {
		t.Fatal("first enqueue unexpectedly failed")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker never started")
	}
	if !q.enqueue(passwordResetJob{ctx: context.Background(), email: "second@example.com"}) {
		t.Fatal("enqueue into free buffer slot failed")
	}
	if q.enqueue(passwordResetJob{ctx: context.Background(), email: "third@example.com"}) {
		t.Fatal("full queue accepted another job")
	}

	svc := &Service{resetQueue: q}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"email":"fourth@example.com"}`))
	rec := httptest.NewRecorder()
	ForgotPasswordHandler(svc)(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 when the queue is full", rec.Code)
	}
}

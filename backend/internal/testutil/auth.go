package testutil

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/auth"
	"o1/internal/config"
	"o1/internal/db/sqlc"
	"o1/internal/email"
)

// FakeSender records every message it is asked to send, so tests can
// assert on the outbox without a real mail server. It is safe for concurrent
// use by asynchronous senders.
type FakeSender struct {
	mu     sync.Mutex
	msgs   []email.Message
	notify chan struct{}
}

// NewFakeSender returns an empty FakeSender.
func NewFakeSender() *FakeSender {
	return &FakeSender{notify: make(chan struct{}, 1)}
}

// Send appends msg to the recorded outbox and wakes any waiters.
func (f *FakeSender) Send(_ context.Context, msg email.Message) error {
	f.mu.Lock()
	f.msgs = append(f.msgs, msg)
	f.mu.Unlock()

	select {
	case f.notify <- struct{}{}:
	default:
	}
	return nil
}

// Messages returns a snapshot copy of the recorded outbox.
func (f *FakeSender) Messages() []email.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]email.Message, len(f.msgs))
	copy(out, f.msgs)
	return out
}

// Len reports how many messages have been recorded.
func (f *FakeSender) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.msgs)
}

// WaitForMessages blocks until at least n messages have been recorded,
// returning a snapshot. It fails the test after a timeout.
func (f *FakeSender) WaitForMessages(t testing.TB, n int) []email.Message {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	for {
		f.mu.Lock()
		if len(f.msgs) >= n {
			out := make([]email.Message, len(f.msgs))
			copy(out, f.msgs)
			f.mu.Unlock()
			return out
		}
		f.mu.Unlock()

		select {
		case <-f.notify:
		case <-timer.C:
			t.Fatalf("timed out waiting for %d message(s); have %d", n, f.Len())
		}
	}
}

// TestConfig returns a deterministic configuration for tests.
func TestConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Env:            "test",
		AppBaseURL:     "http://localhost:5173",
		AllowedOrigins: []string{"http://localhost:5173"},
		SessionTTL:     720 * time.Hour,
		SecretKey:      make([]byte, 32),
		SMTPAddr:       "localhost:1025",
		EmailFrom:      "no-reply@localhost",
	}
}

// CreateVerifiedUser inserts a user with the given email and password and
// marks its email as verified, returning the user ID.
func CreateVerifiedUser(t *testing.T, pool *pgxpool.Pool, emailAddr, password string) uuid.UUID {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	q := sqlc.New(pool)
	userID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generate user id: %v", err)
	}
	user, err := q.CreateUser(context.Background(), sqlc.CreateUserParams{
		ID:           userID,
		Email:        emailAddr,
		PasswordHash: hash,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := q.MarkEmailVerified(context.Background(), user.ID); err != nil {
		t.Fatalf("mark email verified: %v", err)
	}
	return user.ID
}

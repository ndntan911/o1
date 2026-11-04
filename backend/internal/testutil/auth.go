package testutil

import (
	"context"
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
// assert on the outbox without a real mail server.
type FakeSender struct {
	Msgs []email.Message
}

// NewFakeSender returns an empty FakeSender.
func NewFakeSender() *FakeSender {
	return &FakeSender{}
}

// Send appends msg to the recorded outbox.
func (f *FakeSender) Send(_ context.Context, msg email.Message) error {
	f.Msgs = append(f.Msgs, msg)
	return nil
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
	user, err := q.CreateUser(context.Background(), sqlc.CreateUserParams{
		ID:           uuid.New(),
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

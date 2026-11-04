package auth_test

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"

	"o1/internal/auth"
	"o1/internal/db/sqlc"
	"o1/internal/email"
	"o1/internal/testutil"
)

func TestRegister_SendsVerificationAndVerifies(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	fs := testutil.NewFakeSender()
	svc := auth.NewService(pool, sqlc.New(pool), fs, testutil.TestConfig(t))
	u, err := svc.Register(context.Background(), " User@Example.com ", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "user@example.com" {
		t.Fatalf("email = %q", u.Email)
	}
	if len(fs.Msgs) != 1 {
		t.Fatalf("sent = %d", len(fs.Msgs))
	}
	token := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(fs.Msgs[0].Text)[1]
	if err := svc.VerifyEmail(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if err := svc.VerifyEmail(context.Background(), token); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Fatalf("reuse err = %v, want ErrTokenInvalid", err)
	}
}

func TestRegister_DuplicateEmailReturnsSentinel(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := auth.NewService(pool, sqlc.New(pool), testutil.NewFakeSender(), testutil.TestConfig(t))
	ctx := context.Background()
	if _, err := svc.Register(ctx, "a@example.com", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(ctx, "A@Example.com", "correct horse battery staple"); !errors.Is(err, auth.ErrEmailTaken) {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyEmail_ExpiredToken(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	q := sqlc.New(pool)
	userID := testutil.CreateVerifiedUser(t, pool, "expired@example.com", "correct horse battery staple")
	raw, hash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := q.CreateEmailVerificationToken(context.Background(), sqlc.CreateEmailVerificationTokenParams{
		ID:        uuid.New(),
		UserID:    userID,
		TokenHash: hash,
		ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(pool, q, testutil.NewFakeSender(), testutil.TestConfig(t))
	err = svc.VerifyEmail(context.Background(), raw)
	if !errors.Is(err, auth.ErrTokenExpired) {
		t.Fatalf("err = %v, want ErrTokenExpired", err)
	}
}

func TestVerifyEmail_UnknownToken(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := auth.NewService(pool, sqlc.New(pool), testutil.NewFakeSender(), testutil.TestConfig(t))
	if err := svc.VerifyEmail(context.Background(), "not-a-real-token-long-enough"); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

func TestRegister_Validation(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := auth.NewService(pool, sqlc.New(pool), testutil.NewFakeSender(), testutil.TestConfig(t))
	ctx := context.Background()
	if _, err := svc.Register(ctx, "a@example.com", "short"); !errors.Is(err, auth.ErrValidation) {
		t.Fatalf("password err = %v, want ErrValidation", err)
	}
	if _, err := svc.Register(ctx, "not-an-email", "correct horse battery staple"); !errors.Is(err, auth.ErrValidation) {
		t.Fatalf("email err = %v, want ErrValidation", err)
	}
	if _, err := svc.Register(ctx, "", "correct horse battery staple"); !errors.Is(err, auth.ErrValidation) {
		t.Fatalf("empty email err = %v, want ErrValidation", err)
	}
}

type errorSender struct{ err error }

func (e errorSender) Send(context.Context, email.Message) error { return e.err }

func TestRegister_EmailFailureStillCreatesUser(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := auth.NewService(pool, sqlc.New(pool), errorSender{err: errors.New("smtp down")},
		testutil.TestConfig(t))
	u, err := svc.Register(context.Background(), "outboxfails@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatalf("err = %v, want nil (account still created)", err)
	}
	if u.Email != "outboxfails@example.com" {
		t.Fatalf("email = %q", u.Email)
	}
}

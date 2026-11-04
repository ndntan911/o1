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
	if fs.Len() != 1 {
		t.Fatalf("sent = %d", fs.Len())
	}
	token := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(fs.Messages()[0].Text)[1]
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
	tokenID, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	if err := q.CreateEmailVerificationToken(context.Background(), sqlc.CreateEmailVerificationTokenParams{
		ID:        tokenID,
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

func TestRegister_EmailFailureRetiresVerificationToken(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := auth.NewService(pool, sqlc.New(pool), errorSender{err: errors.New("smtp down")},
		testutil.TestConfig(t))
	u, err := svc.Register(context.Background(), "retired@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	var unused int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM email_verification_tokens WHERE user_id=$1 AND used_at IS NULL", u.ID).
		Scan(&unused); err != nil {
		t.Fatal(err)
	}
	if unused != 0 {
		t.Fatalf("unused verification tokens = %d, want 0 after failed delivery", unused)
	}
}

func TestResendVerification_UnknownOrVerifiedIsSilent(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	fs := testutil.NewFakeSender()
	svc := auth.NewService(pool, sqlc.New(pool), fs, testutil.TestConfig(t))
	ctx := context.Background()

	if err := svc.ResendVerification(ctx, "nobody@example.com"); err != nil {
		t.Fatalf("unknown email err = %v, want nil", err)
	}
	if fs.Len() != 0 {
		t.Fatalf("unknown email sent %d messages, want 0", fs.Len())
	}

	testutil.CreateVerifiedUser(t, pool, "verified@example.com", testPassword)
	if err := svc.ResendVerification(ctx, "Verified@Example.com"); err != nil {
		t.Fatalf("verified email err = %v, want nil", err)
	}
	if fs.Len() != 0 {
		t.Fatalf("verified email sent %d messages, want 0", fs.Len())
	}
}

func TestResendVerification_SendsNewAndInvalidatesOldToken(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	fs := testutil.NewFakeSender()
	svc := auth.NewService(pool, sqlc.New(pool), fs, testutil.TestConfig(t))
	ctx := context.Background()

	if _, err := svc.Register(ctx, "resend@example.com", testPassword); err != nil {
		t.Fatal(err)
	}
	if fs.Len() != 1 {
		t.Fatalf("registration sent %d messages, want 1", fs.Len())
	}
	oldToken := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(fs.Messages()[0].Text)[1]

	if err := svc.ResendVerification(ctx, "Resend@Example.com"); err != nil {
		t.Fatal(err)
	}
	if fs.Len() != 2 {
		t.Fatalf("sent = %d after resend, want 2", fs.Len())
	}
	newToken := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(lastSentText(t, fs))[1]
	if newToken == oldToken {
		t.Fatal("resend reused the previous token")
	}

	if err := svc.VerifyEmail(ctx, oldToken); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Fatalf("old token err = %v, want ErrTokenInvalid", err)
	}
	if err := svc.VerifyEmail(ctx, newToken); err != nil {
		t.Fatalf("new token err = %v, want nil", err)
	}
}

func TestRequestPasswordReset_EmailFailureRetiresToken(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := auth.NewService(pool, sqlc.New(pool), errorSender{err: errors.New("smtp down")},
		testutil.TestConfig(t))
	userID := testutil.CreateVerifiedUser(t, pool, "resetretired@example.com", testPassword)
	if err := svc.RequestPasswordReset(context.Background(), "resetretired@example.com"); err != nil {
		t.Fatal(err)
	}
	var unused int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM password_reset_tokens WHERE user_id=$1 AND used_at IS NULL", userID).
		Scan(&unused); err != nil {
		t.Fatal(err)
	}
	if unused != 0 {
		t.Fatalf("unused reset tokens = %d, want 0 after failed delivery", unused)
	}
}

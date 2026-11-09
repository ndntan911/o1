package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/config"
	"o1/internal/db/sqlc"
	"o1/internal/email"
	"o1/internal/secretbox"
)

// Sentinel errors returned by the service. Handlers map them onto HTTP
// responses; errors.Is callers can branch on them.
var (
	ErrEmailTaken   = errors.New("email already registered")
	ErrTokenInvalid = errors.New("verification token invalid")
	ErrTokenExpired = errors.New("verification token expired")

	// ErrInvalidCredentials is returned for both an unknown email and a
	// wrong password so callers cannot enumerate accounts.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrEmailNotVerified is returned when the password is correct but the
	// account has not completed email verification.
	ErrEmailNotVerified = errors.New("email not verified")
	// ErrUnauthenticated is returned for a missing, unknown, or expired
	// session.
	ErrUnauthenticated = errors.New("unauthenticated")
)

const (
	// verificationTokenTTL is how long a verification token remains usable.
	verificationTokenTTL = 24 * time.Hour
	// tokenCleanupTimeout bounds the best-effort cleanup query after a failed
	// email delivery.
	tokenCleanupTimeout = 5 * time.Second
)

// Service implements registration, login, sessions, and password reset on top
// of the auth database schema and an email sender.
type Service struct {
	pool       *pgxpool.Pool
	q          *sqlc.Queries
	sender     email.Sender
	cfg        config.Config
	resetQueue *passwordResetQueue
	box        *secretbox.Box
}

// NewService wires a Service together.
func NewService(pool *pgxpool.Pool, q *sqlc.Queries, sender email.Sender, cfg config.Config) *Service {
	s := &Service{pool: pool, q: q, sender: sender, cfg: cfg}
	s.resetQueue = newPasswordResetQueue(passwordResetWorkers, passwordResetQueueCapacity, s.runPasswordResetJob)
	if box, err := secretbox.New(cfg.EncryptionKey); err == nil {
		s.box = box
	}
	return s
}

// NormalizeEmail trims surrounding whitespace and lowercases an address,
// producing the canonical form stored in the database.
func NormalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// validateEmail normalizes and syntactically validates an address. It
// rejects display-name forms ("Bob <bob@example.com>") by requiring the
// parsed address to equal the normalized input.
func validateEmail(raw string) (string, error) {
	normalized := NormalizeEmail(raw)
	if normalized == "" {
		return "", fmt.Errorf("%w: email is required", ErrValidation)
	}
	parsed, err := mail.ParseAddress(normalized)
	if err != nil {
		return "", fmt.Errorf("%w: invalid email address", ErrValidation)
	}
	if parsed.Address != normalized {
		return "", fmt.Errorf("%w: invalid email address", ErrValidation)
	}
	return normalized, nil
}

// Register creates a new account: it normalizes and validates the input,
// stores the user and its single-use verification token in one transaction,
// then emails the verification link. A delivery failure is logged and the
// token is retired so a lost link cannot stay live; the account remains and
// can be re-verified later with a fresh token.
func (s *Service) Register(ctx context.Context, emailAddr, password string) (sqlc.User, error) {
	emailAddr, err := validateEmail(emailAddr)
	if err != nil {
		return sqlc.User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return sqlc.User{}, err
	}
	userID, err := uuid.NewV7()
	if err != nil {
		return sqlc.User{}, fmt.Errorf("generate user id: %w", err)
	}
	raw, tokenHash, err := NewToken()
	if err != nil {
		return sqlc.User{}, fmt.Errorf("create verification token: %w", err)
	}
	tokenID, err := uuid.NewV7()
	if err != nil {
		return sqlc.User{}, fmt.Errorf("generate verification token id: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return sqlc.User{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	user, err := qtx.CreateUser(ctx, sqlc.CreateUserParams{
		ID:           userID,
		Email:        emailAddr,
		PasswordHash: hash,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return sqlc.User{}, ErrEmailTaken
		}
		return sqlc.User{}, fmt.Errorf("create user: %w", err)
	}
	if err := qtx.CreateEmailVerificationToken(ctx, sqlc.CreateEmailVerificationTokenParams{
		ID:        tokenID,
		UserID:    user.ID,
		TokenHash: tokenHash,
		ExpiresAt: time.Now().Add(verificationTokenTTL),
	}); err != nil {
		return sqlc.User{}, fmt.Errorf("store verification token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlc.User{}, fmt.Errorf("commit registration: %w", err)
	}

	s.deliverVerificationEmail(ctx, user.ID, tokenID, user.Email, raw)
	return user, nil
}

// verificationEmail builds the message shared by registration and resend.
func (s *Service) verificationEmail(to, raw string) email.Message {
	verifyURL := fmt.Sprintf("%s/verify-email?token=%s", s.cfg.AppBaseURL, raw)
	return email.Message{
		To:      to,
		Subject: "Verify your email",
		Text: fmt.Sprintf("Please verify your email by opening the following link:\n%s\n\n"+
			"The link expires in 24 hours and can be used once.\n", verifyURL),
		HTML: fmt.Sprintf(`<p>Please verify your email by opening <a href=%q>this link</a>.</p>`+
			"<p>The link expires in 24 hours and can be used once.</p>\n", verifyURL),
	}
}

// deliverVerificationEmail sends a verification message and, if delivery
// fails, retires the token so the undelivered link cannot be used. Logs carry
// only the error type (never the token, URL, or raw SMTP error).
func (s *Service) deliverVerificationEmail(ctx context.Context, userID, tokenID uuid.UUID, to, raw string) {
	if err := s.sender.Send(ctx, s.verificationEmail(to, raw)); err != nil {
		slog.Error("send verification email failed",
			"user_id", userID, "error_type", fmt.Sprintf("%T", err))
		markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tokenCleanupTimeout)
		defer cancel()
		if markErr := s.q.MarkEmailVerificationTokenUsed(markCtx, tokenID); markErr != nil {
			slog.Error("mark verification token used failed",
				"user_id", userID, "error_type", fmt.Sprintf("%T", markErr))
		}
	}
}

// ResendVerification issues a fresh verification token to an unverified
// account, invalidating any unused tokens from earlier sends. Unknown and
// already-verified addresses are ignored so the endpoint cannot enumerate
// accounts.
func (s *Service) ResendVerification(ctx context.Context, emailAddr string) error {
	emailAddr, err := validateEmail(emailAddr)
	if err != nil {
		return err
	}
	user, err := s.q.GetUserByEmail(ctx, emailAddr)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("load user: %w", err)
	}
	if user.EmailVerifiedAt != nil {
		return nil
	}

	raw, tokenHash, err := NewToken()
	if err != nil {
		return fmt.Errorf("create verification token: %w", err)
	}
	tokenID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate verification token id: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	if err := qtx.MarkEmailVerificationTokensUsedByUserID(ctx, user.ID); err != nil {
		return fmt.Errorf("retire previous verification tokens: %w", err)
	}
	if err := qtx.CreateEmailVerificationToken(ctx, sqlc.CreateEmailVerificationTokenParams{
		ID:        tokenID,
		UserID:    user.ID,
		TokenHash: tokenHash,
		ExpiresAt: time.Now().Add(verificationTokenTTL),
	}); err != nil {
		return fmt.Errorf("store verification token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit verification resend: %w", err)
	}

	s.deliverVerificationEmail(ctx, user.ID, tokenID, user.Email, raw)
	return nil
}

// VerifyEmail consumes a verification token: the row is locked FOR UPDATE
// so concurrent uses race at most one winner, single-use is enforced by
// marking the token used, and the user's email is marked verified in the
// same transaction. Expired and used tokens are told apart and reported
// with ErrTokenExpired and ErrTokenInvalid respectively.
func (s *Service) VerifyEmail(ctx context.Context, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return ErrTokenInvalid
	}
	tokenHash := HashToken(raw)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	qtx := s.q.WithTx(tx)

	token, err := qtx.GetEmailVerificationTokenForUpdate(ctx, tokenHash)
	if err != nil {
		_ = tx.Rollback(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTokenInvalid
		}
		return fmt.Errorf("load verification token: %w", err)
	}
	if token.UsedAt != nil {
		_ = tx.Rollback(ctx)
		return ErrTokenInvalid
	}
	if !token.ExpiresAt.After(time.Now()) {
		_ = tx.Rollback(ctx)
		return ErrTokenExpired
	}
	if err := qtx.MarkEmailVerificationTokenUsed(ctx, token.ID); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("mark token used: %w", err)
	}
	if err := qtx.MarkEmailVerified(ctx, token.UserID); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("mark email verified: %w", err)
	}
	return tx.Commit(ctx)
}

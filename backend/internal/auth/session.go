package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"o1/internal/config"
	"o1/internal/db/sqlc"
	"o1/internal/email"
)

const (
	// passwordResetTokenTTL is how long a password reset link remains usable.
	passwordResetTokenTTL = time.Hour
	// maxUserAgentLen caps the stored User-Agent header length.
	maxUserAgentLen = 512
	// defaultSessionCookieName is used when configuration leaves the name empty.
	defaultSessionCookieName = "o1_session"
)

// timingEqualizationHash is a valid Argon2id PHC string used to spend the same
// verification work when the supplied email does not exist. It is not a
// credential for any account.
const timingEqualizationHash = "$argon2id$v=19$m=65536,t=1,p=4$kXrYb+fPUBVvG7kjHto3oA$01RmmshIqFcpM26wGAqBE2yh7MLcOiRwqCklqXXaw/U" //nolint:gosec // fixed dummy hash, not a real credential

// Principal is the authenticated identity derived from a session.
type Principal struct {
	UserID    uuid.UUID
	SessionID uuid.UUID
	Email     string
}

// principalKey is the context key carrying a Principal.
type principalKey struct{}

// PrincipalFrom returns the Principal stored by RequireSession, if present.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Login verifies credentials and creates a new server-side session. Unknown
// emails and wrong passwords produce the same ErrInvalidCredentials sentinel;
// an unverified account yields ErrEmailNotVerified. Only the SHA-256 digest of
// the returned token is stored.
func (s *Service) Login(ctx context.Context, emailAddr, password, ip, userAgent string) (string, sqlc.User, error) {
	user, err := s.q.GetUserByEmail(ctx, NormalizeEmail(emailAddr))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Spend comparable Argon2 work so account existence is not
			// observable through response timing.
			_, _ = VerifyPassword(timingEqualizationHash, password)
			return "", sqlc.User{}, ErrInvalidCredentials
		}
		return "", sqlc.User{}, fmt.Errorf("load user: %w", err)
	}

	ok, err := VerifyPassword(user.PasswordHash, password)
	if err != nil || !ok {
		return "", sqlc.User{}, ErrInvalidCredentials
	}
	if user.EmailVerifiedAt == nil {
		return "", sqlc.User{}, ErrEmailNotVerified
	}

	raw, tokenHash, err := NewToken()
	if err != nil {
		return "", sqlc.User{}, fmt.Errorf("create session token: %w", err)
	}
	if err := s.q.CreateSession(ctx, sqlc.CreateSessionParams{
		ID:        uuid.New(),
		UserID:    user.ID,
		TokenHash: tokenHash,
		ExpiresAt: time.Now().Add(s.cfg.SessionTTL),
		Ip:        normalizeIP(ip),
		UserAgent: truncateUserAgent(userAgent),
	}); err != nil {
		return "", sqlc.User{}, fmt.Errorf("create session: %w", err)
	}
	return raw, user, nil
}

// Authenticate resolves a raw session token to its principal and slides the
// session expiry forward. Unknown and expired tokens yield ErrUnauthenticated.
func (s *Service) Authenticate(ctx context.Context, rawToken string) (Principal, error) {
	if strings.TrimSpace(rawToken) == "" {
		return Principal{}, ErrUnauthenticated
	}
	session, err := s.q.GetSessionByTokenHash(ctx, HashToken(rawToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{}, fmt.Errorf("load session: %w", err)
	}
	if !session.ExpiresAt.After(time.Now()) {
		return Principal{}, ErrUnauthenticated
	}
	user, err := s.q.GetUserByID(ctx, session.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{}, fmt.Errorf("load session user: %w", err)
	}
	if err := s.q.TouchSession(ctx, sqlc.TouchSessionParams{
		ID:        session.ID,
		ExpiresAt: time.Now().Add(s.cfg.SessionTTL),
	}); err != nil {
		return Principal{}, fmt.Errorf("touch session: %w", err)
	}
	return Principal{UserID: user.ID, SessionID: session.ID, Email: user.Email}, nil
}

// Logout deletes the session identified by rawToken. Missing sessions are not
// an error, making logout idempotent.
func (s *Service) Logout(ctx context.Context, rawToken string) error {
	if strings.TrimSpace(rawToken) == "" {
		return nil
	}
	if err := s.q.DeleteSessionByTokenHash(ctx, HashToken(rawToken)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// RequestPasswordReset issues a single-use, one-hour reset token to a verified
// account. Unknown and unverified emails are silently ignored so the endpoint
// cannot be used to enumerate accounts.
func (s *Service) RequestPasswordReset(ctx context.Context, emailAddr string) error {
	user, err := s.q.GetUserByEmail(ctx, NormalizeEmail(emailAddr))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("load user: %w", err)
	}
	if user.EmailVerifiedAt == nil {
		return nil
	}

	raw, tokenHash, err := NewToken()
	if err != nil {
		return fmt.Errorf("create password reset token: %w", err)
	}
	if err := s.q.CreatePasswordResetToken(ctx, sqlc.CreatePasswordResetTokenParams{
		ID:        uuid.New(),
		UserID:    user.ID,
		TokenHash: tokenHash,
		ExpiresAt: time.Now().Add(passwordResetTokenTTL),
	}); err != nil {
		return fmt.Errorf("store password reset token: %w", err)
	}

	resetURL := fmt.Sprintf("%s/reset-password?token=%s", s.cfg.AppBaseURL, raw)
	msg := email.Message{
		To:      user.Email,
		Subject: "Reset your password",
		Text: fmt.Sprintf("Reset your password by opening the following link:\n%s\n\n"+
			"The link expires in 1 hour and can be used once.\n", resetURL),
		HTML: fmt.Sprintf(`<p>Reset your password by opening <a href=%q>this link</a>.</p>`+
			"<p>The link expires in 1 hour and can be used once.</p>\n", resetURL),
	}
	if err := s.sender.Send(ctx, msg); err != nil {
		// Delivery failed: log only what is safe (never the token).
		slog.Error("send password reset email failed", "user_id", user.ID, "err", err)
	}
	return nil
}

// ResetPassword consumes a reset token and replaces the account password. The
// token row is locked FOR UPDATE so concurrent uses race at most one winner.
// Every existing session for the user is revoked in the same transaction.
func (s *Service) ResetPassword(ctx context.Context, token, newPassword string) error {
	if strings.TrimSpace(token) == "" {
		return ErrTokenInvalid
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	reset, err := qtx.GetPasswordResetTokenForUpdate(ctx, HashToken(token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTokenInvalid
		}
		return fmt.Errorf("load password reset token: %w", err)
	}
	if reset.UsedAt != nil {
		return ErrTokenInvalid
	}
	if !reset.ExpiresAt.After(time.Now()) {
		return ErrTokenExpired
	}

	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := qtx.UpdateUserPassword(ctx, sqlc.UpdateUserPasswordParams{
		ID:           reset.UserID,
		PasswordHash: hash,
	}); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if err := qtx.MarkPasswordResetTokenUsed(ctx, reset.ID); err != nil {
		return fmt.Errorf("mark password reset token used: %w", err)
	}
	if err := qtx.DeleteSessionsByUserID(ctx, reset.UserID); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	return tx.Commit(ctx)
}

// SetSessionCookie writes the session token as a hardened cookie. Secure is
// enabled in production, and MaxAge follows the configured session TTL.
func SetSessionCookie(w http.ResponseWriter, cfg config.Config, rawToken string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName(cfg),
		Value:    rawToken,
		Path:     "/",
		MaxAge:   int(cfg.SessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   cfg.Env == "production",
	})
}

// ClearSessionCookie expires the session cookie using the same attributes as
// SetSessionCookie.
func ClearSessionCookie(w http.ResponseWriter, cfg config.Config) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName(cfg),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   cfg.Env == "production",
	})
}

func sessionCookieName(cfg config.Config) string {
	if cfg.SessionCookieName != "" {
		return cfg.SessionCookieName
	}
	return defaultSessionCookieName
}

// normalizeIP stores a valid IP address, or NULL when the value is empty or
// unparseable.
func normalizeIP(raw string) *string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	ip := net.ParseIP(raw)
	if ip == nil {
		return nil
	}
	normalized := ip.String()
	return &normalized
}

// truncateUserAgent caps the header at maxUserAgentLen bytes without leaving
// invalid UTF-8 behind.
func truncateUserAgent(ua string) string {
	if len(ua) <= maxUserAgentLen {
		return ua
	}
	return strings.ToValidUTF8(ua[:maxUserAgentLen], "")
}

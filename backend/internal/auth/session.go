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

	"o1/internal/audit"
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
	// mfaChallengeTTL is how long a login MFA challenge remains usable.
	mfaChallengeTTL = 5 * time.Minute
	// mfaChallengePrefix marks raw login challenge tokens.
	mfaChallengePrefix = "o1mfa_"
	// mfaMaxAttempts is the number of wrong factors before a challenge is
	// consumed and further attempts are refused.
	mfaMaxAttempts = 5
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
	IsAdmin   bool
}

// principalKey is the context key carrying a Principal.
type principalKey struct{}

// PrincipalFrom returns the Principal stored by RequireSession, if present.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// LoginStart is the result of the password step of a login. When MFARequired
// is true, Token is empty and Challenge carries the raw, single-use token that
// must be submitted with the second factor.
type LoginStart struct {
	Token       string
	User        sqlc.User
	MFARequired bool
	Challenge   string
}

// BeginLogin verifies credentials and either creates a session (when the
// account has no second factor) or issues a short-lived MFA challenge. Unknown
// emails and wrong passwords produce the same ErrInvalidCredentials sentinel;
// an unverified account yields ErrEmailNotVerified. Only the SHA-256 digest of
// stored tokens is persisted.
func (s *Service) BeginLogin(ctx context.Context, emailAddr, password, ip, userAgent string) (LoginStart, error) {
	user, err := s.q.GetUserByEmail(ctx, NormalizeEmail(emailAddr))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Spend comparable Argon2 work so account existence is not
			// observable through response timing.
			_, _ = VerifyPassword(timingEqualizationHash, password)
			recordLoginFailure(ctx, s.q, ip, "invalid_credentials")
			return LoginStart{}, ErrInvalidCredentials
		}
		return LoginStart{}, fmt.Errorf("load user: %w", err)
	}

	ok, err := VerifyPassword(user.PasswordHash, password)
	if err != nil || !ok {
		recordLoginFailure(ctx, s.q, ip, "invalid_credentials")
		return LoginStart{}, ErrInvalidCredentials
	}
	if user.EmailVerifiedAt == nil {
		recordLoginFailure(ctx, s.q, ip, "email_unverified")
		return LoginStart{}, ErrEmailNotVerified
	}

	if user.TOTPEnabledAt != nil {
		challenge, err := s.createMFAChallenge(ctx, user.ID, ip, userAgent)
		if err != nil {
			return LoginStart{}, err
		}
		return LoginStart{User: user, MFARequired: true, Challenge: challenge}, nil
	}

	raw, err := createSession(ctx, s.q, s.cfg, user.ID, ip, userAgent)
	if err != nil {
		return LoginStart{}, err
	}
	audit.RecordBestEffort(ctx, s.q, audit.Event{
		ActorUserID: &user.ID,
		Kind:        audit.KindLoginSucceeded,
		Detail:      map[string]any{"ip": normalizedIP(ip), "method": "password"},
	})
	return LoginStart{Token: raw, User: user}, nil
}

// Login is a compatibility wrapper around BeginLogin. Accounts protected by a
// second factor get ErrMFARequired instead of a session token; callers that
// support two-step login should call BeginLogin directly.
func (s *Service) Login(ctx context.Context, emailAddr, password, ip, userAgent string) (string, sqlc.User, error) {
	start, err := s.BeginLogin(ctx, emailAddr, password, ip, userAgent)
	if err != nil {
		return "", sqlc.User{}, err
	}
	if start.MFARequired {
		return "", sqlc.User{}, ErrMFARequired
	}
	return start.Token, start.User, nil
}

// createMFAChallenge deletes stale challenges for the user and stores a fresh
// single-use challenge, returning the raw token. Only the token hash is stored.
func (s *Service) createMFAChallenge(ctx context.Context, userID uuid.UUID, ip, userAgent string) (string, error) {
	if err := s.q.DeleteExpiredMFAChallenges(ctx, userID); err != nil {
		return "", fmt.Errorf("delete expired MFA challenges: %w", err)
	}
	base, _, err := NewToken()
	if err != nil {
		return "", fmt.Errorf("create MFA challenge token: %w", err)
	}
	raw := mfaChallengePrefix + base
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate MFA challenge id: %w", err)
	}
	if err := s.q.CreateMFAChallenge(ctx, sqlc.CreateMFAChallengeParams{
		ID:        id,
		UserID:    userID,
		TokenHash: HashToken(raw),
		ExpiresAt: time.Now().Add(mfaChallengeTTL),
		Ip:        normalizeIP(ip),
		UserAgent: truncateUserAgent(userAgent),
	}); err != nil {
		return "", fmt.Errorf("store MFA challenge: %w", err)
	}
	return raw, nil
}

// createSession issues a session token for userID through q. Callers that need
// the session and other writes to commit together pass a transaction-backed q.
func createSession(ctx context.Context, q *sqlc.Queries, cfg config.Config, userID uuid.UUID, ip, userAgent string) (string, error) {
	raw, tokenHash, err := NewToken()
	if err != nil {
		return "", fmt.Errorf("create session token: %w", err)
	}
	sessionID, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	if err := q.CreateSession(ctx, sqlc.CreateSessionParams{
		ID:        sessionID,
		UserID:    userID,
		TokenHash: tokenHash,
		ExpiresAt: time.Now().Add(cfg.SessionTTL),
		Ip:        normalizeIP(ip),
		UserAgent: truncateUserAgent(userAgent),
	}); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return raw, nil
}

// CompleteTOTPLogin verifies exactly one second factor against a login
// challenge and, on success, creates a session. Wrong factors increment the
// challenge attempt count in a transaction that is committed before the typed
// error is returned; the fifth failure consumes the challenge. Expired or
// consumed challenges, and exhausted attempt counts, are rejected before any
// factor is checked.
func (s *Service) CompleteTOTPLogin(ctx context.Context, challengeToken, code, recoveryCode, ip, userAgent string) (string, sqlc.User, error) {
	hasCode := strings.TrimSpace(code) != ""
	hasRecovery := strings.TrimSpace(recoveryCode) != ""
	if hasCode == hasRecovery {
		return "", sqlc.User{}, ErrInvalidFactor
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", sqlc.User{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	challenge, err := qtx.GetMFAChallengeForUpdate(ctx, HashToken(challengeToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", sqlc.User{}, ErrMFAChallengeExpired
		}
		return "", sqlc.User{}, fmt.Errorf("load MFA challenge: %w", err)
	}
	now := time.Now()
	if challenge.Attempts >= mfaMaxAttempts {
		return "", sqlc.User{}, ErrMFAAttemptsExhausted
	}
	if challenge.ConsumedAt != nil || !challenge.ExpiresAt.After(now) {
		return "", sqlc.User{}, ErrMFAChallengeExpired
	}

	user, err := qtx.GetUserByID(ctx, challenge.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", sqlc.User{}, ErrMFAChallengeExpired
		}
		return "", sqlc.User{}, fmt.Errorf("load challenge user: %w", err)
	}
	state, err := qtx.GetTOTPState(ctx, challenge.UserID)
	if err != nil {
		return "", sqlc.User{}, fmt.Errorf("load TOTP state: %w", err)
	}

	method := "totp"
	var factorErr error
	if hasRecovery {
		method = "recovery_code"
		matched, err := consumeRecoveryCode(ctx, qtx, challenge.UserID, recoveryCode, now.UTC())
		if err != nil {
			return "", sqlc.User{}, err
		}
		if !matched {
			factorErr = ErrInvalidRecoveryCode
		}
	} else {
		factorErr = s.verifyLoginTOTP(ctx, qtx, challenge.UserID, state, code, now)
	}

	if factorErr != nil {
		if !errors.Is(factorErr, ErrInvalidTOTP) && !errors.Is(factorErr, ErrInvalidRecoveryCode) {
			return "", sqlc.User{}, factorErr
		}
		reason := "invalid_totp"
		if hasRecovery {
			reason = "invalid_recovery_code"
		}
		if err := qtx.IncrementMFAChallengeAttempts(ctx, challenge.ID); err != nil {
			return "", sqlc.User{}, fmt.Errorf("increment MFA attempts: %w", err)
		}
		if challenge.Attempts+1 >= mfaMaxAttempts {
			if err := qtx.ConsumeMFAChallenge(ctx, sqlc.ConsumeMFAChallengeParams{
				ID:         challenge.ID,
				ConsumedAt: now,
			}); err != nil {
				return "", sqlc.User{}, fmt.Errorf("consume MFA challenge: %w", err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return "", sqlc.User{}, fmt.Errorf("commit MFA failure: %w", err)
		}
		audit.RecordBestEffort(ctx, s.q, audit.Event{
			ActorUserID: &challenge.UserID,
			Kind:        audit.KindLoginFailed,
			Detail:      map[string]any{"ip": normalizedIP(ip), "reason": reason},
		})
		return "", sqlc.User{}, factorErr
	}

	if err := qtx.ConsumeMFAChallenge(ctx, sqlc.ConsumeMFAChallengeParams{
		ID:         challenge.ID,
		ConsumedAt: now,
	}); err != nil {
		return "", sqlc.User{}, fmt.Errorf("consume MFA challenge: %w", err)
	}
	raw, err := createSession(ctx, qtx, s.cfg, user.ID, ip, userAgent)
	if err != nil {
		return "", sqlc.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", sqlc.User{}, fmt.Errorf("commit MFA login: %w", err)
	}
	audit.RecordBestEffort(ctx, s.q, audit.Event{
		ActorUserID: &user.ID,
		Kind:        audit.KindLoginSucceeded,
		Detail:      map[string]any{"ip": normalizedIP(ip), "method": method},
	})
	return raw, user, nil
}

// verifyLoginTOTP checks a TOTP code against the user's active secret and
// advances the single-use step atomically. A missing or already-used step is
// ErrInvalidTOTP; unexpected storage failures are returned unchanged.
func (s *Service) verifyLoginTOTP(ctx context.Context, q *sqlc.Queries, userID uuid.UUID, state sqlc.GetTOTPStateRow, code string, now time.Time) error {
	if state.TOTPEnabledAt == nil {
		return ErrInvalidTOTP
	}
	secretEnc := ""
	if state.TOTPSecretEnc != nil {
		secretEnc = *state.TOTPSecretEnc
	}
	secret, err := s.openSecret(secretEnc)
	if err != nil {
		if errors.Is(err, ErrTOTPNotEnabled) {
			return ErrInvalidTOTP
		}
		return err
	}
	step, matched, err := matchTOTPStep(secret, code, now)
	if err != nil {
		return err
	}
	if !matched {
		return ErrInvalidTOTP
	}
	used, err := useTOTPStep(ctx, q, userID, step)
	if err != nil {
		return err
	}
	if !used {
		return ErrInvalidTOTP
	}
	return nil
}

// recordLoginFailure audits a rejected login with a fixed reason. It never
// records the email, the supplied password, or the user ID, so unknown and
// unverified accounts cannot leak identity through the audit trail.
func recordLoginFailure(ctx context.Context, q *sqlc.Queries, ip, reason string) {
	audit.RecordBestEffort(ctx, q, audit.Event{
		Kind:   audit.KindLoginFailed,
		Detail: map[string]any{"ip": normalizedIP(ip), "reason": reason},
	})
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
	return Principal{UserID: user.ID, SessionID: session.ID, Email: user.Email, IsAdmin: user.IsAdmin}, nil
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
	tokenID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate password reset token id: %w", err)
	}
	if err := s.q.CreatePasswordResetToken(ctx, sqlc.CreatePasswordResetTokenParams{
		ID:        tokenID,
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
		// Delivery failed: log only a generic reason and non-sensitive fields.
		// The raw error may echo message contents, and the token/URL are secret.
		slog.Error("send password reset email failed", "user_id", user.ID, "error_type", fmt.Sprintf("%T", err))
		markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tokenCleanupTimeout)
		defer cancel()
		if markErr := s.q.MarkPasswordResetTokenUsed(markCtx, tokenID); markErr != nil {
			slog.Error("mark password reset token used failed",
				"user_id", user.ID, "error_type", fmt.Sprintf("%T", markErr))
		}
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
	if err := qtx.DeleteMFAChallengesForUser(ctx, reset.UserID); err != nil {
		return fmt.Errorf("delete MFA challenges: %w", err)
	}
	if err := qtx.ClearTOTPPending(ctx, reset.UserID); err != nil {
		return fmt.Errorf("clear pending TOTP setup: %w", err)
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

// normalizedIP returns the canonical IP used in audit details, or the empty
// string when the value is empty or unparseable.
func normalizedIP(raw string) string {
	if ip := normalizeIP(raw); ip != nil {
		return *ip
	}
	return ""
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

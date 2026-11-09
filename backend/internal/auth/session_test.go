package auth_test

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pquerna/otp/totp"

	"o1/internal/audit"
	"o1/internal/auth"
	"o1/internal/db/sqlc"
	"o1/internal/testutil"
)

const (
	testEmail    = "user@example.com"
	testPassword = "correct horse battery staple"
)

// newVerifiedService creates a verified user with the shared test credentials
// and returns a Service wired to the same database.
func newVerifiedService(t *testing.T, pool *pgxpool.Pool) *auth.Service {
	t.Helper()
	testutil.CreateVerifiedUser(t, pool, testEmail, testPassword)
	return auth.NewService(pool, sqlc.New(pool), testutil.NewFakeSender(), testutil.TestConfig(t))
}

// lastSentText returns the text body of the most recently recorded message.
func lastSentText(t *testing.T, fs *testutil.FakeSender) string {
	t.Helper()
	msgs := fs.Messages()
	if len(msgs) == 0 {
		t.Fatal("no messages sent")
	}
	return msgs[len(msgs)-1].Text
}

func TestLogin_SuccessAndAuthenticate(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	raw, u, err := svc.Login(ctx, testEmail, testPassword, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || u.ID == uuid.Nil {
		t.Fatal("empty login result")
	}
	p, err := svc.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != u.ID {
		t.Fatalf("principal = %v", p)
	}
	if p.SessionID == uuid.Nil || p.Email != testEmail {
		t.Fatalf("principal = %v", p)
	}
	var stored []byte
	if err := pool.QueryRow(ctx, "SELECT token_hash FROM sessions WHERE user_id=$1", u.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) == raw {
		t.Fatal("session token stored in clear")
	}
	if string(stored) != string(auth.HashToken(raw)) {
		t.Fatal("stored session hash does not match SHA-256 of the token")
	}
}

func TestLogin_UnverifiedEmailRejected(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := auth.NewService(pool, sqlc.New(pool), testutil.NewFakeSender(), testutil.TestConfig(t))
	ctx := context.Background()
	if _, err := svc.Register(ctx, "u@example.com", testPassword); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, "u@example.com", testPassword, "", ""); !errors.Is(err, auth.ErrEmailNotVerified) {
		t.Fatalf("err = %v", err)
	}
}

func TestLogin_InvalidCredentialsAreIndistinguishable(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()

	_, _, errUnknown := svc.Login(ctx, "nobody@example.com", testPassword, "", "")
	_, _, errWrong := svc.Login(ctx, testEmail, "wrong horse battery staple", "", "")
	if !errors.Is(errUnknown, auth.ErrInvalidCredentials) {
		t.Fatalf("unknown email err = %v, want ErrInvalidCredentials", errUnknown)
	}
	if !errors.Is(errWrong, auth.ErrInvalidCredentials) {
		t.Fatalf("wrong password err = %v, want ErrInvalidCredentials", errWrong)
	}
	if errUnknown.Error() != errWrong.Error() {
		t.Fatalf("errors differ: %q vs %q", errUnknown, errWrong)
	}
}

func TestLogin_NormalizesEmailAndRecordsMetadata(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	agent := strings.Repeat("a", 600)

	raw, u, err := svc.Login(ctx, " User@Example.com ", testPassword, "203.0.113.5", agent)
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || u.Email != testEmail {
		t.Fatalf("raw = %q, email = %q", raw, u.Email)
	}

	var ip *string
	var userAgent string
	if err := pool.QueryRow(ctx,
		"SELECT ip, user_agent FROM sessions WHERE user_id=$1", u.ID).Scan(&ip, &userAgent); err != nil {
		t.Fatal(err)
	}
	if ip == nil || *ip != "203.0.113.5" {
		t.Fatalf("ip = %v", ip)
	}
	if len(userAgent) != 512 {
		t.Fatalf("user_agent length = %d, want 512", len(userAgent))
	}
}

func TestLogin_UnparseableIPStoredAsNull(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()

	_, u, err := svc.Login(ctx, testEmail, testPassword, "not-an-ip", "")
	if err != nil {
		t.Fatal(err)
	}
	var ip *string
	if err := pool.QueryRow(ctx,
		"SELECT ip FROM sessions WHERE user_id=$1", u.ID).Scan(&ip); err != nil {
		t.Fatal(err)
	}
	if ip != nil {
		t.Fatalf("ip = %q, want NULL", *ip)
	}
}

func TestLogin_IssuesFreshTokenEachTime(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()

	first, _, err := svc.Login(ctx, testEmail, testPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := svc.Login(ctx, testEmail, testPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("login reused a session token")
	}
	if _, err := svc.Authenticate(ctx, first); err != nil {
		t.Fatalf("first session stopped working: %v", err)
	}
	if _, err := svc.Authenticate(ctx, second); err != nil {
		t.Fatalf("second session stopped working: %v", err)
	}
}

func TestAuthenticate_RejectsExpiredSession(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	raw, u, err := svc.Login(ctx, testEmail, testPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE sessions SET expires_at = now() - interval '1 minute' WHERE user_id=$1", u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, raw); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthenticate_UnknownToken(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	for _, token := range []string{"", "unknown-token", "   "} {
		if _, err := svc.Authenticate(context.Background(), token); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("token %q err = %v, want ErrUnauthenticated", token, err)
		}
	}
}

func TestAuthenticate_SlidesExpiry(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	raw, u, err := svc.Login(ctx, testEmail, testPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"UPDATE sessions SET expires_at = now() + interval '1 minute' WHERE user_id=$1", u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, raw); err != nil {
		t.Fatal(err)
	}
	var expires time.Time
	if err := pool.QueryRow(ctx,
		"SELECT expires_at FROM sessions WHERE user_id=$1", u.ID).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if !expires.After(time.Now().Add(29 * 24 * time.Hour)) {
		t.Fatalf("expires_at = %v, want ~30 days from now", expires)
	}
}

func TestLogout_DeletesSessionAndIsIdempotent(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	raw, _, err := svc.Login(ctx, testEmail, testPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, raw); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
	if err := svc.Logout(ctx, raw); err != nil {
		t.Fatalf("second logout err = %v, want nil", err)
	}
	if err := svc.Logout(ctx, ""); err != nil {
		t.Fatalf("empty logout err = %v, want nil", err)
	}
}

func TestRequestPasswordReset_UnknownOrUnverifiedIsSilent(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	fs := testutil.NewFakeSender()
	svc := auth.NewService(pool, sqlc.New(pool), fs, testutil.TestConfig(t))
	ctx := context.Background()

	if err := svc.RequestPasswordReset(ctx, "nobody@example.com"); err != nil {
		t.Fatalf("unknown email err = %v, want nil", err)
	}
	if fs.Len() != 0 {
		t.Fatalf("unknown email sent %d messages, want 0", fs.Len())
	}

	if _, err := svc.Register(ctx, "unverified@example.com", testPassword); err != nil {
		t.Fatal(err)
	}
	sentAfterRegister := fs.Len()
	if err := svc.RequestPasswordReset(ctx, "unverified@example.com"); err != nil {
		t.Fatalf("unverified email err = %v, want nil", err)
	}
	if fs.Len() != sentAfterRegister {
		t.Fatal("unverified email received a password reset message")
	}
}

func TestResetPassword_RevokesSessions(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	fs := testutil.NewFakeSender()
	svc := auth.NewService(pool, sqlc.New(pool), fs, testutil.TestConfig(t))
	testutil.CreateVerifiedUser(t, pool, testEmail, testPassword)
	ctx := context.Background()
	raw, _, err := svc.Login(ctx, testEmail, testPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RequestPasswordReset(ctx, testEmail); err != nil {
		t.Fatal(err)
	}
	token := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(lastSentText(t, fs))[1]
	if err := svc.ResetPassword(ctx, token, "new correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, raw); err == nil {
		t.Fatal("old session survived password reset")
	}
	if _, _, err := svc.Login(ctx, testEmail, "new correct horse battery", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, testEmail, testPassword, "", ""); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("old password err = %v, want ErrInvalidCredentials", err)
	}
	if err := svc.ResetPassword(ctx, token, "another correct horse battery"); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Fatalf("reused token err = %v, want ErrTokenInvalid", err)
	}
	var stored []byte
	if err := pool.QueryRow(ctx, "SELECT token_hash FROM password_reset_tokens LIMIT 1").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) == token {
		t.Fatal("password reset token stored in clear")
	}
}

func TestResetPassword_ExpiredToken(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	q := sqlc.New(pool)
	userID := testutil.CreateVerifiedUser(t, pool, testEmail, testPassword)
	raw, hash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	tokenID, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	if err := q.CreatePasswordResetToken(context.Background(), sqlc.CreatePasswordResetTokenParams{
		ID:        tokenID,
		UserID:    userID,
		TokenHash: hash,
		ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(pool, q, testutil.NewFakeSender(), testutil.TestConfig(t))
	if err := svc.ResetPassword(context.Background(), raw, "new correct horse battery"); !errors.Is(err, auth.ErrTokenExpired) {
		t.Fatalf("err = %v, want ErrTokenExpired", err)
	}
}

func TestResetPassword_UnknownTokenAndValidation(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	fs := testutil.NewFakeSender()
	svc := auth.NewService(pool, sqlc.New(pool), fs, testutil.TestConfig(t))
	testutil.CreateVerifiedUser(t, pool, testEmail, testPassword)
	ctx := context.Background()

	if err := svc.ResetPassword(ctx, "unknown-token", "new correct horse battery"); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Fatalf("unknown token err = %v, want ErrTokenInvalid", err)
	}
	if err := svc.RequestPasswordReset(ctx, testEmail); err != nil {
		t.Fatal(err)
	}
	token := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(lastSentText(t, fs))[1]
	if err := svc.ResetPassword(ctx, token, "short"); !errors.Is(err, auth.ErrValidation) {
		t.Fatalf("short password err = %v, want ErrValidation", err)
	}
	// The failed attempt must not consume the token.
	if err := svc.ResetPassword(ctx, token, "new correct horse battery"); err != nil {
		t.Fatalf("token unusable after validation failure: %v", err)
	}
}

func TestResetPassword_ClearsChallengesAndPending(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	fs := testutil.NewFakeSender()
	svc := auth.NewService(pool, sqlc.New(pool), fs, testutil.TestConfig(t))
	testutil.CreateVerifiedUser(t, pool, testEmail, testPassword)
	ctx := context.Background()
	q := sqlc.New(pool)
	user, err := q.GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatal(err)
	}

	setup, err := svc.SetupTOTP(ctx, user.ID, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnableTOTP(ctx, user.ID, testPassword, code, uuid.Nil); err != nil {
		t.Fatal(err)
	}

	var enabledAt time.Time
	var secretEnc string
	var lastStep int64
	if err := pool.QueryRow(ctx,
		`SELECT totp_enabled_at, totp_secret_enc, totp_last_used_step FROM users WHERE id=$1`,
		user.ID).Scan(&enabledAt, &secretEnc, &lastStep); err != nil {
		t.Fatal(err)
	}
	beforeHashes := recoveryHashes(t, ctx, pool, user.ID)
	if len(beforeHashes) != 10 {
		t.Fatalf("recovery codes before reset = %d, want 10", len(beforeHashes))
	}

	// Simulate a pending setup and an outstanding login challenge.
	if err := q.SetTOTPPending(ctx, sqlc.SetTOTPPendingParams{
		PendingEnc: "v1:pending-reset",
		ExpiresAt:  time.Now().Add(10 * time.Minute),
		UserID:     user.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.CreateMFAChallenge(ctx, sqlc.CreateMFAChallengeParams{
		ID:        uuid.New(),
		UserID:    user.ID,
		TokenHash: []byte("reset-challenge"),
		ExpiresAt: time.Now().Add(5 * time.Minute),
		UserAgent: "test",
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.RequestPasswordReset(ctx, testEmail); err != nil {
		t.Fatal(err)
	}
	token := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(lastSentText(t, fs))[1]
	if err := svc.ResetPassword(ctx, token, "new correct horse battery"); err != nil {
		t.Fatal(err)
	}

	var challenges int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM mfa_challenges WHERE user_id=$1`, user.ID).Scan(&challenges); err != nil {
		t.Fatal(err)
	}
	if challenges != 0 {
		t.Fatalf("challenges after reset = %d, want 0", challenges)
	}

	var pendingEnc *string
	var pendingExpires *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT totp_pending_enc, totp_pending_expires_at FROM users WHERE id=$1`,
		user.ID).Scan(&pendingEnc, &pendingExpires); err != nil {
		t.Fatal(err)
	}
	if pendingEnc != nil || pendingExpires != nil {
		t.Fatalf("pending TOTP state after reset: enc=%v expires=%v", pendingEnc, pendingExpires)
	}

	var afterEnabledAt time.Time
	var afterSecretEnc string
	var afterLastStep int64
	if err := pool.QueryRow(ctx,
		`SELECT totp_enabled_at, totp_secret_enc, totp_last_used_step FROM users WHERE id=$1`,
		user.ID).Scan(&afterEnabledAt, &afterSecretEnc, &afterLastStep); err != nil {
		t.Fatal(err)
	}
	if !afterEnabledAt.Equal(enabledAt) || afterSecretEnc != secretEnc || afterLastStep != lastStep {
		t.Fatalf("active TOTP state changed across reset: enabled %v->%v secretChanged=%v step %d->%d",
			enabledAt, afterEnabledAt, afterSecretEnc != secretEnc, lastStep, afterLastStep)
	}

	afterHashes := recoveryHashes(t, ctx, pool, user.ID)
	if len(afterHashes) != len(beforeHashes) {
		t.Fatalf("recovery codes after reset = %d, want %d", len(afterHashes), len(beforeHashes))
	}
	for i := range beforeHashes {
		if !bytes.Equal(beforeHashes[i], afterHashes[i]) {
			t.Fatalf("recovery code %d changed across reset", i)
		}
	}
}

// recoveryHashes returns the stored recovery-code hashes in stable order.
func recoveryHashes(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) [][]byte {
	t.Helper()
	rows, err := pool.Query(ctx,
		`SELECT code_hash FROM user_recovery_codes WHERE user_id=$1 ORDER BY id`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var hashes [][]byte
	for rows.Next() {
		var hash []byte
		if err := rows.Scan(&hash); err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, hash)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return hashes
}

func TestAuditLoginEvents(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()
	svc := auth.NewService(pool, sqlc.New(pool), testutil.NewFakeSender(), testutil.TestConfig(t))
	userID := testutil.CreateVerifiedUser(t, pool, "audit-login@example.com", testPassword)
	if _, err := svc.Register(ctx, "audit-unverified@example.com", testPassword); err != nil {
		t.Fatalf("register unverified user: %v", err)
	}

	const ip = " 203.0.113.9 "
	if _, _, err := svc.Login(ctx, "audit-login@example.com", testPassword, ip, ""); err != nil {
		t.Fatalf("successful login: %v", err)
	}
	if _, _, err := svc.Login(ctx, "audit-unknown@example.com", testPassword, ip, ""); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("unknown email err = %v", err)
	}
	if _, _, err := svc.Login(ctx, "audit-login@example.com", "wrong password", ip, ""); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("wrong password err = %v", err)
	}
	if _, _, err := svc.Login(ctx, "audit-unverified@example.com", testPassword, ip, ""); !errors.Is(err, auth.ErrEmailNotVerified) {
		t.Fatalf("unverified err = %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT kind, detail::text, actor_user_id FROM security_events WHERE kind LIKE 'login_%' ORDER BY created_at, id`)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer rows.Close()

	var succeeded, invalid, unverified int
	for rows.Next() {
		var kind, detail string
		var actor *uuid.UUID
		if err := rows.Scan(&kind, &detail, &actor); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		if strings.Contains(detail, "@") {
			t.Fatalf("%s detail contains an email: %s", kind, detail)
		}
		if !strings.Contains(detail, `"ip"`) {
			t.Fatalf("%s detail missing ip: %s", kind, detail)
		}
		switch kind {
		case audit.KindLoginSucceeded:
			succeeded++
			if actor == nil || *actor != userID {
				t.Fatalf("login_succeeded actor = %v, want %v", actor, userID)
			}
			if !strings.Contains(detail, "203.0.113.9") {
				t.Fatalf("login_succeeded ip not normalized: %s", detail)
			}
		case audit.KindLoginFailed:
			if actor != nil {
				t.Fatalf("login_failed actor = %v, want NULL", *actor)
			}
			switch {
			case strings.Contains(detail, "invalid_credentials"):
				invalid++
			case strings.Contains(detail, "email_unverified"):
				unverified++
			default:
				t.Fatalf("login_failed detail = %s", detail)
			}
		default:
			t.Fatalf("unexpected event kind %q", kind)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if succeeded != 1 || invalid != 2 || unverified != 1 {
		t.Fatalf("events: succeeded=%d invalid=%d unverified=%d", succeeded, invalid, unverified)
	}
}

func TestBeginLogin_TwoFactorChallengeAndComplete(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	user, err := sqlc.New(pool).GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := svc.SetupTOTP(ctx, user.ID, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	code := mustTOTPCode(t, setup.Secret, time.Now())
	if _, err := svc.EnableTOTP(ctx, user.ID, testPassword, code, uuid.Nil); err != nil {
		t.Fatal(err)
	}

	start, err := svc.BeginLogin(ctx, testEmail, testPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !start.MFARequired || start.Token != "" || start.Challenge == "" {
		t.Fatalf("start = %+v", start)
	}
	if _, _, err := svc.CompleteTOTPLogin(ctx, start.Challenge, "000000", "", "127.0.0.1", "test"); !errors.Is(err, auth.ErrInvalidTOTP) {
		t.Fatalf("wrong code error = %v", err)
	}

	// The failed attempt is audited with the challenged user as actor.
	var actor *uuid.UUID
	var detail string
	if err := pool.QueryRow(ctx,
		`SELECT actor_user_id, detail::text FROM security_events
		 WHERE kind=$1 AND detail::text LIKE '%invalid_totp%'
		 ORDER BY created_at DESC, id DESC LIMIT 1`, audit.KindLoginFailed).Scan(&actor, &detail); err != nil {
		t.Fatal(err)
	}
	if actor == nil || *actor != user.ID {
		t.Fatalf("login_failed actor = %v, want %v", actor, user.ID)
	}

	fresh, err := svc.BeginLogin(ctx, testEmail, testPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	token, gotUser, err := svc.CompleteTOTPLogin(ctx, fresh.Challenge, mustTOTPCode(t, setup.Secret, time.Now().Add(30*time.Second)), "", "127.0.0.1", "test")
	if err != nil || token == "" || gotUser.ID == uuid.Nil {
		t.Fatalf("complete token=%q user=%+v err=%v", token, gotUser, err)
	}
	if _, err := svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("complete session: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT detail::text FROM security_events
		 WHERE kind=$1 AND detail::text LIKE '%"method"%'
		 ORDER BY created_at DESC, id DESC LIMIT 1`, audit.KindLoginSucceeded).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "totp") {
		t.Fatalf("login_succeeded detail = %s, want method totp", detail)
	}
}

func TestCompleteTOTPLogin_RecoveryCodeSingleUse(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	user, err := sqlc.New(pool).GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := svc.SetupTOTP(ctx, user.ID, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	code := mustTOTPCode(t, setup.Secret, time.Now())
	codes, err := svc.EnableTOTP(ctx, user.ID, testPassword, code, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}

	start, err := svc.BeginLogin(ctx, testEmail, testPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	token, gotUser, err := svc.CompleteTOTPLogin(ctx, start.Challenge, "", codes[0], "127.0.0.1", "test")
	if err != nil || token == "" || gotUser.ID != user.ID {
		t.Fatalf("recovery login token=%q user=%+v err=%v", token, gotUser, err)
	}

	again, err := svc.BeginLogin(ctx, testEmail, testPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CompleteTOTPLogin(ctx, again.Challenge, "", codes[0], "127.0.0.1", "test"); !errors.Is(err, auth.ErrInvalidRecoveryCode) {
		t.Fatalf("reused recovery code err = %v, want ErrInvalidRecoveryCode", err)
	}
}

func TestCompleteTOTPLogin_AttemptsExpire(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	user, err := sqlc.New(pool).GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := svc.SetupTOTP(ctx, user.ID, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	code := mustTOTPCode(t, setup.Secret, time.Now())
	if _, err := svc.EnableTOTP(ctx, user.ID, testPassword, code, uuid.Nil); err != nil {
		t.Fatal(err)
	}

	start, err := svc.BeginLogin(ctx, testEmail, testPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 5; attempt++ {
		if _, _, err := svc.CompleteTOTPLogin(ctx, start.Challenge, "000000", "", "127.0.0.1", "test"); !errors.Is(err, auth.ErrInvalidTOTP) {
			t.Fatalf("attempt %d err = %v, want ErrInvalidTOTP", attempt, err)
		}
	}
	if _, _, err := svc.CompleteTOTPLogin(ctx, start.Challenge, "000000", "", "127.0.0.1", "test"); !errors.Is(err, auth.ErrMFAAttemptsExhausted) {
		t.Fatalf("exhausted err = %v, want ErrMFAAttemptsExhausted", err)
	}

	expired, err := svc.BeginLogin(ctx, testEmail, testPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE mfa_challenges SET expires_at = now() - interval '1 minute' WHERE token_hash=$1`,
		auth.HashToken(expired.Challenge)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CompleteTOTPLogin(ctx, expired.Challenge, "000000", "", "127.0.0.1", "test"); !errors.Is(err, auth.ErrMFAChallengeExpired) {
		t.Fatalf("expired challenge err = %v, want ErrMFAChallengeExpired", err)
	}

	consumed, err := svc.BeginLogin(ctx, testEmail, testPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE mfa_challenges SET consumed_at = now() WHERE token_hash=$1`,
		auth.HashToken(consumed.Challenge)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CompleteTOTPLogin(ctx, consumed.Challenge, "000000", "", "127.0.0.1", "test"); !errors.Is(err, auth.ErrMFAChallengeExpired) {
		t.Fatalf("consumed challenge err = %v, want ErrMFAChallengeExpired", err)
	}
}

func TestCompleteTOTPLogin_Replay(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	user, err := sqlc.New(pool).GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := svc.SetupTOTP(ctx, user.ID, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	code := mustTOTPCode(t, setup.Secret, time.Now())
	if _, err := svc.EnableTOTP(ctx, user.ID, testPassword, code, uuid.Nil); err != nil {
		t.Fatal(err)
	}

	start, err := svc.BeginLogin(ctx, testEmail, testPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	nextStep := mustTOTPCode(t, setup.Secret, time.Now().Add(30*time.Second))
	if _, _, err := svc.CompleteTOTPLogin(ctx, start.Challenge, nextStep, "", "127.0.0.1", "test"); err != nil {
		t.Fatal(err)
	}
	next, err := svc.BeginLogin(ctx, testEmail, testPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CompleteTOTPLogin(ctx, next.Challenge, nextStep, "", "127.0.0.1", "test"); !errors.Is(err, auth.ErrInvalidTOTP) {
		t.Fatalf("replayed code err = %v, want ErrInvalidTOTP", err)
	}
}

func TestBeginLogin_NoTwoFactorUnchanged(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()

	start, err := svc.BeginLogin(ctx, testEmail, testPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if start.MFARequired || start.Challenge != "" || start.Token == "" || start.User.ID == uuid.Nil {
		t.Fatalf("start = %+v", start)
	}
	if _, err := svc.Authenticate(ctx, start.Token); err != nil {
		t.Fatalf("session token: %v", err)
	}
	if _, _, err := svc.Login(ctx, testEmail, testPassword, "", ""); err != nil {
		t.Fatalf("Login wrapper: %v", err)
	}
}

func TestCompleteTOTPLogin_InvalidFactor(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()

	if _, _, err := svc.CompleteTOTPLogin(ctx, "challenge", "", "", "", ""); !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("neither factor err = %v, want ErrInvalidFactor", err)
	}
	if _, _, err := svc.CompleteTOTPLogin(ctx, "challenge", "123456", "ABCDE-FGHIJ", "", ""); !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("both factors err = %v, want ErrInvalidFactor", err)
	}
}

// mustTOTPCode returns the TOTP code for secret at the given instant.
func mustTOTPCode(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, at)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

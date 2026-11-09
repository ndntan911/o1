package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"o1/internal/auth"
	"o1/internal/db/sqlc"
	"o1/internal/secretbox"
	"o1/internal/testutil"
)

func TestSetupTOTP_RequiresPasswordAndStoresCiphertext(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	user, err := sqlc.New(pool).GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetupTOTP(ctx, user.ID, "wrong password"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("wrong password error = %v", err)
	}
	setup, err := svc.SetupTOTP(ctx, user.ID, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if setup.Secret == "" || !strings.HasPrefix(setup.OtpauthURI, "otpauth://totp/") {
		t.Fatalf("setup = %+v", setup)
	}
	var enc string
	if err := pool.QueryRow(ctx, `SELECT totp_pending_enc FROM users WHERE id=$1`, user.ID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if enc == setup.Secret || !strings.HasPrefix(enc, "v1:") {
		t.Fatalf("pending secret not sealed: %q", enc)
	}
}

func TestEnableTOTP_ValidCodeAndRecoveryCodes(t *testing.T) {
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

	// Two existing sessions: one is passed as current and must survive, the
	// other must be revoked.
	if _, _, err := svc.Login(ctx, testEmail, testPassword, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, testEmail, testPassword, "", ""); err != nil {
		t.Fatal(err)
	}
	sessions := listSessionIDs(t, ctx, pool, user.ID)
	if len(sessions) != 2 {
		t.Fatalf("sessions before enable = %d, want 2", len(sessions))
	}
	currentSession := sessions[0]

	code, err := totp.GenerateCode(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	codes, err := svc.EnableTOTP(ctx, user.ID, testPassword, code, currentSession)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 10 {
		t.Fatalf("recovery codes = %d, want 10", len(codes))
	}
	pattern := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{5}-[0-9A-HJKMNP-TV-Z]{5}$`)
	seen := make(map[string]bool, len(codes))
	for _, c := range codes {
		if !pattern.MatchString(c) {
			t.Fatalf("recovery code %q does not match format", c)
		}
		if seen[c] {
			t.Fatalf("duplicate recovery code %q", c)
		}
		seen[c] = true
	}

	var enabledAt *time.Time
	var secretEnc, pendingEnc *string
	var pendingExpires *time.Time
	var lastUsed *int64
	if err := pool.QueryRow(ctx, `SELECT totp_enabled_at, totp_secret_enc, totp_pending_enc, totp_pending_expires_at, totp_last_used_step
		FROM users WHERE id=$1`, user.ID).Scan(&enabledAt, &secretEnc, &pendingEnc, &pendingExpires, &lastUsed); err != nil {
		t.Fatal(err)
	}
	if enabledAt == nil {
		t.Fatal("totp_enabled_at not set")
	}
	if secretEnc == nil || !strings.HasPrefix(*secretEnc, "v1:") || *secretEnc == setup.Secret {
		t.Fatalf("active secret not sealed: %v", secretEnc)
	}
	box, err := secretbox.New(testutil.TestConfig(t).EncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := box.Open(*secretEnc)
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != setup.Secret {
		t.Fatal("active secret does not decrypt to the enrollment secret")
	}
	if lastUsed == nil {
		t.Fatal("totp_last_used_step not set")
	}
	if pendingEnc != nil || pendingExpires != nil {
		t.Fatalf("pending secret not cleared: %v %v", pendingEnc, pendingExpires)
	}

	assertRecoveryRowsHashed(t, ctx, pool, user.ID, codes)

	remaining := listSessionIDs(t, ctx, pool, user.ID)
	if len(remaining) != 1 || remaining[0] != currentSession {
		t.Fatalf("sessions after enable = %v, want [%v]", remaining, currentSession)
	}

	var kind string
	var detail []byte
	if err := pool.QueryRow(ctx, `SELECT kind, detail FROM security_events
		WHERE actor_user_id=$1 AND kind=$2`, user.ID, "totp_enabled").Scan(&kind, &detail); err != nil {
		t.Fatal(err)
	}
	detailText := string(detail)
	var detailMap map[string]any
	if err := json.Unmarshal(detail, &detailMap); err != nil {
		t.Fatalf("audit detail is not JSON: %v", err)
	}
	if got := detailMap["recovery_codes"]; got != float64(10) {
		t.Fatalf("audit detail = %s", detailText)
	}
	if strings.Contains(detailText, setup.Secret) {
		t.Fatal("audit detail contains the TOTP secret")
	}
	for _, c := range codes {
		if strings.Contains(detailText, c) {
			t.Fatal("audit detail contains a recovery code")
		}
	}
}

func TestEnableTOTP_RejectsWrongExpiredAndReplay(t *testing.T) {
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

	wrong := "000000"
	valid, err := totp.GenerateCode(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if wrong == valid {
		wrong = "111111"
	}
	if _, err := svc.EnableTOTP(ctx, user.ID, testPassword, wrong, uuid.Nil); !errors.Is(err, auth.ErrInvalidTOTP) {
		t.Fatalf("wrong code error = %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE users SET totp_pending_expires_at=now()-interval '1 minute' WHERE id=$1`, user.ID); err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnableTOTP(ctx, user.ID, testPassword, code, uuid.Nil); !errors.Is(err, auth.ErrTOTPSetupExpired) {
		t.Fatalf("expired setup error = %v", err)
	}

	// Refresh the pending setup and complete enrollment.
	if setup, err = svc.SetupTOTP(ctx, user.ID, testPassword); err != nil {
		t.Fatal(err)
	}
	code, err = totp.GenerateCode(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnableTOTP(ctx, user.ID, testPassword, code, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnableTOTP(ctx, user.ID, testPassword, code, uuid.Nil); !errors.Is(err, auth.ErrTOTPAlreadyEnabled) {
		t.Fatalf("second enable error = %v", err)
	}
}

func TestGetTOTPState(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	user, err := sqlc.New(pool).GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatal(err)
	}

	state, err := svc.GetTOTPState(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Enabled || state.RecoveryCodesRemaining != 0 {
		t.Fatalf("initial state = %+v", state)
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

	state, err = svc.GetTOTPState(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Enabled || state.RecoveryCodesRemaining != 10 {
		t.Fatalf("enabled state = %+v", state)
	}

	if _, err := pool.Exec(ctx, `UPDATE user_recovery_codes SET used_at=now()
		WHERE id=(SELECT id FROM user_recovery_codes WHERE user_id=$1 LIMIT 1)`, user.ID); err != nil {
		t.Fatal(err)
	}
	state, err = svc.GetTOTPState(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.RecoveryCodesRemaining != 9 {
		t.Fatalf("remaining after use = %d, want 9", state.RecoveryCodesRemaining)
	}
}

func TestVerifyRecoveryCode_SingleUse(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	userID, _, codes := enableTOTP(t, ctx, svc, pool)

	// Lowercase input with spaces instead of the display dash still matches.
	raw := strings.ToLower(strings.ReplaceAll(codes[0], "-", " "))
	ok, err := svc.VerifyRecoveryCode(ctx, userID, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("fresh recovery code was rejected")
	}

	ok, err = svc.VerifyRecoveryCode(ctx, userID, codes[0])
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("recovery code was accepted twice")
	}

	ok, err = svc.VerifyRecoveryCode(ctx, userID, "not-a-recovery-code")
	if err != nil {
		t.Fatalf("malformed code error = %v, want nil", err)
	}
	if ok {
		t.Fatal("malformed code was accepted")
	}

	assertRecoveryRowsHashed(t, ctx, pool, userID, codes)
	var used int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM user_recovery_codes WHERE user_id=$1 AND used_at IS NOT NULL`,
		userID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 1 {
		t.Fatalf("used recovery codes = %d, want 1", used)
	}
}

func TestDisableTOTP_RequiresPasswordAndFactor(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	userID, secret, codes := enableTOTP(t, ctx, svc, pool)

	q := sqlc.New(pool)
	if err := q.CreateMFAChallenge(ctx, sqlc.CreateMFAChallengeParams{
		ID:        uuid.New(),
		UserID:    userID,
		TokenHash: []byte("disable-challenge"),
		ExpiresAt: time.Now().Add(5 * time.Minute),
		UserAgent: "test",
	}); err != nil {
		t.Fatal(err)
	}

	// Two existing sessions created through the two-step flow: one is passed
	// as current and must survive, the other must be revoked. Recovery codes
	// are used so the TOTP replay step is not advanced before the disable
	// checks below.
	for i := 0; i < 2; i++ {
		start, err := svc.BeginLogin(ctx, testEmail, testPassword, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := svc.CompleteTOTPLogin(ctx, start.Challenge, "", codes[i], "", ""); err != nil {
			t.Fatal(err)
		}
	}
	sessions := listSessionIDs(t, ctx, pool, userID)
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(sessions))
	}
	current := sessions[0]
	last := lastUsedStep(t, ctx, pool, userID)

	if err := svc.DisableTOTP(ctx, userID, "wrong password",
		currentOrLaterCode(t, secret, last), "", current); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("wrong password error = %v, want ErrInvalidCredentials", err)
	}
	if err := svc.DisableTOTP(ctx, userID, testPassword,
		wrongTOTPCode(t, secret), "", current); !errors.Is(err, auth.ErrInvalidTOTP) {
		t.Fatalf("wrong code error = %v, want ErrInvalidTOTP", err)
	}
	if err := svc.DisableTOTP(ctx, userID, testPassword,
		currentOrLaterCode(t, secret, last), codes[0], current); !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("both factors error = %v, want ErrInvalidFactor", err)
	}
	if err := svc.DisableTOTP(ctx, userID, testPassword, "", "", current); !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("no factor error = %v, want ErrInvalidFactor", err)
	}

	if err := svc.DisableTOTP(ctx, userID, testPassword,
		currentOrLaterCode(t, secret, last), "", current); err != nil {
		t.Fatal(err)
	}

	var enabledAt *time.Time
	var secretEnc, pendingEnc *string
	var pendingExpires *time.Time
	var lastStep *int64
	if err := pool.QueryRow(ctx,
		`SELECT totp_enabled_at, totp_secret_enc, totp_pending_enc, totp_pending_expires_at, totp_last_used_step
		 FROM users WHERE id=$1`, userID).
		Scan(&enabledAt, &secretEnc, &pendingEnc, &pendingExpires, &lastStep); err != nil {
		t.Fatal(err)
	}
	if enabledAt != nil || secretEnc != nil || pendingEnc != nil || pendingExpires != nil || lastStep != nil {
		t.Fatalf("TOTP state not cleared: enabled=%v secret=%v pending=%v expires=%v step=%v",
			enabledAt, secretEnc, pendingEnc, pendingExpires, lastStep)
	}

	var recoveryCount, challengeCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM user_recovery_codes WHERE user_id=$1`, userID).Scan(&recoveryCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM mfa_challenges WHERE user_id=$1`, userID).Scan(&challengeCount); err != nil {
		t.Fatal(err)
	}
	if recoveryCount != 0 || challengeCount != 0 {
		t.Fatalf("recovery codes = %d, challenges = %d, want 0 and 0", recoveryCount, challengeCount)
	}

	remaining := listSessionIDs(t, ctx, pool, userID)
	if len(remaining) != 1 || remaining[0] != current {
		t.Fatalf("sessions after disable = %v, want [%v]", remaining, current)
	}

	var audited int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM security_events WHERE actor_user_id=$1 AND kind=$2`,
		userID, "totp_disabled").Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if audited != 1 {
		t.Fatalf("totp_disabled audit events = %d, want 1", audited)
	}

	// A disabled account is back to password-only login.
	if _, _, err := svc.Login(ctx, testEmail, testPassword, "", ""); err != nil {
		t.Fatalf("login after disable = %v", err)
	}
}

func TestDisableTOTP_ReplayStep(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	userID, secret, _ := enableTOTP(t, ctx, svc, pool)
	enableStep := lastUsedStep(t, ctx, pool, userID)

	replay := codeAtStep(t, secret, enableStep)
	if err := svc.DisableTOTP(ctx, userID, testPassword, replay, "", uuid.Nil); !errors.Is(err, auth.ErrInvalidTOTP) {
		t.Fatalf("replayed step error = %v, want ErrInvalidTOTP", err)
	}

	later := currentOrLaterCode(t, secret, enableStep)
	if err := svc.DisableTOTP(ctx, userID, testPassword, later, "", uuid.Nil); err != nil {
		t.Fatalf("later step disable = %v", err)
	}
}

func TestRegenerateRecoveryCodes(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	ctx := context.Background()
	userID, secret, oldCodes := enableTOTP(t, ctx, svc, pool)
	last := lastUsedStep(t, ctx, pool, userID)

	if _, err := svc.RegenerateRecoveryCodes(ctx, userID, "wrong password",
		currentOrLaterCode(t, secret, last), ""); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("wrong password error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := svc.RegenerateRecoveryCodes(ctx, userID, testPassword,
		currentOrLaterCode(t, secret, last), oldCodes[0]); !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("both factors error = %v, want ErrInvalidFactor", err)
	}
	if _, err := svc.RegenerateRecoveryCodes(ctx, userID, testPassword, "", ""); !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("no factor error = %v, want ErrInvalidFactor", err)
	}

	fresh, err := svc.RegenerateRecoveryCodes(ctx, userID, testPassword,
		currentOrLaterCode(t, secret, last), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 10 {
		t.Fatalf("regenerated codes = %d, want 10", len(fresh))
	}
	assertRecoveryRowsHashed(t, ctx, pool, userID, fresh)

	ok, err := svc.VerifyRecoveryCode(ctx, userID, oldCodes[0])
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("old recovery code survived regeneration")
	}

	var detail []byte
	if err := pool.QueryRow(ctx,
		`SELECT detail FROM security_events WHERE actor_user_id=$1 AND kind=$2 ORDER BY id DESC LIMIT 1`,
		userID, "totp_recovery_codes_regenerated").Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(detail), fresh[0]) {
		t.Fatal("audit detail contains a plaintext recovery code")
	}

	// A recovery code can authorize regeneration, and is consumed doing so.
	again, err := svc.RegenerateRecoveryCodes(ctx, userID, testPassword, "", fresh[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 10 {
		t.Fatalf("second regenerated codes = %d, want 10", len(again))
	}
	ok, err = svc.VerifyRecoveryCode(ctx, userID, fresh[1])
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("recovery code survived regeneration")
	}
	assertRecoveryRowsHashed(t, ctx, pool, userID, again)
}

// totpTestPeriod mirrors the service's 30-second TOTP step.
const totpTestPeriod = 30

// enableTOTP runs a full enrollment for the shared verified test user and
// returns the user ID, enrollment secret, and plaintext recovery codes.
func enableTOTP(t *testing.T, ctx context.Context, svc *auth.Service, pool *pgxpool.Pool) (uuid.UUID, string, []string) {
	t.Helper()
	user, err := sqlc.New(pool).GetUserByEmail(ctx, testEmail)
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
	codes, err := svc.EnableTOTP(ctx, user.ID, testPassword, code, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	return user.ID, setup.Secret, codes
}

// codeAtStep generates a TOTP code for an exact time step.
func codeAtStep(t *testing.T, secret string, step int64) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret, time.Unix(step*totpTestPeriod, 0), totp.ValidateOpts{
		Period:    totpTestPeriod,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// currentOrLaterCode returns a code for the current step, or the next one when
// the current step was already used, so replay protection accepts it.
func currentOrLaterCode(t *testing.T, secret string, lastStep int64) string {
	t.Helper()
	step := time.Now().Unix() / totpTestPeriod
	if step <= lastStep {
		step = lastStep + 1
	}
	return codeAtStep(t, secret, step)
}

// lastUsedStep reads the recorded TOTP replay step for a user.
func lastUsedStep(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) int64 {
	t.Helper()
	var step int64
	if err := pool.QueryRow(ctx,
		`SELECT totp_last_used_step FROM users WHERE id=$1`, userID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	return step
}

// wrongTOTPCode returns a syntactically valid code that does not match now.
func wrongTOTPCode(t *testing.T, secret string) string {
	t.Helper()
	valid, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	wrong := "000000"
	if wrong == valid {
		wrong = "111111"
	}
	return wrong
}

func listSessionIDs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT id FROM sessions WHERE user_id=$1 ORDER BY id`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func assertRecoveryRowsHashed(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, codes []string) {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT code_hash FROM user_recovery_codes WHERE user_id=$1`, userID)
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
	if len(hashes) != len(codes) {
		t.Fatalf("recovery rows = %d, want %d", len(hashes), len(codes))
	}
	hashSet := make(map[string]bool, len(hashes))
	for _, h := range hashes {
		if len(h) != sha256.Size {
			t.Fatalf("recovery hash length = %d, want %d", len(h), sha256.Size)
		}
		hashSet[string(h)] = true
	}
	for _, c := range codes {
		sum := sha256.Sum256([]byte(strings.ReplaceAll(c, "-", "")))
		if !hashSet[string(sum[:])] {
			t.Fatalf("recovery code %q has no matching hash", c)
		}
		if hashSet[c] || hashSet[strings.ReplaceAll(c, "-", "")] {
			t.Fatal("recovery code stored in plaintext")
		}
	}
}

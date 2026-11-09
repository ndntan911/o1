package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"o1/internal/audit"
	"o1/internal/db/sqlc"
)

// TOTP and MFA sentinel errors. Handlers map them onto HTTP responses.
var (
	ErrTOTPAlreadyEnabled   = errors.New("TOTP already enabled")
	ErrTOTPNotEnabled       = errors.New("TOTP not enabled")
	ErrTOTPSetupExpired     = errors.New("TOTP setup expired")
	ErrInvalidTOTP          = errors.New("invalid TOTP code")
	ErrInvalidRecoveryCode  = errors.New("invalid recovery code")
	ErrMFARequired          = errors.New("MFA required")
	ErrMFAChallengeExpired  = errors.New("MFA challenge expired")
	ErrMFAAttemptsExhausted = errors.New("MFA attempts exhausted")
	ErrInvalidFactor        = errors.New("invalid MFA factor")
)

const (
	// totpIssuer is the issuer shown by authenticator applications.
	totpIssuer = "o1 uptime"
	// totpPendingTTL is how long a pending enrollment secret stays usable.
	totpPendingTTL = 10 * time.Minute
	// totpPeriodSeconds is the standard TOTP time step.
	totpPeriodSeconds = 30
	// totpSkewSteps is the number of steps accepted before and after now.
	totpSkewSteps = 1
	// recoveryCodeCount is the number of codes issued per generation.
	recoveryCodeCount = 10
	// recoveryCodeLength is the number of alphabet characters per code.
	recoveryCodeLength = 10
	// recoveryCodeAlphabet is Crockford base32, which omits I, L, O, and U.
	recoveryCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

// TOTPState is the enrollment state exposed to clients.
type TOTPState struct {
	Enabled                bool `json:"enabled"`
	RecoveryCodesRemaining int  `json:"recovery_codes_remaining"`
}

// TOTPSetup is the one-time secret and provisioning URI returned by
// SetupTOTP.
type TOTPSetup struct {
	Secret     string `json:"secret"`
	OtpauthURI string `json:"otpauth_uri"`
}

// GetTOTPState reports whether TOTP is enabled and how many recovery codes
// remain unused.
func (s *Service) GetTOTPState(ctx context.Context, userID uuid.UUID) (TOTPState, error) {
	state, err := s.q.GetTOTPState(ctx, userID)
	if err != nil {
		return TOTPState{}, fmt.Errorf("load TOTP state: %w", err)
	}
	remaining, err := s.q.CountUnusedRecoveryCodes(ctx, userID)
	if err != nil {
		return TOTPState{}, fmt.Errorf("count recovery codes: %w", err)
	}
	return TOTPState{
		Enabled:                state.TOTPEnabledAt != nil,
		RecoveryCodesRemaining: int(remaining),
	}, nil
}

// SetupTOTP verifies the account password and stores a fresh, sealed pending
// secret for ten minutes. The plaintext secret and provisioning URI are
// returned once so the authenticator app can be configured.
func (s *Service) SetupTOTP(ctx context.Context, userID uuid.UUID, password string) (TOTPSetup, error) {
	user, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TOTPSetup{}, ErrInvalidCredentials
		}
		return TOTPSetup{}, fmt.Errorf("load user: %w", err)
	}
	ok, err := VerifyPassword(user.PasswordHash, password)
	if err != nil || !ok {
		return TOTPSetup{}, ErrInvalidCredentials
	}
	if user.TOTPEnabledAt != nil {
		return TOTPSetup{}, ErrTOTPAlreadyEnabled
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      totpIssuer,
		AccountName: user.Email,
		Period:      totpPeriodSeconds,
		SecretSize:  20,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return TOTPSetup{}, fmt.Errorf("generate TOTP key: %w", err)
	}
	sealed, err := s.sealSecret(key.Secret())
	if err != nil {
		return TOTPSetup{}, err
	}
	if err := s.q.SetTOTPPending(ctx, sqlc.SetTOTPPendingParams{
		PendingEnc: sealed,
		ExpiresAt:  time.Now().Add(totpPendingTTL),
		UserID:     userID,
	}); err != nil {
		return TOTPSetup{}, fmt.Errorf("store pending TOTP secret: %w", err)
	}
	return TOTPSetup{Secret: key.Secret(), OtpauthURI: key.URL()}, nil
}

// EnableTOTP completes enrollment: it verifies the password, the pending
// secret's freshness, and a TOTP code, then activates the secret, issues ten
// fresh recovery codes, invalidates pending MFA challenges, and revokes every
// session except currentSessionID. The plaintext recovery codes are returned
// once and only their hashes are stored.
func (s *Service) EnableTOTP(ctx context.Context, userID uuid.UUID, password, code string, currentSessionID uuid.UUID) ([]string, error) {
	user, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("load user: %w", err)
	}
	ok, err := VerifyPassword(user.PasswordHash, password)
	if err != nil || !ok {
		return nil, ErrInvalidCredentials
	}
	if user.TOTPEnabledAt != nil {
		return nil, ErrTOTPAlreadyEnabled
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	state, err := qtx.GetTOTPState(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load TOTP state: %w", err)
	}
	if state.TOTPEnabledAt != nil {
		return nil, ErrTOTPAlreadyEnabled
	}
	if state.TOTPPendingEnc == nil || state.TOTPPendingExpiresAt == nil ||
		!state.TOTPPendingExpiresAt.After(time.Now()) {
		return nil, ErrTOTPSetupExpired
	}
	secret, err := s.openSecret(*state.TOTPPendingEnc)
	if err != nil {
		return nil, err
	}
	step, matched, err := matchTOTPStep(secret, code, time.Now())
	if err != nil {
		return nil, err
	}
	if !matched {
		return nil, ErrInvalidTOTP
	}

	sealed, err := s.sealSecret(secret)
	if err != nil {
		return nil, err
	}
	if err := qtx.EnableTOTP(ctx, sqlc.EnableTOTPParams{
		SecretEnc:    sealed,
		LastUsedStep: step,
		UserID:       userID,
	}); err != nil {
		return nil, fmt.Errorf("enable TOTP: %w", err)
	}

	if err := qtx.DeleteRecoveryCodes(ctx, userID); err != nil {
		return nil, fmt.Errorf("delete recovery codes: %w", err)
	}
	codes, hashes, err := generateRecoveryCodes(recoveryCodeCount)
	if err != nil {
		return nil, err
	}
	for _, hash := range hashes {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, fmt.Errorf("generate recovery code id: %w", err)
		}
		if err := qtx.CreateRecoveryCode(ctx, sqlc.CreateRecoveryCodeParams{
			ID:       id,
			UserID:   userID,
			CodeHash: hash,
		}); err != nil {
			return nil, fmt.Errorf("store recovery code: %w", err)
		}
	}

	if err := qtx.DeleteMFAChallengesForUser(ctx, userID); err != nil {
		return nil, fmt.Errorf("delete MFA challenges: %w", err)
	}
	if err := qtx.DeleteOtherUserSessions(ctx, sqlc.DeleteOtherUserSessionsParams{
		UserID:    userID,
		SessionID: currentSessionID,
	}); err != nil {
		return nil, fmt.Errorf("revoke other sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit TOTP enrollment: %w", err)
	}

	audit.RecordBestEffort(ctx, s.q, audit.Event{
		ActorUserID: &userID,
		Kind:        audit.KindTOTPEnabled,
		Detail:      map[string]any{"recovery_codes": len(codes)},
	})
	return codes, nil
}

// sealSecret encrypts a TOTP plaintext secret. A missing box or secret means
// TOTP cannot be enabled in this deployment.
func (s *Service) sealSecret(secret string) (string, error) {
	if s.box == nil || secret == "" {
		return "", ErrTOTPNotEnabled
	}
	sealed, err := s.box.Seal([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("seal TOTP secret: %w", err)
	}
	return sealed, nil
}

// openSecret decrypts a stored TOTP secret. A missing box or stored value
// means TOTP is not enabled.
func (s *Service) openSecret(value string) (string, error) {
	if s.box == nil || value == "" {
		return "", ErrTOTPNotEnabled
	}
	plaintext, err := s.box.Open(value)
	if err != nil {
		return "", fmt.Errorf("open TOTP secret: %w", err)
	}
	return string(plaintext), nil
}

// normalizeRecoveryCode uppercases a code, removes the display separator and
// whitespace, and maps the ambiguous letters O, I, and L to digits so hashing
// and comparison are formatting-independent.
func normalizeRecoveryCode(code string) string {
	replacer := strings.NewReplacer("-", "", " ", "", "O", "0", "I", "1", "L", "1")
	return replacer.Replace(strings.ToUpper(strings.TrimSpace(code)))
}

// validRecoveryCode reports whether a normalized value is ten Crockford base32
// characters.
func validRecoveryCode(code string) bool {
	if len(code) != recoveryCodeLength {
		return false
	}
	for i := 0; i < len(code); i++ {
		if strings.IndexByte(recoveryCodeAlphabet, code[i]) < 0 {
			return false
		}
	}
	return true
}

// consumeRecoveryCode verifies a recovery code against the user's unused rows
// and marks the match used, all through q. Callers must run it in a transaction
// so the lock and the update commit or roll back together. Used, unknown, and
// malformed codes are reported as false.
func consumeRecoveryCode(ctx context.Context, q *sqlc.Queries, userID uuid.UUID, raw string, now time.Time) (bool, error) {
	normalized := normalizeRecoveryCode(raw)
	if !validRecoveryCode(normalized) {
		return false, nil
	}
	sum := sha256.Sum256([]byte(normalized))

	codes, err := q.ListRecoveryCodesForUpdate(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("load recovery codes: %w", err)
	}
	matched := uuid.Nil
	for _, code := range codes {
		if code.UsedAt != nil {
			continue
		}
		if subtle.ConstantTimeCompare(code.CodeHash, sum[:]) == 1 {
			matched = code.ID
		}
	}
	if matched == uuid.Nil {
		return false, nil
	}

	rows, err := q.MarkRecoveryCodeUsed(ctx, sqlc.MarkRecoveryCodeUsedParams{
		UsedAt: &now,
		ID:     matched,
	})
	if err != nil {
		return false, fmt.Errorf("mark recovery code used: %w", err)
	}
	return rows == 1, nil
}

// VerifyRecoveryCode reports whether raw matches one unused recovery code and
// consumes it atomically. Used, unknown, and malformed codes return false.
func (s *Service) VerifyRecoveryCode(ctx context.Context, userID uuid.UUID, raw string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	ok, err := consumeRecoveryCode(ctx, s.q.WithTx(tx), userID, raw, time.Now().UTC())
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit recovery code verification: %w", err)
	}
	return true, nil
}

// loadTOTPManagementUser verifies the account password and that TOTP is
// enabled before a management action. Unknown users and wrong passwords share
// ErrInvalidCredentials so account existence is not observable.
func (s *Service) loadTOTPManagementUser(ctx context.Context, userID uuid.UUID, password string) (sqlc.User, error) {
	user, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.User{}, ErrInvalidCredentials
		}
		return sqlc.User{}, fmt.Errorf("load user: %w", err)
	}
	ok, err := VerifyPassword(user.PasswordHash, password)
	if err != nil || !ok {
		return sqlc.User{}, ErrInvalidCredentials
	}
	if user.TOTPEnabledAt == nil {
		return sqlc.User{}, ErrTOTPNotEnabled
	}
	return user, nil
}

// verifyManagementFactor enforces exactly one of a TOTP code or a recovery
// code and verifies it through q. TOTP codes advance the replay step
// atomically and recovery codes are consumed.
func (s *Service) verifyManagementFactor(ctx context.Context, q *sqlc.Queries, userID uuid.UUID, state sqlc.GetTOTPStateRow, code, recoveryCode string) error {
	hasCode := strings.TrimSpace(code) != ""
	hasRecovery := strings.TrimSpace(recoveryCode) != ""
	if hasCode == hasRecovery {
		return ErrInvalidFactor
	}
	if hasRecovery {
		ok, err := consumeRecoveryCode(ctx, q, userID, recoveryCode, time.Now().UTC())
		if err != nil {
			return err
		}
		if !ok {
			return ErrInvalidRecoveryCode
		}
		return nil
	}

	var secretEnc string
	if state.TOTPSecretEnc != nil {
		secretEnc = *state.TOTPSecretEnc
	}
	secret, err := s.openSecret(secretEnc)
	if err != nil {
		return err
	}
	step, matched, err := matchTOTPStep(secret, code, time.Now())
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

// DisableTOTP verifies the password and exactly one second factor, then clears
// active and pending TOTP state, deletes recovery codes and MFA challenges,
// and revokes every session except currentSessionID.
func (s *Service) DisableTOTP(ctx context.Context, userID uuid.UUID, password, code, recoveryCode string, currentSessionID uuid.UUID) error {
	if _, err := s.loadTOTPManagementUser(ctx, userID, password); err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	state, err := qtx.GetTOTPState(ctx, userID)
	if err != nil {
		return fmt.Errorf("load TOTP state: %w", err)
	}
	if state.TOTPEnabledAt == nil {
		return ErrTOTPNotEnabled
	}
	if err := s.verifyManagementFactor(ctx, qtx, userID, state, code, recoveryCode); err != nil {
		return err
	}

	if err := qtx.DisableTOTP(ctx, userID); err != nil {
		return fmt.Errorf("disable TOTP: %w", err)
	}
	if err := qtx.DeleteRecoveryCodes(ctx, userID); err != nil {
		return fmt.Errorf("delete recovery codes: %w", err)
	}
	if err := qtx.DeleteMFAChallengesForUser(ctx, userID); err != nil {
		return fmt.Errorf("delete MFA challenges: %w", err)
	}
	if err := qtx.DeleteOtherUserSessions(ctx, sqlc.DeleteOtherUserSessionsParams{
		UserID:    userID,
		SessionID: currentSessionID,
	}); err != nil {
		return fmt.Errorf("revoke other sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit TOTP disable: %w", err)
	}

	audit.RecordBestEffort(ctx, s.q, audit.Event{
		ActorUserID: &userID,
		Kind:        audit.KindTOTPDisabled,
	})
	return nil
}

// RegenerateRecoveryCodes verifies the password and exactly one second factor,
// replaces every recovery code with ten fresh hashes, and returns the plaintext
// codes exactly once.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, userID uuid.UUID, password, code, recoveryCode string) ([]string, error) {
	if _, err := s.loadTOTPManagementUser(ctx, userID, password); err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	state, err := qtx.GetTOTPState(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load TOTP state: %w", err)
	}
	if state.TOTPEnabledAt == nil {
		return nil, ErrTOTPNotEnabled
	}
	if err := s.verifyManagementFactor(ctx, qtx, userID, state, code, recoveryCode); err != nil {
		return nil, err
	}

	if err := qtx.DeleteRecoveryCodes(ctx, userID); err != nil {
		return nil, fmt.Errorf("delete recovery codes: %w", err)
	}
	codes, hashes, err := generateRecoveryCodes(recoveryCodeCount)
	if err != nil {
		return nil, err
	}
	for _, hash := range hashes {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, fmt.Errorf("generate recovery code id: %w", err)
		}
		if err := qtx.CreateRecoveryCode(ctx, sqlc.CreateRecoveryCodeParams{
			ID:       id,
			UserID:   userID,
			CodeHash: hash,
		}); err != nil {
			return nil, fmt.Errorf("store recovery code: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit recovery code regeneration: %w", err)
	}

	audit.RecordBestEffort(ctx, s.q, audit.Event{
		ActorUserID: &userID,
		Kind:        audit.KindTOTPRecoveryCodesRegenerated,
		Detail:      map[string]any{"recovery_codes": len(codes)},
	})
	return codes, nil
}

// hashRecoveryCode returns the SHA-256 digest of the normalized code.
func hashRecoveryCode(code string) []byte {
	sum := sha256.Sum256([]byte(normalizeRecoveryCode(code)))
	return sum[:]
}

// newRecoveryCode returns one Crockford base32 code in XXXXX-XXXXX form.
func newRecoveryCode() (string, error) {
	raw := make([]byte, recoveryCodeLength)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate recovery code: %w", err)
	}
	chars := make([]byte, recoveryCodeLength)
	for i, b := range raw {
		chars[i] = recoveryCodeAlphabet[int(b)%len(recoveryCodeAlphabet)]
	}
	return string(chars[:5]) + "-" + string(chars[5:]), nil
}

// generateRecoveryCodes creates count unique plaintext codes and their
// hashes. Callers return the plaintext to the user exactly once.
func generateRecoveryCodes(count int) ([]string, [][]byte, error) {
	codes := make([]string, 0, count)
	hashes := make([][]byte, 0, count)
	seen := make(map[string]struct{}, count)
	for len(codes) < count {
		code, err := newRecoveryCode()
		if err != nil {
			return nil, nil, err
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
		hashes = append(hashes, hashRecoveryCode(code))
	}
	return codes, hashes, nil
}

// matchTOTPStep accepts a code for the current step or one step either side,
// returning the matched step so callers can enforce single use. Every
// candidate is compared in constant time.
func matchTOTPStep(secret, code string, now time.Time) (int64, bool, error) {
	code = strings.TrimSpace(code)
	if secret == "" || code == "" {
		return 0, false, nil
	}
	step := now.Unix() / totpPeriodSeconds
	opts := totp.ValidateOpts{
		Period:    totpPeriodSeconds,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	}
	for _, candidate := range []int64{step - totpSkewSteps, step, step + totpSkewSteps} {
		expected, err := totp.GenerateCodeCustom(secret, time.Unix(candidate*totpPeriodSeconds, 0), opts)
		if err != nil {
			return 0, false, fmt.Errorf("generate TOTP code: %w", err)
		}
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return candidate, true, nil
		}
	}
	return 0, false, nil
}

// useTOTPStep records a successfully verified step. It returns false when the
// step was already used (or is older than a previously used step), which
// makes TOTP codes single-use under concurrency.
func useTOTPStep(ctx context.Context, q *sqlc.Queries, userID uuid.UUID, step int64) (bool, error) {
	n, err := q.UpdateTOTPLastUsedStepIfGreater(ctx, sqlc.UpdateTOTPLastUsedStepIfGreaterParams{
		LastUsedStep: step,
		UserID:       userID,
	})
	if err != nil {
		return false, fmt.Errorf("record TOTP step: %w", err)
	}
	return n > 0, nil
}

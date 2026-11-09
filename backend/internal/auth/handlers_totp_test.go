package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pquerna/otp/totp"

	"o1/internal/auth"
	"o1/internal/db/sqlc"
	"o1/internal/testutil"
)

// enableTOTPForTest enrolls and activates TOTP for the shared test user,
// returning the active secret and the one-time recovery codes.
func enableTOTPForTest(t *testing.T, svc *auth.Service, pool *pgxpool.Pool) (sqlc.User, string, []string) {
	t.Helper()
	ctx := context.Background()
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
	return user, setup.Secret, codes
}

// serveWithSession runs h behind RequireSession so session-only handlers see a
// principal derived from the supplied cookies.
func serveWithSession(t *testing.T, svc *auth.Service, method string, h http.HandlerFunc, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	auth.RequireSession(svc)(h).ServeHTTP(rec, req)
	return rec
}

// loginBody is the JSON body for the shared test credentials.
func loginBody() string {
	return `{"email":"` + testEmail + `","password":"` + testPassword + `"}`
}

func TestLoginHandler_NoTwoFactorSetsCookie(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	cfg := testutil.TestConfig(t)

	rec := postJSONRec(t, auth.LoginHandler(svc, cfg), loginBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if c := sessionCookie(t, rec); c.Value == "" {
		t.Fatal("empty session cookie")
	}
	var resp struct {
		User struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
		MFARequired bool `json:"mfa_required"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.User.ID == "" || resp.User.Email != testEmail || resp.MFARequired {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestLoginHandler_TwoFactorReturnsChallengeWithoutCookie(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	user, _, _ := enableTOTPForTest(t, svc, pool)
	cfg := testutil.TestConfig(t)

	rec := postJSONRec(t, auth.LoginHandler(svc, cfg), loginBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("2FA login set cookies: %v", cookies)
	}
	var resp struct {
		MFARequired    bool   `json:"mfa_required"`
		ChallengeToken string `json:"challenge_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.MFARequired || resp.ChallengeToken == "" {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if !strings.HasPrefix(resp.ChallengeToken, "o1mfa_") {
		t.Fatalf("challenge token = %q, want o1mfa_ prefix", resp.ChallengeToken)
	}
	var sessions int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM sessions WHERE user_id=$1`, user.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("sessions after password step = %d, want 0", sessions)
	}
}

func TestCompleteTOTPLoginHandler_SetsCookieAndReturnsUser(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	user, secret, _ := enableTOTPForTest(t, svc, pool)
	cfg := testutil.TestConfig(t)
	ctx := context.Background()

	start, err := svc.BeginLogin(ctx, testEmail, testPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	rec := postJSONRec(t, auth.CompleteTOTPLoginHandler(svc, cfg),
		`{"challenge_token":"`+start.Challenge+`","code":"`+code+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	c := sessionCookie(t, rec)
	if c.Value == "" {
		t.Fatal("empty session cookie")
	}
	var resp struct {
		User struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.User.ID != user.ID.String() || resp.User.Email != testEmail {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if p, err := svc.Authenticate(ctx, c.Value); err != nil || p.UserID != user.ID {
		t.Fatalf("session principal = %+v, err = %v", p, err)
	}
}

func TestCompleteTOTPLoginHandler_ErrorCodes(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	_, _, _ = enableTOTPForTest(t, svc, pool)
	cfg := testutil.TestConfig(t)
	ctx := context.Background()
	h := auth.CompleteTOTPLoginHandler(svc, cfg)

	start, err := svc.BeginLogin(ctx, testEmail, testPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}

	status, ctype, body := postJSON(t, h, `{"challenge_token":"`+start.Challenge+`"}`)
	if code := problemCode(t, status, ctype, body); status != http.StatusBadRequest || code != "validation_error" {
		t.Fatalf("missing factor: status = %d, code = %q", status, code)
	}

	status, ctype, body = postJSON(t, h, `{"challenge_token":"`+start.Challenge+`","code":"000000"}`)
	if code := problemCode(t, status, ctype, body); status != http.StatusUnauthorized || code != "invalid_totp" {
		t.Fatalf("wrong code: status = %d, code = %q", status, code)
	}

	status, ctype, body = postJSON(t, h, `{"challenge_token":"unknown-token","code":"000000"}`)
	if code := problemCode(t, status, ctype, body); status != http.StatusGone || code != "mfa_challenge_expired" {
		t.Fatalf("unknown challenge: status = %d, code = %q", status, code)
	}

	exhausted, err := svc.BeginLogin(ctx, testEmail, testPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 5; attempt++ {
		status, ctype, body = postJSON(t, h, `{"challenge_token":"`+exhausted.Challenge+`","code":"000000"}`)
		if code := problemCode(t, status, ctype, body); status != http.StatusUnauthorized || code != "invalid_totp" {
			t.Fatalf("attempt %d: status = %d, code = %q", attempt, status, code)
		}
	}
	status, ctype, body = postJSON(t, h, `{"challenge_token":"`+exhausted.Challenge+`","code":"000000"}`)
	if code := problemCode(t, status, ctype, body); status != http.StatusTooManyRequests || code != "mfa_attempts_exhausted" {
		t.Fatalf("exhausted: status = %d, code = %q", status, code)
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
	status, ctype, body = postJSON(t, h, `{"challenge_token":"`+expired.Challenge+`","code":"000000"}`)
	if code := problemCode(t, status, ctype, body); status != http.StatusGone || code != "mfa_challenge_expired" {
		t.Fatalf("expired: status = %d, code = %q", status, code)
	}
}

func TestTOTPHandlers_SetupEnableStateAndDisable(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	cfg := testutil.TestConfig(t)

	login := postJSONRec(t, auth.LoginHandler(svc, cfg), loginBody())
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d", login.Code)
	}
	cookie := sessionCookie(t, login)

	stateRec := serveWithSession(t, svc, http.MethodGet, auth.GetTOTPStateHandler(svc), "", cookie)
	if stateRec.Code != http.StatusOK {
		t.Fatalf("state status = %d, body = %s", stateRec.Code, stateRec.Body.String())
	}
	var state struct {
		Enabled                bool `json:"enabled"`
		RecoveryCodesRemaining int  `json:"recovery_codes_remaining"`
	}
	if err := json.Unmarshal(stateRec.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state.Enabled || state.RecoveryCodesRemaining != 0 {
		t.Fatalf("initial state = %+v", state)
	}

	setupRec := serveWithSession(t, svc, http.MethodPost, auth.SetupTOTPHandler(svc),
		`{"password":"`+testPassword+`"}`, cookie)
	if setupRec.Code != http.StatusOK {
		t.Fatalf("setup status = %d, body = %s", setupRec.Code, setupRec.Body.String())
	}
	var setup struct {
		Secret     string `json:"secret"`
		OtpauthURI string `json:"otpauth_uri"`
	}
	if err := json.Unmarshal(setupRec.Body.Bytes(), &setup); err != nil {
		t.Fatalf("decode setup: %v", err)
	}
	if setup.Secret == "" || !strings.HasPrefix(setup.OtpauthURI, "otpauth://totp/") {
		t.Fatalf("setup = %+v", setup)
	}

	wrongRec := serveWithSession(t, svc, http.MethodPost, auth.EnableTOTPHandler(svc),
		`{"password":"`+testPassword+`","code":"000000"}`, cookie)
	if code := problemCode(t, wrongRec.Code, wrongRec.Result().Header.Get("Content-Type"), wrongRec.Body.String()); wrongRec.Code != http.StatusBadRequest || code != "invalid_totp" {
		t.Fatalf("wrong enable code: status = %d, code = %q", wrongRec.Code, code)
	}

	code, err := totp.GenerateCode(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	enableRec := serveWithSession(t, svc, http.MethodPost, auth.EnableTOTPHandler(svc),
		`{"password":"`+testPassword+`","code":"`+code+`"}`, cookie)
	if enableRec.Code != http.StatusOK {
		t.Fatalf("enable status = %d, body = %s", enableRec.Code, enableRec.Body.String())
	}
	var enabled struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := json.Unmarshal(enableRec.Body.Bytes(), &enabled); err != nil {
		t.Fatalf("decode enable: %v", err)
	}
	if len(enabled.RecoveryCodes) != 10 {
		t.Fatalf("recovery codes = %d, want 10", len(enabled.RecoveryCodes))
	}

	stateRec = serveWithSession(t, svc, http.MethodGet, auth.GetTOTPStateHandler(svc), "", cookie)
	if stateRec.Code != http.StatusOK {
		t.Fatalf("state status = %d", stateRec.Code)
	}
	if err := json.Unmarshal(stateRec.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if !state.Enabled || state.RecoveryCodesRemaining != 10 {
		t.Fatalf("enabled state = %+v", state)
	}

	againRec := serveWithSession(t, svc, http.MethodPost, auth.SetupTOTPHandler(svc),
		`{"password":"`+testPassword+`"}`, cookie)
	if code := problemCode(t, againRec.Code, againRec.Result().Header.Get("Content-Type"), againRec.Body.String()); againRec.Code != http.StatusConflict || code != "totp_already_enabled" {
		t.Fatalf("repeat setup: status = %d, code = %q", againRec.Code, code)
	}

	bothRec := serveWithSession(t, svc, http.MethodPost, auth.DisableTOTPHandler(svc),
		`{"password":"`+testPassword+`","code":"123456","recovery_code":"ABCDE-FGHIJ"}`, cookie)
	if code := problemCode(t, bothRec.Code, bothRec.Result().Header.Get("Content-Type"), bothRec.Body.String()); bothRec.Code != http.StatusBadRequest || code != "validation_error" {
		t.Fatalf("both factors: status = %d, code = %q", bothRec.Code, code)
	}

	wrongRecovery := serveWithSession(t, svc, http.MethodPost, auth.DisableTOTPHandler(svc),
		`{"password":"`+testPassword+`","recovery_code":"ABCDE-FGHIJ"}`, cookie)
	if code := problemCode(t, wrongRecovery.Code, wrongRecovery.Result().Header.Get("Content-Type"), wrongRecovery.Body.String()); wrongRecovery.Code != http.StatusBadRequest || code != "invalid_recovery_code" {
		t.Fatalf("wrong recovery: status = %d, code = %q", wrongRecovery.Code, code)
	}

	disableRec := serveWithSession(t, svc, http.MethodPost, auth.DisableTOTPHandler(svc),
		`{"password":"`+testPassword+`","recovery_code":"`+enabled.RecoveryCodes[0]+`"}`, cookie)
	if disableRec.Code != http.StatusNoContent {
		t.Fatalf("disable status = %d, body = %s", disableRec.Code, disableRec.Body.String())
	}

	stateRec = serveWithSession(t, svc, http.MethodGet, auth.GetTOTPStateHandler(svc), "", cookie)
	if err := json.Unmarshal(stateRec.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state.Enabled || state.RecoveryCodesRemaining != 0 {
		t.Fatalf("state after disable = %+v", state)
	}
}

func TestTOTPHandlers_RegenerateRecoveryCodes(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	cfg := testutil.TestConfig(t)

	_, secret, codes := enableTOTPForTest(t, svc, pool)
	ctx := context.Background()

	// Enabling with no current session revoked the password-step session, so
	// obtain a fresh one through the two-step flow.
	start, err := svc.BeginLogin(ctx, testEmail, testPassword, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	loginRec := postJSONRec(t, auth.CompleteTOTPLoginHandler(svc, cfg),
		`{"challenge_token":"`+start.Challenge+`","code":"`+mustTOTPCode(t, secret, time.Now().Add(30*time.Second))+`"}`)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("totp login status = %d, body = %s", loginRec.Code, loginRec.Body.String())
	}
	cookie := sessionCookie(t, loginRec)

	rec := serveWithSession(t, svc, http.MethodPost, auth.RegenerateRecoveryCodesHandler(svc),
		`{"password":"`+testPassword+`","recovery_code":"`+codes[0]+`"}`, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("regenerate status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.RecoveryCodes) != 10 {
		t.Fatalf("recovery codes = %d, want 10", len(resp.RecoveryCodes))
	}

	oldRec := serveWithSession(t, svc, http.MethodPost, auth.RegenerateRecoveryCodesHandler(svc),
		`{"password":"`+testPassword+`","recovery_code":"`+codes[1]+`"}`, cookie)
	if code := problemCode(t, oldRec.Code, oldRec.Result().Header.Get("Content-Type"), oldRec.Body.String()); oldRec.Code != http.StatusBadRequest || code != "invalid_recovery_code" {
		t.Fatalf("old code: status = %d, code = %q", oldRec.Code, code)
	}
}

func TestTOTPHandlers_NotEnabledAndSetupExpired(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := newVerifiedService(t, pool)
	cfg := testutil.TestConfig(t)

	login := postJSONRec(t, auth.LoginHandler(svc, cfg), loginBody())
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d", login.Code)
	}
	cookie := sessionCookie(t, login)

	disableRec := serveWithSession(t, svc, http.MethodPost, auth.DisableTOTPHandler(svc),
		`{"password":"`+testPassword+`","recovery_code":"ABCDE-FGHIJ"}`, cookie)
	if code := problemCode(t, disableRec.Code, disableRec.Result().Header.Get("Content-Type"), disableRec.Body.String()); disableRec.Code != http.StatusConflict || code != "totp_not_enabled" {
		t.Fatalf("disable not enabled: status = %d, code = %q", disableRec.Code, code)
	}

	regenerateRec := serveWithSession(t, svc, http.MethodPost, auth.RegenerateRecoveryCodesHandler(svc),
		`{"password":"`+testPassword+`","recovery_code":"ABCDE-FGHIJ"}`, cookie)
	if code := problemCode(t, regenerateRec.Code, regenerateRec.Result().Header.Get("Content-Type"), regenerateRec.Body.String()); regenerateRec.Code != http.StatusConflict || code != "totp_not_enabled" {
		t.Fatalf("regenerate not enabled: status = %d, code = %q", regenerateRec.Code, code)
	}

	enableRec := serveWithSession(t, svc, http.MethodPost, auth.EnableTOTPHandler(svc),
		`{"password":"`+testPassword+`","code":"000000"}`, cookie)
	if code := problemCode(t, enableRec.Code, enableRec.Result().Header.Get("Content-Type"), enableRec.Body.String()); enableRec.Code != http.StatusGone || code != "totp_setup_expired" {
		t.Fatalf("enable without setup: status = %d, code = %q", enableRec.Code, code)
	}

	wrongPassword := serveWithSession(t, svc, http.MethodPost, auth.SetupTOTPHandler(svc),
		`{"password":"wrong horse battery staple"}`, cookie)
	if code := problemCode(t, wrongPassword.Code, wrongPassword.Result().Header.Get("Content-Type"), wrongPassword.Body.String()); wrongPassword.Code != http.StatusUnauthorized || code != "invalid_credentials" {
		t.Fatalf("setup wrong password: status = %d, code = %q", wrongPassword.Code, code)
	}

	unknownField := serveWithSession(t, svc, http.MethodPost, auth.SetupTOTPHandler(svc),
		`{"password":"`+testPassword+`","extra":true}`, cookie)
	if code := problemCode(t, unknownField.Code, unknownField.Result().Header.Get("Content-Type"), unknownField.Body.String()); unknownField.Code != http.StatusBadRequest || code != "validation_error" {
		t.Fatalf("unknown field: status = %d, code = %q", unknownField.Code, code)
	}
}

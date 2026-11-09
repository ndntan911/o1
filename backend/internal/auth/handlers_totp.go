package auth

import (
	"encoding/json"
	"errors"
	"net/http"

	"o1/internal/config"
	"o1/internal/httpx"
)

// totpPasswordRequest is the JSON body of the TOTP setup endpoint.
type totpPasswordRequest struct {
	Password string `json:"password"`
}

// totpEnableRequest is the JSON body of the TOTP enable endpoint.
type totpEnableRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

// totpFactorRequest is the JSON body of the disable and recovery-code
// endpoints. Exactly one of Code or RecoveryCode is required.
type totpFactorRequest struct {
	Password     string `json:"password"`
	Code         string `json:"code"`
	RecoveryCode string `json:"recovery_code"`
}

// completeTOTPLoginRequest is the JSON body of POST /auth/login/totp.
type completeTOTPLoginRequest struct {
	ChallengeToken string `json:"challenge_token"`
	Code           string `json:"code"`
	RecoveryCode   string `json:"recovery_code"`
}

// recoveryCodesResponse carries freshly issued one-time recovery codes.
type recoveryCodesResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

// GetTOTPStateHandler returns the caller's TOTP enrollment state. The route is
// session-only; a missing principal still gets 401 rather than a panic.
func GetTOTPStateHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			writeUnauthenticated(w)
			return
		}
		state, err := svc.GetTOTPState(r.Context(), principal.UserID)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, httpx.CodeInternalError,
				"Internal Server Error")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, state)
	}
}

// SetupTOTPHandler returns a fresh pending secret and provisioning URI after
// verifying the account password.
func SetupTOTPHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			writeUnauthenticated(w)
			return
		}
		var req totpPasswordRequest
		if !decodeTOTPBody(w, r, &req) {
			return
		}
		setup, err := svc.SetupTOTP(r.Context(), principal.UserID, req.Password)
		if err != nil {
			writeTOTPManagementProblem(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, setup)
	}
}

// EnableTOTPHandler completes enrollment and returns the one-time recovery
// codes. Other sessions for the user are revoked except the current one.
func EnableTOTPHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			writeUnauthenticated(w)
			return
		}
		var req totpEnableRequest
		if !decodeTOTPBody(w, r, &req) {
			return
		}
		codes, err := svc.EnableTOTP(r.Context(), principal.UserID, req.Password, req.Code, principal.SessionID)
		if err != nil {
			writeTOTPManagementProblem(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
	}
}

// DisableTOTPHandler clears TOTP and recovery codes after verifying the
// password and exactly one second factor.
func DisableTOTPHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			writeUnauthenticated(w)
			return
		}
		var req totpFactorRequest
		if !decodeTOTPBody(w, r, &req) {
			return
		}
		if err := svc.DisableTOTP(r.Context(), principal.UserID, req.Password, req.Code, req.RecoveryCode, principal.SessionID); err != nil {
			writeTOTPManagementProblem(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// RegenerateRecoveryCodesHandler replaces every recovery code after verifying
// the password and exactly one second factor.
func RegenerateRecoveryCodesHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			writeUnauthenticated(w)
			return
		}
		var req totpFactorRequest
		if !decodeTOTPBody(w, r, &req) {
			return
		}
		codes, err := svc.RegenerateRecoveryCodes(r.Context(), principal.UserID, req.Password, req.Code, req.RecoveryCode)
		if err != nil {
			writeTOTPManagementProblem(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
	}
}

// CompleteTOTPLoginHandler consumes a login challenge and a second factor,
// sets the session cookie on success, and returns the authenticated user.
func CompleteTOTPLoginHandler(svc *Service, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req completeTOTPLoginRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeValidationProblem(w)
			return
		}
		rawToken, user, err := svc.CompleteTOTPLogin(r.Context(), req.ChallengeToken, req.Code, req.RecoveryCode, requestIP(r), r.UserAgent())
		if err != nil {
			switch {
			case errors.Is(err, ErrInvalidFactor):
				writeFactorProblem(w)
			case errors.Is(err, ErrInvalidTOTP):
				writeProblem(w, http.StatusUnauthorized, httpx.CodeInvalidTOTP,
					"Invalid authentication code")
			case errors.Is(err, ErrInvalidRecoveryCode):
				writeProblem(w, http.StatusUnauthorized, httpx.CodeInvalidRecoveryCode,
					"Invalid recovery code")
			case errors.Is(err, ErrMFAChallengeExpired):
				writeProblem(w, http.StatusGone, httpx.CodeMFAChallengeExpired,
					"Authentication challenge expired")
			case errors.Is(err, ErrMFAAttemptsExhausted):
				writeProblem(w, http.StatusTooManyRequests, httpx.CodeMFAAttemptsExhausted,
					"Too many authentication attempts")
			default:
				writeProblem(w, http.StatusInternalServerError, httpx.CodeInternalError,
					"Internal Server Error")
			}
			return
		}
		SetSessionCookie(w, cfg, rawToken)
		httpx.WriteJSON(w, http.StatusOK, loginResponse{
			User: &userProfile{ID: user.ID.String(), Email: user.Email},
		})
	}
}

// decodeTOTPBody decodes a JSON body with DisallowUnknownFields, writing a 400
// validation problem and returning false on failure.
func decodeTOTPBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeValidationProblem(w)
		return false
	}
	return true
}

// writeTOTPManagementProblem maps the account-management sentinel errors onto
// their documented problem responses. Invalid factors are 400 here because
// management requests are already authenticated.
func writeTOTPManagementProblem(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		writeProblem(w, http.StatusUnauthorized, httpx.CodeInvalidCredentials,
			"Invalid password")
	case errors.Is(err, ErrTOTPAlreadyEnabled):
		writeProblem(w, http.StatusConflict, httpx.CodeTOTPAlreadyEnabled,
			"Two-factor authentication is already enabled")
	case errors.Is(err, ErrTOTPNotEnabled):
		writeProblem(w, http.StatusConflict, httpx.CodeTOTPNotEnabled,
			"Two-factor authentication is not enabled")
	case errors.Is(err, ErrTOTPSetupExpired):
		writeProblem(w, http.StatusGone, httpx.CodeTOTPSetupExpired,
			"Two-factor setup expired")
	case errors.Is(err, ErrInvalidTOTP):
		writeProblem(w, http.StatusBadRequest, httpx.CodeInvalidTOTP,
			"Invalid authentication code")
	case errors.Is(err, ErrInvalidRecoveryCode):
		writeProblem(w, http.StatusBadRequest, httpx.CodeInvalidRecoveryCode,
			"Invalid recovery code")
	case errors.Is(err, ErrInvalidFactor):
		writeFactorProblem(w)
	default:
		writeProblem(w, http.StatusInternalServerError, httpx.CodeInternalError,
			"Internal Server Error")
	}
}

// writeFactorProblem responds 400 validation_error when a request supplies
// both or neither second factor. The fields map mirrors the other handlers so
// clients can point at the offending input.
func writeFactorProblem(w http.ResponseWriter) {
	httpx.WriteProblem(w, httpx.Problem{
		Type:   "about:blank",
		Title:  "Provide exactly one second factor",
		Status: http.StatusBadRequest,
		Code:   httpx.CodeValidationError,
		Fields: map[string]string{"factor": "provide exactly one second factor"},
	})
}

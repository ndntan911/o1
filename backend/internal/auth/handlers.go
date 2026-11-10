package auth

import (
	"encoding/json"
	"errors"
	"net/http"

	"o1/internal/config"
	"o1/internal/httpx"
)

// registerRequest is the JSON body of POST /register.
type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// registerResponse is the JSON body of a successful registration.
type registerResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// verifyEmailRequest is the JSON body of POST /verify-email.
type verifyEmailRequest struct {
	Token string `json:"token"`
}

// RegisterHandler returns a handler that validates the JSON body and
// registers a new account. Validation and malformed-JSON failures map to
// 400 validation_error, a duplicate email to 409 email_taken, and success
// to 201 with the created user's id and email.
func RegisterHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req registerRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeValidationProblem(w)
			return
		}
		user, err := svc.Register(r.Context(), req.Email, req.Password)
		if err != nil {
			switch {
			case errors.Is(err, ErrEmailTaken):
				writeProblem(w, http.StatusConflict, "email_taken",
					"Email already registered")
			case errors.Is(err, ErrValidation):
				writeProblem(w, http.StatusBadRequest, "validation_error", err.Error())
			default:
				writeProblem(w, http.StatusInternalServerError, "internal_error",
					"Internal Server Error")
			}
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, registerResponse{
			ID:    user.ID.String(),
			Email: user.Email,
		})
	}
}

// VerifyEmailHandler returns a handler that consumes a verification token
// and marks the user's email verified. It answers 204 on success and 400
// token_invalid for invalid, reused, or expired tokens.
func VerifyEmailHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req verifyEmailRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeValidationProblem(w)
			return
		}
		if err := svc.VerifyEmail(r.Context(), req.Token); err != nil {
			switch {
			case errors.Is(err, ErrTokenInvalid), errors.Is(err, ErrTokenExpired):
				writeProblem(w, http.StatusBadRequest, "token_invalid",
					"Invalid or expired verification token")
			case errors.Is(err, ErrValidation):
				writeProblem(w, http.StatusBadRequest, "validation_error", err.Error())
			default:
				writeProblem(w, http.StatusInternalServerError, "internal_error",
					"Internal Server Error")
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// loginRequest is the JSON body of POST /login.
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// userProfile is the public representation of an authenticated user.
type userProfile struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// loginResponse is the JSON body of a successful login. The password step of
// an account with a second factor instead returns mfa_required and a
// challenge_token with no user and no session cookie.
type loginResponse struct {
	User           *userProfile `json:"user,omitempty"`
	MFARequired    bool         `json:"mfa_required,omitempty"`
	ChallengeToken string       `json:"challenge_token,omitempty"`
}

// forgotPasswordRequest is the JSON body of POST /password/forgot.
type forgotPasswordRequest struct {
	Email string `json:"email"`
}

// resendVerificationRequest is the JSON body of POST /verify-email/resend.
type resendVerificationRequest struct {
	Email string `json:"email"`
}

// resetPasswordRequest is the JSON body of POST /password/reset.
type resetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// LoginHandler authenticates a user, sets the session cookie, and returns the
// user's public profile. Accounts with a second factor receive
// {mfa_required: true, challenge_token} and no cookie. Unknown emails and wrong
// passwords share the same 401 invalid_credentials response; unverified
// accounts get 403 email_not_verified.
func LoginHandler(svc *Service, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeValidationProblem(w)
			return
		}
		start, err := svc.BeginLogin(r.Context(), req.Email, req.Password, requestIP(r), r.UserAgent())
		if err != nil {
			switch {
			case errors.Is(err, ErrInvalidCredentials):
				writeProblem(w, http.StatusUnauthorized, httpx.CodeInvalidCredentials,
					"Invalid email or password")
			case errors.Is(err, ErrEmailNotVerified):
				writeProblem(w, http.StatusForbidden, httpx.CodeEmailNotVerified,
					"Email address is not verified")
			case errors.Is(err, ErrMFAAttemptsExhausted):
				writeProblem(w, http.StatusTooManyRequests, httpx.CodeMFAAttemptsExhausted,
					"Too many authentication attempts")
			default:
				writeProblem(w, http.StatusInternalServerError, "internal_error",
					"Internal Server Error")
			}
			return
		}
		if start.MFARequired {
			httpx.WriteJSON(w, http.StatusOK, loginResponse{
				MFARequired:    true,
				ChallengeToken: start.Challenge,
			})
			return
		}
		SetSessionCookie(w, cfg, start.Token)
		httpx.WriteJSON(w, http.StatusOK, loginResponse{
			User: &userProfile{ID: start.User.ID.String(), Email: start.User.Email},
		})
	}
}

// LogoutHandler deletes the session and expires the cookie. It returns 204 even
// when no session cookie was supplied, so callers can always clear state. If
// the delete fails it still expires the cookie before returning 500, so the
// client never keeps a session cookie from a failed logout.
func LogoutHandler(svc *Service, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(sessionCookieName(cfg)); err == nil && cookie.Value != "" {
			if err := svc.Logout(r.Context(), cookie.Value); err != nil {
				ClearSessionCookie(w, cfg)
				writeProblem(w, http.StatusInternalServerError, "internal_error",
					"Internal Server Error")
				return
			}
		}
		ClearSessionCookie(w, cfg)
		w.WriteHeader(http.StatusNoContent)
	}
}

// ResendVerificationHandler re-issues a verification email. Well-formed JSON
// always answers 204, including unknown and already-verified addresses, so the
// endpoint cannot be used to enumerate accounts. Malformed JSON is a 400
// validation_error.
func ResendVerificationHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req resendVerificationRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeValidationProblem(w)
			return
		}
		if err := svc.ResendVerification(r.Context(), req.Email); err != nil {
			// Validation problems look the same as an unknown address so a
			// syntactically invalid request cannot distinguish accounts.
			if !errors.Is(err, ErrValidation) {
				writeProblem(w, http.StatusInternalServerError, "internal_error",
					"Internal Server Error")
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ForgotPasswordHandler enqueues a reset email and returns 204 immediately,
// without waiting for the database lookup, token insert, or SMTP delivery.
// Unknown and unverified emails are handled identically in the background, so
// neither response bodies nor request latency reveal whether an account exists.
func ForgotPasswordHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req forgotPasswordRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeValidationProblem(w)
			return
		}
		svc.QueuePasswordReset(r.Context(), req.Email)
		w.WriteHeader(http.StatusNoContent)
	}
}

// ResetPasswordHandler consumes a single-use reset token, replaces the
// password, and revokes existing sessions. It answers 204 on success, 400
// token_invalid for unknown/used/expired tokens, and 400 validation_error for
// an unacceptable new password.
func ResetPasswordHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req resetPasswordRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeValidationProblem(w)
			return
		}
		if err := svc.ResetPassword(r.Context(), req.Token, req.NewPassword); err != nil {
			switch {
			case errors.Is(err, ErrTokenInvalid), errors.Is(err, ErrTokenExpired):
				writeProblem(w, http.StatusBadRequest, httpx.CodeTokenInvalid,
					"Invalid or expired reset token")
			case errors.Is(err, ErrValidation):
				writeProblem(w, http.StatusBadRequest, httpx.CodeValidationError, err.Error())
			default:
				writeProblem(w, http.StatusInternalServerError, "internal_error",
					"Internal Server Error")
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// requestIP delegates to httpx.ClientIP so sessions and audit records use the
// same trusted forwarded-header logic as HTTP rate limiting.
func requestIP(r *http.Request) string {
	return httpx.ClientIP(r)
}

// writeValidationProblem responds 400 with code validation_error.
func writeValidationProblem(w http.ResponseWriter) {
	writeProblem(w, http.StatusBadRequest, httpx.CodeValidationError, "Invalid request body")
}

// writeProblem emits an application/problem+json response with the given
// status and error code. It never echoes secrets, tokens, or internals.
func writeProblem(w http.ResponseWriter, status int, code, title string) {
	httpx.WriteProblem(w, httpx.Problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Code:   code,
	})
}

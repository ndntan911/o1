package auth

import (
	"encoding/json"
	"errors"
	"net/http"

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

// writeValidationProblem responds 400 with code validation_error.
func writeValidationProblem(w http.ResponseWriter) {
	writeProblem(w, http.StatusBadRequest, "validation_error", "Invalid request body")
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

-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, token_hash, expires_at, ip, user_agent)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetSessionByTokenHash :one
SELECT * FROM sessions WHERE token_hash = $1;

-- name: TouchSession :exec
UPDATE sessions
SET last_used_at = now(), expires_at = $2
WHERE id = $1;

-- name: DeleteSessionByTokenHash :exec
DELETE FROM sessions WHERE token_hash = $1;

-- name: DeleteSessionsByUserID :exec
DELETE FROM sessions WHERE user_id = $1;

-- name: CreateEmailVerificationToken :exec
INSERT INTO email_verification_tokens (id, user_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4);

-- name: GetEmailVerificationToken :one
SELECT * FROM email_verification_tokens
WHERE token_hash = $1 AND used_at IS NULL;

-- name: GetEmailVerificationTokenForUpdate :one
SELECT * FROM email_verification_tokens
WHERE token_hash = $1
FOR UPDATE;

-- name: MarkEmailVerificationTokenUsed :exec
UPDATE email_verification_tokens
SET used_at = now()
WHERE id = $1;

-- name: MarkEmailVerificationTokensUsedByUserID :exec
UPDATE email_verification_tokens
SET used_at = now()
WHERE user_id = $1 AND used_at IS NULL;

-- name: CreatePasswordResetToken :exec
INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4);

-- name: GetPasswordResetToken :one
SELECT * FROM password_reset_tokens
WHERE token_hash = $1 AND used_at IS NULL;

-- name: GetPasswordResetTokenForUpdate :one
SELECT * FROM password_reset_tokens
WHERE token_hash = $1
FOR UPDATE;

-- name: MarkPasswordResetTokenUsed :exec
UPDATE password_reset_tokens
SET used_at = now()
WHERE id = $1;

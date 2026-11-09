-- name: SetTOTPPending :exec
UPDATE users
SET totp_pending_enc = sqlc.arg(pending_enc)::text,
    totp_pending_expires_at = sqlc.arg(expires_at)::timestamptz,
    updated_at = now()
WHERE id = sqlc.arg(user_id);

-- name: GetTOTPState :one
SELECT totp_secret_enc,
       totp_pending_enc,
       totp_pending_expires_at,
       totp_enabled_at,
       totp_last_used_step
FROM users
WHERE id = sqlc.arg(user_id);

-- name: EnableTOTP :exec
UPDATE users
SET totp_secret_enc = sqlc.arg(secret_enc)::text,
    totp_enabled_at = now(),
    totp_last_used_step = sqlc.arg(last_used_step)::bigint,
    totp_pending_enc = NULL,
    totp_pending_expires_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(user_id);

-- name: DisableTOTP :exec
UPDATE users
SET totp_secret_enc = NULL,
    totp_enabled_at = NULL,
    totp_last_used_step = NULL,
    totp_pending_enc = NULL,
    totp_pending_expires_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(user_id);

-- name: ClearTOTPPending :exec
UPDATE users
SET totp_pending_enc = NULL,
    totp_pending_expires_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(user_id);

-- name: UpdateTOTPLastUsedStepIfGreater :execrows
UPDATE users
SET totp_last_used_step = sqlc.arg(last_used_step)::bigint,
    updated_at = now()
WHERE id = sqlc.arg(user_id)
  AND (totp_last_used_step IS NULL OR totp_last_used_step < sqlc.arg(last_used_step)::bigint);

-- name: CreateRecoveryCode :exec
INSERT INTO user_recovery_codes (id, user_id, code_hash)
VALUES ($1, $2, $3);

-- name: ListRecoveryCodes :many
SELECT * FROM user_recovery_codes
WHERE user_id = $1
ORDER BY created_at, id;

-- name: ListRecoveryCodesForUpdate :many
SELECT * FROM user_recovery_codes
WHERE user_id = sqlc.arg('user_id')
ORDER BY created_at, id
FOR UPDATE;

-- name: MarkRecoveryCodeUsed :execrows
UPDATE user_recovery_codes
SET used_at = sqlc.arg('used_at')
WHERE id = sqlc.arg('id') AND used_at IS NULL;

-- name: DeleteRecoveryCodes :exec
DELETE FROM user_recovery_codes
WHERE user_id = $1;

-- name: CountUnusedRecoveryCodes :one
SELECT count(*) FROM user_recovery_codes
WHERE user_id = $1 AND used_at IS NULL;

-- name: CreateMFAChallenge :exec
INSERT INTO mfa_challenges (id, user_id, token_hash, expires_at, ip, user_agent)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetMFAChallengeForUpdate :one
SELECT * FROM mfa_challenges
WHERE token_hash = $1
FOR UPDATE;

-- name: IncrementMFAChallengeAttempts :exec
UPDATE mfa_challenges
SET attempts = attempts + 1
WHERE id = $1 AND attempts < 5;

-- name: ConsumeMFAChallenge :exec
UPDATE mfa_challenges
SET consumed_at = sqlc.arg(consumed_at)::timestamptz
WHERE id = $1 AND consumed_at IS NULL;

-- name: DeleteMFAChallengesForUser :exec
DELETE FROM mfa_challenges
WHERE user_id = $1;

-- name: DeleteExpiredMFAChallenges :exec
DELETE FROM mfa_challenges
WHERE user_id = $1
  AND (expires_at < now() OR consumed_at IS NOT NULL);

-- name: DeleteOtherUserSessions :exec
DELETE FROM sessions
WHERE user_id = $1 AND id <> sqlc.arg(session_id);

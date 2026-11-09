-- name: CreateAPIKey :one
INSERT INTO api_keys (id, workspace_id, name, prefix, token_hash, role, created_by)
VALUES ($1,$2,$3,$4,$5,$6,$7)
RETURNING *;

-- name: GetActiveAPIKeyByPrefix :one
SELECT * FROM api_keys
WHERE prefix=$1 AND revoked_at IS NULL;

-- name: GetAPIKey :one
SELECT * FROM api_keys
WHERE id=$1 AND workspace_id=$2;

-- name: ListActiveAPIKeys :many
SELECT * FROM api_keys
WHERE workspace_id=$1 AND revoked_at IS NULL
ORDER BY created_at DESC, id DESC;

-- name: CountActiveAPIKeys :one
SELECT count(*) FROM api_keys
WHERE workspace_id=$1 AND revoked_at IS NULL;

-- name: RevokeAPIKey :execrows
UPDATE api_keys
SET revoked_at=now()
WHERE id=$1 AND workspace_id=$2 AND revoked_at IS NULL;

-- name: TouchAPIKeyLastUsed :exec
UPDATE api_keys
SET last_used_at=now()
WHERE id=$1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

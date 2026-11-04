-- name: CreateWorkspace :one
INSERT INTO workspaces (id, name, slug, created_by)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetWorkspaceByID :one
SELECT * FROM workspaces WHERE id = $1;

-- name: GetWorkspaceBySlug :one
SELECT * FROM workspaces WHERE slug = $1;

-- name: UpsertWorkspaceMember :exec
INSERT INTO workspace_members (workspace_id, user_id, role)
VALUES ($1, $2, $3)
ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = EXCLUDED.role;

-- name: GetWorkspaceMember :one
SELECT * FROM workspace_members WHERE workspace_id = $1 AND user_id = $2;

-- name: ListWorkspacesForUser :many
SELECT w.id, w.name, w.slug, wm.role
FROM workspaces w
JOIN workspace_members wm ON wm.workspace_id = w.id
WHERE wm.user_id = $1
ORDER BY w.created_at, w.id;

-- name: ListWorkspaceMembers :many
SELECT u.id AS user_id, u.email, wm.role
FROM workspace_members wm
JOIN users u ON u.id = wm.user_id
WHERE wm.workspace_id = $1
ORDER BY wm.created_at, u.id;

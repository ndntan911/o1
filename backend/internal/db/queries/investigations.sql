-- name: CreateInvestigation :one
INSERT INTO incident_investigations (
  id, workspace_id, incident_id, status, model, prompt_version, seed_hash,
  result, trace, attempts, tool_calls, input_tokens, output_tokens,
  duration_ms, error_code, created_by, created_at, completed_at
) VALUES (
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18
) RETURNING *;

-- name: GetInvestigation :one
SELECT * FROM incident_investigations
WHERE id=$1 AND workspace_id=$2 AND incident_id=$3;

-- name: GetInvestigationByID :one
SELECT * FROM incident_investigations WHERE id=$1;

-- name: GetLatestInvestigation :one
SELECT * FROM incident_investigations
WHERE workspace_id=$1 AND incident_id=$2
ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: MarkInvestigationRunning :execrows
UPDATE incident_investigations
SET status='running', attempts=$2, seed_hash=$3, completed_at=NULL
WHERE id=$1 AND status IN ('queued','running');

-- name: SaveInvestigationProgress :execrows
UPDATE incident_investigations
SET trace=$2, attempts=$3, tool_calls=$4, input_tokens=$5, output_tokens=$6
WHERE id=$1 AND status='running';

-- name: CompleteInvestigation :execrows
UPDATE incident_investigations
SET status=$2, result=$3, trace=$4, duration_ms=$5, error_code=$6,
    input_tokens=$7, output_tokens=$8, tool_calls=$9, completed_at=now()
WHERE id=$1 AND status IN ('queued','running');

-- name: ReapStaleInvestigations :execrows
UPDATE incident_investigations
SET status='failed', error_code='stale', completed_at=now()
WHERE incident_id=$1 AND status IN ('queued','running') AND created_at < $2;

-- name: ReapAllStaleInvestigations :execrows
UPDATE incident_investigations
SET status='failed', error_code='stale', completed_at=now()
WHERE status IN ('queued','running') AND created_at < $1;

-- name: CountInvestigationsForIncidentSince :one
SELECT count(*) FROM incident_investigations
WHERE incident_id=$1 AND created_at >= $2;

-- name: CountInvestigationsForWorkspaceSince :one
SELECT count(*) FROM incident_investigations
WHERE workspace_id=$1 AND created_at >= $2;

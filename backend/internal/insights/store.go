package insights

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store persists analyses. PostgresStore is the production implementation.
type Store interface {
	Latest(ctx context.Context, workspaceID, incidentID uuid.UUID) (*Insight, error)
	Insert(ctx context.Context, insight Insight) (Insight, error)
}

// PostgresStore keeps analyses in incident_insights.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore adapts a pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const insertInsightSQL = `
INSERT INTO incident_insights (
	id, workspace_id, incident_id, model, prompt_version, payload_hash, status,
	result, input_tokens, output_tokens, created_by, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
RETURNING id, created_at`

// Insert writes one analysis and returns it with its generated id.
func (s *PostgresStore) Insert(ctx context.Context, insight Insight) (Insight, error) {
	if insight.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return Insight{}, fmt.Errorf("insights: generate id: %w", err)
		}
		insight.ID = id
	}
	if insight.CreatedAt.IsZero() {
		return Insight{}, errors.New("insights: created_at is required")
	}

	encoded, err := json.Marshal(insight.Result)
	if err != nil {
		return Insight{}, fmt.Errorf("insights: encode result: %w", err)
	}

	var storedAt Insight
	if err := s.pool.QueryRow(
		ctx, insertInsightSQL,
		insight.ID, insight.WorkspaceID, insight.IncidentID, insight.Model,
		insight.PromptVersion, insight.PayloadHash, insight.Status, encoded,
		insight.InputTokens, insight.OutputTokens, insight.CreatedBy, insight.CreatedAt,
	).Scan(&storedAt.ID, &storedAt.CreatedAt); err != nil {
		return Insight{}, fmt.Errorf("insights: insert: %w", err)
	}

	insight.CreatedAt = storedAt.CreatedAt
	return insight, nil
}

const latestInsightSQL = `
SELECT id, workspace_id, incident_id, model, prompt_version, payload_hash, status,
       result, input_tokens, output_tokens, created_by, created_at
FROM incident_insights
WHERE workspace_id = $1 AND incident_id = $2
ORDER BY created_at DESC
LIMIT 1`

// Latest returns the most recent analysis for an incident, or nil when none
// exists.
func (s *PostgresStore) Latest(ctx context.Context, workspaceID, incidentID uuid.UUID) (*Insight, error) {
	var (
		insight Insight
		raw     []byte
	)
	err := s.pool.QueryRow(ctx, latestInsightSQL, workspaceID, incidentID).Scan(
		&insight.ID, &insight.WorkspaceID, &insight.IncidentID, &insight.Model,
		&insight.PromptVersion, &insight.PayloadHash, &insight.Status, &raw,
		&insight.InputTokens, &insight.OutputTokens, &insight.CreatedBy, &insight.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("insights: latest: %w", err)
	}
	if err := json.Unmarshal(raw, &insight.Result); err != nil {
		return nil, fmt.Errorf("insights: decode stored result: %w", err)
	}
	insight.Result = normaliseResult(insight.Result)
	return &insight, nil
}

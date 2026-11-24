package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/db/sqlc"
)

// ErrNotFound means no investigation exists for the given ids. It is also
// returned for a row in another workspace, so tenant isolation is not
// observable.
var ErrNotFound = errors.New("copilot: investigation not found")

// ErrActive means the incident already has a queued or running investigation.
// The partial unique index incident_investigations_one_active_idx enforces it.
var ErrActive = errors.New("copilot: investigation already active")

// ErrNotActive means the row is no longer queued or running, typically because
// the stale reaper already failed it. The worker's late write must not revive
// it, so MarkRunning, SaveProgress, and Complete return this instead.
var ErrNotActive = errors.New("copilot: investigation no longer active")

// oneActiveConstraint is the index name Postgres reports on the unique
// violation for a second active row.
const oneActiveConstraint = "incident_investigations_one_active_idx"

// Store persists investigations in incident_investigations.
type Store struct {
	pool *pgxpool.Pool
	q    *sqlc.Queries
}

// NewPostgresStore builds a store over a pgx pool.
func NewPostgresStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: sqlc.New(pool)}
}

// Create inserts one row in its own transaction. It returns ErrActive when the
// incident already has a queued or running run. A nil id or zero created_at is
// filled in.
func (s *Store) Create(ctx context.Context, inv Investigation) (Investigation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Investigation{}, fmt.Errorf("copilot: begin create: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	created, err := s.CreateTx(ctx, tx, inv)
	if err != nil {
		return Investigation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Investigation{}, fmt.Errorf("copilot: commit create: %w", err)
	}
	return created, nil
}

// CreateTx inserts one row on the caller's transaction. Task 10 uses it to
// write the investigation row and its River job atomically. It maps the
// one-active unique violation to ErrActive; a nil id or zero created_at is
// filled in.
func (s *Store) CreateTx(ctx context.Context, tx pgx.Tx, inv Investigation) (Investigation, error) {
	if inv.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return Investigation{}, fmt.Errorf("copilot: generate id: %w", err)
		}
		inv.ID = id
	}
	if inv.CreatedAt.IsZero() {
		inv.CreatedAt = time.Now().UTC()
	}

	result, trace, err := encodePayloads(inv)
	if err != nil {
		return Investigation{}, err
	}

	row, err := sqlc.New(tx).CreateInvestigation(ctx, sqlc.CreateInvestigationParams{
		ID:            inv.ID,
		WorkspaceID:   inv.WorkspaceID,
		IncidentID:    inv.IncidentID,
		Status:        string(inv.Status),
		Model:         inv.Model,
		PromptVersion: inv.PromptVersion,
		SeedHash:      inv.SeedHash,
		Result:        result,
		Trace:         trace,
		Attempts:      int32(inv.Attempts),
		ToolCalls:     int32(inv.ToolCalls),
		InputTokens:   int32(inv.InputTokens),
		OutputTokens:  int32(inv.OutputTokens),
		DurationMs:    int32(inv.DurationMS),
		ErrorCode:     nullableString(inv.ErrorCode),
		CreatedBy:     nullableUUID(inv.CreatedBy),
		CreatedAt:     inv.CreatedAt,
		CompletedAt:   inv.CompletedAt,
	})
	if err != nil {
		if isOneActiveViolation(err) {
			return Investigation{}, ErrActive
		}
		return Investigation{}, fmt.Errorf("copilot: create investigation: %w", err)
	}
	return decodeRow(row)
}

// Get returns one row scoped to its workspace and incident. A row in another
// workspace, or an unknown id, returns ErrNotFound.
func (s *Store) Get(ctx context.Context, workspaceID, incidentID, id uuid.UUID) (Investigation, error) {
	row, err := s.q.GetInvestigation(ctx, sqlc.GetInvestigationParams{
		ID:          id,
		WorkspaceID: workspaceID,
		IncidentID:  incidentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Investigation{}, ErrNotFound
	}
	if err != nil {
		return Investigation{}, fmt.Errorf("copilot: get investigation: %w", err)
	}
	return decodeRow(row)
}

// GetByID returns one row without workspace scoping. Only trusted internal
// callers, such as the worker, may use it; request handlers must use Get.
func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (Investigation, error) {
	row, err := s.q.GetInvestigationByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Investigation{}, ErrNotFound
	}
	if err != nil {
		return Investigation{}, fmt.Errorf("copilot: get investigation by id: %w", err)
	}
	return decodeRow(row)
}

// Latest returns the most recent row for an incident, or nil when none exists.
func (s *Store) Latest(ctx context.Context, workspaceID, incidentID uuid.UUID) (*Investigation, error) {
	row, err := s.q.GetLatestInvestigation(ctx, sqlc.GetLatestInvestigationParams{
		WorkspaceID: workspaceID,
		IncidentID:  incidentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("copilot: latest investigation: %w", err)
	}
	inv, err := decodeRow(row)
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

// MarkRunning moves a queued row to running and clears any completed_at. It
// returns ErrNotActive when the row is missing or already terminal, so a worker
// whose row was reaped cannot revive it.
func (s *Store) MarkRunning(ctx context.Context, id uuid.UUID, attempt int, seedHash string) error {
	affected, err := s.q.MarkInvestigationRunning(ctx, sqlc.MarkInvestigationRunningParams{
		ID:       id,
		Attempts: int32(attempt),
		SeedHash: seedHash,
	})
	if err != nil {
		return fmt.Errorf("copilot: mark investigation running: %w", err)
	}
	if affected == 0 {
		return ErrNotActive
	}
	return nil
}

// SaveProgress stores the trace and usage counters of a still-running row. It
// returns ErrNotActive when the row is no longer running.
func (s *Store) SaveProgress(ctx context.Context, id uuid.UUID, trace Trace, attempts, toolCalls, inputTokens, outputTokens int) error {
	encoded, err := json.Marshal(trace)
	if err != nil {
		return fmt.Errorf("copilot: encode trace: %w", err)
	}
	affected, err := s.q.SaveInvestigationProgress(ctx, sqlc.SaveInvestigationProgressParams{
		ID:           id,
		Trace:        encoded,
		Attempts:     int32(attempts),
		ToolCalls:    int32(toolCalls),
		InputTokens:  int32(inputTokens),
		OutputTokens: int32(outputTokens),
	})
	if err != nil {
		return fmt.Errorf("copilot: save investigation progress: %w", err)
	}
	if affected == 0 {
		return ErrNotActive
	}
	return nil
}

// Complete writes the terminal state of a run. completed_at is always set to
// the database clock, so the caller's value is ignored. It returns ErrNotActive
// when the row is already terminal.
func (s *Store) Complete(ctx context.Context, inv Investigation) error {
	result, trace, err := encodePayloads(inv)
	if err != nil {
		return err
	}
	affected, err := s.q.CompleteInvestigation(ctx, sqlc.CompleteInvestigationParams{
		ID:           inv.ID,
		Status:       string(inv.Status),
		Result:       result,
		Trace:        trace,
		DurationMs:   int32(inv.DurationMS),
		ErrorCode:    nullableString(inv.ErrorCode),
		InputTokens:  int32(inv.InputTokens),
		OutputTokens: int32(inv.OutputTokens),
		ToolCalls:    int32(inv.ToolCalls),
	})
	if err != nil {
		return fmt.Errorf("copilot: complete investigation: %w", err)
	}
	if affected == 0 {
		return ErrNotActive
	}
	return nil
}

// ReapStale marks the incident's rows older than before as failed/stale and
// returns how many it changed.
func (s *Store) ReapStale(ctx context.Context, incidentID uuid.UUID, before time.Time) (int64, error) {
	reaped, err := s.q.ReapStaleInvestigations(ctx, sqlc.ReapStaleInvestigationsParams{
		IncidentID: incidentID,
		CreatedAt:  before,
	})
	if err != nil {
		return 0, fmt.Errorf("copilot: reap stale investigations: %w", err)
	}
	return reaped, nil
}

// ReapAllStale is ReapStale across every incident, for the scheduler sweep.
func (s *Store) ReapAllStale(ctx context.Context, before time.Time) (int64, error) {
	reaped, err := s.q.ReapAllStaleInvestigations(ctx, before)
	if err != nil {
		return 0, fmt.Errorf("copilot: reap all stale investigations: %w", err)
	}
	return reaped, nil
}

// CountForIncidentSince counts every run for an incident created at or after
// since. It includes failed runs, which is what a daily cap wants.
func (s *Store) CountForIncidentSince(ctx context.Context, incidentID uuid.UUID, since time.Time) (int64, error) {
	count, err := s.q.CountInvestigationsForIncidentSince(ctx, sqlc.CountInvestigationsForIncidentSinceParams{
		IncidentID: incidentID,
		CreatedAt:  since,
	})
	if err != nil {
		return 0, fmt.Errorf("copilot: count investigations for incident: %w", err)
	}
	return count, nil
}

// CountForWorkspaceSince counts every run in a workspace created at or after
// since.
func (s *Store) CountForWorkspaceSince(ctx context.Context, workspaceID uuid.UUID, since time.Time) (int64, error) {
	count, err := s.q.CountInvestigationsForWorkspaceSince(ctx, sqlc.CountInvestigationsForWorkspaceSinceParams{
		WorkspaceID: workspaceID,
		CreatedAt:   since,
	})
	if err != nil {
		return 0, fmt.Errorf("copilot: count investigations for workspace: %w", err)
	}
	return count, nil
}

// encodePayloads marshals the result and trace. A nil result is stored as SQL
// NULL; the trace is always an object because the column is NOT NULL.
func encodePayloads(inv Investigation) (result, trace []byte, err error) {
	if inv.Result != nil {
		result, err = json.Marshal(inv.Result)
		if err != nil {
			return nil, nil, fmt.Errorf("copilot: encode result: %w", err)
		}
	}
	trace, err = json.Marshal(inv.Trace)
	if err != nil {
		return nil, nil, fmt.Errorf("copilot: encode trace: %w", err)
	}
	return result, trace, nil
}

// decodeRow maps a generated row to an Investigation.
func decodeRow(row sqlc.IncidentInvestigation) (Investigation, error) {
	inv := Investigation{
		ID:            row.ID,
		WorkspaceID:   row.WorkspaceID,
		IncidentID:    row.IncidentID,
		Status:        Status(row.Status),
		Model:         row.Model,
		PromptVersion: row.PromptVersion,
		SeedHash:      row.SeedHash,
		Attempts:      int(row.Attempts),
		ToolCalls:     int(row.ToolCalls),
		InputTokens:   int(row.InputTokens),
		OutputTokens:  int(row.OutputTokens),
		DurationMS:    int(row.DurationMs),
		CreatedAt:     row.CreatedAt,
		CompletedAt:   row.CompletedAt,
	}
	if row.ErrorCode != nil {
		inv.ErrorCode = *row.ErrorCode
	}
	if row.CreatedBy.Valid {
		createdBy := uuid.UUID(row.CreatedBy.Bytes)
		inv.CreatedBy = &createdBy
	}
	if len(row.Trace) > 0 {
		if err := json.Unmarshal(row.Trace, &inv.Trace); err != nil {
			return Investigation{}, fmt.Errorf("copilot: decode stored trace: %w", err)
		}
	}
	if len(row.Result) > 0 && string(row.Result) != "null" {
		var result Result
		if err := json.Unmarshal(row.Result, &result); err != nil {
			return Investigation{}, fmt.Errorf("copilot: decode stored result: %w", err)
		}
		inv.Result = &result
	}
	return inv, nil
}

func nullableUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func isOneActiveViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		pgErr.ConstraintName == oneActiveConstraint
}

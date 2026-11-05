package monitors

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"o1/internal/db/sqlc"
	"o1/internal/workspaces"
)

// Results windows supported by the API.
const (
	ResultsWindow1h  = time.Hour
	ResultsWindow6h  = 6 * time.Hour
	ResultsWindow24h = 24 * time.Hour
	ResultsWindow7d  = 7 * 24 * time.Hour
)

// ProbeResult is one raw probe outcome.
type ProbeResult struct {
	ID           uuid.UUID `json:"id"`
	CheckedAt    time.Time `json:"checked_at"`
	ScheduledFor time.Time `json:"scheduled_for"`
	Status       string    `json:"status"`
	StatusCode   *int      `json:"status_code"`
	LatencyMS    int       `json:"latency_ms"`
	Error        *string   `json:"error"`
}

// Results returns raw probe results for a monitor, newest first.
func (s *Service) Results(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID, window time.Duration, cursor *Cursor, limit int) ([]ProbeResult, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleViewer); err != nil {
		return nil, err
	}
	if err := s.ensureMonitorInWorkspace(ctx, workspaceID, monitorID); err != nil {
		return nil, err
	}
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	return s.results(ctx, workspaceID, monitorID, window, cursor, limit)
}

func (s *Service) results(ctx context.Context, workspaceID, monitorID uuid.UUID, window time.Duration, cursor *Cursor, limit int) ([]ProbeResult, error) {
	params := sqlc.ListProbeResultsParams{
		MonitorID:   monitorID,
		WorkspaceID: workspaceID,
		Since:       time.Now().UTC().Add(-window),
		PageLimit:   int32(limit), //nolint:gosec // clamped to 500 above
	}
	if cursor != nil {
		after := cursor.Time
		params.AfterTime = &after
		params.AfterID = cursor.ID
	}
	rows, err := s.q.ListProbeResults(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]ProbeResult, 0, len(rows))
	for _, row := range rows {
		out = append(out, probeResult(row))
	}
	return out, nil
}

// Summary aggregates probe results for a monitor over window.
func (s *Service) Summary(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID, window time.Duration) (Summary, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleViewer); err != nil {
		return Summary{}, err
	}
	if err := s.ensureMonitorInWorkspace(ctx, workspaceID, monitorID); err != nil {
		return Summary{}, err
	}
	row, err := s.q.MonitorSummary(ctx, sqlc.MonitorSummaryParams{
		MonitorID:   monitorID,
		WorkspaceID: workspaceID,
		Since:       time.Now().UTC().Add(-window),
	})
	if err != nil {
		return Summary{}, err
	}
	return summaryFromRow(row, window), nil
}

// ensureMonitorInWorkspace maps missing or out-of-workspace monitors onto
// ErrNotFound, matching the rest of the workspace-scoped API.
func (s *Service) ensureMonitorInWorkspace(ctx context.Context, workspaceID, monitorID uuid.UUID) error {
	_, err := s.q.GetMonitor(ctx, sqlc.GetMonitorParams{ID: monitorID, WorkspaceID: workspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load monitor: %w", err)
	}
	return nil
}

func probeResult(row sqlc.ProbeResult) ProbeResult {
	out := ProbeResult{
		ID:           row.ID,
		CheckedAt:    row.CheckedAt,
		ScheduledFor: row.ScheduledFor,
		Status:       row.Status,
		LatencyMS:    int(row.LatencyMs),
		Error:        row.Error,
	}
	if row.StatusCode != nil {
		code := int(*row.StatusCode)
		out.StatusCode = &code
	}
	return out
}

func summaryFromRow(row sqlc.MonitorSummaryRow, window time.Duration) Summary {
	out := Summary{
		Checks:       row.Checks,
		Up:           row.UpCount,
		Down:         row.DownCount,
		AvgLatencyMS: row.AvgLatencyMs,
		P95LatencyMS: row.P95LatencyMs,
		MinLatencyMS: int(row.MinLatencyMs),
		MaxLatencyMS: int(row.MaxLatencyMs),
		Window:       windowLabel(window),
	}
	if row.Checks > 0 {
		uptime := float64(row.UpCount) / float64(row.Checks)
		out.Uptime = &uptime
	}
	return out
}

func windowLabel(window time.Duration) string {
	switch window {
	case ResultsWindow1h:
		return "1h"
	case ResultsWindow6h:
		return "6h"
	case ResultsWindow7d:
		return "7d"
	default:
		return "24h"
	}
}

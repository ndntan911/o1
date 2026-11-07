package statuspage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/db/sqlc"
	"o1/internal/workspaces"
)

const (
	// maxTitleLen mirrors the status_pages title check constraint.
	maxTitleLen = 80
	// maxDescriptionLen mirrors the status_pages description check constraint.
	maxDescriptionLen = 500
	// defaultPageCacheTTL is how long public payloads may be served stale.
	defaultPageCacheTTL = 15 * time.Second
)

// Service owns status page configuration for admins and, in later work, the
// public payload aggregation and caching. It keeps the cache here so handlers
// and the public path share one instance.
type Service struct {
	pool  *pgxpool.Pool
	q     *sqlc.Queries
	ws    *workspaces.Service
	now   func() time.Time
	cache *pageCache
}

// NewService wires a status page service. The cache reads time through s.now so
// tests can advance a fake clock past the TTL.
func NewService(pool *pgxpool.Pool, q *sqlc.Queries, ws *workspaces.Service) *Service {
	s := &Service{
		pool: pool,
		q:    q,
		ws:   ws,
		now:  time.Now,
	}
	s.cache = newPageCache(defaultPageCacheTTL, func() time.Time { return s.now() })
	return s
}

// GetConfig returns the saved status page configuration for a workspace. When
// no page has been saved it returns defaults: disabled, the workspace name as
// the title, no description, and an empty (non-nil) monitor selection.
func (s *Service) GetConfig(ctx context.Context, actorID, workspaceID uuid.UUID) (Config, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleAdmin); err != nil {
		return Config{}, err
	}
	ws, err := s.q.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Config{}, ErrNotFound
		}
		return Config{}, fmt.Errorf("load workspace: %w", err)
	}

	cfg := Config{Title: ws.Name, MonitorIDs: []uuid.UUID{}}
	page, err := s.q.GetStatusPage(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("load status page: %w", err)
	}
	cfg.Enabled = page.Enabled
	cfg.Title = page.Title
	cfg.Description = page.Description

	ids, err := s.q.ListStatusPageMonitorIDs(ctx, workspaceID)
	if err != nil {
		return Config{}, fmt.Errorf("list status page monitors: %w", err)
	}
	if ids != nil {
		cfg.MonitorIDs = ids
	}
	return cfg, nil
}

// UpdateConfig validates and saves a workspace's status page configuration.
// Only admins and owners may update it, and suspended workspaces are rejected.
// The returned selection follows workspace monitor creation order rather than
// request order.
func (s *Service) UpdateConfig(ctx context.Context, actorID, workspaceID uuid.UUID, in ConfigInput) (Config, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleAdmin); err != nil {
		return Config{}, err
	}
	ws, err := s.q.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Config{}, ErrNotFound
		}
		return Config{}, fmt.Errorf("load workspace: %w", err)
	}

	selection, err := s.selectMonitors(ctx, workspaceID, in.MonitorIDs)
	if err != nil {
		return Config{}, err
	}
	if in.Enabled && len(selection) == 0 {
		return Config{}, &ValidationError{Fields: map[string]string{
			"monitor_ids": "an enabled page requires at least one monitor",
		}}
	}

	title := strings.TrimSpace(in.Title)
	description := strings.TrimSpace(in.Description)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Config{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	locked, err := qtx.LockWorkspace(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Config{}, ErrNotFound
		}
		return Config{}, fmt.Errorf("lock workspace: %w", err)
	}
	if locked.SuspendedAt != nil {
		return Config{}, ErrSuspended
	}
	if err := validateConfigText(title, description); err != nil {
		return Config{}, err
	}

	page, err := qtx.UpsertStatusPage(ctx, sqlc.UpsertStatusPageParams{
		WorkspaceID: workspaceID,
		Enabled:     in.Enabled,
		Title:       title,
		Description: description,
	})
	if err != nil {
		return Config{}, fmt.Errorf("upsert status page: %w", err)
	}
	if err := qtx.DeleteStatusPageMonitors(ctx, workspaceID); err != nil {
		return Config{}, fmt.Errorf("delete status page monitors: %w", err)
	}
	for _, monitorID := range selection {
		if err := qtx.InsertStatusPageMonitor(ctx, sqlc.InsertStatusPageMonitorParams{
			StatusPageID: workspaceID,
			MonitorID:    monitorID,
		}); err != nil {
			return Config{}, fmt.Errorf("insert status page monitor: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Config{}, fmt.Errorf("commit transaction: %w", err)
	}

	s.cache.delete(ws.Slug)
	return Config{
		Enabled:     page.Enabled,
		Title:       page.Title,
		Description: page.Description,
		MonitorIDs:  selection,
	}, nil
}

// selectMonitors dedupes requested monitor IDs and returns them in workspace
// monitor creation order. Any ID outside the workspace is rejected with a
// fixed message so raw IDs never appear in validation errors.
func (s *Service) selectMonitors(ctx context.Context, workspaceID uuid.UUID, requested []uuid.UUID) ([]uuid.UUID, error) {
	allowed, err := s.q.ListWorkspaceMonitorIDs(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list workspace monitors: %w", err)
	}
	allowedSet := make(map[uuid.UUID]struct{}, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = struct{}{}
	}
	wanted := make(map[uuid.UUID]struct{}, len(requested))
	for _, id := range requested {
		if _, ok := allowedSet[id]; !ok {
			return nil, &ValidationError{Fields: map[string]string{
				"monitor_ids": "monitor is not in this workspace",
			}}
		}
		wanted[id] = struct{}{}
	}

	selection := make([]uuid.UUID, 0, len(wanted))
	for _, id := range allowed {
		if _, ok := wanted[id]; ok {
			selection = append(selection, id)
		}
	}
	return selection, nil
}

// validateConfigText checks trimmed values against the database constraints.
func validateConfigText(title, description string) error {
	fields := make(map[string]string)
	switch {
	case title == "":
		fields["title"] = "title is required"
	case utf8.RuneCountInString(title) > maxTitleLen:
		fields["title"] = fmt.Sprintf("title must be at most %d characters", maxTitleLen)
	}
	if utf8.RuneCountInString(description) > maxDescriptionLen {
		fields["description"] = fmt.Sprintf("description must be at most %d characters", maxDescriptionLen)
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

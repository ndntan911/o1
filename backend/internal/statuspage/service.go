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
	// publicDayCount is the number of daily uptime buckets on a public page.
	publicDayCount = 90
	// publicRecentWindow is how far back resolved incidents are listed.
	publicRecentWindow = 7 * 24 * time.Hour
	// publicRecentLimit caps the resolved incident list.
	publicRecentLimit = 20
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

// PublicPage assembles the public payload for a workspace slug. Disabled or
// unknown pages return ErrNotFound so the public path cannot distinguish them.
// Only successful payloads are cached; a page assembled here never contains
// raw monitor URLs, credentials, errors, acknowledgement identity, or delivery
// details.
func (s *Service) PublicPage(ctx context.Context, slug string) (PublicPage, error) {
	if page, ok := s.cache.get(slug); ok {
		return page, nil
	}

	ws, err := s.q.GetWorkspaceBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicPage{}, ErrNotFound
		}
		return PublicPage{}, fmt.Errorf("load workspace by slug: %w", err)
	}
	cfg, err := s.q.GetStatusPage(ctx, ws.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicPage{}, ErrNotFound
		}
		return PublicPage{}, fmt.Errorf("load status page: %w", err)
	}
	if !cfg.Enabled {
		return PublicPage{}, ErrNotFound
	}

	monitors, err := s.q.ListPublicStatusMonitors(ctx, ws.ID)
	if err != nil {
		return PublicPage{}, fmt.Errorf("list public monitors: %w", err)
	}

	page := PublicPage{
		Workspace: PublicWorkspace{Name: ws.Name, Slug: ws.Slug},
		Page: PublicPageInfo{
			Title:       cfg.Title,
			Description: cfg.Description,
			UpdatedAt:   cfg.UpdatedAt,
		},
		Monitors: make([]PublicMonitor, 0, len(monitors)),
		Incidents: PublicIncidents{
			Active: []PublicIncident{},
			Recent: []PublicIncident{},
		},
	}
	if len(monitors) == 0 {
		s.cache.set(slug, page)
		return page, nil
	}

	ids := make([]uuid.UUID, 0, len(monitors))
	for _, monitor := range monitors {
		ids = append(ids, monitor.ID)
	}

	now := s.now()
	since := startOfUTCDay(now).Add(-(publicDayCount - 1) * 24 * time.Hour)
	stats, err := s.q.ListProbeDayStats(ctx, sqlc.ListProbeDayStatsParams{
		MonitorIds: ids,
		Since:      since,
	})
	if err != nil {
		return PublicPage{}, fmt.Errorf("list probe day stats: %w", err)
	}
	statsByMonitor := make(map[uuid.UUID][]sqlc.ListProbeDayStatsRow, len(monitors))
	for _, row := range stats {
		statsByMonitor[row.MonitorID] = append(statsByMonitor[row.MonitorID], row)
	}
	for _, monitor := range monitors {
		rows := statsByMonitor[monitor.ID]
		page.Monitors = append(page.Monitors, PublicMonitor{
			ID:     monitor.ID,
			Name:   monitor.Name,
			Status: monitorStatus(monitor.Paused, monitor.Status),
			Uptime: buildUptime(rows),
			Days:   buildDays(rows, since, now),
		})
	}

	active, err := s.q.ListPublicActiveIncidents(ctx, ids)
	if err != nil {
		return PublicPage{}, fmt.Errorf("list active incidents: %w", err)
	}
	for _, incident := range active {
		page.Incidents.Active = append(page.Incidents.Active, PublicIncident{
			ID:          incident.ID,
			MonitorID:   incident.MonitorID,
			MonitorName: incident.MonitorName,
			Status:      "ongoing",
			StartedAt:   incident.StartedAt,
		})
	}

	recent, err := s.q.ListPublicRecentIncidents(ctx, sqlc.ListPublicRecentIncidentsParams{
		MonitorIds: ids,
		Since:      now.Add(-publicRecentWindow),
		PageLimit:  publicRecentLimit,
	})
	if err != nil {
		return PublicPage{}, fmt.Errorf("list recent incidents: %w", err)
	}
	for _, incident := range recent {
		page.Incidents.Recent = append(page.Incidents.Recent, PublicIncident{
			ID:          incident.ID,
			MonitorID:   incident.MonitorID,
			MonitorName: incident.MonitorName,
			Status:      "resolved",
			StartedAt:   incident.StartedAt,
			ResolvedAt:  incident.ResolvedAt,
		})
	}

	s.cache.set(slug, page)
	return page, nil
}

// startOfUTCDay truncates t to midnight UTC.
func startOfUTCDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// buildDays returns publicDayCount daily buckets ending on the UTC day of now.
// Days are keyed by the SQL date string YYYY-MM-DD and marked no_data, up,
// down, or partial.
func buildDays(stats []sqlc.ListProbeDayStatsRow, since, now time.Time) []PublicDay {
	byDay := make(map[string]sqlc.ListProbeDayStatsRow, len(stats))
	for _, row := range stats {
		byDay[row.Day] = row
	}

	first := startOfUTCDay(since)
	if last := startOfUTCDay(now).Add(-(publicDayCount - 1) * 24 * time.Hour); last.After(first) {
		first = last
	}

	days := make([]PublicDay, 0, publicDayCount)
	for i := 0; i < publicDayCount; i++ {
		date := first.AddDate(0, 0, i)
		key := date.Format("2006-01-02")
		row, ok := byDay[key]
		status := "no_data"
		var uptime *float64
		if ok && row.UpCount+row.DownCount > 0 {
			value := float64(row.UpCount) / float64(row.UpCount+row.DownCount)
			uptime = &value
			switch {
			case row.DownCount == 0:
				status = "up"
			case row.UpCount == 0:
				status = "down"
			default:
				status = "partial"
			}
		}
		days = append(days, PublicDay{Date: key, Status: status, Uptime: uptime})
	}
	return days
}

// buildUptime returns the share of successful checks across the rows, or nil
// when the monitor has no checks in the window.
func buildUptime(stats []sqlc.ListProbeDayStatsRow) *float64 {
	var checks, up int64
	for _, row := range stats {
		checks += row.Checks
		up += row.UpCount
	}
	if checks == 0 {
		return nil
	}
	value := float64(up) / float64(checks)
	return &value
}

// monitorStatus maps a paused monitor to "paused"; otherwise it reports the
// stored monitor state.
func monitorStatus(paused bool, state string) string {
	if paused {
		return "paused"
	}
	return state
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

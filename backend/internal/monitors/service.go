package monitors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/audit"
	"o1/internal/auth"
	"o1/internal/config"
	"o1/internal/db/sqlc"
	"o1/internal/probe"
	"o1/internal/secretbox"
	"o1/internal/workspaces"
)

// Input is the create payload for a monitor. Body and ContentType are
// optional; an empty body pointer means no body.
type Input struct {
	Name                  string            `json:"name"`
	URL                   string            `json:"url"`
	Method                string            `json:"method"`
	Headers               map[string]string `json:"headers"`
	Body                  *string           `json:"body"`
	ContentType           *string           `json:"content_type"`
	ExpectedStatus        string            `json:"expected_status"`
	IntervalSeconds       int               `json:"interval_seconds"`
	TimeoutSeconds        int               `json:"timeout_seconds"`
	ConfirmationThreshold int               `json:"confirmation_threshold"`
	RecoveryThreshold     int               `json:"recovery_threshold"`
}

// UpdateInput is a partial update. Nil pointers and a nil Headers map mean
// "leave unchanged". Setting Body to a pointer to "" clears the body (and the
// content type).
type UpdateInput struct {
	Name                  *string           `json:"name"`
	URL                   *string           `json:"url"`
	Method                *string           `json:"method"`
	Headers               map[string]string `json:"headers"`
	Body                  *string           `json:"body"`
	ContentType           *string           `json:"content_type"`
	ExpectedStatus        *string           `json:"expected_status"`
	IntervalSeconds       *int              `json:"interval_seconds"`
	TimeoutSeconds        *int              `json:"timeout_seconds"`
	ConfirmationThreshold *int              `json:"confirmation_threshold"`
	RecoveryThreshold     *int              `json:"recovery_threshold"`
}

// LastCheck is the most recent probe result for a monitor.
type LastCheck struct {
	CheckedAt  time.Time `json:"checked_at"`
	StatusCode *int      `json:"status_code"`
	LatencyMS  *int      `json:"latency_ms"`
	Error      *string   `json:"error"`
}

// State is the live scheduling and health state of a monitor.
type State struct {
	Status               string
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	LastCheckedAt        *time.Time
	LastStatusCode       *int
	LastLatencyMS        *int
	LastError            *string
	NextCheckAt          time.Time
}

// Summary aggregates probe results over a window. Uptime is nil when Checks
// is zero.
type Summary struct {
	Window       string   `json:"window"`
	Uptime       *float64 `json:"uptime"`
	Checks       int64    `json:"checks"`
	Up           int64    `json:"up"`
	Down         int64    `json:"down"`
	AvgLatencyMS float64  `json:"avg_latency_ms"`
	P95LatencyMS float64  `json:"p95_latency_ms"`
	MinLatencyMS int      `json:"min_latency_ms"`
	MaxLatencyMS int      `json:"max_latency_ms"`
}

// Monitor is the API-facing monitor object. Headers contains header names
// with masked values; plaintext header values never leave the service.
type Monitor struct {
	ID                    uuid.UUID         `json:"id"`
	WorkspaceID           uuid.UUID         `json:"workspace_id"`
	Name                  string            `json:"name"`
	URL                   string            `json:"url"`
	Method                string            `json:"method"`
	Headers               map[string]string `json:"headers"`
	HasBody               bool              `json:"has_body"`
	ContentType           *string           `json:"content_type"`
	ExpectedStatus        string            `json:"expected_status"`
	IntervalSeconds       int               `json:"interval_seconds"`
	TimeoutSeconds        int               `json:"timeout_seconds"`
	ConfirmationThreshold int               `json:"confirmation_threshold"`
	RecoveryThreshold     int               `json:"recovery_threshold"`
	Paused                bool              `json:"paused"`
	Status                string            `json:"status"`
	LastCheck             *LastCheck        `json:"last_check"`
	Summary24h            *Summary          `json:"summary_24h"`
	CreatedAt             time.Time         `json:"created_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
}

// Cursor is a keyset pagination cursor: the last row's creation time and ID.
type Cursor struct {
	Time time.Time `json:"created_at"`
	ID   uuid.UUID `json:"id"`
}

// Service implements monitor CRUD, authorization, quotas, rate limiting, and
// secret encryption on top of the workspace schema.
type Service struct {
	pool  *pgxpool.Pool
	q     *sqlc.Queries
	ws    *workspaces.Service
	box   *secretbox.Box
	guard *probe.Guard
	cfg   config.Config
}

// NewService wires a Service together.
func NewService(pool *pgxpool.Pool, q *sqlc.Queries, ws *workspaces.Service, box *secretbox.Box, guard *probe.Guard, cfg config.Config) *Service {
	return &Service{pool: pool, q: q, ws: ws, box: box, guard: guard, cfg: cfg}
}

// Create authorizes a member, validates the input, and inserts the monitor,
// its state row, and a monitor_created security event in one transaction. The
// workspace row lock serializes concurrent creates so quota and rate limits
// cannot be exceeded.
func (s *Service) Create(ctx context.Context, actorID, workspaceID uuid.UUID, in Input) (Monitor, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleMember); err != nil {
		return Monitor{}, err
	}
	// Check suspension before validation so a suspended workspace always
	// reports ErrSuspended, even for malformed input. The create transaction
	// rechecks under the workspace lock.
	if err := s.ensureActiveWorkspace(ctx, workspaceID); err != nil {
		return Monitor{}, err
	}
	normalized, err := validateContext(ctx, in, s.guard, s.cfg.MonitorMinInterval, s.cfg.MonitorMaxInterval)
	if err != nil {
		s.recordSSRFBlocked(ctx, workspaceID, actorID, in.URL, err)
		return Monitor{}, err
	}

	created, err := s.create(ctx, workspaceID, actorID, normalized)
	if err != nil {
		var rl *RateLimitError
		if errors.As(err, &rl) {
			_ = s.insertEvent(ctx, s.q, workspaceID, actorID, "monitor_create_rate_limited", map[string]any{
				"limit":               s.createLimit(),
				"window_seconds":      3600,
				"retry_after_seconds": int64(rl.RetryAfter / time.Second),
			})
		}
		return Monitor{}, err
	}
	return s.Get(ctx, actorID, workspaceID, created)
}

// create runs the serialized insert portion of Create.
func (s *Service) create(ctx context.Context, workspaceID, actorID uuid.UUID, in Input) (uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	locked, err := qtx.LockWorkspace(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrNotFound
		}
		return uuid.Nil, fmt.Errorf("lock workspace: %w", err)
	}
	if locked.SuspendedAt != nil {
		return uuid.Nil, ErrSuspended
	}

	count, err := qtx.CountMonitors(ctx, workspaceID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("count monitors: %w", err)
	}
	if count >= int64(locked.MonitorLimit) {
		return uuid.Nil, ErrQuotaExceeded
	}

	now := time.Now().UTC()
	since := now.Add(-time.Hour)
	recent, err := qtx.CountMonitorCreationsSince(ctx, sqlc.CountMonitorCreationsSinceParams{
		WorkspaceID: workspaceID,
		CreatedAt:   since,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("count recent creations: %w", err)
	}
	if recent >= int64(s.createLimit()) {
		oldest, err := qtx.OldestMonitorCreationSince(ctx, sqlc.OldestMonitorCreationSinceParams{
			WorkspaceID: workspaceID,
			CreatedAt:   since,
		})
		if err != nil {
			return uuid.Nil, fmt.Errorf("load oldest creation: %w", err)
		}
		retryAfter := oldest.Add(time.Hour).Sub(now)
		if retryAfter <= 0 {
			retryAfter = time.Second
		}
		return uuid.Nil, &RateLimitError{RetryAfter: retryAfter}
	}

	headersEnc, err := s.encryptHeaders(in.Headers)
	if err != nil {
		return uuid.Nil, err
	}
	bodyEnc, err := s.encryptBody(in.Body)
	if err != nil {
		return uuid.Nil, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("generate monitor id: %w", err)
	}
	monitor, err := qtx.CreateMonitor(ctx, sqlc.CreateMonitorParams{
		ID:                    id,
		WorkspaceID:           workspaceID,
		Name:                  in.Name,
		Url:                   in.URL,
		Method:                in.Method,
		HeadersEnc:            headersEnc,
		BodyEnc:               bodyEnc,
		ContentType:           in.ContentType,
		ExpectedStatus:        in.ExpectedStatus,
		IntervalSeconds:       int32(in.IntervalSeconds),       //nolint:gosec // bounded by validation
		TimeoutSeconds:        int32(in.TimeoutSeconds),        //nolint:gosec // bounded by validation
		ConfirmationThreshold: int16(in.ConfirmationThreshold), //nolint:gosec // bounded by validation
		RecoveryThreshold:     int16(in.RecoveryThreshold),     //nolint:gosec // bounded by validation
		Paused:                false,
		CreatedBy:             actorID,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("create monitor: %w", err)
	}
	// CreateMonitorState uses the schema default next_check_at = now(), so a
	// new monitor is due on the next scheduler tick.
	if err := qtx.CreateMonitorState(ctx, monitor.ID); err != nil {
		return uuid.Nil, fmt.Errorf("create monitor state: %w", err)
	}
	// A monitor created after channels exist starts with every per-monitor
	// switch enabled, including channels disabled globally. This mirrors the
	// auto-attach behavior when a channel itself is created.
	channels, err := qtx.ListAlertChannels(ctx, workspaceID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("list alert channels: %w", err)
	}
	for _, channel := range channels {
		if err := qtx.UpsertMonitorAlertChannel(ctx, sqlc.UpsertMonitorAlertChannelParams{
			MonitorID: monitor.ID,
			ChannelID: channel.ID,
			Enabled:   true,
		}); err != nil {
			return uuid.Nil, fmt.Errorf("attach alert channel %s: %w", channel.ID, err)
		}
	}
	if err := s.insertEvent(ctx, qtx, workspaceID, actorID, "monitor_created", map[string]any{
		"url": redactURL(in.URL),
	}); err != nil {
		return uuid.Nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("commit monitor: %w", err)
	}
	return monitor.ID, nil
}

// List returns one page of monitors, newest first, with masked header names
// and 24-hour summaries.
func (s *Service) List(ctx context.Context, actorID, workspaceID uuid.UUID, cursor *Cursor, limit int) ([]Monitor, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleViewer); err != nil {
		return nil, err
	}

	params := sqlc.ListMonitorsParams{
		WorkspaceID: workspaceID,
		AfterID:     uuid.Nil,
		PageLimit:   clampLimit(limit),
	}
	if cursor != nil {
		params.AfterTime = &cursor.Time
		params.AfterID = cursor.ID
	}
	rows, err := s.q.ListMonitors(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list monitors: %w", err)
	}
	if len(rows) == 0 {
		return []Monitor{}, nil
	}

	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	summaries := make(map[uuid.UUID]Summary, len(rows))
	summaryRows, err := s.q.ListMonitorSummaries(ctx, sqlc.ListMonitorSummariesParams{
		MonitorIds: ids,
		Since:      time.Now().UTC().Add(-24 * time.Hour),
	})
	if err != nil {
		return nil, fmt.Errorf("list monitor summaries: %w", err)
	}
	for _, row := range summaryRows {
		if row.Checks == 0 {
			continue
		}
		summaries[row.MonitorID] = Summary{
			Window:       "24h",
			Uptime:       uptime(row.UpCount, row.Checks),
			Checks:       row.Checks,
			Up:           row.UpCount,
			Down:         row.DownCount,
			AvgLatencyMS: row.AvgLatencyMs,
			P95LatencyMS: row.P95LatencyMs,
			MinLatencyMS: int(row.MinLatencyMs),
			MaxLatencyMS: int(row.MaxLatencyMs),
		}
	}

	out := make([]Monitor, 0, len(rows))
	for _, row := range rows {
		var summary *Summary
		if got, ok := summaries[row.ID]; ok {
			got := got
			summary = &got
		}
		view, err := s.monitorView(monitorRow{
			ID:                    row.ID,
			WorkspaceID:           row.WorkspaceID,
			Name:                  row.Name,
			URL:                   row.Url,
			Method:                row.Method,
			HeadersEnc:            row.HeadersEnc,
			BodyEnc:               row.BodyEnc,
			ContentType:           row.ContentType,
			ExpectedStatus:        row.ExpectedStatus,
			IntervalSeconds:       row.IntervalSeconds,
			TimeoutSeconds:        row.TimeoutSeconds,
			ConfirmationThreshold: row.ConfirmationThreshold,
			RecoveryThreshold:     row.RecoveryThreshold,
			Paused:                row.Paused,
			CreatedAt:             row.CreatedAt,
			UpdatedAt:             row.UpdatedAt,
			Status:                row.Status,
			LastCheckedAt:         row.LastCheckedAt,
			LastStatusCode:        row.LastStatusCode,
			LastLatencyMS:         row.LastLatencyMs,
			LastError:             row.LastError,
		}, summary)
		if err != nil {
			return nil, err
		}
		out = append(out, view)
	}
	return out, nil
}

// Get returns a monitor with its state and 24-hour summary. Non-members get
// ErrNotFound through Authorize.
func (s *Service) Get(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID) (Monitor, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleViewer); err != nil {
		return Monitor{}, err
	}
	monitor, err := s.q.GetMonitor(ctx, sqlc.GetMonitorParams{ID: monitorID, WorkspaceID: workspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Monitor{}, ErrNotFound
		}
		return Monitor{}, fmt.Errorf("load monitor: %w", err)
	}
	state, err := s.q.GetMonitorState(ctx, monitorID)
	if err != nil {
		return Monitor{}, fmt.Errorf("load monitor state: %w", err)
	}
	summaryRow, err := s.q.MonitorSummary(ctx, sqlc.MonitorSummaryParams{
		MonitorID:   monitorID,
		WorkspaceID: workspaceID,
		Since:       time.Now().UTC().Add(-24 * time.Hour),
	})
	if err != nil {
		return Monitor{}, fmt.Errorf("load monitor summary: %w", err)
	}

	var summary *Summary
	if summaryRow.Checks > 0 {
		summary = &Summary{
			Window:       "24h",
			Uptime:       uptime(summaryRow.UpCount, summaryRow.Checks),
			Checks:       summaryRow.Checks,
			Up:           summaryRow.UpCount,
			Down:         summaryRow.DownCount,
			AvgLatencyMS: summaryRow.AvgLatencyMs,
			P95LatencyMS: summaryRow.P95LatencyMs,
			MinLatencyMS: int(summaryRow.MinLatencyMs),
			MaxLatencyMS: int(summaryRow.MaxLatencyMs),
		}
	}
	return s.monitorView(monitorRow{
		ID:                    monitor.ID,
		WorkspaceID:           monitor.WorkspaceID,
		Name:                  monitor.Name,
		URL:                   monitor.Url,
		Method:                monitor.Method,
		HeadersEnc:            monitor.HeadersEnc,
		BodyEnc:               monitor.BodyEnc,
		ContentType:           monitor.ContentType,
		ExpectedStatus:        monitor.ExpectedStatus,
		IntervalSeconds:       monitor.IntervalSeconds,
		TimeoutSeconds:        monitor.TimeoutSeconds,
		ConfirmationThreshold: monitor.ConfirmationThreshold,
		RecoveryThreshold:     monitor.RecoveryThreshold,
		Paused:                monitor.Paused,
		CreatedAt:             monitor.CreatedAt,
		UpdatedAt:             monitor.UpdatedAt,
		Status:                state.Status,
		LastCheckedAt:         state.LastCheckedAt,
		LastStatusCode:        state.LastStatusCode,
		LastLatencyMS:         state.LastLatencyMs,
		LastError:             state.LastError,
	}, summary)
}

// Update merges a partial update over the stored monitor, revalidates, and
// writes the full row. Changing the configuration triggers an immediate
// check. Suspended workspaces return ErrSuspended.
func (s *Service) Update(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID, in UpdateInput) (Monitor, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleMember); err != nil {
		return Monitor{}, err
	}
	if err := s.update(ctx, workspaceID, monitorID, in); err != nil {
		rawURL := ""
		if in.URL != nil {
			rawURL = *in.URL
		}
		s.recordSSRFBlocked(ctx, workspaceID, actorID, rawURL, err)
		return Monitor{}, err
	}
	return s.Get(ctx, actorID, workspaceID, monitorID)
}

// update runs the serialized portion of Update.
func (s *Service) update(ctx context.Context, workspaceID, monitorID uuid.UUID, in UpdateInput) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	locked, err := qtx.LockWorkspace(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock workspace: %w", err)
	}
	if locked.SuspendedAt != nil {
		return ErrSuspended
	}

	monitor, err := qtx.GetMonitor(ctx, sqlc.GetMonitorParams{ID: monitorID, WorkspaceID: workspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load monitor: %w", err)
	}

	storedHeaders, err := s.decryptHeaders(monitor.HeadersEnc)
	if err != nil {
		return err
	}
	storedBody, err := s.decryptBody(monitor.BodyEnc)
	if err != nil {
		return err
	}

	merged := Input{
		Name:                  monitor.Name,
		URL:                   monitor.Url,
		Method:                monitor.Method,
		Headers:               storedHeaders,
		Body:                  storedBody,
		ContentType:           monitor.ContentType,
		ExpectedStatus:        monitor.ExpectedStatus,
		IntervalSeconds:       int(monitor.IntervalSeconds),
		TimeoutSeconds:        int(monitor.TimeoutSeconds),
		ConfirmationThreshold: int(monitor.ConfirmationThreshold),
		RecoveryThreshold:     int(monitor.RecoveryThreshold),
	}
	if in.Name != nil {
		merged.Name = *in.Name
	}
	if in.URL != nil {
		merged.URL = *in.URL
	}
	if in.Method != nil {
		merged.Method = *in.Method
	}
	if in.Headers != nil {
		merged.Headers = in.Headers
	}
	if in.Body != nil {
		if *in.Body == "" {
			merged.Body = nil
			merged.ContentType = nil
		} else {
			merged.Body = in.Body
		}
	}
	if in.ContentType != nil {
		if *in.ContentType == "" {
			merged.ContentType = nil
		} else {
			merged.ContentType = in.ContentType
		}
	}
	if in.ExpectedStatus != nil {
		merged.ExpectedStatus = *in.ExpectedStatus
	}
	if in.IntervalSeconds != nil {
		merged.IntervalSeconds = *in.IntervalSeconds
	}
	if in.TimeoutSeconds != nil {
		merged.TimeoutSeconds = *in.TimeoutSeconds
	}
	if in.ConfirmationThreshold != nil {
		merged.ConfirmationThreshold = *in.ConfirmationThreshold
	}
	if in.RecoveryThreshold != nil {
		merged.RecoveryThreshold = *in.RecoveryThreshold
	}

	normalized, err := validateContext(ctx, merged, s.guard, s.cfg.MonitorMinInterval, s.cfg.MonitorMaxInterval)
	if err != nil {
		return err
	}

	headersEnc, err := s.encryptHeaders(normalized.Headers)
	if err != nil {
		return err
	}
	bodyEnc, err := s.encryptBody(normalized.Body)
	if err != nil {
		return err
	}

	updated, err := qtx.UpdateMonitor(ctx, sqlc.UpdateMonitorParams{
		ID:                    monitorID,
		WorkspaceID:           workspaceID,
		Name:                  normalized.Name,
		Url:                   normalized.URL,
		Method:                normalized.Method,
		HeadersEnc:            headersEnc,
		BodyEnc:               bodyEnc,
		ContentType:           normalized.ContentType,
		ExpectedStatus:        normalized.ExpectedStatus,
		IntervalSeconds:       int32(normalized.IntervalSeconds),       //nolint:gosec // bounded by validation
		TimeoutSeconds:        int32(normalized.TimeoutSeconds),        //nolint:gosec // bounded by validation
		ConfirmationThreshold: int16(normalized.ConfirmationThreshold), //nolint:gosec // bounded by validation
		RecoveryThreshold:     int16(normalized.RecoveryThreshold),     //nolint:gosec // bounded by validation
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("update monitor: %w", err)
	}
	if err := qtx.SetNextCheckAt(ctx, sqlc.SetNextCheckAtParams{
		MonitorID:   updated.ID,
		NextCheckAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("schedule next check: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit monitor update: %w", err)
	}
	return nil
}

// Delete hard-deletes a monitor, cascading its state and results. Suspended
// workspaces return ErrSuspended.
func (s *Service) Delete(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID) error {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleMember); err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	locked, err := qtx.LockWorkspace(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock workspace: %w", err)
	}
	if locked.SuspendedAt != nil {
		return ErrSuspended
	}

	monitor, err := qtx.GetMonitor(ctx, sqlc.GetMonitorParams{ID: monitorID, WorkspaceID: workspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load monitor: %w", err)
	}

	deleted, err := qtx.DeleteMonitor(ctx, sqlc.DeleteMonitorParams{ID: monitorID, WorkspaceID: workspaceID})
	if err != nil {
		return fmt.Errorf("delete monitor: %w", err)
	}
	if deleted == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit monitor delete: %w", err)
	}
	audit.RecordBestEffort(ctx, s.q, audit.Event{
		WorkspaceID:   &workspaceID,
		ActorUserID:   &actorID,
		ActorAPIKeyID: auth.APIKeyIDFrom(ctx),
		Kind:          audit.KindMonitorDeleted,
		Detail:        map[string]any{"monitor_id": monitorID.String(), "name": monitor.Name},
	})
	return nil
}

// SetPaused pauses or resumes a monitor. Both directions are idempotent;
// resuming also makes the monitor due immediately without resetting its
// health counters.
func (s *Service) SetPaused(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID, paused bool) (Monitor, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleMember); err != nil {
		return Monitor{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Monitor{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	locked, err := qtx.LockWorkspace(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Monitor{}, ErrNotFound
		}
		return Monitor{}, fmt.Errorf("lock workspace: %w", err)
	}
	if locked.SuspendedAt != nil {
		return Monitor{}, ErrSuspended
	}

	if _, err := qtx.SetMonitorPaused(ctx, sqlc.SetMonitorPausedParams{
		ID:          monitorID,
		WorkspaceID: workspaceID,
		Paused:      paused,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Monitor{}, ErrNotFound
		}
		return Monitor{}, fmt.Errorf("set monitor paused: %w", err)
	}
	if !paused {
		if err := qtx.ResumeMonitorState(ctx, monitorID); err != nil {
			return Monitor{}, fmt.Errorf("resume monitor state: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Monitor{}, fmt.Errorf("commit monitor pause: %w", err)
	}
	return s.Get(ctx, actorID, workspaceID, monitorID)
}

// monitorRow is the shared input for turning a database row into a Monitor.
type monitorRow struct {
	ID                    uuid.UUID
	WorkspaceID           uuid.UUID
	Name                  string
	URL                   string
	Method                string
	HeadersEnc            *string
	BodyEnc               *string
	ContentType           *string
	ExpectedStatus        string
	IntervalSeconds       int32
	TimeoutSeconds        int32
	ConfirmationThreshold int16
	RecoveryThreshold     int16
	Paused                bool
	CreatedAt             time.Time
	UpdatedAt             time.Time
	Status                string
	LastCheckedAt         *time.Time
	LastStatusCode        *int32
	LastLatencyMS         *int32
	LastError             *string
}

// monitorView masks secrets and derives the API status.
func (s *Service) monitorView(row monitorRow, summary *Summary) (Monitor, error) {
	headers, err := s.maskedHeaders(row.HeadersEnc)
	if err != nil {
		return Monitor{}, err
	}
	status := row.Status
	if row.Paused {
		status = "paused"
	}
	monitor := Monitor{
		ID:                    row.ID,
		WorkspaceID:           row.WorkspaceID,
		Name:                  row.Name,
		URL:                   row.URL,
		Method:                row.Method,
		Headers:               headers,
		HasBody:               row.BodyEnc != nil && *row.BodyEnc != "",
		ContentType:           row.ContentType,
		ExpectedStatus:        row.ExpectedStatus,
		IntervalSeconds:       int(row.IntervalSeconds),
		TimeoutSeconds:        int(row.TimeoutSeconds),
		ConfirmationThreshold: int(row.ConfirmationThreshold),
		RecoveryThreshold:     int(row.RecoveryThreshold),
		Paused:                row.Paused,
		Status:                status,
		Summary24h:            summary,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}
	if row.LastCheckedAt != nil {
		monitor.LastCheck = &LastCheck{
			CheckedAt:  *row.LastCheckedAt,
			StatusCode: intFrom32(row.LastStatusCode),
			LatencyMS:  intFrom32(row.LastLatencyMS),
			Error:      row.LastError,
		}
	}
	return monitor, nil
}

// maskedHeaders decrypts the header map only to expose its names with "***"
// values. It returns an empty (non-nil) map when the monitor has no headers,
// so the JSON shape is always an object.
func (s *Service) maskedHeaders(enc *string) (map[string]string, error) {
	headers, err := s.decryptHeaders(enc)
	if err != nil {
		return nil, err
	}
	masked := make(map[string]string, len(headers))
	for name := range headers {
		masked[name] = "***"
	}
	return masked, nil
}

// decryptHeaders returns the plaintext header map, or nil when no headers are
// stored.
func (s *Service) decryptHeaders(enc *string) (map[string]string, error) {
	if enc == nil || *enc == "" {
		return nil, nil
	}
	raw, err := s.box.Open(*enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt headers: %w", err)
	}
	var headers map[string]string
	if err := json.Unmarshal(raw, &headers); err != nil {
		return nil, fmt.Errorf("decode headers: %w", err)
	}
	return headers, nil
}

// decryptBody returns the plaintext body, or nil when no body is stored.
func (s *Service) decryptBody(enc *string) (*string, error) {
	if enc == nil || *enc == "" {
		return nil, nil
	}
	raw, err := s.box.Open(*enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt body: %w", err)
	}
	body := string(raw)
	if body == "" {
		return nil, nil
	}
	return &body, nil
}

// encryptHeaders seals the JSON header map. Empty headers are stored as SQL
// NULL.
func (s *Service) encryptHeaders(headers map[string]string) (*string, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(headers)
	if err != nil {
		return nil, fmt.Errorf("encode headers: %w", err)
	}
	sealed, err := s.box.Seal(raw)
	if err != nil {
		return nil, fmt.Errorf("encrypt headers: %w", err)
	}
	if sealed == "" {
		return nil, nil
	}
	return &sealed, nil
}

// encryptBody seals the request body. Empty bodies are stored as SQL NULL.
func (s *Service) encryptBody(body *string) (*string, error) {
	if body == nil || *body == "" {
		return nil, nil
	}
	sealed, err := s.box.Seal([]byte(*body))
	if err != nil {
		return nil, fmt.Errorf("encrypt body: %w", err)
	}
	if sealed == "" {
		return nil, nil
	}
	return &sealed, nil
}

// recordSSRFBlocked records an ssrf_blocked event when err wraps
// probe.ErrBlocked. Event failures are deliberately ignored: the caller must
// still see the validation error even if the audit insert fails.
func (s *Service) recordSSRFBlocked(ctx context.Context, workspaceID, actorID uuid.UUID, rawURL string, err error) {
	if !errors.Is(err, probe.ErrBlocked) {
		return
	}
	detail := map[string]any{"reason": "url is not allowed"}
	if rawURL != "" {
		detail["url"] = redactURL(rawURL)
	}
	_ = s.insertEvent(ctx, s.q, workspaceID, actorID, "ssrf_blocked", detail)
}

// insertEvent writes one security event. Detail must never contain header
// values, bodies, or raw query strings.
func (s *Service) insertEvent(ctx context.Context, q *sqlc.Queries, workspaceID, actorID uuid.UUID, kind string, detail map[string]any) error {
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate %s event id: %w", kind, err)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode %s event: %w", kind, err)
	}
	if err := q.CreateSecurityEvent(ctx, sqlc.CreateSecurityEventParams{
		ID:            id,
		WorkspaceID:   pgUUID(workspaceID),
		ActorUserID:   pgUUID(actorID),
		Kind:          kind,
		Detail:        raw,
		ActorAPIKeyID: optionalPGUUID(auth.APIKeyIDFrom(ctx)),
	}); err != nil {
		return fmt.Errorf("record %s event: %w", kind, err)
	}
	return nil
}

// ensureActiveWorkspace rejects mutations in a suspended workspace. The
// FOR UPDATE lock is released immediately because this statement runs in
// autocommit; the create transaction takes its own lock before inserting.
func (s *Service) ensureActiveWorkspace(ctx context.Context, workspaceID uuid.UUID) error {
	locked, err := s.q.LockWorkspace(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock workspace: %w", err)
	}
	if locked.SuspendedAt != nil {
		return ErrSuspended
	}
	return nil
}

// createLimit returns the configured per-hour creation limit, falling back to
// the documented default when the config was not populated.
func (s *Service) createLimit() int {
	if s.cfg.MonitorCreateLimit > 0 {
		return s.cfg.MonitorCreateLimit
	}
	return 10
}

// clampLimit applies the list default and maximum.
func clampLimit(limit int) int32 {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	return int32(limit)
}

// uptime computes up/checks. Callers only call it when checks is non-zero.
func uptime(up, checks int64) *float64 {
	if checks == 0 {
		return nil
	}
	value := float64(up) / float64(checks)
	return &value
}

// intFrom32 converts an optional database integer to an optional int.
func intFrom32(value *int32) *int {
	if value == nil {
		return nil
	}
	out := int(*value)
	return &out
}

// pgUUID converts a required UUID into the nullable pgtype form.
func pgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

// optionalPGUUID converts a nullable UUID pointer into the pgtype form.
func optionalPGUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

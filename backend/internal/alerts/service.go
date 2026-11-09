package alerts

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/audit"
	"o1/internal/auth"
	"o1/internal/db/sqlc"
	"o1/internal/email"
	"o1/internal/probe"
	"o1/internal/secretbox"
	"o1/internal/workspaces"
)

const (
	// maxChannelsPerKind caps active channels of one kind per workspace.
	maxChannelsPerKind = 10
	// maxChannelsTotal caps all active channels per workspace.
	maxChannelsTotal = 30
)

// Service owns alert channel CRUD. Incident enqueue and delivery stay in the
// incidents and worker packages.
type Service struct {
	pool     *pgxpool.Pool
	q        *sqlc.Queries
	ws       *workspaces.Service
	box      *secretbox.Box
	guard    *probe.Guard
	notifier NotificationSender
	sender   email.Sender
	baseURL  string
}

// NewService builds an alert channel service.
func NewService(
	pool *pgxpool.Pool,
	q *sqlc.Queries,
	ws *workspaces.Service,
	box *secretbox.Box,
	guard *probe.Guard,
	notifier NotificationSender,
	sender email.Sender,
	baseURL string,
) *Service {
	return &Service{
		pool: pool, q: q, ws: ws, box: box, guard: guard,
		notifier: notifier, sender: sender, baseURL: baseURL,
	}
}

// List returns every active alert channel in a workspace. Only admins may
// list channels.
func (s *Service) List(ctx context.Context, actorID, workspaceID uuid.UUID) ([]Channel, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleAdmin); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAlertChannels(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list alert channels: %w", err)
	}
	out := make([]Channel, 0, len(rows))
	for _, row := range rows {
		view, err := ChannelView(row)
		if err != nil {
			return nil, err
		}
		out = append(out, view)
	}
	return out, nil
}

// Create adds a non-email channel and attaches it to every monitor.
func (s *Service) Create(ctx context.Context, actorID, workspaceID uuid.UUID, in CreateInput) (Channel, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleAdmin); err != nil {
		return Channel{}, err
	}
	if err := validateKind(in.Kind); err != nil {
		return Channel{}, err
	}
	name, err := ValidateName(in.Name)
	if err != nil {
		return Channel{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Channel{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	locked, err := qtx.LockWorkspace(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Channel{}, ErrNotFound
		}
		return Channel{}, fmt.Errorf("lock workspace: %w", err)
	}
	if locked.SuspendedAt != nil {
		return Channel{}, ErrSuspended
	}

	if _, err := qtx.GetActiveAlertChannelByName(ctx, sqlc.GetActiveAlertChannelByNameParams{
		WorkspaceID: workspaceID, Kind: in.Kind, Name: name,
	}); err == nil {
		return Channel{}, ErrNameTaken
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, fmt.Errorf("check channel name: %w", err)
	}

	total, err := qtx.CountAllActiveAlertChannels(ctx, workspaceID)
	if err != nil {
		return Channel{}, fmt.Errorf("count alert channels: %w", err)
	}
	if total >= maxChannelsTotal {
		return Channel{}, ErrLimit
	}
	perKind, err := qtx.CountActiveAlertChannels(ctx, sqlc.CountActiveAlertChannelsParams{
		WorkspaceID: workspaceID, Kind: in.Kind,
	})
	if err != nil {
		return Channel{}, fmt.Errorf("count alert channels by kind: %w", err)
	}
	if perKind >= maxChannelsPerKind {
		return Channel{}, ErrLimit
	}

	config, err := SealCreateConfig(s.box, s.guard, in.Kind, in)
	if err != nil {
		return Channel{}, err
	}

	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Channel{}, fmt.Errorf("new channel id: %w", err)
	}
	created, err := qtx.CreateAlertChannel(ctx, sqlc.CreateAlertChannelParams{
		ID: id, WorkspaceID: workspaceID, Kind: in.Kind, Name: name,
		Config: config, Enabled: enabled,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Channel{}, ErrNameTaken
		}
		return Channel{}, fmt.Errorf("create alert channel: %w", err)
	}
	if err := qtx.AttachMonitorsToChannel(ctx, sqlc.AttachMonitorsToChannelParams{
		ChannelID: created.ID, WorkspaceID: workspaceID,
	}); err != nil {
		return Channel{}, fmt.Errorf("attach alert channel: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Channel{}, fmt.Errorf("commit transaction: %w", err)
	}
	audit.RecordBestEffort(ctx, s.q, audit.Event{
		WorkspaceID:   &workspaceID,
		ActorUserID:   &actorID,
		ActorAPIKeyID: auth.APIKeyIDFrom(ctx),
		Kind:          audit.KindAlertChannelCreated,
		Detail: map[string]any{
			"channel_id": created.ID.String(),
			"kind":       created.Kind,
			"name":       created.Name,
		},
	})
	return ChannelView(created)
}

// Update changes a non-email channel. Omitted secrets and URLs keep their
// stored value.
func (s *Service) Update(ctx context.Context, actorID, workspaceID, channelID uuid.UUID, in UpdateInput) (Channel, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleAdmin); err != nil {
		return Channel{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Channel{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	locked, err := qtx.LockWorkspace(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Channel{}, ErrNotFound
		}
		return Channel{}, fmt.Errorf("lock workspace: %w", err)
	}
	if locked.SuspendedAt != nil {
		return Channel{}, ErrSuspended
	}

	existing, err := qtx.GetAlertChannelByID(ctx, sqlc.GetAlertChannelByIDParams{
		ID: channelID, WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Channel{}, ErrNotFound
		}
		return Channel{}, fmt.Errorf("load alert channel: %w", err)
	}
	if existing.Kind == KindEmail {
		return Channel{}, ErrEmailChannel
	}

	name := existing.Name
	if in.Name != nil {
		name, err = ValidateName(*in.Name)
		if err != nil {
			return Channel{}, err
		}
	}
	if name != existing.Name {
		if _, err := qtx.GetActiveAlertChannelByName(ctx, sqlc.GetActiveAlertChannelByNameParams{
			WorkspaceID: workspaceID, Kind: existing.Kind, Name: name,
		}); err == nil {
			return Channel{}, ErrNameTaken
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return Channel{}, fmt.Errorf("check channel name: %w", err)
		}
	}

	config, err := MergeUpdateConfig(s.box, s.guard, existing.Kind, existing.Config, in)
	if err != nil {
		return Channel{}, err
	}
	enabled := existing.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	updated, err := qtx.UpdateAlertChannel(ctx, sqlc.UpdateAlertChannelParams{
		ID: channelID, WorkspaceID: workspaceID, Name: name, Config: config, Enabled: enabled,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Channel{}, ErrNotFound
		}
		if isUniqueViolation(err) {
			return Channel{}, ErrNameTaken
		}
		return Channel{}, fmt.Errorf("update alert channel: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Channel{}, fmt.Errorf("commit transaction: %w", err)
	}
	audit.RecordBestEffort(ctx, s.q, audit.Event{
		WorkspaceID:   &workspaceID,
		ActorUserID:   &actorID,
		ActorAPIKeyID: auth.APIKeyIDFrom(ctx),
		Kind:          audit.KindAlertChannelUpdated,
		Detail: map[string]any{
			"channel_id": updated.ID.String(),
			"kind":       updated.Kind,
			"name":       updated.Name,
			"enabled":    updated.Enabled,
		},
	})
	return ChannelView(updated)
}

// Delete soft-deletes a non-email channel. Delivery history is preserved.
func (s *Service) Delete(ctx context.Context, actorID, workspaceID, channelID uuid.UUID) error {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleAdmin); err != nil {
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

	existing, err := qtx.GetAlertChannelByID(ctx, sqlc.GetAlertChannelByIDParams{
		ID: channelID, WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load alert channel: %w", err)
	}
	if existing.Kind == KindEmail {
		return ErrEmailChannel
	}

	rows, err := qtx.SoftDeleteAlertChannel(ctx, sqlc.SoftDeleteAlertChannelParams{
		ID: channelID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return fmt.Errorf("delete alert channel: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	audit.RecordBestEffort(ctx, s.q, audit.Event{
		WorkspaceID:   &workspaceID,
		ActorUserID:   &actorID,
		ActorAPIKeyID: auth.APIKeyIDFrom(ctx),
		Kind:          audit.KindAlertChannelDeleted,
		Detail: map[string]any{
			"channel_id": channelID.String(),
			"kind":       existing.Kind,
			"name":       existing.Name,
		},
	})
	return nil
}

// ListMonitorChannels returns every active channel with this monitor's switch
// state. Members and above may read it.
func (s *Service) ListMonitorChannels(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID) ([]MonitorChannel, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleMember); err != nil {
		return nil, err
	}
	if _, err := s.q.GetMonitor(ctx, sqlc.GetMonitorParams{ID: monitorID, WorkspaceID: workspaceID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load monitor: %w", err)
	}
	rows, err := s.q.ListMonitorAlertChannels(ctx, sqlc.ListMonitorAlertChannelsParams{
		MonitorID: monitorID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return nil, fmt.Errorf("list monitor alert channels: %w", err)
	}
	out := make([]MonitorChannel, 0, len(rows))
	for _, row := range rows {
		out = append(out, MonitorChannel{
			ID:             row.ID,
			Kind:           row.Kind,
			Name:           row.Name,
			ChannelEnabled: row.ChannelEnabled,
			Enabled:        row.MonitorEnabled,
		})
	}
	return out, nil
}

// SetMonitorChannel toggles one channel's switch for one monitor.
func (s *Service) SetMonitorChannel(ctx context.Context, actorID, workspaceID, monitorID, channelID uuid.UUID, enabled bool) error {
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
	if _, err := qtx.GetMonitor(ctx, sqlc.GetMonitorParams{ID: monitorID, WorkspaceID: workspaceID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load monitor: %w", err)
	}
	if _, err := qtx.GetAlertChannelByID(ctx, sqlc.GetAlertChannelByIDParams{
		ID: channelID, WorkspaceID: workspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load alert channel: %w", err)
	}
	if err := qtx.UpsertMonitorAlertChannel(ctx, sqlc.UpsertMonitorAlertChannelParams{
		MonitorID: monitorID, ChannelID: channelID, Enabled: enabled,
	}); err != nil {
		return fmt.Errorf("update monitor alert channel: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// SendTest delivers a test notification without writing a delivery row.
// Disabled but complete channels can be tested; admins only.
func (s *Service) SendTest(ctx context.Context, actorID, workspaceID, channelID uuid.UUID) error {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleAdmin); err != nil {
		return err
	}
	row, err := s.q.GetAlertChannelByID(ctx, sqlc.GetAlertChannelByIDParams{
		ID: channelID, WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load alert channel: %w", err)
	}
	workspace, err := s.q.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load workspace: %w", err)
	}

	switch row.Kind {
	case KindEmail:
		var cfg EmailConfig
		if err := decodeConfig(row.Config, &cfg); err != nil || len(cfg.Recipients) == 0 {
			return ErrInvalidChannelConfig
		}
		if s.sender == nil {
			return fmt.Errorf("email sender is not configured")
		}
		subject := fmt.Sprintf("o1 test notification for %s", workspace.Name)
		text := fmt.Sprintf(
			"This is a test notification from o1 for %s.\n\nNo incident is open and no delivery was recorded.\n",
			workspace.Name,
		)
		body := fmt.Sprintf(
			"<p>This is a test notification from o1 for <strong>%s</strong>.</p>"+
				"<p>No incident is open and no delivery was recorded.</p>",
			html.EscapeString(workspace.Name),
		)
		for _, recipient := range cfg.Recipients {
			if err := s.sender.Send(ctx, email.Message{To: recipient, Subject: subject, Text: text, HTML: body}); err != nil {
				return err
			}
		}
		return nil

	case KindWebhook, KindSlack, KindTelegram:
		target, err := OpenTarget(s.box, row.Kind, row.Config)
		if err != nil {
			return ErrInvalidChannelConfig
		}
		if s.notifier == nil {
			return fmt.Errorf("alert notifier is not configured")
		}
		return s.notifier.Send(ctx, target, Event{
			Kind:          "test",
			WorkspaceName: workspace.Name,
			WorkspaceSlug: workspace.Slug,
			Link:          strings.TrimRight(s.baseURL, "/"),
		})

	default:
		return ErrInvalidChannelConfig
	}
}

func validateKind(kind string) error {
	switch kind {
	case KindEmail:
		return ErrEmailChannel
	case KindWebhook, KindSlack, KindTelegram:
		return nil
	default:
		return &ValidationError{Fields: map[string]string{
			"kind": "kind must be webhook, slack, or telegram",
		}}
	}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

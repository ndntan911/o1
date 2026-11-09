// Package audit records best-effort security events for actions that are not
// otherwise audited transactionally. Callers pass actor identity explicitly;
// this package intentionally does not import auth.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"o1/internal/db/sqlc"
)

// Security event kinds recorded by this package.
const (
	KindLoginSucceeded      = "login_succeeded"
	KindLoginFailed         = "login_failed"
	KindMemberAdded         = "member_added"
	KindMemberRoleChanged   = "member_role_changed"
	KindMonitorDeleted      = "monitor_deleted"
	KindAlertChannelCreated = "alert_channel_created"
	KindAlertChannelUpdated = "alert_channel_updated"
	KindAlertChannelDeleted = "alert_channel_deleted"
	KindStatusPageUpdated   = "status_page_updated"
	KindAPIKeyCreated       = "api_key_created"
	KindAPIKeyRevoked       = "api_key_revoked"
)

// Event is one security event to record. WorkspaceID and actor IDs are nil for
// events that are not scoped to a workspace or an actor, such as failed logins
// for unknown accounts.
type Event struct {
	WorkspaceID   *uuid.UUID
	ActorUserID   *uuid.UUID
	ActorAPIKeyID *uuid.UUID
	Kind          string
	Detail        map[string]any
}

// RecordBestEffort inserts e and logs a warning on failure. It never returns an
// error and never panics, so callers can record audit events without changing
// the outcome of the audited action.
func RecordBestEffort(ctx context.Context, q *sqlc.Queries, e Event) {
	id, err := uuid.NewV7()
	if err != nil {
		logAuditFailure(e, err)
		return
	}
	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		logAuditFailure(e, err)
		return
	}
	if err := q.CreateSecurityEvent(ctx, sqlc.CreateSecurityEventParams{
		ID:            id,
		WorkspaceID:   optionalUUID(e.WorkspaceID),
		ActorUserID:   optionalUUID(e.ActorUserID),
		Kind:          e.Kind,
		Detail:        raw,
		ActorAPIKeyID: optionalUUID(e.ActorAPIKeyID),
	}); err != nil {
		logAuditFailure(e, err)
	}
}

// logAuditFailure reports a failed best-effort write without echoing event
// detail, which may contain workspace-owned names.
func logAuditFailure(e Event, err error) {
	slog.Warn("record audit event failed",
		"kind", e.Kind,
		"workspace_id", e.WorkspaceID,
		"actor_user_id", e.ActorUserID,
		"actor_api_key_id", e.ActorAPIKeyID,
		"error_type", fmt.Sprintf("%T", err),
		"error", err.Error(),
	)
}

// optionalUUID converts a nullable UUID pointer to the pgtype form.
func optionalUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

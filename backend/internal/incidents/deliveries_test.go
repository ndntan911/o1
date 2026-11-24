package incidents_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/db/sqlc"
	"o1/internal/incidents"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

const deliveryOutcomePassword = "correct horse battery staple"

// addDeliveryOutcomeMember creates a verified user and upserts a membership
// with the given role.
func addDeliveryOutcomeMember(t *testing.T, pool *pgxpool.Pool, q *sqlc.Queries, workspaceID uuid.UUID, role string) uuid.UUID {
	t.Helper()
	userID := testutil.CreateVerifiedUser(t, pool, role+"-"+uuid.NewString()+"@example.com", deliveryOutcomePassword)
	if err := q.UpsertWorkspaceMember(context.Background(), sqlc.UpsertWorkspaceMemberParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
		Role:        role,
	}); err != nil {
		t.Fatalf("add %s member: %v", role, err)
	}
	return userID
}

// insertDeliveryOutcomeIncident creates a monitor and an open incident in the
// workspace and returns the incident id.
func insertDeliveryOutcomeIncident(t *testing.T, pool *pgxpool.Pool, workspaceID, ownerID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	monitorID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new monitor id: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO monitors
			(id, workspace_id, name, url, method, expected_status,
			 interval_seconds, timeout_seconds, confirmation_threshold,
			 recovery_threshold, paused, created_by)
		VALUES ($1, $2, 'delivery outcomes monitor', 'https://example.com/health', 'GET', '200-299', 60, 5, 1, 1, false, $3)`,
		monitorID, workspaceID, ownerID); err != nil {
		t.Fatalf("insert monitor: %v", err)
	}

	incidentID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new incident id: %v", err)
	}
	startedAt := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO incidents
			(id, workspace_id, monitor_id, status, started_at, last_notified_at)
		VALUES ($1, $2, $3, 'open', $4, $4)`,
		incidentID, workspaceID, monitorID, startedAt); err != nil {
		t.Fatalf("insert incident: %v", err)
	}
	return incidentID
}

// insertDeliveryOutcomeChannel creates an alert channel and returns its id.
func insertDeliveryOutcomeChannel(t *testing.T, pool *pgxpool.Pool, workspaceID uuid.UUID, kind, name, config string) uuid.UUID {
	t.Helper()
	channelID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new channel id: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO alert_channels (id, workspace_id, kind, name, config)
		VALUES ($1, $2, $3, $4, $5)`,
		channelID, workspaceID, kind, name, []byte(config)); err != nil {
		t.Fatalf("insert alert channel: %v", err)
	}
	return channelID
}

func TestDeliveryOutcomes_MemberOnlyAndRedacted(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	q := sqlc.New(pool)
	wsSvc := workspaces.NewService(pool, q)
	svc := incidents.NewService(pool, q, wsSvc)

	ownerID := testutil.CreateVerifiedUser(t, pool, "delivery-owner-"+uuid.NewString()+"@example.com", deliveryOutcomePassword)
	ws, err := wsSvc.Create(ctx, ownerID, "delivery outcomes "+uuid.NewString())
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	memberID := addDeliveryOutcomeMember(t, pool, q, ws.ID, "member")
	viewerID := addDeliveryOutcomeMember(t, pool, q, ws.ID, "viewer")

	incidentID := insertDeliveryOutcomeIncident(t, pool, ws.ID, ownerID)

	// Deliberately recognizable secrets: none of these may reach a member.
	const (
		channelName  = "Ops Email Secret"
		recipient    = "hidden@example.com"
		lastError    = "smtp connect failed for hidden@example.com"
		channelToken = "super-secret-webhook-token"
	)
	emailChannelID := insertDeliveryOutcomeChannel(t, pool, ws.ID, "email", channelName,
		`{"recipients":["hidden@example.com"]}`)
	webhookChannelID := insertDeliveryOutcomeChannel(t, pool, ws.ID, "webhook", "Deploy Hook Secret",
		`{"url_enc":"v1:`+channelToken+`"}`)

	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	sentAt := base.Add(30 * time.Second)
	deliveries := []struct {
		channelID uuid.UUID
		event     string
		status    string
		attempts  int
		lastError *string
		sentAt    *time.Time
		createdAt time.Time
	}{
		{channelID: emailChannelID, event: "opened", status: "sent", attempts: 1, sentAt: &sentAt, createdAt: base},
		{channelID: webhookChannelID, event: "reminder", status: "failed", attempts: 3, lastError: ptr(lastError), createdAt: base.Add(time.Second)},
	}
	for _, delivery := range deliveries {
		id, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("new delivery id: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO alert_deliveries
				(id, incident_id, channel_id, event, sequence, recipient, status,
				 attempts, last_error, sent_at, created_at)
			VALUES ($1, $2, $3, $4, 0, $5, $6, $7, $8, $9, $10)`,
			id, incidentID, delivery.channelID, delivery.event, recipient,
			delivery.status, delivery.attempts, delivery.lastError,
			delivery.sentAt, delivery.createdAt); err != nil {
			t.Fatalf("insert delivery: %v", err)
		}
	}

	// A viewer is a workspace member but may not read delivery outcomes.
	if _, err := svc.DeliveryOutcomes(ctx, viewerID, ws.ID, incidentID); !errors.Is(err, workspaces.ErrForbidden) {
		t.Fatalf("viewer error = %v, want ErrForbidden", err)
	}

	rows, err := svc.DeliveryOutcomes(ctx, memberID, ws.ID, incidentID)
	if err != nil {
		t.Fatalf("member delivery outcomes: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("delivery outcomes = %+v, want 2", rows)
	}
	if rows[0].ChannelKind != "email" || rows[0].Event != "opened" ||
		rows[0].Status != "sent" || rows[0].Attempts != 1 || rows[0].SentAt == nil {
		t.Fatalf("first outcome = %+v", rows[0])
	}
	if !rows[0].CreatedAt.Equal(base) {
		t.Fatalf("first created_at = %v, want %v", rows[0].CreatedAt, base)
	}
	if rows[1].ChannelKind != "webhook" || rows[1].Event != "reminder" ||
		rows[1].Status != "failed" || rows[1].Attempts != 3 || rows[1].SentAt != nil {
		t.Fatalf("second outcome = %+v", rows[1])
	}

	// The serialized rows must not contain recipients, channel names, error
	// text, channel secrets, or the fields that carry them.
	payload, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal delivery outcomes: %v", err)
	}
	encoded := string(payload)
	for _, forbidden := range []string{
		recipient, channelName, "Deploy Hook Secret", lastError, channelToken,
		`"recipient"`, `"channel_name"`, `"last_error"`,
	} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("delivery outcomes leaked %q: %s", forbidden, encoded)
		}
	}
	for _, expected := range []string{`"channel_kind"`, `"event"`, `"status"`, `"attempts"`, `"sent_at"`, `"created_at"`} {
		if !strings.Contains(encoded, expected) {
			t.Fatalf("delivery outcomes missing %s: %s", expected, encoded)
		}
	}

	// An incident in another workspace is reported as not found even when the
	// caller is a member of the workspace in the path.
	otherOwnerID := testutil.CreateVerifiedUser(t, pool, "delivery-other-"+uuid.NewString()+"@example.com", deliveryOutcomePassword)
	otherWS, err := wsSvc.Create(ctx, otherOwnerID, "other delivery outcomes "+uuid.NewString())
	if err != nil {
		t.Fatalf("create other workspace: %v", err)
	}
	otherIncidentID := insertDeliveryOutcomeIncident(t, pool, otherWS.ID, otherOwnerID)
	if _, err := svc.DeliveryOutcomes(ctx, memberID, ws.ID, otherIncidentID); !errors.Is(err, incidents.ErrNotFound) {
		t.Fatalf("cross-workspace error = %v, want ErrNotFound", err)
	}
}

func ptr[T any](value T) *T {
	return &value
}

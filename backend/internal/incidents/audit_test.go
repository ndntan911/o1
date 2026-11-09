package incidents_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"o1/internal/audit"
	"o1/internal/db/sqlc"
	"o1/internal/incidents"
)

func TestPutEmailChannelRecordsAuditEvent(t *testing.T) {
	env := newIncidentAPIEnv(t)
	wsID, ownerID := env.createWorkspace("Email Audit")
	svc := incidents.NewService(env.pool, sqlc.New(env.pool), env.wsSvc)
	ctx := context.Background()
	recipient := "ops@example.com"

	channel, err := svc.PutEmailChannel(ctx, ownerID, wsID, []string{recipient}, true)
	if err != nil {
		t.Fatalf("put email channel: %v", err)
	}

	var kind string
	var gotWorkspace, gotActor uuid.UUID
	var raw []byte
	if err := env.pool.QueryRow(ctx, `
		SELECT kind, workspace_id, actor_user_id, detail
		FROM security_events
		WHERE workspace_id = $1 AND kind = $2`,
		wsID, audit.KindAlertChannelUpdated).Scan(&kind, &gotWorkspace, &gotActor, &raw); err != nil {
		t.Fatalf("load audit event: %v", err)
	}
	if kind != audit.KindAlertChannelUpdated {
		t.Fatalf("kind = %q, want %q", kind, audit.KindAlertChannelUpdated)
	}
	if gotWorkspace != wsID {
		t.Fatalf("workspace = %s, want %s", gotWorkspace, wsID)
	}
	if gotActor != ownerID {
		t.Fatalf("actor = %s, want %s", gotActor, ownerID)
	}

	var detail map[string]any
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail["channel_id"] != channel.ID.String() {
		t.Fatalf("channel_id = %v, want %s", detail["channel_id"], channel.ID)
	}
	if detail["kind"] != "email" {
		t.Fatalf("kind detail = %v, want email", detail["kind"])
	}
	if detail["enabled"] != true {
		t.Fatalf("enabled detail = %v, want true", detail["enabled"])
	}
	if strings.Contains(string(raw), recipient) {
		t.Fatalf("detail leaked recipient address: %s", raw)
	}
}

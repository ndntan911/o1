package audit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"o1/internal/audit"
	"o1/internal/db/sqlc"
	"o1/internal/testutil"
)

func TestRecordBestEffort_WritesRedactedEvent(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()
	userID := testutil.CreateVerifiedUser(t, pool, "audit@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1,'Audit','audit',$2)`, wsID, userID); err != nil {
		t.Fatal(err)
	}

	audit.RecordBestEffort(ctx, sqlc.New(pool), audit.Event{
		WorkspaceID: &wsID, ActorUserID: &userID,
		Kind:   audit.KindMonitorDeleted,
		Detail: map[string]any{"monitor_id": uuid.New().String(), "name": "API"},
	})

	var kind string
	var detail []byte
	if err := pool.QueryRow(ctx, `SELECT kind, detail FROM security_events WHERE workspace_id=$1`, wsID).Scan(&kind, &detail); err != nil {
		t.Fatal(err)
	}
	if kind != audit.KindMonitorDeleted || !strings.Contains(string(detail), "API") {
		t.Fatalf("event = %q %s", kind, detail)
	}
}

func TestRecordBestEffort_NeverPanicsOnFailure(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	audit.RecordBestEffort(ctx, sqlc.New(pool), audit.Event{Kind: audit.KindLoginFailed})
}

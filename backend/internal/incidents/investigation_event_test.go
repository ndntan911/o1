package incidents_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/auth"
	"o1/internal/db/sqlc"
	"o1/internal/incidents"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

type investigationEventFixture struct {
	pool           *pgxpool.Pool
	ownerID        uuid.UUID
	viewerID       uuid.UUID
	outsiderID     uuid.UUID
	workspaceID    uuid.UUID
	otherWorkspace uuid.UUID
	incidentID     uuid.UUID
	otherIncident  uuid.UUID
}

func seedInvestigationEventFixture(t *testing.T) (*investigationEventFixture, *incidents.Service) {
	t.Helper()
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()

	ownerID := testutil.CreateVerifiedUser(t, pool, "event-owner-"+uuid.NewString()+"@example.com", "correct horse battery staple")
	viewerID := testutil.CreateVerifiedUser(t, pool, "event-viewer-"+uuid.NewString()+"@example.com", "correct horse battery staple")
	outsiderID := testutil.CreateVerifiedUser(t, pool, "event-outsider-"+uuid.NewString()+"@example.com", "correct horse battery staple")

	wsSvc := workspaces.NewService(pool, sqlc.New(pool))
	ws, err := wsSvc.Create(ctx, ownerID, "event "+uuid.NewString())
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	otherWS, err := wsSvc.Create(ctx, ownerID, "event other "+uuid.NewString())
	if err != nil {
		t.Fatalf("create other workspace: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'viewer')`,
		ws.ID, viewerID,
	); err != nil {
		t.Fatalf("add viewer: %v", err)
	}

	insertIncident := func(workspaceID uuid.UUID) uuid.UUID {
		monitorID := uuid.New()
		if _, err := pool.Exec(ctx,
			`INSERT INTO monitors (id, workspace_id, name, url, method, interval_seconds, timeout_seconds, created_by)
			 VALUES ($1, $2, 'Event monitor', 'https://example.com', 'GET', 60, 10, $3)`,
			monitorID, workspaceID, ownerID); err != nil {
			t.Fatalf("insert monitor: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO monitor_state (monitor_id) VALUES ($1)`, monitorID); err != nil {
			t.Fatalf("insert monitor state: %v", err)
		}
		var incidentID uuid.UUID
		if err := pool.QueryRow(ctx,
			`INSERT INTO incidents (id, workspace_id, monitor_id, status)
			 VALUES (gen_random_uuid(), $1, $2, 'open') RETURNING id`,
			workspaceID, monitorID).Scan(&incidentID); err != nil {
			t.Fatalf("insert incident: %v", err)
		}
		return incidentID
	}

	fixture := &investigationEventFixture{
		pool:           pool,
		ownerID:        ownerID,
		viewerID:       viewerID,
		outsiderID:     outsiderID,
		workspaceID:    ws.ID,
		otherWorkspace: otherWS.ID,
		incidentID:     insertIncident(ws.ID),
		otherIncident:  insertIncident(otherWS.ID),
	}
	return fixture, incidents.NewService(pool, sqlc.New(pool), wsSvc)
}

func TestRecordInvestigationEvent_AppendsOneBoundedEvent(t *testing.T) {
	fixture, svc := seedInvestigationEventFixture(t)
	ctx := auth.ContextWithPrincipal(context.Background(), auth.Principal{UserID: fixture.ownerID})

	investigationID := uuid.New()
	model := strings.Repeat("m", 200)
	if err := svc.RecordInvestigationEvent(
		ctx, fixture.workspaceID, fixture.incidentID, investigationID, "ok", model, 2, 1,
	); err != nil {
		t.Fatalf("RecordInvestigationEvent: %v", err)
	}

	rows, err := fixture.pool.Query(ctx,
		`SELECT kind, actor_user_id, detail FROM incident_events WHERE incident_id = $1`,
		fixture.incidentID)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
		var kind string
		var actor *uuid.UUID
		var raw []byte
		if err := rows.Scan(&kind, &actor, &raw); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		if kind != "investigation_completed" {
			t.Fatalf("kind = %q, want investigation_completed", kind)
		}
		if actor != nil {
			t.Fatalf("actor = %s, want NULL for an automated run", actor)
		}
		var detail map[string]any
		if err := json.Unmarshal(raw, &detail); err != nil {
			t.Fatalf("decode detail: %v", err)
		}
		if detail["investigation_id"] != investigationID.String() {
			t.Fatalf("investigation_id = %v, want %s", detail["investigation_id"], investigationID)
		}
		if detail["status"] != "ok" {
			t.Fatalf("status = %v, want ok", detail["status"])
		}
		if detail["tool_calls"] != float64(2) || detail["hypotheses"] != float64(1) {
			t.Fatalf("counts = %+v, want 2/1", detail)
		}
		bounded, ok := detail["model"].(string)
		if !ok || len([]rune(bounded)) != 100 {
			t.Fatalf("model = %v, want a 100-rune bound", detail["model"])
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate events: %v", err)
	}
	if count != 1 {
		t.Fatalf("events = %d, want 1", count)
	}
}

func TestRecordInvestigationEvent_RequiresLiveMemberAndTenantScope(t *testing.T) {
	fixture, svc := seedInvestigationEventFixture(t)
	base := context.Background()
	investigationID := uuid.New()

	viewerCtx := auth.ContextWithPrincipal(base, auth.Principal{UserID: fixture.viewerID})
	if err := svc.RecordInvestigationEvent(
		viewerCtx, fixture.workspaceID, fixture.incidentID, investigationID, "ok", "stub", 1, 1,
	); !errors.Is(err, workspaces.ErrForbidden) {
		t.Fatalf("viewer err = %v, want ErrForbidden", err)
	}

	outsiderCtx := auth.ContextWithPrincipal(base, auth.Principal{UserID: fixture.outsiderID})
	if err := svc.RecordInvestigationEvent(
		outsiderCtx, fixture.workspaceID, fixture.incidentID, investigationID, "ok", "stub", 1, 1,
	); !errors.Is(err, workspaces.ErrNotFound) {
		t.Fatalf("outsider err = %v, want ErrNotFound", err)
	}

	ownerCtx := auth.ContextWithPrincipal(base, auth.Principal{UserID: fixture.ownerID})
	if err := svc.RecordInvestigationEvent(
		ownerCtx, fixture.workspaceID, fixture.otherIncident, investigationID, "ok", "stub", 1, 1,
	); !errors.Is(err, workspaces.ErrNotFound) {
		t.Fatalf("cross-workspace incident err = %v, want ErrNotFound", err)
	}

	var count int
	if err := fixture.pool.QueryRow(base,
		`SELECT count(*) FROM incident_events WHERE incident_id = $1`, fixture.incidentID,
	).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 0 {
		t.Fatalf("events = %d, want 0 after rejected calls", count)
	}
}

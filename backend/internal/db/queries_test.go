package db_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"o1/internal/auth"
	"o1/internal/db/sqlc"
	"o1/internal/testutil"
)

func TestCreateUser_DuplicateEmailIsCaseInsensitive(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	q := sqlc.New(pool)
	ctx := context.Background()
	id, _ := uuid.NewV7()
	if _, err := q.CreateUser(ctx, sqlc.CreateUserParams{ID: id, Email: "User@Example.com", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	id2, _ := uuid.NewV7()
	if _, err := q.CreateUser(ctx, sqlc.CreateUserParams{ID: id2, Email: "user@example.com", PasswordHash: "x"}); err == nil {
		t.Fatal("expected unique violation for case-insensitive duplicate email")
	}
}

func TestGetUserByEmail_NormalizesCase(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	q := sqlc.New(pool)
	ctx := context.Background()
	id, _ := uuid.NewV7()
	if _, err := q.CreateUser(ctx, sqlc.CreateUserParams{ID: id, Email: "user@example.com", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetUserByEmail(ctx, "User@Example.com"); err != nil {
		t.Fatalf("expected case-insensitive lookup: %v", err)
	}
}

func mustUUIDv7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generate uuid v7: %v", err)
	}
	return id
}

func TestMonitorQueries_PaginationSummaryAndConflict(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()
	q := sqlc.New(pool)

	userID := testutil.CreateVerifiedUser(t, pool, "monitor-owner@example.com", "password12345")
	wsID := mustUUIDv7(t)
	if _, err := q.CreateWorkspace(ctx, sqlc.CreateWorkspaceParams{
		ID:        wsID,
		Name:      "Monitor workspace",
		Slug:      "monitor-workspace",
		CreatedBy: userID,
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	monitors := make([]sqlc.Monitor, 3)
	for i := range monitors {
		m, err := q.CreateMonitor(ctx, sqlc.CreateMonitorParams{
			ID:                    mustUUIDv7(t),
			WorkspaceID:           wsID,
			Name:                  "monitor-" + string(rune('a'+i)),
			Url:                   "https://example.com/health",
			Method:                "GET",
			ExpectedStatus:        "2xx",
			IntervalSeconds:       60,
			TimeoutSeconds:        10,
			ConfirmationThreshold: 2,
			RecoveryThreshold:     2,
			Paused:                false,
			CreatedBy:             userID,
		})
		if err != nil {
			t.Fatalf("create monitor %d: %v", i, err)
		}
		// Deterministic ordering regardless of clock resolution.
		if _, err := pool.Exec(ctx, "UPDATE monitors SET created_at = $2 WHERE id = $1", m.ID, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("set monitor %d created_at: %v", i, err)
		}
		if err := q.CreateMonitorState(ctx, m.ID); err != nil {
			t.Fatalf("create monitor state %d: %v", i, err)
		}
		monitors[i] = m
	}

	// The state rows start due at now(), so all three monitors are selectable.
	due, err := q.ListDueMonitors(ctx, sqlc.ListDueMonitorsParams{
		Now:   time.Now().UTC().Add(time.Minute),
		Batch: 10,
	})
	if err != nil {
		t.Fatalf("list due monitors: %v", err)
	}
	if len(due) != 3 {
		t.Fatalf("due monitors = %d, want 3", len(due))
	}

	// Keyset pagination: newest first, two per page.
	page1, err := q.ListMonitors(ctx, sqlc.ListMonitorsParams{
		WorkspaceID: wsID,
		AfterID:     uuid.Nil,
		PageLimit:   2,
	})
	if err != nil {
		t.Fatalf("list monitors page 1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page 1 length = %d, want 2", len(page1))
	}
	if page1[0].ID != monitors[2].ID || page1[1].ID != monitors[1].ID {
		t.Fatalf("page 1 ids = [%s %s], want [%s %s]", page1[0].ID, page1[1].ID, monitors[2].ID, monitors[1].ID)
	}
	if page1[0].Status != "unknown" {
		t.Fatalf("page 1 status = %q, want unknown", page1[0].Status)
	}

	after := page1[1].CreatedAt
	page2, err := q.ListMonitors(ctx, sqlc.ListMonitorsParams{
		WorkspaceID: wsID,
		AfterTime:   &after,
		AfterID:     page1[1].ID,
		PageLimit:   2,
	})
	if err != nil {
		t.Fatalf("list monitors page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].ID != monitors[0].ID {
		t.Fatalf("page 2 = %+v, want only %s", page2, monitors[0].ID)
	}

	// Two results: 100ms up, 300ms down.
	now := time.Now().UTC().Truncate(time.Second)
	scheduled1 := now.Add(-2 * time.Minute)
	checked1 := scheduled1.Add(time.Second)
	upCode := int32(200)
	first := sqlc.InsertProbeResultParams{
		ID:           mustUUIDv7(t),
		MonitorID:    monitors[0].ID,
		WorkspaceID:  wsID,
		ScheduledFor: scheduled1,
		CheckedAt:    checked1,
		Status:       "up",
		StatusCode:   &upCode,
		LatencyMs:    100,
	}
	if rows, err := q.InsertProbeResult(ctx, first); err != nil {
		t.Fatalf("insert first result: %v", err)
	} else if rows != 1 {
		t.Fatalf("first insert rows = %d, want 1", rows)
	}

	// Exact duplicate is ignored: this is the worker's replay guard.
	dupe := first
	dupe.ID = mustUUIDv7(t)
	if rows, err := q.InsertProbeResult(ctx, dupe); err != nil {
		t.Fatalf("insert duplicate result: %v", err)
	} else if rows != 0 {
		t.Fatalf("duplicate insert rows = %d, want 0", rows)
	}

	scheduled2 := now.Add(-1 * time.Minute)
	downCode := int32(500)
	latency2 := int32(300)
	if rows, err := q.InsertProbeResult(ctx, sqlc.InsertProbeResultParams{
		ID:           mustUUIDv7(t),
		MonitorID:    monitors[0].ID,
		WorkspaceID:  wsID,
		ScheduledFor: scheduled2,
		CheckedAt:    scheduled2.Add(time.Second),
		Status:       "down",
		StatusCode:   &downCode,
		LatencyMs:    latency2,
		Error:        ptr("status 500"),
	}); err != nil {
		t.Fatalf("insert second result: %v", err)
	} else if rows != 1 {
		t.Fatalf("second insert rows = %d, want 1", rows)
	}

	exists, err := q.ProbeResultExists(ctx, sqlc.ProbeResultExistsParams{
		MonitorID:    monitors[0].ID,
		ScheduledFor: scheduled1,
	})
	if err != nil {
		t.Fatalf("probe result exists: %v", err)
	}
	if !exists {
		t.Fatal("ProbeResultExists = false, want true")
	}
	missing, err := q.ProbeResultExists(ctx, sqlc.ProbeResultExistsParams{
		MonitorID:    monitors[0].ID,
		ScheduledFor: scheduled1.Add(30 * time.Second),
	})
	if err != nil {
		t.Fatalf("probe result exists (missing): %v", err)
	}
	if missing {
		t.Fatal("ProbeResultExists for absent result = true, want false")
	}

	// Probe results are newest first with a keyset cursor.
	results, err := q.ListProbeResults(ctx, sqlc.ListProbeResultsParams{
		MonitorID:   monitors[0].ID,
		WorkspaceID: wsID,
		Since:       now.Add(-24 * time.Hour),
		AfterID:     uuid.Nil,
		PageLimit:   1,
	})
	if err != nil {
		t.Fatalf("list probe results page 1: %v", err)
	}
	if len(results) != 1 || results[0].Status != "down" {
		t.Fatalf("result page 1 = %+v, want the newest (down) result", results)
	}
	afterResult := results[0].CheckedAt
	results, err = q.ListProbeResults(ctx, sqlc.ListProbeResultsParams{
		MonitorID:   monitors[0].ID,
		WorkspaceID: wsID,
		Since:       now.Add(-24 * time.Hour),
		AfterTime:   &afterResult,
		AfterID:     results[0].ID,
		PageLimit:   1,
	})
	if err != nil {
		t.Fatalf("list probe results page 2: %v", err)
	}
	if len(results) != 1 || results[0].Status != "up" {
		t.Fatalf("result page 2 = %+v, want the older (up) result", results)
	}

	summary, err := q.MonitorSummary(ctx, sqlc.MonitorSummaryParams{
		MonitorID:   monitors[0].ID,
		WorkspaceID: wsID,
		Since:       now.Add(-24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("monitor summary: %v", err)
	}
	if summary.Checks != 2 || summary.UpCount != 1 || summary.DownCount != 1 {
		t.Fatalf("summary counts = %+v, want checks=2 up=1 down=1", summary)
	}
	if summary.MinLatencyMs != 100 || summary.MaxLatencyMs != 300 {
		t.Fatalf("summary min/max = %d/%d, want 100/300", summary.MinLatencyMs, summary.MaxLatencyMs)
	}
	if math.Abs(summary.AvgLatencyMs-200) > 0.001 {
		t.Fatalf("summary avg = %v, want 200", summary.AvgLatencyMs)
	}
	if math.Abs(summary.P95LatencyMs-290) > 0.001 {
		t.Fatalf("summary p95 = %v, want 290", summary.P95LatencyMs)
	}

	// Empty window still scans (COALESCE) and reports zero checks.
	empty, err := q.MonitorSummary(ctx, sqlc.MonitorSummaryParams{
		MonitorID:   monitors[1].ID,
		WorkspaceID: wsID,
		Since:       now.Add(-24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("empty monitor summary: %v", err)
	}
	if empty.Checks != 0 || empty.AvgLatencyMs != 0 {
		t.Fatalf("empty summary = %+v, want zero checks and zero latency", empty)
	}

	summaries, err := q.ListMonitorSummaries(ctx, sqlc.ListMonitorSummariesParams{
		MonitorIds: []uuid.UUID{monitors[0].ID, monitors[1].ID},
		Since:      now.Add(-24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("list monitor summaries: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summaries length = %d, want 1 (only monitors with results)", len(summaries))
	}
	if summaries[0].MonitorID != monitors[0].ID || summaries[0].Checks != 2 || summaries[0].UpCount != 1 {
		t.Fatalf("summary row = %+v, want monitor %s checks=2 up=1", summaries[0], monitors[0].ID)
	}
}

func ptr[T any](v T) *T { return &v }

func TestAdminQueries_FiltersSuspensionAndEvents(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()
	q := sqlc.New(pool)

	adminID := testutil.CreateVerifiedUser(t, pool, "admin@example.com", "password12345")
	isAdmin, err := q.GetUserIsAdmin(ctx, adminID)
	if err != nil {
		t.Fatalf("get user is_admin: %v", err)
	}
	if isAdmin {
		t.Fatal("new user is_admin = true, want false")
	}
	if _, err := pool.Exec(ctx, "UPDATE users SET is_admin = true WHERE id = $1", adminID); err != nil {
		t.Fatalf("promote admin: %v", err)
	}
	if isAdmin, err = q.GetUserIsAdmin(ctx, adminID); err != nil || !isAdmin {
		t.Fatalf("promoted is_admin = %v, %v; want true, nil", isAdmin, err)
	}

	newWorkspace := func(name, slug string) uuid.UUID {
		t.Helper()
		id := mustUUIDv7(t)
		if _, err := q.CreateWorkspace(ctx, sqlc.CreateWorkspaceParams{
			ID: id, Name: name, Slug: slug, CreatedBy: adminID,
		}); err != nil {
			t.Fatalf("create workspace %q: %v", slug, err)
		}
		return id
	}
	wsID := newWorkspace("Acme Monitoring", "acme-monitoring")
	newWorkspace("Other Tenant", "other-tenant")

	monitorID := mustUUIDv7(t)
	if _, err := q.CreateMonitor(ctx, sqlc.CreateMonitorParams{
		ID:                    monitorID,
		WorkspaceID:           wsID,
		Name:                  "Acme API",
		Url:                   "https://example.com/health",
		Method:                "GET",
		ExpectedStatus:        "2xx",
		IntervalSeconds:       60,
		TimeoutSeconds:        10,
		ConfirmationThreshold: 2,
		RecoveryThreshold:     2,
		CreatedBy:             adminID,
	}); err != nil {
		t.Fatalf("create monitor: %v", err)
	}
	if err := q.CreateMonitorState(ctx, monitorID); err != nil {
		t.Fatalf("create monitor state: %v", err)
	}

	search := "ACME"
	workspaces, err := q.ListWorkspacesForAdmin(ctx, sqlc.ListWorkspacesForAdminParams{
		Query:     &search,
		AfterID:   uuid.Nil,
		PageLimit: 10,
	})
	if err != nil {
		t.Fatalf("list workspaces filtered: %v", err)
	}
	if len(workspaces) != 1 || workspaces[0].ID != wsID {
		t.Fatalf("filtered workspaces = %+v, want only %s", workspaces, wsID)
	}
	if workspaces[0].MonitorCount != 1 || workspaces[0].MonitorLimit != 5 {
		t.Fatalf("workspace counts = monitor %d limit %d, want 1/5", workspaces[0].MonitorCount, workspaces[0].MonitorLimit)
	}

	notSuspended := false
	workspaces, err = q.ListWorkspacesForAdmin(ctx, sqlc.ListWorkspacesForAdminParams{
		Suspended: &notSuspended,
		AfterID:   uuid.Nil,
		PageLimit: 10,
	})
	if err != nil {
		t.Fatalf("list workspaces unsuspended: %v", err)
	}
	if len(workspaces) != 2 {
		t.Fatalf("unsuspended workspaces = %d, want 2", len(workspaces))
	}

	reason := "abuse"
	if err := q.SuspendWorkspace(ctx, sqlc.SuspendWorkspaceParams{
		ID:              wsID,
		SuspendedBy:     pgtype.UUID{Bytes: adminID, Valid: true},
		SuspendedReason: &reason,
	}); err != nil {
		t.Fatalf("suspend workspace: %v", err)
	}
	suspended := true
	workspaces, err = q.ListWorkspacesForAdmin(ctx, sqlc.ListWorkspacesForAdminParams{
		Suspended: &suspended,
		AfterID:   uuid.Nil,
		PageLimit: 10,
	})
	if err != nil {
		t.Fatalf("list suspended workspaces: %v", err)
	}
	if len(workspaces) != 1 || workspaces[0].SuspendedAt == nil || workspaces[0].SuspendedReason == nil || *workspaces[0].SuspendedReason != reason {
		t.Fatalf("suspended workspaces = %+v, want %s suspended with reason %q", workspaces, wsID, reason)
	}

	monitors, err := q.ListMonitorsForAdmin(ctx, sqlc.ListMonitorsForAdminParams{
		WorkspaceID: pgtype.UUID{Bytes: wsID, Valid: true},
		AfterID:     uuid.Nil,
		PageLimit:   10,
	})
	if err != nil {
		t.Fatalf("list monitors for admin: %v", err)
	}
	if len(monitors) != 1 || monitors[0].ID != monitorID || monitors[0].WorkspaceName != "Acme Monitoring" {
		t.Fatalf("admin monitors = %+v, want monitor %s", monitors, monitorID)
	}

	eventID := mustUUIDv7(t)
	if err := q.CreateSecurityEvent(ctx, sqlc.CreateSecurityEventParams{
		ID:            eventID,
		WorkspaceID:   pgtype.UUID{Bytes: wsID, Valid: true},
		ActorUserID:   pgtype.UUID{Bytes: adminID, Valid: true},
		Kind:          "workspace_suspended",
		Detail:        []byte(`{"reason":"abuse"}`),
		ActorAPIKeyID: pgtype.UUID{},
	}); err != nil {
		t.Fatalf("create security event: %v", err)
	}
	if err := q.CreateSecurityEvent(ctx, sqlc.CreateSecurityEventParams{
		ID:            mustUUIDv7(t),
		WorkspaceID:   pgtype.UUID{Bytes: wsID, Valid: true},
		ActorUserID:   pgtype.UUID{Bytes: adminID, Valid: true},
		Kind:          "monitor_created",
		Detail:        []byte(`{}`),
		ActorAPIKeyID: pgtype.UUID{},
	}); err != nil {
		t.Fatalf("create second security event: %v", err)
	}

	kind := "workspace_suspended"
	events, err := q.ListSecurityEvents(ctx, sqlc.ListSecurityEventsParams{
		Kind:      &kind,
		AfterID:   uuid.Nil,
		PageLimit: 10,
	})
	if err != nil {
		t.Fatalf("list security events: %v", err)
	}
	if len(events) != 1 || events[0].ID != eventID || events[0].Kind != kind {
		t.Fatalf("filtered events = %+v, want only %s", events, eventID)
	}

	if err := q.UnsuspendWorkspace(ctx, wsID); err != nil {
		t.Fatalf("unsuspend workspace: %v", err)
	}
	ws, err := q.GetWorkspaceByID(ctx, wsID)
	if err != nil {
		t.Fatalf("get workspace after unsuspend: %v", err)
	}
	if ws.SuspendedAt != nil || ws.SuspendedReason != nil || ws.SuspendedBy.Valid {
		t.Fatalf("workspace still suspended after unsuspend: %+v", ws)
	}
}

func TestIncidentSchema_OneActivePerMonitorAndDeliveryDedup(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()

	q := sqlc.New(pool)
	ctx := context.Background()
	userID := testutil.CreateVerifiedUser(t, pool, "incident-schema@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1,'Acme','acme-schema',$2)`, wsID, userID); err != nil {
		t.Fatal(err)
	}
	monitorID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO monitors (id, workspace_id, name, url, method, interval_seconds, timeout_seconds, created_by) VALUES ($1,$2,'API','https://example.com','GET',60,10,$3)`, monitorID, wsID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO monitor_state (monitor_id) VALUES ($1)`, monitorID); err != nil {
		t.Fatal(err)
	}

	incidentID := uuid.New()
	if _, err := q.CreateIncident(ctx, sqlc.CreateIncidentParams{
		ID: incidentID, WorkspaceID: wsID, MonitorID: monitorID,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateIncident(ctx, sqlc.CreateIncidentParams{
		ID: uuid.New(), WorkspaceID: wsID, MonitorID: monitorID,
		StartedAt: time.Now().UTC(),
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second active incident error = %v, want pgx.ErrNoRows", err)
	}

	// Resolving the first incident must free the partial unique index for a
	// new open incident on the same monitor. A plain UNIQUE (monitor_id) would
	// reject this insert.
	if err := q.ResolveIncident(ctx, sqlc.ResolveIncidentParams{
		ID: incidentID, ResolvedAt: ptr(time.Now().UTC()),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateIncident(ctx, sqlc.CreateIncidentParams{
		ID: uuid.New(), WorkspaceID: wsID, MonitorID: monitorID,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("incident after resolution: %v", err)
	}

	channelID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO alert_channels (id, workspace_id, kind, name, config) VALUES ($1,$2,'email','Email','{"recipients":["ops@example.com"]}')`, channelID, wsID); err != nil {
		t.Fatal(err)
	}
	deliveryID := uuid.New()
	insert := func(id uuid.UUID, recipient string) error {
		_, err := q.CreateAlertDelivery(ctx, sqlc.CreateAlertDeliveryParams{
			ID: id, IncidentID: incidentID, ChannelID: channelID,
			Event: "opened", Sequence: 0, Recipient: recipient,
		})
		return err
	}
	if err := insert(deliveryID, "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	// Same dedup key, fresh id: this must conflict on the delivery UNIQUE
	// constraint, not on the primary key.
	if err := insert(uuid.New(), "ops@example.com"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("duplicate delivery error = %v, want pgx.ErrNoRows", err)
	}
	// Same id, different key: this exercises the primary-key conflict path.
	if err := insert(deliveryID, "other@example.com"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("duplicate delivery id error = %v, want pgx.ErrNoRows", err)
	}
}

func TestAlertChannelSchema_SoftDeleteAndFilters(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()

	userID := testutil.CreateVerifiedUser(t, pool, "alert-schema@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1,'Alerts','alerts-schema',$2)`, wsID, userID); err != nil {
		t.Fatal(err)
	}
	monitorID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO monitors (id, workspace_id, name, url, method, interval_seconds, timeout_seconds, created_by) VALUES ($1,$2,'API','https://example.com','GET',60,10,$3)`, monitorID, wsID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO monitor_state (monitor_id) VALUES ($1)`, monitorID); err != nil {
		t.Fatal(err)
	}

	q := sqlc.New(pool)
	channelID := uuid.New()
	channel, err := q.CreateAlertChannel(ctx, sqlc.CreateAlertChannelParams{
		ID: channelID, WorkspaceID: wsID, Kind: "webhook", Name: "Deploy",
		Config: []byte(`{"url_enc":"v1:x","url_preview":"https://example.com/…"}`), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.UpsertMonitorAlertChannel(ctx, sqlc.UpsertMonitorAlertChannelParams{
		MonitorID: monitorID, ChannelID: channelID, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	enabled, err := q.ListEnabledAlertChannelsForMonitor(ctx, monitorID)
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 1 || enabled[0].ID != channelID {
		t.Fatalf("enabled channels = %+v, want the webhook channel", enabled)
	}

	if rows, err := q.SoftDeleteAlertChannel(ctx, sqlc.SoftDeleteAlertChannelParams{
		ID: channel.ID, WorkspaceID: wsID,
	}); err != nil || rows != 1 {
		t.Fatalf("soft delete rows = %d, err = %v", rows, err)
	}
	enabled, err = q.ListEnabledAlertChannelsForMonitor(ctx, monitorID)
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 0 {
		t.Fatalf("enabled channels after delete = %+v, want none", enabled)
	}
	if _, err := q.GetAlertChannelByID(ctx, sqlc.GetAlertChannelByIDParams{ID: channelID, WorkspaceID: wsID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("get deleted channel error = %v, want pgx.ErrNoRows", err)
	}

	// A deleted active name can be reused.
	if _, err := q.CreateAlertChannel(ctx, sqlc.CreateAlertChannelParams{
		ID: uuid.New(), WorkspaceID: wsID, Kind: "webhook", Name: "Deploy",
		Config: []byte(`{"url_enc":"v1:y","url_preview":"https://example.org/…"}`), Enabled: false,
	}); err != nil {
		t.Fatalf("reuse deleted name: %v", err)
	}

	// The email upsert still infers the partial unique index.
	if _, err := q.UpsertEmailAlertChannel(ctx, sqlc.UpsertEmailAlertChannelParams{
		ID: uuid.New(), WorkspaceID: wsID, Config: []byte(`{"recipients":["ops@example.com"]}`), Enabled: true,
	}); err != nil {
		t.Fatalf("upsert email channel: %v", err)
	}
}

func TestStatusPageSchema_SelectionAndCascade(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()

	userID := testutil.CreateVerifiedUser(t, pool, "status-schema@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1,'Status Schema','status-schema',$2)`, wsID, userID); err != nil {
		t.Fatal(err)
	}
	monitorID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO monitors (id, workspace_id, name, url, method, interval_seconds, timeout_seconds, created_by) VALUES ($1,$2,'API','https://example.com','GET',60,30,$3)`, monitorID, wsID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO monitor_state (monitor_id) VALUES ($1)`, monitorID); err != nil {
		t.Fatal(err)
	}

	q := sqlc.New(pool)
	page, err := q.UpsertStatusPage(ctx, sqlc.UpsertStatusPageParams{
		WorkspaceID: wsID, Enabled: true, Title: "Status Schema", Description: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Enabled || page.Title != "Status Schema" {
		t.Fatalf("page = %+v", page)
	}
	if err := q.InsertStatusPageMonitor(ctx, sqlc.InsertStatusPageMonitorParams{
		StatusPageID: page.WorkspaceID, MonitorID: monitorID,
	}); err != nil {
		t.Fatal(err)
	}
	// Inserting the same selection twice is idempotent.
	if err := q.InsertStatusPageMonitor(ctx, sqlc.InsertStatusPageMonitorParams{
		StatusPageID: page.WorkspaceID, MonitorID: monitorID,
	}); err != nil {
		t.Fatalf("duplicate selection: %v", err)
	}
	ids, err := q.ListStatusPageMonitorIDs(ctx, page.WorkspaceID)
	if err != nil || len(ids) != 1 || ids[0] != monitorID {
		t.Fatalf("selection ids = %v, err = %v", ids, err)
	}

	// Deleting the monitor removes its selection.
	if _, err := pool.Exec(ctx, `DELETE FROM monitors WHERE id = $1`, monitorID); err != nil {
		t.Fatal(err)
	}
	if ids, err := q.ListStatusPageMonitorIDs(ctx, page.WorkspaceID); err != nil || len(ids) != 0 {
		t.Fatalf("selection after monitor delete = %v, err = %v", ids, err)
	}

	// Deleting the workspace removes the page.
	if _, err := pool.Exec(ctx, `DELETE FROM workspaces WHERE id = $1`, wsID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetStatusPage(ctx, wsID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("page after workspace delete error = %v, want pgx.ErrNoRows", err)
	}
}

func TestProbeDayStats_GroupsByUTCDate(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()

	userID := testutil.CreateVerifiedUser(t, pool, "status-days@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1,'Status Days','status-days',$2)`, wsID, userID); err != nil {
		t.Fatal(err)
	}
	monitorID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO monitors (id, workspace_id, name, url, method, interval_seconds, timeout_seconds, created_by) VALUES ($1,$2,'API','https://example.com','GET',60,30,$3)`, monitorID, wsID, userID); err != nil {
		t.Fatal(err)
	}
	insert := func(at time.Time, status string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO probe_results (id, monitor_id, workspace_id, scheduled_for, checked_at, status, latency_ms)
			VALUES ($1,$2,$3,$4,$4,$5,100)`,
			uuid.New(), monitorID, wsID, at, status); err != nil {
			t.Fatal(err)
		}
	}
	// Use yesterday/today so the probe partitions created by migrations exist.
	now := time.Now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	insert(day.Add(23*time.Hour+59*time.Minute), "up")
	insert(day.Add(time.Hour), "down")
	insert(day.Add(25*time.Hour), "up") // next UTC day, 01:00

	rows, err := sqlc.New(pool).ListProbeDayStats(ctx, sqlc.ListProbeDayStatsParams{
		MonitorIds: []uuid.UUID{monitorID},
		Since:      day,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("day rows = %+v, want 2", rows)
	}
	if rows[0].Day != day.Format("2006-01-02") || rows[0].Checks != 2 || rows[0].UpCount != 1 || rows[0].DownCount != 1 {
		t.Fatalf("first day = %+v", rows[0])
	}
	next := day.AddDate(0, 0, 1)
	if rows[1].Day != next.Format("2006-01-02") || rows[1].Checks != 1 || rows[1].UpCount != 1 {
		t.Fatalf("second day = %+v", rows[1])
	}
}

func TestAPIKeySchema_ConstraintsAndCascades(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()

	userID := testutil.CreateVerifiedUser(t, pool, "apikey-schema@example.com", "correct horse battery staple")
	keyOwner := testutil.CreateVerifiedUser(t, pool, "apikey-owner@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1,'Keys','keys-schema',$2)`, wsID, userID); err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(pool)
	keyID := uuid.New()
	hash := auth.HashToken("o1_abcdefghijkl_secret")
	key, err := q.CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{
		ID: keyID, WorkspaceID: wsID, Name: "CI", Prefix: "abcdefghijkl",
		TokenHash: hash, Role: "viewer", CreatedBy: keyOwner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if key.Prefix != "abcdefghijkl" || key.Role != "viewer" || key.RevokedAt != nil {
		t.Fatalf("key = %+v", key)
	}

	if _, err := q.CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{
		ID: uuid.New(), WorkspaceID: wsID, Name: "CI", Prefix: "mnopqrstuvwx",
		TokenHash: auth.HashToken("o1_mnopqrstuvwx_secret"), Role: "member", CreatedBy: keyOwner,
	}); err == nil {
		t.Fatal("duplicate active name was accepted")
	}

	if rows, err := q.RevokeAPIKey(ctx, sqlc.RevokeAPIKeyParams{ID: keyID, WorkspaceID: wsID}); err != nil || rows != 1 {
		t.Fatalf("revoke rows = %d, err = %v", rows, err)
	}
	if _, err := q.GetActiveAPIKeyByPrefix(ctx, "abcdefghijkl"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("revoked key lookup error = %v, want pgx.ErrNoRows", err)
	}

	// A revoked name can be reused.
	if _, err := q.CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{
		ID: uuid.New(), WorkspaceID: wsID, Name: "CI", Prefix: "zzzzzzzzzzzz",
		TokenHash: auth.HashToken("o1_zzzzzzzzzzzz_secret"), Role: "viewer", CreatedBy: keyOwner,
	}); err != nil {
		t.Fatalf("reuse revoked name: %v", err)
	}

	// Security events can attribute an actor key.
	if _, err := pool.Exec(ctx, `INSERT INTO security_events (id, workspace_id, actor_user_id, actor_api_key_id, kind, detail) VALUES ($1,$2,$3,$4,'api_key_created','{}')`, uuid.New(), wsID, keyOwner, keyID); err != nil {
		t.Fatal(err)
	}

	// Deleting the key's creator deletes their keys.
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, keyOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetAPIKey(ctx, sqlc.GetAPIKeyParams{ID: keyID, WorkspaceID: wsID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("key after user delete error = %v, want pgx.ErrNoRows", err)
	}
}

func TestTOTPSchema_StateRecoveryAndChallenges(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()
	userID := testutil.CreateVerifiedUser(t, pool, "totp-schema@example.com", "correct horse battery staple")
	q := sqlc.New(pool)

	if err := q.SetTOTPPending(ctx, sqlc.SetTOTPPendingParams{
		UserID: userID, PendingEnc: "sealed-pending", ExpiresAt: time.Now().Add(10 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	state, err := q.GetTOTPState(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if state.TOTPPendingEnc == nil || *state.TOTPPendingEnc != "sealed-pending" || state.TOTPEnabledAt != nil {
		t.Fatalf("state = %+v", state)
	}

	if rows, err := q.EnableTOTP(ctx, sqlc.EnableTOTPParams{
		UserID: userID, SecretEnc: "sealed-active", LastUsedStep: 100,
	}); err != nil || rows != 1 {
		t.Fatalf("enable rows = %d, err = %v, want 1", rows, err)
	}
	if rows, err := q.EnableTOTP(ctx, sqlc.EnableTOTPParams{
		UserID: userID, SecretEnc: "sealed-replaced", LastUsedStep: 200,
	}); err != nil || rows != 0 {
		t.Fatalf("second enable rows = %d, err = %v, want 0", rows, err)
	}
	if rows, err := q.UpdateTOTPLastUsedStepIfGreater(ctx, sqlc.UpdateTOTPLastUsedStepIfGreaterParams{
		UserID: userID, LastUsedStep: 100,
	}); err != nil || rows != 0 {
		t.Fatalf("replay update rows = %d, err = %v, want 0", rows, err)
	}
	if rows, err := q.UpdateTOTPLastUsedStepIfGreater(ctx, sqlc.UpdateTOTPLastUsedStepIfGreaterParams{
		UserID: userID, LastUsedStep: 101,
	}); err != nil || rows != 1 {
		t.Fatalf("advance update rows = %d, err = %v, want 1", rows, err)
	}

	// Recovery codes are hashed rows that cascade with the user.
	recoveryIDs := make([]uuid.UUID, 2)
	for i := range recoveryIDs {
		id := uuid.New()
		recoveryIDs[i] = id
		if err := q.CreateRecoveryCode(ctx, sqlc.CreateRecoveryCodeParams{
			ID: id, UserID: userID, CodeHash: []byte{byte(i)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := q.CountUnusedRecoveryCodes(ctx, userID); err != nil || n != 2 {
		t.Fatalf("unused codes = %d, err = %v", n, err)
	}
	if codes, err := q.ListRecoveryCodesForUpdate(ctx, userID); err != nil || len(codes) != 2 {
		t.Fatalf("codes for update = %+v, err = %v, want 2", codes, err)
	}

	// A recovery code can be marked used exactly once.
	if rows, err := q.MarkRecoveryCodeUsed(ctx, sqlc.MarkRecoveryCodeUsedParams{
		ID: recoveryIDs[0], UsedAt: ptr(time.Now().UTC()),
	}); err != nil || rows != 1 {
		t.Fatalf("first mark rows = %d, err = %v, want 1", rows, err)
	}
	if rows, err := q.MarkRecoveryCodeUsed(ctx, sqlc.MarkRecoveryCodeUsedParams{
		ID: recoveryIDs[0], UsedAt: ptr(time.Now().UTC()),
	}); err != nil || rows != 0 {
		t.Fatalf("second mark rows = %d, err = %v, want 0", rows, err)
	}
	codes, err := q.ListRecoveryCodes(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	var usedCode *sqlc.UserRecoveryCode
	for i := range codes {
		if codes[i].ID == recoveryIDs[0] {
			usedCode = &codes[i]
		}
	}
	if usedCode == nil || usedCode.UsedAt == nil {
		t.Fatalf("marked code = %+v, want used_at set", usedCode)
	}

	// Challenges are unique by token hash and cascade with the user.
	hash := []byte("challenge-hash")
	if err := q.CreateMFAChallenge(ctx, sqlc.CreateMFAChallengeParams{
		ID: uuid.New(), UserID: userID, TokenHash: hash, ExpiresAt: time.Now().Add(5 * time.Minute),
		UserAgent: "test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.CreateMFAChallenge(ctx, sqlc.CreateMFAChallengeParams{
		ID: uuid.New(), UserID: userID, TokenHash: hash, ExpiresAt: time.Now().Add(5 * time.Minute),
	}); err == nil {
		t.Fatal("duplicate challenge hash was accepted")
	}

	// Deleting other sessions keeps the named session.
	sessionID := uuid.New()
	otherID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO sessions (id, user_id, token_hash, expires_at) VALUES ($1,$2,$3,now()+interval '1 hour'), ($4,$2,$5,now()+interval '1 hour')`,
		sessionID, userID, []byte("keep"), otherID, []byte("drop")); err != nil {
		t.Fatal(err)
	}
	if err := q.DeleteOtherUserSessions(ctx, sqlc.DeleteOtherUserSessionsParams{
		UserID: userID, SessionID: sessionID,
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1`, userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("sessions = %d, want 1", count)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if n, err := q.CountUnusedRecoveryCodes(ctx, userID); err != nil || n != 0 {
		t.Fatalf("recovery after delete = %d, err = %v", n, err)
	}
}

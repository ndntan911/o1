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
		ID:          eventID,
		WorkspaceID: pgtype.UUID{Bytes: wsID, Valid: true},
		ActorUserID: pgtype.UUID{Bytes: adminID, Valid: true},
		Kind:        "workspace_suspended",
		Detail:      []byte(`{"reason":"abuse"}`),
	}); err != nil {
		t.Fatalf("create security event: %v", err)
	}
	if err := q.CreateSecurityEvent(ctx, sqlc.CreateSecurityEventParams{
		ID:          mustUUIDv7(t),
		WorkspaceID: pgtype.UUID{Bytes: wsID, Valid: true},
		ActorUserID: pgtype.UUID{Bytes: adminID, Valid: true},
		Kind:        "monitor_created",
		Detail:      []byte(`{}`),
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

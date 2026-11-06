package incidents_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"o1/internal/db/sqlc"
	"o1/internal/incidents"
	"o1/internal/jobs"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

type fakeInserter struct {
	jobs []river.JobArgs
	opts []*river.InsertOpts
}

func (f *fakeInserter) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	f.jobs = append(f.jobs, args)
	f.opts = append(f.opts, opts)
	return &rivertype.JobInsertResult{}, nil
}

func createLifecycleFixtures(t *testing.T, pool *pgxpool.Pool) (workspaceID, monitorID, channelID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	ownerID := testutil.CreateVerifiedUser(t, pool, "lifecycle-"+uuid.NewString()+"@example.com", "correct horse battery staple")
	wsvc := workspaces.NewService(pool, sqlc.New(pool))
	ws, err := wsvc.Create(ctx, ownerID, "lifecycle "+uuid.NewString())
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	monitorID, err = uuid.NewV7()
	if err != nil {
		t.Fatalf("new monitor id: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO monitors
			(id, workspace_id, name, url, method, expected_status,
			 interval_seconds, timeout_seconds, confirmation_threshold,
			 recovery_threshold, paused, created_by)
		VALUES ($1, $2, 'lifecycle monitor', 'https://example.com/health', 'GET', '200-299', 60, 5, 1, 1, false, $3)`,
		monitorID, ws.ID, ownerID); err != nil {
		t.Fatalf("insert monitor: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO monitor_state (monitor_id) VALUES ($1)`, monitorID); err != nil {
		t.Fatalf("insert monitor state: %v", err)
	}

	channelID, err = uuid.NewV7()
	if err != nil {
		t.Fatalf("new channel id: %v", err)
	}
	config := []byte(`{"recipients":["one@example.com","two@example.com"]}`)
	if _, err := pool.Exec(ctx, `
		INSERT INTO alert_channels (id, workspace_id, kind, name, config)
		VALUES ($1, $2, 'email', 'Email', $3)`,
		channelID, ws.ID, config); err != nil {
		t.Fatalf("insert alert channel: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO monitor_alert_channels (monitor_id, channel_id)
		VALUES ($1, $2)`, monitorID, channelID); err != nil {
		t.Fatalf("attach alert channel: %v", err)
	}

	return ws.ID, monitorID, channelID
}

func countRows(t *testing.T, tx pgx.Tx, query string, args ...any) int {
	t.Helper()
	var n int
	if err := tx.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

func TestApplyProbeTransition(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	workspaceID, monitorID, _ := createLifecycleFixtures(t, pool)

	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	q := sqlc.New(pool).WithTx(tx)
	inserter := &fakeInserter{}
	at := time.Now().UTC().Truncate(time.Millisecond)

	open := incidents.Transition{
		WorkspaceID: workspaceID,
		MonitorID:   monitorID,
		Previous:    "unknown",
		Next:        "down",
		At:          at,
	}
	if err := incidents.ApplyProbeTransition(ctx, tx, q, inserter, open); err != nil {
		t.Fatalf("open incident: %v", err)
	}

	if got := countRows(t, tx, `SELECT count(*) FROM incidents WHERE monitor_id = $1`, monitorID); got != 1 {
		t.Fatalf("incidents after open = %d, want 1", got)
	}
	incident, err := q.GetActiveIncidentForMonitor(ctx, monitorID)
	if err != nil {
		t.Fatalf("get active incident: %v", err)
	}
	if incident.Status != "open" {
		t.Fatalf("incident status = %q, want open", incident.Status)
	}

	events, err := q.ListIncidentEvents(ctx, incident.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "opened" {
		t.Fatalf("events = %+v, want one opened event", events)
	}
	if events[0].ActorUserID.Valid {
		t.Fatal("opened event has an actor, want null")
	}
	var detail map[string]string
	if err := json.Unmarshal(events[0].Detail, &detail); err != nil {
		t.Fatalf("decode opened detail: %v", err)
	}
	if detail["previous_status"] != "unknown" || detail["new_status"] != "down" {
		t.Fatalf("opened detail = %v", detail)
	}

	deliveries, err := q.ListIncidentDeliveries(ctx, incident.ID)
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	if len(deliveries) != 2 {
		t.Fatalf("deliveries after open = %d, want 2", len(deliveries))
	}
	assertDeliveries(t, deliveries, "opened", map[string]bool{
		"one@example.com": true,
		"two@example.com": true,
	})
	assertAlertJobs(t, inserter, deliveries)

	// Replaying the same transition must not create another incident, event,
	// delivery, or job.
	if err := incidents.ApplyProbeTransition(ctx, tx, q, inserter, open); err != nil {
		t.Fatalf("replay open: %v", err)
	}
	if got := countRows(t, tx, `SELECT count(*) FROM incidents WHERE monitor_id = $1`, monitorID); got != 1 {
		t.Fatalf("incidents after replay = %d, want 1", got)
	}
	if got := countRows(t, tx, `SELECT count(*) FROM incident_events WHERE incident_id = $1`, incident.ID); got != 1 {
		t.Fatalf("events after replay = %d, want 1", got)
	}
	if got := countRows(t, tx, `SELECT count(*) FROM alert_deliveries WHERE incident_id = $1`, incident.ID); got != 2 {
		t.Fatalf("deliveries after replay = %d, want 2", got)
	}
	if len(inserter.jobs) != 2 {
		t.Fatalf("jobs after replay = %d, want 2", len(inserter.jobs))
	}

	// Acknowledging does not suppress the resolve notification.
	if err := q.AcknowledgeIncident(ctx, sqlc.AcknowledgeIncidentParams{
		ID:             incident.ID,
		AcknowledgedAt: &at,
		AcknowledgedBy: pgtype.UUID{},
	}); err != nil {
		t.Fatalf("acknowledge incident: %v", err)
	}

	resolve := open
	resolve.Previous = "down"
	resolve.Next = "up"
	resolve.At = at.Add(time.Minute)
	if err := incidents.ApplyProbeTransition(ctx, tx, q, inserter, resolve); err != nil {
		t.Fatalf("resolve incident: %v", err)
	}

	resolved, err := q.GetIncident(ctx, sqlc.GetIncidentParams{ID: incident.ID, WorkspaceID: workspaceID})
	if err != nil {
		t.Fatalf("get resolved incident: %v", err)
	}
	if resolved.Status != "resolved" || resolved.ResolvedAt == nil {
		t.Fatalf("resolved incident = status %q, resolved_at %v", resolved.Status, resolved.ResolvedAt)
	}

	events, err = q.ListIncidentEvents(ctx, incident.ID)
	if err != nil {
		t.Fatalf("list events after resolve: %v", err)
	}
	if len(events) != 2 || events[0].Kind != "opened" || events[1].Kind != "resolved" {
		t.Fatalf("events after resolve = %+v", events)
	}
	if events[1].ActorUserID.Valid {
		t.Fatal("resolved event has an actor, want null")
	}
	detail = nil
	if err := json.Unmarshal(events[1].Detail, &detail); err != nil {
		t.Fatalf("decode resolved detail: %v", err)
	}
	if detail["previous_status"] != "down" || detail["new_status"] != "up" {
		t.Fatalf("resolved detail = %v", detail)
	}

	deliveries, err = q.ListIncidentDeliveries(ctx, incident.ID)
	if err != nil {
		t.Fatalf("list deliveries after resolve: %v", err)
	}
	if len(deliveries) != 4 {
		t.Fatalf("deliveries after resolve = %d, want 4", len(deliveries))
	}
	assertDeliveries(t, deliveries, "resolved", map[string]bool{
		"one@example.com": true,
		"two@example.com": true,
	})
	if len(inserter.jobs) != 4 {
		t.Fatalf("jobs after resolve = %d, want 4", len(inserter.jobs))
	}

	// Resolving again (no active incident) and staying up are no-ops.
	if err := incidents.ApplyProbeTransition(ctx, tx, q, inserter, resolve); err != nil {
		t.Fatalf("replay resolve: %v", err)
	}
	up := open
	up.Previous = "up"
	up.Next = "up"
	if err := incidents.ApplyProbeTransition(ctx, tx, q, inserter, up); err != nil {
		t.Fatalf("up to up: %v", err)
	}
	if got := countRows(t, tx, `SELECT count(*) FROM incidents WHERE monitor_id = $1`, monitorID); got != 1 {
		t.Fatalf("incidents after no-ops = %d, want 1", got)
	}
	if got := countRows(t, tx, `SELECT count(*) FROM incident_events WHERE incident_id = $1`, incident.ID); got != 2 {
		t.Fatalf("events after no-ops = %d, want 2", got)
	}
	if got := countRows(t, tx, `SELECT count(*) FROM alert_deliveries WHERE incident_id = $1`, incident.ID); got != 4 {
		t.Fatalf("deliveries after no-ops = %d, want 4", got)
	}
	if len(inserter.jobs) != 4 {
		t.Fatalf("jobs after no-ops = %d, want 4", len(inserter.jobs))
	}

	// Once the monitor is up again, a later failure opens a new incident.
	openAgain := open
	openAgain.Previous = "up"
	openAgain.Next = "down"
	openAgain.At = at.Add(2 * time.Minute)
	if err := incidents.ApplyProbeTransition(ctx, tx, q, inserter, openAgain); err != nil {
		t.Fatalf("open after recovery: %v", err)
	}
	if got := countRows(t, tx, `SELECT count(*) FROM incidents WHERE monitor_id = $1`, monitorID); got != 2 {
		t.Fatalf("incidents after up to down = %d, want 2", got)
	}
	second, err := q.GetActiveIncidentForMonitor(ctx, monitorID)
	if err != nil {
		t.Fatalf("get second active incident: %v", err)
	}
	if second.ID == incident.ID || second.Status != "open" {
		t.Fatalf("second incident = id %s status %q", second.ID, second.Status)
	}
	secondEvents, err := q.ListIncidentEvents(ctx, second.ID)
	if err != nil {
		t.Fatalf("list second incident events: %v", err)
	}
	if len(secondEvents) != 1 || secondEvents[0].Kind != "opened" {
		t.Fatalf("second incident events = %+v", secondEvents)
	}
	var secondDetail map[string]string
	if err := json.Unmarshal(secondEvents[0].Detail, &secondDetail); err != nil {
		t.Fatalf("decode second opened detail: %v", err)
	}
	if secondDetail["previous_status"] != "up" || secondDetail["new_status"] != "down" {
		t.Fatalf("second opened detail = %v", secondDetail)
	}
	secondDeliveries, err := q.ListIncidentDeliveries(ctx, second.ID)
	if err != nil {
		t.Fatalf("list second incident deliveries: %v", err)
	}
	if len(secondDeliveries) != 2 {
		t.Fatalf("second incident deliveries = %d, want 2", len(secondDeliveries))
	}
	if len(inserter.jobs) != 6 {
		t.Fatalf("jobs after up to down = %d, want 6", len(inserter.jobs))
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestApplyProbeTransitionSkipsMalformedChannelConfig(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	workspaceID, monitorID, channelID := createLifecycleFixtures(t, pool)
	ctx := context.Background()

	// Valid JSON but the wrong shape for EmailConfig. The incident must still
	// open; only the malformed channel's deliveries are skipped.
	if _, err := pool.Exec(ctx, `UPDATE alert_channels SET config = $1 WHERE id = $2`,
		[]byte(`{"recipients":"not-a-list"}`), channelID); err != nil {
		t.Fatalf("corrupt channel config: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	q := sqlc.New(pool).WithTx(tx)
	inserter := &fakeInserter{}

	if err := incidents.ApplyProbeTransition(ctx, tx, q, inserter, incidents.Transition{
		WorkspaceID: workspaceID,
		MonitorID:   monitorID,
		Previous:    "unknown",
		Next:        "down",
		At:          time.Now().UTC(),
	}); err != nil {
		t.Fatalf("apply transition with malformed channel: %v", err)
	}

	if got := countRows(t, tx, `SELECT count(*) FROM incidents WHERE monitor_id = $1`, monitorID); got != 1 {
		t.Fatalf("incidents = %d, want 1", got)
	}
	incident, err := q.GetActiveIncidentForMonitor(ctx, monitorID)
	if err != nil {
		t.Fatalf("get active incident: %v", err)
	}
	if got := countRows(t, tx, `SELECT count(*) FROM incident_events WHERE incident_id = $1`, incident.ID); got != 1 {
		t.Fatalf("events = %d, want 1", got)
	}
	if got := countRows(t, tx, `SELECT count(*) FROM alert_deliveries WHERE incident_id = $1`, incident.ID); got != 0 {
		t.Fatalf("deliveries = %d, want 0 for malformed channel", got)
	}
	if len(inserter.jobs) != 0 {
		t.Fatalf("jobs = %d, want 0 for malformed channel", len(inserter.jobs))
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestApplyProbeTransitionSkipsDuplicateRecipient(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	workspaceID, monitorID, channelID := createLifecycleFixtures(t, pool)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `UPDATE alert_channels SET config = $1 WHERE id = $2`,
		[]byte(`{"recipients":["dup@example.com","dup@example.com"]}`), channelID); err != nil {
		t.Fatalf("set duplicate channel config: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	q := sqlc.New(pool).WithTx(tx)
	inserter := &fakeInserter{}

	if err := incidents.ApplyProbeTransition(ctx, tx, q, inserter, incidents.Transition{
		WorkspaceID: workspaceID,
		MonitorID:   monitorID,
		Previous:    "unknown",
		Next:        "down",
		At:          time.Now().UTC(),
	}); err != nil {
		t.Fatalf("apply transition with duplicate recipient: %v", err)
	}

	incident, err := q.GetActiveIncidentForMonitor(ctx, monitorID)
	if err != nil {
		t.Fatalf("get active incident: %v", err)
	}
	deliveries, err := q.ListIncidentDeliveries(ctx, incident.ID)
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	if len(deliveries) != 1 || deliveries[0].Recipient != "dup@example.com" {
		t.Fatalf("deliveries = %+v, want one dup@example.com delivery", deliveries)
	}
	if len(inserter.jobs) != 1 {
		t.Fatalf("jobs = %d, want 1 after delivery conflict", len(inserter.jobs))
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func assertDeliveries(t *testing.T, deliveries []sqlc.ListIncidentDeliveriesRow, event string, recipients map[string]bool) {
	t.Helper()
	seen := make(map[string]bool)
	for _, delivery := range deliveries {
		if delivery.Event != event {
			continue
		}
		if delivery.Sequence != 0 || delivery.Status != "pending" {
			t.Fatalf("delivery sequence/status = %d/%q", delivery.Sequence, delivery.Status)
		}
		seen[delivery.Recipient] = true
	}
	if len(seen) != len(recipients) {
		t.Fatalf("%s recipients = %v, want %v", event, seen, recipients)
	}
	for recipient := range recipients {
		if !seen[recipient] {
			t.Fatalf("%s delivery missing recipient %q", event, recipient)
		}
	}
}

func assertAlertJobs(t *testing.T, inserter *fakeInserter, deliveries []sqlc.ListIncidentDeliveriesRow) {
	t.Helper()
	if len(inserter.jobs) != len(deliveries) {
		t.Fatalf("jobs = %d, want %d", len(inserter.jobs), len(deliveries))
	}
	wantDeliveries := make(map[uuid.UUID]bool, len(deliveries))
	for _, delivery := range deliveries {
		wantDeliveries[delivery.ID] = true
	}
	for i, args := range inserter.jobs {
		alertArgs, ok := args.(jobs.AlertArgs)
		if !ok {
			t.Fatalf("job %d type = %T, want jobs.AlertArgs", i, args)
		}
		if !wantDeliveries[alertArgs.DeliveryID] {
			t.Fatalf("job %d delivery %s does not match an inserted delivery", i, alertArgs.DeliveryID)
		}
		if inserter.opts[i] == nil {
			t.Fatalf("job %d has nil insert opts", i)
		}
		if inserter.opts[i].MaxAttempts != 5 || inserter.opts[i].Queue != "alert" {
			t.Fatalf("job %d opts = max %d queue %q, want max 5 queue alert",
				i, inserter.opts[i].MaxAttempts, inserter.opts[i].Queue)
		}
	}
}

package statuspage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/db/sqlc"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

const serviceTestPassword = "correct horse battery staple"

type serviceEnv struct {
	t      *testing.T
	pool   *pgxpool.Pool
	ws     *workspaces.Service
	svc    *Service
	wsID   uuid.UUID
	slug   string
	owner  uuid.UUID
	admin  uuid.UUID
	member uuid.UUID
	viewer uuid.UUID
}

func newServiceEnv(t *testing.T) *serviceEnv {
	t.Helper()
	pool, cleanup := testutil.NewPostgres(t)
	t.Cleanup(cleanup)
	wsSvc := workspaces.NewService(pool, sqlc.New(pool))
	owner := testutil.CreateVerifiedUser(t, pool, "status-svc-owner@example.com", serviceTestPassword)
	ws, err := wsSvc.Create(context.Background(), owner, "Status Service")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	env := &serviceEnv{t: t, pool: pool, ws: wsSvc, svc: NewService(pool, sqlc.New(pool), wsSvc), wsID: ws.ID, slug: ws.Slug, owner: owner}
	env.admin = env.addMember("status-svc-admin@example.com", "admin")
	env.member = env.addMember("status-svc-member@example.com", "member")
	env.viewer = env.addMember("status-svc-viewer@example.com", "viewer")
	// Disable caching by default so tests observe DB changes immediately. The
	// dedicated cache test installs its own 15-second cache with a fake clock.
	env.svc.cache = newPageCache(-1*time.Second, time.Now)
	return env
}

func (e *serviceEnv) addMember(emailAddr, role string) uuid.UUID {
	e.t.Helper()
	id := testutil.CreateVerifiedUser(e.t, e.pool, emailAddr, serviceTestPassword)
	if err := sqlc.New(e.pool).UpsertWorkspaceMember(context.Background(), sqlc.UpsertWorkspaceMemberParams{
		WorkspaceID: e.wsID, UserID: id, Role: role,
	}); err != nil {
		e.t.Fatalf("add member: %v", err)
	}
	return id
}

func (e *serviceEnv) addMonitor(name string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO monitors (id, workspace_id, name, url, method, interval_seconds, timeout_seconds, created_by) VALUES ($1,$2,$3,'https://secret.example.com/path?token=abc','GET',60,10,$4)`, id, e.wsID, name, e.owner); err != nil {
		e.t.Fatalf("insert monitor: %v", err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO monitor_state (monitor_id) VALUES ($1)`, id); err != nil {
		e.t.Fatalf("insert monitor state: %v", err)
	}
	return id
}

func TestService_GetConfigDefaults(t *testing.T) {
	env := newServiceEnv(t)
	cfg, err := env.svc.GetConfig(context.Background(), env.owner, env.wsID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || cfg.Title != "Status Service" || cfg.Description != "" || cfg.MonitorIDs == nil || len(cfg.MonitorIDs) != 0 {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestService_UpdateConfigValidation(t *testing.T) {
	env := newServiceEnv(t)
	otherOwner := testutil.CreateVerifiedUser(t, env.pool, "status-svc-other@example.com", serviceTestPassword)
	other, err := env.ws.Create(context.Background(), otherOwner, "Other")
	if err != nil {
		t.Fatal(err)
	}
	otherMonitor := uuid.New()
	if _, err := env.pool.Exec(context.Background(), `INSERT INTO monitors (id, workspace_id, name, url, method, interval_seconds, timeout_seconds, created_by) VALUES ($1,$2,'Other','https://example.com','GET',60,10,$3)`, otherMonitor, other.ID, otherOwner); err != nil {
		t.Fatal(err)
	}
	monitorID := env.addMonitor("API")

	cases := []struct {
		name  string
		in    ConfigInput
		field string
	}{
		{"blank title", ConfigInput{Title: "  ", MonitorIDs: []uuid.UUID{monitorID}}, "title"},
		{"long title", ConfigInput{Title: strings.Repeat("a", 81), MonitorIDs: []uuid.UUID{monitorID}}, "title"},
		{"long description", ConfigInput{Title: "Acme", Description: strings.Repeat("a", 501), MonitorIDs: []uuid.UUID{monitorID}}, "description"},
		{"enabled without monitors", ConfigInput{Enabled: true, Title: "Acme"}, "monitor_ids"},
		{"foreign monitor", ConfigInput{Title: "Acme", MonitorIDs: []uuid.UUID{otherMonitor}}, "monitor_ids"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.svc.UpdateConfig(context.Background(), env.owner, env.wsID, tc.in)
			var validation *ValidationError
			if !errors.As(err, &validation) || validation.Fields[tc.field] == "" {
				t.Fatalf("error = %v, want field %q", err, tc.field)
			}
		})
	}
}

func TestService_UpdateConfigReplacesSelection(t *testing.T) {
	env := newServiceEnv(t)
	first := env.addMonitor("API")
	second := env.addMonitor("Web")

	cfg, err := env.svc.UpdateConfig(context.Background(), env.owner, env.wsID, ConfigInput{
		Enabled: true, Title: "  Acme status  ", Description: "Live availability",
		MonitorIDs: []uuid.UUID{second, first, second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Title != "Acme status" || !cfg.Enabled {
		t.Fatalf("cfg = %+v", cfg)
	}
	if len(cfg.MonitorIDs) != 2 || cfg.MonitorIDs[0] != first || cfg.MonitorIDs[1] != second {
		t.Fatalf("monitor ids = %v, want creation order [first second]", cfg.MonitorIDs)
	}
	ids, err := sqlc.New(env.pool).ListStatusPageMonitorIDs(context.Background(), env.wsID)
	if err != nil || len(ids) != 2 {
		t.Fatalf("stored selection = %v, err = %v", ids, err)
	}

	cfg, err = env.svc.UpdateConfig(context.Background(), env.owner, env.wsID, ConfigInput{
		Enabled: true, Title: "Acme status", MonitorIDs: []uuid.UUID{second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MonitorIDs) != 1 || cfg.MonitorIDs[0] != second {
		t.Fatalf("replaced selection = %v", cfg.MonitorIDs)
	}
}

func TestService_UpdateConfigRolesAndSuspension(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()
	monitorID := env.addMonitor("API")
	in := ConfigInput{Enabled: true, Title: "Acme", MonitorIDs: []uuid.UUID{monitorID}}

	for _, tc := range []struct {
		name  string
		actor uuid.UUID
	}{
		{"member", env.member},
		{"viewer", env.viewer},
	} {
		t.Run(tc.name+" denied", func(t *testing.T) {
			if _, err := env.svc.UpdateConfig(ctx, tc.actor, env.wsID, in); !errors.Is(err, ErrForbidden) {
				t.Fatalf("UpdateConfig as %s = %v, want ErrForbidden", tc.name, err)
			}
		})
	}

	for _, tc := range []struct {
		name  string
		actor uuid.UUID
	}{
		{"admin", env.admin},
		{"owner", env.owner},
	} {
		t.Run(tc.name+" allowed", func(t *testing.T) {
			cfg, err := env.svc.UpdateConfig(ctx, tc.actor, env.wsID, in)
			if err != nil {
				t.Fatalf("UpdateConfig as %s: %v", tc.name, err)
			}
			if cfg.Title != "Acme" || !cfg.Enabled || len(cfg.MonitorIDs) != 1 {
				t.Fatalf("UpdateConfig as %s = %+v", tc.name, cfg)
			}
		})
	}

	if _, err := env.pool.Exec(ctx, `UPDATE workspaces SET suspended_at = now() WHERE id = $1`, env.wsID); err != nil {
		t.Fatalf("suspend workspace: %v", err)
	}
	if _, err := env.svc.UpdateConfig(ctx, env.owner, env.wsID, in); !errors.Is(err, ErrSuspended) {
		t.Fatalf("suspended UpdateConfig = %v, want ErrSuspended", err)
	}
	cfg, err := env.svc.GetConfig(ctx, env.owner, env.wsID)
	if err != nil {
		t.Fatalf("suspended GetConfig: %v", err)
	}
	if cfg.Title != "Acme" || !cfg.Enabled || len(cfg.MonitorIDs) != 1 || cfg.MonitorIDs[0] != monitorID {
		t.Fatalf("suspended GetConfig = %+v", cfg)
	}

	otherOwner := testutil.CreateVerifiedUser(t, env.pool, "status-svc-other-ws@example.com", serviceTestPassword)
	other, err := env.ws.Create(ctx, otherOwner, "Other Workspace")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.UpdateConfig(ctx, env.owner, other.ID, in); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace UpdateConfig = %v, want ErrNotFound", err)
	}
	if _, err := env.svc.GetConfig(ctx, env.owner, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace GetConfig = %v, want ErrNotFound", err)
	}
}

func (e *serviceEnv) insertProbe(t *testing.T, monitorID uuid.UUID, at time.Time, status string) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO probe_results (id, monitor_id, workspace_id, scheduled_for, checked_at, status, latency_ms)
		VALUES ($1,$2,$3,$4,$4,$5,100)`,
		uuid.New(), monitorID, e.wsID, at, status); err != nil {
		t.Fatalf("insert probe: %v", err)
	}
}

func (e *serviceEnv) enablePage(t *testing.T, monitors ...uuid.UUID) {
	t.Helper()
	if _, err := e.svc.UpdateConfig(context.Background(), e.owner, e.wsID, ConfigInput{
		Enabled: true, Title: "Acme status", Description: "Live availability", MonitorIDs: monitors,
	}); err != nil {
		t.Fatalf("enable page: %v", err)
	}
}

func (e *serviceEnv) ensureProbePartition(t *testing.T, day time.Time) {
	t.Helper()
	month := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	stmt := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s PARTITION OF probe_results FOR VALUES FROM ('%s') TO ('%s')",
		"probe_results_"+month.Format("2006_01"),
		month.Format(time.RFC3339),
		month.AddDate(0, 1, 0).Format(time.RFC3339),
	)
	if _, err := e.pool.Exec(context.Background(), stmt); err != nil {
		t.Fatalf("create probe partition: %v", err)
	}
}

// insertHourlyStat inserts one completed hourly rollup bucket.
func (e *serviceEnv) insertHourlyStat(t *testing.T, monitorID uuid.UUID, bucket time.Time, up, down int64) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO monitor_hourly_stats (
			monitor_id, workspace_id, bucket_start, up_count, down_count, checks,
			avg_latency_ms, min_latency_ms, max_latency_ms, p95_latency_ms
		) VALUES ($1,$2,$3,$4,$5,$6,100,90,110,105)`,
		monitorID, e.wsID, bucket, up, down, up+down); err != nil {
		t.Fatalf("insert hourly stat: %v", err)
	}
}

// setRollupWatermark marks the last contiguous fully rolled-up hour.
func (e *serviceEnv) setRollupWatermark(t *testing.T, at time.Time) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE rollup_state SET watermark=$1 WHERE id`, at); err != nil {
		t.Fatalf("set rollup watermark: %v", err)
	}
}

// insertDayProbes writes up and down raw checks spread across one UTC day.
func (e *serviceEnv) insertDayProbes(t *testing.T, monitorID uuid.UUID, day time.Time, up, down int) {
	t.Helper()
	e.ensureProbePartition(t, day)
	for i := 0; i < up; i++ {
		e.insertProbe(t, monitorID, day.Add(time.Duration(i+1)*time.Minute), "up")
	}
	for i := 0; i < down; i++ {
		e.insertProbe(t, monitorID, day.Add(time.Duration(up+i+1)*time.Minute), "down")
	}
}

func (e *serviceEnv) insertIncident(t *testing.T, monitorID uuid.UUID, status string, startedAt time.Time, acknowledgedAt, resolvedAt *time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	var ackBy pgtype.UUID
	if acknowledgedAt != nil {
		ackBy = pgtype.UUID{Bytes: e.owner, Valid: true}
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO incidents
			(id, workspace_id, monitor_id, status, started_at, acknowledged_at,
			 acknowledged_by, resolved_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, e.wsID, monitorID, status, startedAt, acknowledgedAt, ackBy, resolvedAt); err != nil {
		t.Fatalf("insert incident: %v", err)
	}
	return id
}

func TestService_PublicPageBuckets(t *testing.T) {
	env := newServiceEnv(t)
	now := time.Now().UTC().Truncate(time.Second)
	env.svc.now = func() time.Time { return now }
	monitorID := env.addMonitor("API")
	env.enablePage(t, monitorID)

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	old := today.Add(-100 * 24 * time.Hour)
	env.ensureProbePartition(t, old)                            // the month is outside the maintained window
	env.insertProbe(t, monitorID, today.Add(time.Minute), "up") // partial today
	env.insertProbe(t, monitorID, today.Add(2*time.Minute), "down")
	env.insertProbe(t, monitorID, today.Add(-1*time.Hour), "up")    // yesterday
	env.insertProbe(t, monitorID, today.Add(-48*time.Hour), "down") // two days ago
	env.insertProbe(t, monitorID, old, "down")                      // outside 90 days
	tomorrow := today.AddDate(0, 0, 1)
	env.ensureProbePartition(t, tomorrow)
	env.insertProbe(t, monitorID, tomorrow.Add(time.Minute), "up") // future-dated, outside the emitted days

	page, err := env.svc.PublicPage(context.Background(), env.slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Monitors) != 1 {
		t.Fatalf("monitors = %+v", page.Monitors)
	}
	days := page.Monitors[0].Days
	if len(days) != 90 {
		t.Fatalf("days = %d, want 90", len(days))
	}
	if days[89].Date != today.Format("2006-01-02") || days[89].Status != "partial" {
		t.Fatalf("today = %+v", days[89])
	}
	if days[88].Date != today.AddDate(0, 0, -1).Format("2006-01-02") || days[88].Status != "up" {
		t.Fatalf("yesterday = %+v", days[88])
	}
	if days[87].Status != "down" {
		t.Fatalf("two days ago = %+v", days[87])
	}
	if days[86].Status != "no_data" || days[86].Uptime != nil {
		t.Fatalf("empty day = %+v", days[86])
	}
	if page.Monitors[0].Uptime == nil || *page.Monitors[0].Uptime != 0.5 {
		t.Fatalf("uptime = %v, want 0.5", page.Monitors[0].Uptime)
	}
}

func TestPublicPage_UsesRollupsBelowWatermarkAndRawAtTheFrontier(t *testing.T) {
	env := newServiceEnv(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	env.svc.now = func() time.Time { return now }
	monitorID := env.addMonitor("API")
	env.enablePage(t, monitorID)

	day := func(offset int) time.Time {
		return startOfUTCDay(now).AddDate(0, 0, offset)
	}

	// The watermark is late on 11-09, so rollups are trusted only below the
	// split at the start of that day. The 11-09 rollup row must not be counted
	// because raw owns 11-09 onward.
	env.setRollupWatermark(t, day(-2).Add(23*time.Hour))
	env.insertHourlyStat(t, monitorID, day(-3).Add(10*time.Hour), 9, 0) // 11-08, rollup
	env.insertHourlyStat(t, monitorID, day(-2).Add(6*time.Hour), 5, 0)  // 11-09, ignored
	// Raw rows below the split must never be read: the rollup owns 11-08.
	// Merging these in would make the day partial and inflate uptime to 14/17.
	env.insertDayProbes(t, monitorID, day(-3), 1, 1) // 11-08, raw below split
	env.insertDayProbes(t, monitorID, day(-1), 2, 2) // 11-10, raw
	env.insertDayProbes(t, monitorID, day(0), 2, 0)  // 11-11, raw

	page, err := env.svc.PublicPage(context.Background(), env.slug)
	if err != nil {
		t.Fatal(err)
	}
	days := page.Monitors[0].Days
	if len(days) != 90 {
		t.Fatalf("days = %d, want 90", len(days))
	}
	if got := days[89]; got.Date != "2026-11-11" || got.Status != "up" {
		t.Fatalf("today = %+v, want 11-11 up from raw", got)
	}
	if got := days[88]; got.Date != "2026-11-10" || got.Status != "partial" {
		t.Fatalf("frontier day = %+v, want 11-10 partial from raw", got)
	}
	if got := days[87]; got.Date != "2026-11-09" || got.Status != "no_data" || got.Uptime != nil {
		t.Fatalf("split day = %+v, want 11-09 no_data from absent raw", got)
	}
	if got := days[86]; got.Date != "2026-11-08" || got.Status != "up" {
		t.Fatalf("rollup day = %+v, want 11-08 up from rollups (raw below the split ignored)", got)
	}
	// 11-08 (9/9) + 11-10 (2/4) + 11-11 (2/2) = 13/15. The 11-09 rollup is
	// excluded, so neither source is double counted.
	if page.Monitors[0].Uptime == nil || *page.Monitors[0].Uptime != 13.0/15.0 {
		t.Fatalf("uptime = %v, want %v", page.Monitors[0].Uptime, 13.0/15.0)
	}
}

func TestPublicPage_NoWatermarkUsesRawOnly(t *testing.T) {
	env := newServiceEnv(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	env.svc.now = func() time.Time { return now }
	monitorID := env.addMonitor("API")
	env.enablePage(t, monitorID)

	// A fresh database has a null watermark, so every day is served by raw.
	today := startOfUTCDay(now)
	env.insertDayProbes(t, monitorID, today.AddDate(0, 0, -1), 1, 1)
	env.insertDayProbes(t, monitorID, today, 1, 0)

	page, err := env.svc.PublicPage(context.Background(), env.slug)
	if err != nil {
		t.Fatal(err)
	}
	days := page.Monitors[0].Days
	if got := days[89]; got.Date != "2026-11-11" || got.Status != "up" {
		t.Fatalf("today = %+v", got)
	}
	if got := days[88]; got.Date != "2026-11-10" || got.Status != "partial" {
		t.Fatalf("yesterday = %+v", got)
	}
	if got := days[87]; got.Status != "no_data" || got.Uptime != nil {
		t.Fatalf("empty day = %+v", got)
	}
	if page.Monitors[0].Uptime == nil || *page.Monitors[0].Uptime != 2.0/3.0 {
		t.Fatalf("uptime = %v, want %v", page.Monitors[0].Uptime, 2.0/3.0)
	}
}

func TestPublicPage_MissingRollupDayIsNoData(t *testing.T) {
	env := newServiceEnv(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	env.svc.now = func() time.Time { return now }
	monitorID := env.addMonitor("API")
	env.enablePage(t, monitorID)

	day := func(offset int) time.Time {
		return startOfUTCDay(now).AddDate(0, 0, offset)
	}

	// The split is 11-10. Rollups own 11-09 but no row exists for it, and raw
	// starts at the split, so the day is empty rather than fabricated.
	env.setRollupWatermark(t, day(-1).Add(5*time.Hour))
	env.insertHourlyStat(t, monitorID, day(-3).Add(10*time.Hour), 3, 0) // 11-08
	env.insertDayProbes(t, monitorID, day(-1), 0, 2)                    // 11-10
	env.insertDayProbes(t, monitorID, day(0), 1, 1)                     // 11-11

	page, err := env.svc.PublicPage(context.Background(), env.slug)
	if err != nil {
		t.Fatal(err)
	}
	days := page.Monitors[0].Days
	if got := days[87]; got.Date != "2026-11-09" || got.Status != "no_data" || got.Uptime != nil {
		t.Fatalf("missing rollup day = %+v, want 11-09 no_data", got)
	}
	if got := days[86]; got.Date != "2026-11-08" || got.Status != "up" {
		t.Fatalf("rollup day = %+v", got)
	}
	// 11-08 (3/3) + 11-10 (0/2) + 11-11 (1/2) = 4/7; the empty 11-09 adds no
	// checks and cannot drag uptime down.
	if page.Monitors[0].Uptime == nil || *page.Monitors[0].Uptime != 4.0/7.0 {
		t.Fatalf("uptime = %v, want %v", page.Monitors[0].Uptime, 4.0/7.0)
	}
}

func TestPublicPage_WatermarkBeforeWindowUsesRawOnly(t *testing.T) {
	env := newServiceEnv(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	env.svc.now = func() time.Time { return now }
	monitorID := env.addMonitor("API")
	env.enablePage(t, monitorID)

	today := startOfUTCDay(now)
	// The watermark predates the 90-day window, so its UTC day is before
	// since: no rollup day is usable and raw must cover the whole window.
	env.setRollupWatermark(t, today.AddDate(-1, 0, 0))
	// This in-window rollup row must be ignored because the split is behind
	// the window. Using it would turn 11-08 into a 102-check up day.
	env.insertHourlyStat(t, monitorID, today.AddDate(0, 0, -3), 100, 0)
	env.insertDayProbes(t, monitorID, today.AddDate(0, 0, -3), 1, 1) // 11-08
	env.insertDayProbes(t, monitorID, today.AddDate(0, 0, -1), 2, 2) // 11-10
	env.insertDayProbes(t, monitorID, today, 1, 0)                   // 11-11

	page, err := env.svc.PublicPage(context.Background(), env.slug)
	if err != nil {
		t.Fatal(err)
	}
	days := page.Monitors[0].Days
	if got := days[89]; got.Date != "2026-11-11" || got.Status != "up" {
		t.Fatalf("today = %+v", got)
	}
	if got := days[88]; got.Date != "2026-11-10" || got.Status != "partial" {
		t.Fatalf("yesterday = %+v", got)
	}
	if got := days[87]; got.Status != "no_data" || got.Uptime != nil {
		t.Fatalf("empty day = %+v", got)
	}
	if got := days[86]; got.Date != "2026-11-08" || got.Status != "partial" {
		t.Fatalf("old watermark day = %+v, want 11-08 partial from raw", got)
	}
	// Raw only: 11-08 (1/2) + 11-10 (2/4) + 11-11 (1/1) = 4/7.
	if page.Monitors[0].Uptime == nil || *page.Monitors[0].Uptime != 4.0/7.0 {
		t.Fatalf("uptime = %v, want %v", page.Monitors[0].Uptime, 4.0/7.0)
	}
}

// ninetyBarCase returns the rollup data and expected status for the i-th bar of
// the 90-day window. It is the test's independent expectation, not the
// production classification logic.
func ninetyBarCase(i int) (up, down int64, status string) {
	switch {
	case i%10 == 3:
		return 0, 0, "no_data"
	case i%7 == 0:
		return 0, 5, "down"
	case i%3 == 0:
		return 2, 1, "partial"
	default:
		return 4, 0, "up"
	}
}

func TestPublicPage_NinetyBarsRemainUnchanged(t *testing.T) {
	env := newServiceEnv(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	env.svc.now = func() time.Time { return now }
	monitorID := env.addMonitor("API")
	env.enablePage(t, monitorID)

	today := startOfUTCDay(now)
	first := today.AddDate(0, 0, -(publicDayCount - 1))

	// Rollups cover the completed days; the watermark day's first hours are
	// rolled up too, but that whole day is re-read from raw.
	env.setRollupWatermark(t, today.Add(10*time.Hour))
	var wantUp, wantChecks int64
	for i := 0; i < publicDayCount-1; i++ {
		up, down, _ := ninetyBarCase(i)
		wantUp += up
		wantChecks += up + down
		if up+down == 0 {
			continue
		}
		env.insertHourlyStat(t, monitorID, first.AddDate(0, 0, i).Add(12*time.Hour), up, down)
	}
	env.insertDayProbes(t, monitorID, today, 3, 1)
	wantUp += 3
	wantChecks += 4

	page, err := env.svc.PublicPage(context.Background(), env.slug)
	if err != nil {
		t.Fatal(err)
	}
	days := page.Monitors[0].Days
	if len(days) != publicDayCount {
		t.Fatalf("days = %d, want %d", len(days), publicDayCount)
	}
	for i, got := range days {
		wantDate := first.AddDate(0, 0, i).Format("2006-01-02")
		wantStatus := "partial"
		if i < publicDayCount-1 {
			_, _, wantStatus = ninetyBarCase(i)
		}
		if got.Date != wantDate || got.Status != wantStatus {
			t.Fatalf("day %d = %+v, want date %s status %s", i, got, wantDate, wantStatus)
		}
	}
	want := float64(wantUp) / float64(wantChecks)
	if page.Monitors[0].Uptime == nil || *page.Monitors[0].Uptime != want {
		t.Fatalf("uptime = %v, want %v", page.Monitors[0].Uptime, want)
	}
}

func TestService_PublicPageStatusesAndEmptySelection(t *testing.T) {
	env := newServiceEnv(t)
	monitorID := env.addMonitor("API")
	env.enablePage(t, monitorID)

	page, err := env.svc.PublicPage(context.Background(), env.slug)
	if err != nil {
		t.Fatal(err)
	}
	if page.Monitors[0].Status != "unknown" {
		t.Fatalf("unknown status = %q", page.Monitors[0].Status)
	}
	if page.Page.UpdatedAt.IsZero() {
		t.Fatal("page updated_at is zero")
	}
	if _, err := env.pool.Exec(context.Background(), `UPDATE workspaces SET suspended_at=now() WHERE id=$1`, env.wsID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.PublicPage(context.Background(), env.slug); err != nil {
		t.Fatalf("public read while suspended: %v", err)
	}
	if _, err := env.pool.Exec(context.Background(), `UPDATE monitor_state SET status='down' WHERE monitor_id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(context.Background(), `UPDATE monitors SET paused=true WHERE id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}
	page, err = env.svc.PublicPage(context.Background(), env.slug)
	if err != nil {
		t.Fatal(err)
	}
	if page.Monitors[0].Status != "paused" {
		t.Fatalf("paused status = %q", page.Monitors[0].Status)
	}

	// Removing the last monitor leaves an enabled page with an empty list.
	if _, err := env.pool.Exec(context.Background(), `DELETE FROM monitors WHERE id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}
	page, err = env.svc.PublicPage(context.Background(), env.slug)
	if err != nil {
		t.Fatalf("empty page: %v", err)
	}
	if len(page.Monitors) != 0 || page.Incidents.Active == nil || page.Incidents.Recent == nil {
		t.Fatalf("empty page = %+v", page)
	}
}

func TestService_PublicPageIncidents(t *testing.T) {
	env := newServiceEnv(t)
	now := time.Now().UTC().Truncate(time.Second)
	env.svc.now = func() time.Time { return now }
	selected := env.addMonitor("API")
	// The unresolved-incident unique index allows only one unresolved incident
	// per monitor, so the acknowledged incident needs its own selected monitor.
	acknowledgedMonitor := env.addMonitor("Web")
	unselected := env.addMonitor("Hidden")
	env.enablePage(t, selected, acknowledgedMonitor)

	openID := env.insertIncident(t, selected, "open", now.Add(-2*time.Hour), nil, nil)
	acknowledgedAt := now.Add(-90 * time.Minute)
	acknowledgedID := env.insertIncident(t, acknowledgedMonitor, "acknowledged", acknowledgedAt, &acknowledgedAt, nil)
	resolvedAt := now.Add(-48 * time.Hour)
	resolvedID := env.insertIncident(t, selected, "resolved", now.Add(-50*time.Hour), nil, &resolvedAt)
	oldResolvedAt := now.Add(-10 * 24 * time.Hour)
	env.insertIncident(t, selected, "resolved", now.Add(-11*24*time.Hour), nil, &oldResolvedAt)
	unselectedID := env.insertIncident(t, unselected, "open", now.Add(-time.Hour), nil, nil)

	page, err := env.svc.PublicPage(context.Background(), env.slug)
	if err != nil {
		t.Fatal(err)
	}

	active := page.Incidents.Active
	if len(active) != 2 {
		t.Fatalf("active = %+v", active)
	}
	if active[0].ID != acknowledgedID || active[1].ID != openID {
		t.Fatalf("active order = %+v, want acknowledged then open", active)
	}
	for _, incident := range active {
		if incident.Status != "ongoing" || incident.ResolvedAt != nil {
			t.Fatalf("active incident = %+v", incident)
		}
		if incident.ID == unselectedID || incident.MonitorID == unselected {
			t.Fatalf("unselected monitor leaked into active: %+v", incident)
		}
	}
	recent := page.Incidents.Recent
	if len(recent) != 1 || recent[0].ID != resolvedID || recent[0].Status != "resolved" || recent[0].ResolvedAt == nil {
		t.Fatalf("recent = %+v", recent)
	}
	if recent[0].ID == unselectedID || recent[0].MonitorID == unselected {
		t.Fatalf("unselected monitor leaked into recent: %+v", recent[0])
	}

	// Twenty-five newer resolutions plus the existing one exceed the page cap.
	for i := 0; i < 25; i++ {
		at := now.Add(-time.Duration(i+1) * time.Minute)
		env.insertIncident(t, selected, "resolved", at.Add(-time.Hour), nil, &at)
	}
	page, err = env.svc.PublicPage(context.Background(), env.slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Incidents.Recent) != 20 {
		t.Fatalf("recent = %d, want 20", len(page.Incidents.Recent))
	}
}

func TestService_PublicPageNotFoundAndCache(t *testing.T) {
	env := newServiceEnv(t)
	now := time.Now().UTC().Truncate(time.Second)
	env.svc.now = func() time.Time { return now }
	env.svc.cache = newPageCache(15*time.Second, func() time.Time { return now })

	ctx := context.Background()
	if _, err := env.svc.PublicPage(ctx, "missing-slug"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing slug error = %v", err)
	}
	monitorID := env.addMonitor("API")
	if _, err := env.svc.PublicPage(ctx, env.slug); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unconfigured page error = %v", err)
	}
	env.enablePage(t, monitorID)

	today := startOfUTCDay(now)
	env.insertProbe(t, monitorID, today.Add(time.Minute), "up")
	first, err := env.svc.PublicPage(ctx, env.slug)
	if err != nil || first.Monitors[0].Uptime == nil || *first.Monitors[0].Uptime != 1 {
		t.Fatalf("first page = %+v, err = %v", first, err)
	}

	env.insertProbe(t, monitorID, today.Add(2*time.Minute), "down")
	second, err := env.svc.PublicPage(ctx, env.slug)
	if err != nil || second.Monitors[0].Uptime == nil || *second.Monitors[0].Uptime != 1 {
		t.Fatalf("cache hit page = %+v, err = %v", second, err)
	}

	now = now.Add(16 * time.Second)
	third, err := env.svc.PublicPage(ctx, env.slug)
	if err != nil || third.Monitors[0].Uptime == nil || *third.Monitors[0].Uptime != 0.5 {
		t.Fatalf("expired cache page = %+v, err = %v", third, err)
	}

	if _, err := env.svc.UpdateConfig(ctx, env.owner, env.wsID, ConfigInput{
		Enabled: true, Title: "Renamed", MonitorIDs: []uuid.UUID{monitorID},
	}); err != nil {
		t.Fatal(err)
	}
	fourth, err := env.svc.PublicPage(ctx, env.slug)
	if err != nil || fourth.Page.Title != "Renamed" {
		t.Fatalf("invalidated page = %+v, err = %v", fourth, err)
	}
}

func TestService_PublicPagePrivacy(t *testing.T) {
	env := newServiceEnv(t)
	now := time.Now().UTC().Truncate(time.Second)
	env.svc.now = func() time.Time { return now }
	monitorID := env.addMonitor("API")
	env.enablePage(t, monitorID)

	if _, err := env.pool.Exec(context.Background(),
		`UPDATE monitor_state SET status='down', last_error='token abc' WHERE monitor_id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}
	acknowledgedAt := now.Add(-time.Minute)
	resolvedAt := now.Add(-time.Hour)
	env.insertIncident(t, monitorID, "resolved", now.Add(-2*time.Hour), &acknowledgedAt, &resolvedAt)

	page, err := env.svc.PublicPage(context.Background(), env.slug)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{
		"secret.example.com", "token=abc", "acknowledged_by", "acknowledged_at",
		"delivery", "headers", "body", env.wsID.String(),
	} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("public payload leaks %q: %s", leak, raw)
		}
	}
}

func TestAuditStatusPageUpdated(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()
	monitorID := env.addMonitor("API")

	if _, err := env.svc.UpdateConfig(ctx, env.owner, env.wsID, ConfigInput{
		Enabled: true, Title: "Acme", MonitorIDs: []uuid.UUID{monitorID},
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}

	var detail string
	var actor *uuid.UUID
	if err := env.pool.QueryRow(ctx,
		`SELECT detail::text, actor_user_id FROM security_events WHERE workspace_id=$1 AND kind='status_page_updated'`,
		env.wsID).Scan(&detail, &actor); err != nil {
		t.Fatalf("load event: %v", err)
	}
	if actor == nil || *actor != env.owner {
		t.Fatalf("actor = %v, want %v", actor, env.owner)
	}
	if !strings.Contains(detail, `"enabled": true`) || !strings.Contains(detail, `"monitor_count": 1`) {
		t.Fatalf("detail = %s, want enabled and monitor_count", detail)
	}
}

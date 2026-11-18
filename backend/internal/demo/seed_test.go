package demo

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"o1/internal/auth"
	"o1/internal/testutil"
)

func TestSeedCreatesReusableDemoWorkspace(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)

	first, err := Seed(ctx, pool, Options{Now: now})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !first.AccountCreated {
		t.Fatal("first seed did not create the demo account")
	}
	if first.Password == "" {
		t.Fatal("first seed did not generate a password")
	}
	if first.Monitors != len(monitorSpecs) {
		t.Fatalf("monitors = %d, want %d", first.Monitors, len(monitorSpecs))
	}
	if first.Incidents != len(outages) {
		t.Fatalf("incidents = %d, want %d", first.Incidents, len(outages))
	}
	if first.StatusPath != "/status/demo" {
		t.Fatalf("status path = %q", first.StatusPath)
	}

	var hash string
	if err := pool.QueryRow(ctx,
		`SELECT password_hash FROM users WHERE id = $1`, first.UserID).Scan(&hash); err != nil {
		t.Fatalf("read demo password hash: %v", err)
	}
	ok, err := auth.VerifyPassword(hash, first.Password)
	if err != nil || !ok {
		t.Fatalf("generated password does not verify (ok=%v, err=%v)", ok, err)
	}

	var verified bool
	if err := pool.QueryRow(ctx,
		`SELECT email_verified_at IS NOT NULL FROM users WHERE id = $1`, first.UserID).Scan(&verified); err != nil {
		t.Fatalf("read demo verification: %v", err)
	}
	if !verified {
		t.Fatal("demo account is not verified")
	}

	var slug string
	var role string
	if err := pool.QueryRow(ctx, `
		SELECT w.slug, m.role
		FROM workspaces w
		JOIN workspace_members m ON m.workspace_id = w.id
		WHERE w.id = $1 AND m.user_id = $2`,
		first.WorkspaceID, first.UserID).Scan(&slug, &role); err != nil {
		t.Fatalf("read demo workspace: %v", err)
	}
	if slug != "demo" || role != "owner" {
		t.Fatalf("workspace slug/role = %q/%q", slug, role)
	}

	var monitors, paused int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE paused)
		FROM monitors WHERE workspace_id = $1`, first.WorkspaceID).Scan(&monitors, &paused); err != nil {
		t.Fatalf("count demo monitors: %v", err)
	}
	if monitors != len(monitorSpecs) || paused != 0 {
		t.Fatalf("monitors = %d (paused %d), want %d active", monitors, paused, len(monitorSpecs))
	}

	var enabled bool
	var published int
	if err := pool.QueryRow(ctx, `
		SELECT sp.enabled, count(spm.monitor_id)
		FROM status_pages sp
		LEFT JOIN status_page_monitors spm ON spm.status_page_id = sp.workspace_id
		WHERE sp.workspace_id = $1
		GROUP BY sp.enabled`, first.WorkspaceID).Scan(&enabled, &published); err != nil {
		t.Fatalf("read demo status page: %v", err)
	}
	if !enabled || published != len(monitorSpecs) {
		t.Fatalf("status page enabled=%v monitors=%d", enabled, published)
	}

	// 89 complete days of rollups; the current day is raw probe data.
	wantRows := int64(89 * 24 * len(monitorSpecs))
	if first.HourlyRows != wantRows {
		t.Fatalf("hourly rows = %d, want %d", first.HourlyRows, wantRows)
	}
	// One raw row per minute, per monitor, up to but excluding the current
	// minute: 15:00 UTC means 900 completed minutes.
	wantRaw := int64(15 * 60 * len(monitorSpecs))
	if first.RawRows != wantRaw {
		t.Fatalf("raw rows = %d, want %d", first.RawRows, wantRaw)
	}

	var watermark *time.Time
	if err := pool.QueryRow(ctx, `SELECT watermark FROM rollup_state`).Scan(&watermark); err != nil {
		t.Fatalf("read rollup watermark: %v", err)
	}
	if watermark == nil || !watermark.Equal(now.Truncate(24*time.Hour)) {
		t.Fatalf("rollup watermark = %v, want %v", watermark, now.Truncate(24*time.Hour))
	}

	var resolved int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM incidents WHERE workspace_id = $1 AND status = 'resolved'`,
		first.WorkspaceID).Scan(&resolved); err != nil {
		t.Fatalf("count demo incidents: %v", err)
	}
	if resolved != len(outages) {
		t.Fatalf("resolved incidents = %d, want %d", resolved, len(outages))
	}

	var events int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM incident_events e
		JOIN incidents i ON i.id = e.incident_id
		WHERE i.workspace_id = $1`, first.WorkspaceID).Scan(&events); err != nil {
		t.Fatalf("count demo incident events: %v", err)
	}
	if events != len(outages)*3 {
		t.Fatalf("incident events = %d, want %d", events, len(outages)*3)
	}

	// A second run must reuse everything and regenerate the same history.
	// Two hours have passed, so today gains two more completed buckets.
	second, err := Seed(ctx, pool, Options{Now: now.Add(2 * time.Hour)})
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if second.AccountCreated {
		t.Fatal("second seed created a duplicate account")
	}
	if second.UserID != first.UserID || second.WorkspaceID != first.WorkspaceID {
		t.Fatalf("second seed changed ids: user %s/%s workspace %s/%s",
			first.UserID, second.UserID, first.WorkspaceID, second.WorkspaceID)
	}
	if second.Incidents != 0 {
		t.Fatalf("second seed added %d incidents", second.Incidents)
	}

	var afterMonitors, afterIncidents int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM monitors WHERE workspace_id = $1),
		       (SELECT count(*) FROM incidents WHERE workspace_id = $1)`,
		first.WorkspaceID).Scan(&afterMonitors, &afterIncidents); err != nil {
		t.Fatalf("recount demo rows: %v", err)
	}
	if afterMonitors != len(monitorSpecs) || afterIncidents != len(outages) {
		t.Fatalf("after second seed monitors=%d incidents=%d", afterMonitors, afterIncidents)
	}

	var afterRows int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM monitor_hourly_stats WHERE workspace_id = $1`,
		first.WorkspaceID).Scan(&afterRows); err != nil {
		t.Fatalf("count history after second seed: %v", err)
	}
	if afterRows != wantRows {
		t.Fatalf("history rows after second seed = %d, want %d", afterRows, wantRows)
	}

	var afterRaw int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM probe_results WHERE workspace_id = $1`,
		first.WorkspaceID).Scan(&afterRaw); err != nil {
		t.Fatalf("count raw probes after second seed: %v", err)
	}
	wantAfterRaw := int64(17 * 60 * len(monitorSpecs))
	if afterRaw != wantAfterRaw {
		t.Fatalf("raw rows after second seed = %d, want %d", afterRaw, wantAfterRaw)
	}
}

func TestNormalizeSlug(t *testing.T) {
	cases := map[string]string{
		"demo":        "demo",
		" Demo-2026 ": "demo-2026",
	}
	for input, want := range cases {
		got, err := normalizeSlug(input)
		if err != nil {
			t.Fatalf("normalizeSlug(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("normalizeSlug(%q) = %q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"", "-demo", "demo page", "demo/status"} {
		if _, err := normalizeSlug(input); err == nil {
			t.Fatalf("normalizeSlug(%q) accepted an invalid slug", input)
		}
	}
}

func TestSeedResetsExistingPassword(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()

	const password = "correct horse battery staple"
	first, err := Seed(ctx, pool, Options{Password: password, Now: time.Now()})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if first.Password != "" {
		t.Fatalf("explicit password was echoed back: %q", first.Password)
	}

	const replacement = "a different demo password"
	second, err := Seed(ctx, pool, Options{Password: replacement, Now: time.Now()})
	if err != nil {
		t.Fatalf("reseed: %v", err)
	}
	if second.AccountCreated {
		t.Fatal("reseed created a duplicate account")
	}

	var hash string
	if err := pool.QueryRow(ctx,
		`SELECT password_hash FROM users WHERE id = $1`, first.UserID).Scan(&hash); err != nil {
		t.Fatalf("read password hash: %v", err)
	}
	ok, err := auth.VerifyPassword(hash, replacement)
	if err != nil || !ok {
		t.Fatalf("replacement password does not verify (ok=%v, err=%v)", ok, err)
	}
}

func TestSeedCreatesReadOnlyViewer(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()

	const viewerPassword = "o1-demo-viewer-2026" //nolint:gosec // public demo fixture
	opts := Options{
		Now:            time.Now(),
		ViewerEmail:    "demo-viewer@o1.work.gd",
		ViewerPassword: viewerPassword,
	}
	first, err := Seed(ctx, pool, opts)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	var viewerID uuid.UUID
	var role string
	var totpEnabled *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT u.id, m.role, u.totp_enabled_at
		FROM users u
		JOIN workspace_members m ON m.user_id = u.id
		WHERE u.email = 'demo-viewer@o1.work.gd' AND m.workspace_id = $1`,
		first.WorkspaceID).Scan(&viewerID, &role, &totpEnabled); err != nil {
		t.Fatalf("read demo viewer: %v", err)
	}
	if role != "viewer" {
		t.Fatalf("viewer role = %q", role)
	}
	if totpEnabled != nil {
		t.Fatal("viewer has TOTP enabled")
	}

	var hash string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, viewerID).Scan(&hash); err != nil {
		t.Fatalf("read viewer password hash: %v", err)
	}
	ok, err := auth.VerifyPassword(hash, viewerPassword)
	if err != nil || !ok {
		t.Fatalf("viewer password does not verify (ok=%v, err=%v)", ok, err)
	}

	second, err := Seed(ctx, pool, opts)
	if err != nil {
		t.Fatalf("reseed: %v", err)
	}
	if second.WorkspaceID != first.WorkspaceID {
		t.Fatal("reseed changed the workspace")
	}
}

// Package demo seeds a labelled demo workspace: a verified account, a
// published status page, live public monitors, and synthetic 90-day uptime
// history. It exists so a fresh deployment can show the dashboard and status
// page with realistic data instead of an empty state.
//
// The generated history is explicitly described as synthetic on the status
// page. The monitors themselves are real and the scheduler keeps probing them,
// so the demo becomes live as soon as the stack runs.
package demo

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/auth"
)

const (
	// DefaultEmail is the demo account address. It never needs to receive mail:
	// the account is created already verified and signs in with a password.
	DefaultEmail = "demo@o1.work.gd"
	// DefaultSlug is the workspace slug and therefore the public status page
	// path: /status/demo.
	DefaultSlug = "demo"
	// DefaultWorkspaceName is the display name of the demo workspace.
	DefaultWorkspaceName = "o1 Demo"

	historyDays   = 90
	checksPerHour = 60
)

// Options controls what Seed creates. Empty fields fall back to the defaults.
type Options struct {
	Email         string
	Password      string
	WorkspaceName string
	Slug          string
	// Now anchors the generated history. Defaults to time.Now.
	Now time.Time

	// ViewerEmail and ViewerPassword optionally create a read-only account in
	// the demo workspaces, for sharing a dashboard without write access. Leave
	// both empty to skip.
	ViewerEmail    string
	ViewerPassword string
}

// Result describes what Seed created or refreshed.
type Result struct {
	UserID         uuid.UUID
	WorkspaceID    uuid.UUID
	AccountCreated bool
	// Password is set only when Seed generated one. It is printed once by the
	// caller and never stored in plaintext.
	Password   string
	Monitors   int
	HourlyRows int64
	RawRows    int64
	Incidents  int
	StatusPath string
}

type monitorSpec struct {
	Name            string
	URL             string
	IntervalSeconds int
}

var monitorSpecs = []monitorSpec{
	{Name: "Dashboard", URL: "https://o1.work.gd/", IntervalSeconds: 60},
	{Name: "GitHub API", URL: "https://api.github.com/", IntervalSeconds: 120},
	{Name: "npm registry", URL: "https://registry.npmjs.org/", IntervalSeconds: 180},
	{Name: "MDN Web Docs", URL: "https://developer.mozilla.org/", IntervalSeconds: 300},
	{Name: "GitHub Status", URL: "https://www.githubstatus.com/", IntervalSeconds: 300},
}

// outage is one synthetic degraded hour. It drives both the hourly rollup
// counts and the incident history so the two agree.
type outage struct {
	Monitor    string
	DaysAgo    int
	Hour       int
	DownChecks int
	Duration   time.Duration
}

var outages = []outage{
	{Monitor: "GitHub API", DaysAgo: 3, Hour: 2, DownChecks: 12, Duration: 24 * time.Minute},
	{Monitor: "npm registry", DaysAgo: 18, Hour: 14, DownChecks: 9, Duration: 9 * time.Minute},
	{Monitor: "Dashboard", DaysAgo: 12, Hour: 3, DownChecks: 60, Duration: 56 * time.Minute},
}

// Seed creates or refreshes the demo workspace inside one transaction. Running
// it twice is safe: the account, workspace, monitors, and status page are
// reused, synthetic history is regenerated, and incidents are only added when
// the workspace has none.
func Seed(ctx context.Context, pool *pgxpool.Pool, opts Options) (Result, error) {
	opts = withDefaults(opts)
	slug, err := normalizeSlug(opts.Slug)
	if err != nil {
		return Result{}, err
	}
	opts.Slug = slug

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("begin: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	userID, created, password, err := ensureUser(ctx, tx, opts)
	if err != nil {
		return Result{}, err
	}
	wsID, err := ensureWorkspace(ctx, tx, opts, userID)
	if err != nil {
		return Result{}, err
	}

	res := Result{
		UserID:         userID,
		WorkspaceID:    wsID,
		AccountCreated: created,
		Password:       password,
		Monitors:       len(monitorSpecs),
		StatusPath:     "/status/" + opts.Slug,
	}

	monitorIDs := make(map[string]uuid.UUID, len(monitorSpecs))
	for i, spec := range monitorSpecs {
		id, err := ensureMonitor(ctx, tx, wsID, userID, spec)
		if err != nil {
			return Result{}, err
		}
		monitorIDs[spec.Name] = id
		if err := ensureMonitorState(ctx, tx, id, opts.Now, 120+i*45); err != nil {
			return Result{}, err
		}
	}

	rows, err := seedHourlyStats(ctx, tx, wsID, opts.Now, monitorIDs)
	if err != nil {
		return Result{}, err
	}
	res.HourlyRows = rows

	rawRows, err := seedTodayProbes(ctx, tx, wsID, opts.Now, monitorIDs)
	if err != nil {
		return Result{}, err
	}
	res.RawRows = rawRows

	if opts.ViewerEmail != "" {
		if err := ensureViewer(ctx, tx, opts, wsID); err != nil {
			return Result{}, err
		}
	}

	incidents, err := seedIncidents(ctx, tx, wsID, userID, opts.Now, monitorIDs)
	if err != nil {
		return Result{}, err
	}
	res.Incidents = incidents

	if err := ensureStatusPage(ctx, tx, wsID, monitorIDs); err != nil {
		return Result{}, err
	}
	if err := ensureRollupWatermark(ctx, tx, opts.Now); err != nil {
		return Result{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("commit: %w", err)
	}
	return res, nil
}

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// normalizeSlug keeps the status page path predictable: lowercase letters,
// digits, and hyphens only.
func normalizeSlug(slug string) (string, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !slugPattern.MatchString(slug) {
		return "", fmt.Errorf("invalid demo slug %q: use lowercase letters, digits, and hyphens", slug)
	}
	return slug, nil
}

func withDefaults(opts Options) Options {
	opts.Email = auth.NormalizeEmail(opts.Email)
	opts.ViewerEmail = auth.NormalizeEmail(opts.ViewerEmail)
	if opts.Email == "" {
		opts.Email = DefaultEmail
	}
	if opts.WorkspaceName == "" {
		opts.WorkspaceName = DefaultWorkspaceName
	}
	if opts.Slug == "" {
		opts.Slug = DefaultSlug
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	opts.Now = opts.Now.UTC()
	return opts
}

func ensureUser(ctx context.Context, tx pgx.Tx, opts Options) (uuid.UUID, bool, string, error) {
	var userID uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, opts.Email).Scan(&userID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		password := opts.Password
		generated := password == ""
		if generated {
			generatedPassword, err := generatePassword()
			if err != nil {
				return uuid.Nil, false, "", err
			}
			password = generatedPassword
		}
		hash, err := auth.HashPassword(password)
		if err != nil {
			return uuid.Nil, false, "", fmt.Errorf("hash demo password: %w", err)
		}
		userID = uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO users (id, email, password_hash, email_verified_at)
			VALUES ($1, $2, $3, now())`,
			userID, opts.Email, hash); err != nil {
			return uuid.Nil, false, "", fmt.Errorf("create demo user: %w", err)
		}
		if !generated {
			password = ""
		}
		return userID, true, password, nil
	case err != nil:
		return uuid.Nil, false, "", fmt.Errorf("look up demo user: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE users SET email_verified_at = COALESCE(email_verified_at, now()) WHERE id = $1`,
		userID); err != nil {
		return uuid.Nil, false, "", fmt.Errorf("verify demo user: %w", err)
	}
	if opts.Password != "" {
		hash, err := auth.HashPassword(opts.Password)
		if err != nil {
			return uuid.Nil, false, "", fmt.Errorf("hash demo password: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`,
			userID, hash); err != nil {
			return uuid.Nil, false, "", fmt.Errorf("reset demo password: %w", err)
		}
	}
	return userID, false, "", nil
}

// ensureViewer creates or resets a read-only demo account and adds it to every
// seeded workspace. It also clears TOTP and sessions on reset so a shared demo
// account cannot lock out the next visitor with a second factor.
func ensureViewer(ctx context.Context, tx pgx.Tx, opts Options, wsID uuid.UUID) error {
	if opts.ViewerEmail == opts.Email {
		return fmt.Errorf("viewer email must differ from the demo owner email")
	}
	if opts.ViewerPassword == "" {
		return fmt.Errorf("viewer password is required when viewer email is set")
	}
	hash, err := auth.HashPassword(opts.ViewerPassword)
	if err != nil {
		return fmt.Errorf("hash viewer password: %w", err)
	}

	var viewerID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, opts.ViewerEmail).Scan(&viewerID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		viewerID = uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO users (id, email, password_hash, email_verified_at)
			VALUES ($1, $2, $3, now())`, viewerID, opts.ViewerEmail, hash); err != nil {
			return fmt.Errorf("create demo viewer: %w", err)
		}
	case err != nil:
		return fmt.Errorf("look up demo viewer: %w", err)
	default:
		if _, err := tx.Exec(ctx, `
			UPDATE users SET
				password_hash = $2,
				email_verified_at = now(),
				totp_secret_enc = NULL,
				totp_pending_enc = NULL,
				totp_pending_expires_at = NULL,
				totp_enabled_at = NULL,
				totp_last_used_step = NULL,
				updated_at = now()
			WHERE id = $1`, viewerID, hash); err != nil {
			return fmt.Errorf("reset demo viewer: %w", err)
		}
		for _, stmt := range []string{
			`DELETE FROM user_recovery_codes WHERE user_id = $1`,
			`DELETE FROM mfa_challenges WHERE user_id = $1`,
			`DELETE FROM sessions WHERE user_id = $1`,
		} {
			if _, err := tx.Exec(ctx, stmt, viewerID); err != nil {
				return fmt.Errorf("clear demo viewer state: %w", err)
			}
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO workspace_members (workspace_id, user_id, role)
		VALUES ($1, $2, 'viewer')
		ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = 'viewer'`,
		wsID, viewerID); err != nil {
		return fmt.Errorf("add demo viewer: %w", err)
	}
	return nil
}

func ensureWorkspace(ctx context.Context, tx pgx.Tx, opts Options, userID uuid.UUID) (uuid.UUID, error) {
	var wsID, createdBy uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspaces (id, name, slug, created_by, monitor_limit)
		VALUES ($1, $2, $3, $4, 10)
		ON CONFLICT (slug) DO UPDATE SET
			name = EXCLUDED.name,
			monitor_limit = GREATEST(workspaces.monitor_limit, EXCLUDED.monitor_limit)
		RETURNING id, created_by`,
		uuid.New(), opts.WorkspaceName, opts.Slug, userID).Scan(&wsID, &createdBy); err != nil {
		return uuid.Nil, fmt.Errorf("upsert demo workspace: %w", err)
	}
	if createdBy != userID {
		return uuid.Nil, fmt.Errorf("workspace slug %q already belongs to another user", opts.Slug)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO workspace_members (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
		ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = 'owner'`,
		wsID, userID); err != nil {
		return uuid.Nil, fmt.Errorf("add demo owner: %w", err)
	}
	return wsID, nil
}

func ensureMonitor(ctx context.Context, tx pgx.Tx, wsID, userID uuid.UUID, spec monitorSpec) (uuid.UUID, error) {
	var monitorID uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM monitors
		WHERE workspace_id = $1 AND name = $2
		ORDER BY created_at, id
		LIMIT 1`, wsID, spec.Name).Scan(&monitorID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		monitorID = uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO monitors (
				id, workspace_id, name, url, method, expected_status,
				interval_seconds, timeout_seconds, confirmation_threshold,
				recovery_threshold, paused, created_by
			) VALUES ($1, $2, $3, $4, 'GET', '2xx', $5, 10, 2, 2, false, $6)`,
			monitorID, wsID, spec.Name, spec.URL, spec.IntervalSeconds, userID); err != nil {
			return uuid.Nil, fmt.Errorf("create demo monitor %q: %w", spec.Name, err)
		}
	case err != nil:
		return uuid.Nil, fmt.Errorf("look up demo monitor %q: %w", spec.Name, err)
	default:
		if _, err := tx.Exec(ctx, `
			UPDATE monitors SET
				url = $2, method = 'GET', expected_status = '2xx',
				interval_seconds = $3, timeout_seconds = 10,
				confirmation_threshold = 2, recovery_threshold = 2,
				paused = false, updated_at = now()
			WHERE id = $1`,
			monitorID, spec.URL, spec.IntervalSeconds); err != nil {
			return uuid.Nil, fmt.Errorf("update demo monitor %q: %w", spec.Name, err)
		}
	}
	return monitorID, nil
}

func ensureMonitorState(ctx context.Context, tx pgx.Tx, monitorID uuid.UUID, now time.Time, latencyMS int) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO monitor_state (
			monitor_id, status, consecutive_successes, last_checked_at,
			last_status_code, last_latency_ms, next_check_at, updated_at
		) VALUES ($1, 'up', 2, $2, 200, $3, $2, $2)
		ON CONFLICT (monitor_id) DO UPDATE SET
			status = 'up',
			consecutive_failures = 0,
			consecutive_successes = 2,
			last_checked_at = EXCLUDED.last_checked_at,
			last_status_code = 200,
			last_latency_ms = EXCLUDED.last_latency_ms,
			last_error = NULL,
			next_check_at = EXCLUDED.next_check_at,
			updated_at = EXCLUDED.updated_at`,
		monitorID, now.Add(-time.Minute), latencyMS); err != nil {
		return fmt.Errorf("seed monitor state: %w", err)
	}
	return nil
}

// seedHourlyStats replaces the synthetic rollup range for the demo monitors.
// The status page reads completed days from monitor_hourly_stats, so seeding
// this table is enough to show 90 days of history without raw probe rows.
func seedHourlyStats(ctx context.Context, tx pgx.Tx, wsID uuid.UUID, now time.Time, monitorIDs map[string]uuid.UUID) (int64, error) {
	start := dayStart(now).AddDate(0, 0, -(historyDays - 1))
	downByKey := make(map[string]int, len(outages))
	for _, o := range outages {
		downByKey[outageKey(o.Monitor, o.DaysAgo, o.Hour)] = o.DownChecks
	}

	var total int64
	split := dayStart(now)
	columns := []string{
		"monitor_id", "workspace_id", "bucket_start",
		"up_count", "down_count", "checks",
		"avg_latency_ms", "min_latency_ms", "max_latency_ms",
		"p95_latency_ms", "updated_at",
	}
	for i, spec := range monitorSpecs {
		monitorID, ok := monitorIDs[spec.Name]
		if !ok {
			return total, fmt.Errorf("seed history: monitor %q has no id", spec.Name)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM monitor_hourly_stats WHERE monitor_id = $1 AND bucket_start >= $2`,
			monitorID, start); err != nil {
			return total, fmt.Errorf("clear demo history: %w", err)
		}

		var rows [][]any
		for day := 0; day < historyDays; day++ {
			bucketDay := start.AddDate(0, 0, day)
			if !bucketDay.Before(split) {
				break
			}
			daysAgo := historyDays - 1 - day
			for hour := 0; hour < 24; hour++ {
				bucket := bucketDay.Add(time.Duration(hour) * time.Hour)
				down := downByKey[outageKey(spec.Name, daysAgo, hour)]
				if down > checksPerHour {
					down = checksPerHour
				}
				up := checksPerHour - down
				latency := syntheticLatency(i, day, hour)
				rows = append(rows, []any{
					monitorID, wsID, bucket,
					int64(up), int64(down), int64(checksPerHour),
					float64(latency), latency - 12, latency + 18, float64(latency) + 9,
					now,
				})
			}
		}
		copied, err := tx.CopyFrom(ctx, pgx.Identifier{"monitor_hourly_stats"}, columns, pgx.CopyFromRows(rows))
		if err != nil {
			return total, fmt.Errorf("seed demo history: %w", err)
		}
		total += copied
	}
	return total, nil
}

// seedTodayProbes writes minute-level raw results for the current UTC day.
// The public status page reads completed days from rollups but the current day
// from probe_results, so a demo without today's raw rows shows no data for
// today. Rows use deterministic IDs and ON CONFLICT, so re-seeding is a no-op
// for minutes that already exist and never touches real probe rows.
func seedTodayProbes(ctx context.Context, tx pgx.Tx, wsID uuid.UUID, now time.Time, monitorIDs map[string]uuid.UUID) (int64, error) {
	start := dayStart(now)
	end := now.Truncate(time.Minute)
	if !end.After(start) {
		return 0, nil
	}

	var total int64
	for i, spec := range monitorSpecs {
		monitorID, ok := monitorIDs[spec.Name]
		if !ok {
			return total, fmt.Errorf("seed today's probes: monitor %q has no id", spec.Name)
		}

		var rows [][]any
		for minute := start; minute.Before(end); minute = minute.Add(time.Minute) {
			checked := minute.Add(30 * time.Second)
			latency := todayLatency(i, minute)
			rows = append(rows, []any{
				uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%s|%d", monitorID, minute.Unix()))),
				monitorID, wsID, minute, checked,
				"up", 200, latency, nil, now,
			})
			if len(rows) == 200 {
				inserted, err := insertProbeRows(ctx, tx, rows)
				if err != nil {
					return total, err
				}
				total += inserted
				rows = rows[:0]
			}
		}
		if len(rows) > 0 {
			inserted, err := insertProbeRows(ctx, tx, rows)
			if err != nil {
				return total, err
			}
			total += inserted
		}
	}
	return total, nil
}

func insertProbeRows(ctx context.Context, tx pgx.Tx, rows [][]any) (int64, error) {
	var b strings.Builder
	b.WriteString(`INSERT INTO probe_results (
		id, monitor_id, workspace_id, scheduled_for, checked_at,
		status, status_code, latency_ms, error, created_at
	) VALUES `)
	args := make([]any, 0, len(rows)*10)
	for i, row := range rows {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("(")
		for j := range row {
			if j > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, "$%d", i*10+j+1)
		}
		b.WriteString(")")
		args = append(args, row...)
	}
	b.WriteString(` ON CONFLICT (id, checked_at) DO NOTHING`)
	tag, err := tx.Exec(ctx, b.String(), args...)
	if err != nil {
		return 0, fmt.Errorf("seed today's probes: %w", err)
	}
	return tag.RowsAffected(), nil
}

func seedIncidents(ctx context.Context, tx pgx.Tx, wsID, userID uuid.UUID, now time.Time, monitorIDs map[string]uuid.UUID) (int, error) {
	var existing int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM incidents WHERE workspace_id = $1`, wsID).Scan(&existing); err != nil {
		return 0, fmt.Errorf("count demo incidents: %w", err)
	}
	if existing > 0 {
		return 0, nil
	}

	for _, o := range outages {
		monitorID, ok := monitorIDs[o.Monitor]
		if !ok {
			continue
		}
		started := dayStart(now).AddDate(0, 0, -o.DaysAgo).
			Add(time.Duration(o.Hour)*time.Hour + 2*time.Minute)
		acknowledged := started.Add(4 * time.Minute)
		resolved := started.Add(o.Duration)

		incidentID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO incidents (
				id, workspace_id, monitor_id, status, started_at,
				acknowledged_at, acknowledged_by, resolved_at,
				last_notified_at, reminder_count, created_at, updated_at
			) VALUES ($1, $2, $3, 'resolved', $4, $5, $6, $7, $5, 0, $4, $7)`,
			incidentID, wsID, monitorID, started, acknowledged, userID, resolved); err != nil {
			return 0, fmt.Errorf("seed demo incident: %w", err)
		}

		events := []struct {
			kind string
			at   time.Time
		}{
			{kind: "opened", at: started},
			{kind: "acknowledged", at: acknowledged},
			{kind: "resolved", at: resolved},
		}
		for _, event := range events {
			var actor any
			if event.kind != "opened" {
				actor = userID
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO incident_events (id, incident_id, kind, actor_user_id, created_at)
				VALUES ($1, $2, $3, $4, $5)`,
				uuid.New(), incidentID, event.kind, actor, event.at); err != nil {
				return 0, fmt.Errorf("seed demo incident event: %w", err)
			}
		}
	}
	return len(outages), nil
}

func ensureStatusPage(ctx context.Context, tx pgx.Tx, wsID uuid.UUID, monitorIDs map[string]uuid.UUID) error {
	title := "o1 demo status"
	description := "Synthetic 90-day uptime history for the o1 demo workspace. " +
		"The monitors are live and probed by the real scheduler."
	if _, err := tx.Exec(ctx, `
		INSERT INTO status_pages (workspace_id, enabled, title, description)
		VALUES ($1, true, $2, $3)
		ON CONFLICT (workspace_id) DO UPDATE SET
			enabled = true,
			title = EXCLUDED.title,
			description = EXCLUDED.description,
			updated_at = now()`,
		wsID, title, description); err != nil {
		return fmt.Errorf("publish demo status page: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM status_page_monitors WHERE status_page_id = $1`, wsID); err != nil {
		return fmt.Errorf("reset demo status page monitors: %w", err)
	}
	for _, spec := range monitorSpecs {
		monitorID, ok := monitorIDs[spec.Name]
		if !ok {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO status_page_monitors (status_page_id, monitor_id) VALUES ($1, $2)`,
			wsID, monitorID); err != nil {
			return fmt.Errorf("add monitor to demo status page: %w", err)
		}
	}
	return nil
}

// ensureRollupWatermark points the rollup frontier at the start of the current
// UTC day. The status page trusts rollups only for days below the watermark and
// reads the current day from raw probes, so this is exactly the split the
// seeder fills: synthetic rollups for past days, synthetic raw rows for today.
//
// This is a demo tool; moving the watermark forward can skip a rollup pass for
// real probe rows written earlier. Run it on a demo deployment, not beside
// production tenants.
func ensureRollupWatermark(ctx context.Context, tx pgx.Tx, now time.Time) error {
	if _, err := tx.Exec(ctx, `
		UPDATE rollup_state SET watermark = $1, updated_at = now() WHERE id`,
		dayStart(now)); err != nil {
		return fmt.Errorf("set demo rollup watermark: %w", err)
	}
	return nil
}

func outageKey(monitor string, daysAgo, hour int) string {
	return fmt.Sprintf("%s/%d/%d", monitor, daysAgo, hour)
}

func dayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// syntheticLatency produces a stable, plausible latency for a monitor/day/hour
// so repeated seeds and screenshots do not jump around.
func syntheticLatency(monitor, day, hour int) int {
	n := (monitor*7919 + day*104729 + hour*1299709) % 240
	return 90 + monitor*35 + n
}

// todayLatency is syntheticLatency's minute-resolution sibling: a slow wave
// plus deterministic jitter, so the raw-probe latency chart looks measured
// instead of stepping once per hour.
func todayLatency(monitor int, minute time.Time) int {
	minutes := minute.Hour()*60 + minute.Minute()
	wave := int(16 * math.Sin(float64(minutes)/11))
	jitter := (monitor*7919 + minutes*104729) % 11
	return 120 + monitor*18 + wave + jitter
}

func generatePassword() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate demo password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

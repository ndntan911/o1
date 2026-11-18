// Package loadseed builds a reference dataset for capacity runs: many tenants,
// monitors, raw probe history, hourly rollups, and one API key for k6.
//
// The dataset is deliberately inert. Every monitor is paused and its next check
// is parked far in the future, so running the stack's scheduler cannot turn the
// load fixture into outbound traffic.
package loadseed

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/auth"
	"o1/internal/db"
)

const (
	// LoadEmail is the single account that owns every load workspace.
	LoadEmail = "load@o1.work.gd"
	// DefaultPassword keeps local runs reproducible; it is not a secret.
	DefaultPassword = "load test password"

	checksPerHour = 60
	rawBatchRows  = 500
)

// Options controls the dataset size. Zero values fall back to the reference
// workload: 10 tenants, 20 monitors, 7 days of raw results, 90 days of rollups.
type Options struct {
	Tenants           int
	MonitorsPerTenant int
	RawDays           int
	RollupDays        int
	Password          string
	Now               time.Time
}

// Result describes what Seed created.
type Result struct {
	WorkspaceIDs []uuid.UUID
	Monitors     int
	RawRows      int64
	RollupRows   int64
	// APIPrefix is the public prefix (o1_<prefix>); APIKey is the full secret
	// and is only ever returned here so the caller can print it once.
	APIPrefix string
	APIKey    string
}

// Seed creates or refreshes the reference dataset. Re-running reuses the load
// account and workspace slugs, replaces their history, and replaces the load
// API key.
func Seed(ctx context.Context, pool *pgxpool.Pool, opts Options) (Result, error) {
	opts = withDefaults(opts)

	if err := db.EnsureProbePartitions(ctx, pool, opts.Now); err != nil {
		return Result{}, fmt.Errorf("ensure probe partitions: %w", err)
	}

	userID, err := ensureUser(ctx, pool, opts)
	if err != nil {
		return Result{}, err
	}

	res := Result{WorkspaceIDs: make([]uuid.UUID, 0, opts.Tenants)}
	rawStart := opts.Now.Truncate(time.Minute).Add(-time.Duration(opts.RawDays) * 24 * time.Hour)
	rawEnd := opts.Now.Truncate(time.Minute)

	for tenant := 1; tenant <= opts.Tenants; tenant++ {
		wsID, err := ensureWorkspace(ctx, pool, userID, tenant)
		if err != nil {
			return Result{}, err
		}
		res.WorkspaceIDs = append(res.WorkspaceIDs, wsID)

		monitorIDs, err := ensureMonitors(ctx, pool, wsID, userID, opts.MonitorsPerTenant)
		if err != nil {
			return Result{}, err
		}
		res.Monitors += len(monitorIDs)

		if err := ensureStatusPage(ctx, pool, wsID, tenant, monitorIDs); err != nil {
			return Result{}, err
		}

		raw, err := seedRaw(ctx, pool, wsID, monitorIDs, rawStart, rawEnd, opts.Now)
		if err != nil {
			return Result{}, err
		}
		res.RawRows += raw

		rollups, err := seedRollups(ctx, pool, wsID, monitorIDs, opts.Now, opts.RollupDays)
		if err != nil {
			return Result{}, err
		}
		res.RollupRows += rollups
	}

	prefix, key, err := ensureAPIKey(ctx, pool, res.WorkspaceIDs[0], userID)
	if err != nil {
		return Result{}, err
	}
	res.APIPrefix, res.APIKey = prefix, key

	return res, nil
}

func withDefaults(opts Options) Options {
	if opts.Tenants < 1 {
		opts.Tenants = 10
	}
	if opts.MonitorsPerTenant < 1 {
		opts.MonitorsPerTenant = 20
	}
	if opts.RawDays < 1 {
		opts.RawDays = 7
	}
	if opts.RollupDays < 1 {
		opts.RollupDays = 90
	}
	if opts.Password == "" {
		opts.Password = DefaultPassword
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	opts.Now = opts.Now.UTC()
	return opts
}

func ensureUser(ctx context.Context, pool *pgxpool.Pool, opts Options) (uuid.UUID, error) {
	var userID uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, LoadEmail).Scan(&userID)
	if err == nil {
		return userID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("look up load user: %w", err)
	}

	hash, err := auth.HashPassword(opts.Password)
	if err != nil {
		return uuid.Nil, fmt.Errorf("hash load password: %w", err)
	}
	userID = uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, email_verified_at)
		VALUES ($1, $2, $3, now())`, userID, LoadEmail, hash); err != nil {
		return uuid.Nil, fmt.Errorf("create load user: %w", err)
	}
	return userID, nil
}

func ensureWorkspace(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, tenant int) (uuid.UUID, error) {
	name := fmt.Sprintf("Load %02d", tenant)
	slug := fmt.Sprintf("load-%02d", tenant)

	var wsID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspaces (id, name, slug, created_by, monitor_limit)
		VALUES ($1, $2, $3, $4, 100)
		ON CONFLICT (slug) DO UPDATE SET
			name = EXCLUDED.name,
			monitor_limit = GREATEST(workspaces.monitor_limit, EXCLUDED.monitor_limit)
		RETURNING id`,
		uuid.New(), name, slug, userID).Scan(&wsID); err != nil {
		return uuid.Nil, fmt.Errorf("upsert load workspace: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
		ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = 'owner'`,
		wsID, userID); err != nil {
		return uuid.Nil, fmt.Errorf("add load owner: %w", err)
	}
	return wsID, nil
}

func ensureMonitors(ctx context.Context, pool *pgxpool.Pool, wsID, userID uuid.UUID, count int) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, count)
	for i := 1; i <= count; i++ {
		name := fmt.Sprintf("Load monitor %02d", i)
		var id uuid.UUID
		err := pool.QueryRow(ctx, `
			SELECT id FROM monitors WHERE workspace_id = $1 AND name = $2`, wsID, name).Scan(&id)
		switch {
		case err == pgx.ErrNoRows:
			id = uuid.New()
			if _, err := pool.Exec(ctx, `
				INSERT INTO monitors (
					id, workspace_id, name, url, method, expected_status,
					interval_seconds, timeout_seconds, confirmation_threshold,
					recovery_threshold, paused, created_by
				) VALUES ($1, $2, $3, 'https://example.com/', 'GET', '2xx', 60, 10, 2, 2, true, $4)`,
				id, wsID, name, userID); err != nil {
				return nil, fmt.Errorf("create load monitor: %w", err)
			}
		case err != nil:
			return nil, fmt.Errorf("look up load monitor: %w", err)
		default:
			if _, err := pool.Exec(ctx, `
				UPDATE monitors SET paused = true, updated_at = now() WHERE id = $1`, id); err != nil {
				return nil, fmt.Errorf("pause load monitor: %w", err)
			}
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO monitor_state (monitor_id, status, next_check_at, updated_at)
			VALUES ($1, 'unknown', $2, now())
			ON CONFLICT (monitor_id) DO UPDATE SET
				status = 'unknown',
				consecutive_failures = 0,
				consecutive_successes = 0,
				last_error = NULL,
				next_check_at = EXCLUDED.next_check_at,
				updated_at = now()`,
			id, time.Now().Add(100*365*24*time.Hour)); err != nil {
			return nil, fmt.Errorf("park load monitor: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func ensureStatusPage(ctx context.Context, pool *pgxpool.Pool, wsID uuid.UUID, tenant int, monitors []uuid.UUID) error {
	if _, err := pool.Exec(ctx, `
		INSERT INTO status_pages (workspace_id, enabled, title, description)
		VALUES ($1, true, $2, 'Synthetic workspace for the load baseline.')
		ON CONFLICT (workspace_id) DO UPDATE SET
			enabled = true,
			title = EXCLUDED.title,
			description = EXCLUDED.description,
			updated_at = now()`,
		wsID, fmt.Sprintf("Load %02d status", tenant)); err != nil {
		return fmt.Errorf("publish load status page: %w", err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM status_page_monitors WHERE status_page_id = $1`, wsID); err != nil {
		return fmt.Errorf("reset load status page: %w", err)
	}
	for _, id := range monitors {
		if _, err := pool.Exec(ctx,
			`INSERT INTO status_page_monitors (status_page_id, monitor_id) VALUES ($1, $2)`,
			wsID, id); err != nil {
			return fmt.Errorf("add load status monitor: %w", err)
		}
	}
	return nil
}

func seedRaw(ctx context.Context, pool *pgxpool.Pool, wsID uuid.UUID, monitors []uuid.UUID, start, end, now time.Time) (int64, error) {
	var total int64
	for i, monitorID := range monitors {
		if _, err := pool.Exec(ctx,
			`DELETE FROM probe_results WHERE monitor_id = $1 AND checked_at >= $2 AND checked_at < $3`,
			monitorID, start, end); err != nil {
			return total, fmt.Errorf("clear load history: %w", err)
		}

		rows := make([][]any, 0, rawBatchRows)
		flush := func() error {
			if len(rows) == 0 {
				return nil
			}
			if err := insertRawBatch(ctx, pool, rows); err != nil {
				return err
			}
			total += int64(len(rows))
			rows = rows[:0]
			return nil
		}

		row := 0
		for minute := start; minute.Before(end); minute = minute.Add(time.Minute) {
			latency := 80 + i*15 + (row*37)%90
			rows = append(rows, []any{
				uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%s|%d", monitorID, minute.Unix()))),
				monitorID, wsID, minute, minute.Add(30 * time.Second),
				"up", 200, latency, nil, now,
			})
			row++
			if len(rows) == rawBatchRows {
				if err := flush(); err != nil {
					return total, err
				}
			}
		}
		if err := flush(); err != nil {
			return total, err
		}
	}
	return total, nil
}

func insertRawBatch(ctx context.Context, pool *pgxpool.Pool, rows [][]any) error {
	args := make([]any, 0, len(rows)*10)
	placeholders := ""
	for i, row := range rows {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "("
		for j := range row {
			if j > 0 {
				placeholders += ","
			}
			placeholders += fmt.Sprintf("$%d", i*10+j+1)
		}
		placeholders += ")"
		args = append(args, row...)
	}
	query := `INSERT INTO probe_results (
		id, monitor_id, workspace_id, scheduled_for, checked_at,
		status, status_code, latency_ms, error, created_at
	) VALUES ` + placeholders + ` ON CONFLICT (id, checked_at) DO NOTHING`
	if _, err := pool.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("insert load history: %w", err)
	}
	return nil
}

func seedRollups(ctx context.Context, pool *pgxpool.Pool, wsID uuid.UUID, monitors []uuid.UUID, now time.Time, days int) (int64, error) {
	start := dayStart(now).AddDate(0, 0, -days)
	end := dayStart(now)

	columns := []string{
		"monitor_id", "workspace_id", "bucket_start",
		"up_count", "down_count", "checks",
		"avg_latency_ms", "min_latency_ms", "max_latency_ms",
		"p95_latency_ms", "updated_at",
	}
	var total int64
	for i, monitorID := range monitors {
		if _, err := pool.Exec(ctx,
			`DELETE FROM monitor_hourly_stats WHERE monitor_id = $1 AND bucket_start >= $2 AND bucket_start < $3`,
			monitorID, start, end); err != nil {
			return total, fmt.Errorf("clear load rollups: %w", err)
		}

		rows := make([][]any, 0, days*24)
		for bucket := start; bucket.Before(end); bucket = bucket.Add(time.Hour) {
			latency := 80 + i*15 + bucket.Hour()%40
			rows = append(rows, []any{
				monitorID, wsID, bucket,
				int64(checksPerHour), int64(0), int64(checksPerHour),
				float64(latency), latency - 10, latency + 20, float64(latency) + 8,
				now,
			})
		}
		copied, err := pool.CopyFrom(ctx, pgx.Identifier{"monitor_hourly_stats"}, columns, pgx.CopyFromRows(rows))
		if err != nil {
			return total, fmt.Errorf("insert load rollups: %w", err)
		}
		total += copied
	}

	if _, err := pool.Exec(ctx, `
		UPDATE rollup_state SET watermark = $1, updated_at = now()
		WHERE id AND watermark IS NULL`, dayStart(now)); err != nil {
		return total, fmt.Errorf("set load rollup watermark: %w", err)
	}
	return total, nil
}

func ensureAPIKey(ctx context.Context, pool *pgxpool.Pool, wsID, userID uuid.UUID) (string, string, error) {
	const name = "load"

	if _, err := pool.Exec(ctx,
		`DELETE FROM api_keys WHERE workspace_id = $1 AND name = $2 AND revoked_at IS NULL`,
		wsID, name); err != nil {
		return "", "", fmt.Errorf("replace load api key: %w", err)
	}

	prefix, fullKey, hash, err := newKeyMaterial()
	if err != nil {
		return "", "", err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO api_keys (id, workspace_id, name, prefix, token_hash, role, created_by)
		VALUES ($1, $2, $3, $4, $5, 'member', $6)`,
		uuid.New(), wsID, name, prefix, hash, userID); err != nil {
		return "", "", fmt.Errorf("create load api key: %w", err)
	}
	return prefix, fullKey, nil
}

// newKeyMaterial mirrors internal/apikeys: o1_<prefix>_<secret>, where the
// stored hash covers the full key.
func newKeyMaterial() (prefix, fullKey string, hash []byte, err error) {
	var raw [9]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", nil, fmt.Errorf("generate load key prefix: %w", err)
	}
	prefix = new(big.Int).SetBytes(raw[:]).Text(36)
	if len(prefix) > 12 {
		prefix = prefix[len(prefix)-12:]
	}
	for len(prefix) < 12 {
		prefix = "0" + prefix
	}

	var secretRaw [18]byte
	if _, err := rand.Read(secretRaw[:]); err != nil {
		return "", "", nil, fmt.Errorf("generate load key secret: %w", err)
	}
	fullKey = fmt.Sprintf("o1_%s_%s", prefix, base64.RawURLEncoding.EncodeToString(secretRaw[:]))
	sum := sha256.Sum256([]byte(fullKey))
	return prefix, fullKey, sum[:], nil
}

func dayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

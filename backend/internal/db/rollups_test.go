package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/db/sqlc"
	"o1/internal/testutil"
)

// rollupFixture creates a user, workspace, and monitor for rollup query tests.
func rollupFixture(t *testing.T, pool *pgxpool.Pool) (workspaceID, monitorID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	userID := testutil.CreateVerifiedUser(t, pool, "rollup-"+uuid.NewString()+"@example.com", "correct horse battery staple")
	workspaceID = uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1,'Rollups',$2,$3)`,
		workspaceID, "rollups-"+workspaceID.String(), userID); err != nil {
		t.Fatal(err)
	}
	monitorID = uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO monitors (id, workspace_id, name, url, method, interval_seconds, timeout_seconds, created_by)
		 VALUES ($1,$2,'API','https://example.com','GET',60,10,$3)`,
		monitorID, workspaceID, userID); err != nil {
		t.Fatal(err)
	}
	return workspaceID, monitorID
}

func TestOldestProbeHour(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()
	wsID, monitorID := rollupFixture(t, pool)
	q := sqlc.New(pool)

	got, err := q.OldestProbeHour(ctx)
	if err != nil {
		t.Fatalf("OldestProbeHour empty: %v", err)
	}
	if got != nil {
		t.Fatalf("OldestProbeHour empty = %v, want nil", got)
	}

	now := time.Now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{day.Add(10*time.Hour + 15*time.Minute), day.Add(10*time.Hour + 45*time.Minute)} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO probe_results (id, monitor_id, workspace_id, scheduled_for, checked_at, status, latency_ms)
			VALUES ($1,$2,$3,$4,$4,'up',100)`,
			uuid.New(), monitorID, wsID, at); err != nil {
			t.Fatal(err)
		}
	}

	got, err = q.OldestProbeHour(ctx)
	if err != nil {
		t.Fatalf("OldestProbeHour: %v", err)
	}
	want := day.Add(10 * time.Hour)
	if got == nil || !got.Equal(want) {
		t.Fatalf("OldestProbeHour = %v, want %v", got, want)
	}
}

func TestCountOldHourlyStatsAndSecurityEvents(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()
	wsID, monitorID := rollupFixture(t, pool)
	q := sqlc.New(pool)
	cutoff := time.Now().UTC().Truncate(time.Hour)

	for i, bucket := range []time.Time{cutoff.Add(-2 * time.Hour), cutoff.Add(-time.Hour), cutoff} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO monitor_hourly_stats (
				monitor_id, workspace_id, bucket_start, up_count, down_count, checks,
				avg_latency_ms, min_latency_ms, max_latency_ms, p95_latency_ms
			) VALUES ($1,$2,$3,$4,0,$4,100,90,110,105)`,
			monitorID, wsID, bucket, int64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	oldHourly, err := q.CountOldHourlyStats(ctx, cutoff)
	if err != nil {
		t.Fatalf("CountOldHourlyStats: %v", err)
	}
	if oldHourly != 2 {
		t.Fatalf("CountOldHourlyStats = %d, want 2", oldHourly)
	}

	for _, at := range []time.Time{cutoff.Add(-time.Hour), cutoff.Add(time.Hour)} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO security_events (id, workspace_id, kind, created_at) VALUES ($1,$2,'test',$3)`,
			uuid.New(), wsID, at); err != nil {
			t.Fatal(err)
		}
	}
	oldEvents, err := q.CountOldSecurityEvents(ctx, cutoff)
	if err != nil {
		t.Fatalf("CountOldSecurityEvents: %v", err)
	}
	if oldEvents != 1 {
		t.Fatalf("CountOldSecurityEvents = %d, want 1", oldEvents)
	}
}

func TestListRollupDayStats_ExcludesUntil(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	ctx := context.Background()
	wsID, monitorID := rollupFixture(t, pool)
	q := sqlc.New(pool)

	now := time.Now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	insert := func(bucket time.Time, up, down int64) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO monitor_hourly_stats (
				monitor_id, workspace_id, bucket_start, up_count, down_count, checks,
				avg_latency_ms, min_latency_ms, max_latency_ms, p95_latency_ms
			) VALUES ($1,$2,$3,$4,$5,$6,100,90,110,105)`,
			monitorID, wsID, bucket, up, down, up+down); err != nil {
			t.Fatal(err)
		}
	}
	insert(day.Add(10*time.Hour), 1, 1)
	insert(day.Add(20*time.Hour), 2, 1)
	insert(day.Add(24*time.Hour), 100, 0) // exactly at Until, must be excluded

	rows, err := q.ListRollupDayStats(ctx, sqlc.ListRollupDayStatsParams{
		MonitorIds: []uuid.UUID{monitorID},
		Since:      day,
		Until:      day.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("ListRollupDayStats: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want 1", rows)
	}
	got := rows[0]
	if got.MonitorID != monitorID || got.Day != day.Format("2006-01-02") ||
		got.Checks != 5 || got.UpCount != 3 || got.DownCount != 2 {
		t.Fatalf("row = %+v", got)
	}
}

package retention

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// seedHourlyStat writes one hourly rollup bucket with a single up check.
func (e *retentionEnv) seedHourlyStat(bucket time.Time, checks int64) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO monitor_hourly_stats (
			monitor_id, workspace_id, bucket_start, up_count, down_count, checks,
			avg_latency_ms, min_latency_ms, max_latency_ms, p95_latency_ms
		) VALUES ($1,$2,$3,$4,0,$4,100,90,110,105)`,
		e.monitorID, e.workspaceID, bucket.UTC(), checks); err != nil {
		e.t.Fatalf("insert hourly stat at %s: %v", bucket, err)
	}
}

// seedSecurityEvent writes one audit row at the given time.
func (e *retentionEnv) seedSecurityEvent(at time.Time) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO security_events (id, workspace_id, kind, created_at)
		VALUES ($1,$2,'retention-test',$3)`,
		uuid.New(), e.workspaceID, at.UTC()); err != nil {
		e.t.Fatalf("insert security event at %s: %v", at, err)
	}
}

func (e *retentionEnv) hourlyStatExists(bucket time.Time) bool {
	e.t.Helper()
	var exists bool
	if err := e.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM monitor_hourly_stats WHERE monitor_id=$1 AND bucket_start=$2)`,
		e.monitorID, bucket.UTC()).Scan(&exists); err != nil {
		e.t.Fatalf("check hourly stat at %s: %v", bucket, err)
	}
	return exists
}

func (e *retentionEnv) securityEventExists(at time.Time) bool {
	e.t.Helper()
	var exists bool
	if err := e.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM security_events WHERE workspace_id=$1 AND created_at=$2)`,
		e.workspaceID, at.UTC()).Scan(&exists); err != nil {
		e.t.Fatalf("check security event at %s: %v", at, err)
	}
	return exists
}

func (e *retentionEnv) countHourlyStats() int64 {
	e.t.Helper()
	var n int64
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM monitor_hourly_stats`).Scan(&n); err != nil {
		e.t.Fatalf("count hourly stats: %v", err)
	}
	return n
}

func (e *retentionEnv) countSecurityEvents() int64 {
	e.t.Helper()
	var n int64
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM security_events`).Scan(&n); err != nil {
		e.t.Fatalf("count security events: %v", err)
	}
	return n
}

func TestRun_PrunesOldRollupsAndKeepsNew(t *testing.T) {
	e := newRetentionEnv(t)
	oldBucket := retentionNow.AddDate(0, -14, 0).Truncate(time.Hour)
	freshBucket := retentionNow.AddDate(0, -2, 0).Truncate(time.Hour)
	e.seedHourlyStat(oldBucket, 1)
	e.seedHourlyStat(freshBucket, 2)

	res, err := e.retainer(testConfig(), &fakeRepairer{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.RollupRows != 1 {
		t.Fatalf("RollupRows = %d, want 1", res.RollupRows)
	}
	if e.hourlyStatExists(oldBucket) {
		t.Fatal("14-month-old rollup still exists, want pruned")
	}
	if !e.hourlyStatExists(freshBucket) {
		t.Fatal("2-month-old rollup was pruned, want retained")
	}
}

func TestRun_PrunesOldAuditEventsAndKeepsNew(t *testing.T) {
	e := newRetentionEnv(t)
	oldEvent := retentionNow.AddDate(0, -13, 0)
	freshEvent := retentionNow.AddDate(0, -1, 0)
	e.seedSecurityEvent(oldEvent)
	e.seedSecurityEvent(freshEvent)

	res, err := e.retainer(testConfig(), &fakeRepairer{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.AuditRows != 1 {
		t.Fatalf("AuditRows = %d, want 1", res.AuditRows)
	}
	if e.securityEventExists(oldEvent) {
		t.Fatal("13-month-old audit event still exists, want pruned")
	}
	if !e.securityEventExists(freshEvent) {
		t.Fatal("1-month-old audit event was pruned, want retained")
	}
}

func TestRun_PruneBatchesUseConfiguredBatchSize(t *testing.T) {
	e := newRetentionEnv(t)
	r := e.retainer(testConfig(), &fakeRepairer{})
	r.batchSize = 2

	oldRollupBase := retentionNow.AddDate(0, -14, 0).Truncate(time.Hour)
	for i := 0; i < 5; i++ {
		e.seedHourlyStat(oldRollupBase.Add(time.Duration(i)*time.Hour), int64(i+1))
	}
	freshBucket := retentionNow.AddDate(0, -2, 0).Truncate(time.Hour)
	e.seedHourlyStat(freshBucket, 1)

	oldAuditBase := retentionNow.AddDate(0, -13, 0)
	for i := 0; i < 5; i++ {
		e.seedSecurityEvent(oldAuditBase.Add(time.Duration(i) * time.Hour))
	}
	freshEvent := retentionNow.AddDate(0, -1, 0)
	e.seedSecurityEvent(freshEvent)

	res, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.RollupRows != 5 {
		t.Fatalf("RollupRows = %d, want 5", res.RollupRows)
	}
	if res.AuditRows != 5 {
		t.Fatalf("AuditRows = %d, want 5", res.AuditRows)
	}
	if got := e.countHourlyStats(); got != 1 {
		t.Fatalf("hourly stats remaining = %d, want 1", got)
	}
	if got := e.countSecurityEvents(); got != 1 {
		t.Fatalf("security events remaining = %d, want 1", got)
	}
	if !e.hourlyStatExists(freshBucket) {
		t.Fatal("fresh rollup was pruned, want retained")
	}
	if !e.securityEventExists(freshEvent) {
		t.Fatal("fresh audit event was pruned, want retained")
	}
}

func TestRun_DryRunPrunesNothing(t *testing.T) {
	e := newRetentionEnv(t)
	oldBucket := retentionNow.AddDate(0, -14, 0).Truncate(time.Hour)
	oldEvent := retentionNow.AddDate(0, -13, 0)
	e.seedHourlyStat(oldBucket, 1)
	e.seedSecurityEvent(oldEvent)

	cfg := testConfig()
	cfg.DryRun = true
	res, err := e.retainer(cfg, &fakeRepairer{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.RollupRows != 1 {
		t.Fatalf("RollupRows = %d, want the 1 dry-run candidate", res.RollupRows)
	}
	if res.AuditRows != 1 {
		t.Fatalf("AuditRows = %d, want the 1 dry-run candidate", res.AuditRows)
	}
	if got := e.countHourlyStats(); got != 1 {
		t.Fatalf("hourly stats remaining = %d, want 1 (dry-run deletes nothing)", got)
	}
	if got := e.countSecurityEvents(); got != 1 {
		t.Fatalf("security events remaining = %d, want 1 (dry-run deletes nothing)", got)
	}
	if !e.hourlyStatExists(oldBucket) {
		t.Fatal("dry-run pruned the old rollup")
	}
	if !e.securityEventExists(oldEvent) {
		t.Fatal("dry-run pruned the old audit event")
	}
}

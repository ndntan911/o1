package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"golang.org/x/time/rate"

	"o1/internal/db/sqlc"
	"o1/internal/jobs"
	"o1/internal/probe"
	"o1/internal/secretbox"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

type recordingInserter struct {
	mu   sync.Mutex
	jobs []river.JobArgs
	opts []*river.InsertOpts
}

func (f *recordingInserter) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, args)
	f.opts = append(f.opts, opts)
	return &rivertype.JobInsertResult{}, nil
}

func (f *recordingInserter) snapshot() ([]river.JobArgs, []*river.InsertOpts) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]river.JobArgs(nil), f.jobs...), append([]*river.InsertOpts(nil), f.opts...)
}

type workerEnv struct {
	t        *testing.T
	pool     *pgxpool.Pool
	worker   *ProbeWorker
	ownerID  uuid.UUID
	inserter *recordingInserter
}

type staticResolver struct{}

func (staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
}

func newWorkerEnv(t *testing.T) *workerEnv {
	t.Helper()
	pool, _ := testutil.NewPostgres(t)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("secretbox: %v", err)
	}
	guard := probe.NewGuardWithResolver(probe.Policy{
		AllowedPorts:         map[int]struct{}{80: {}, 443: {}},
		MaxRedirects:         5,
		MaxResponseBody:      1 << 20,
		AllowPrivateNetworks: true,
	}, staticResolver{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := sqlc.New(pool)
	ownerID := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	probeWorker := NewProbeWorker(pool, q, box, probe.NewProber(guard), rate.NewLimiter(rate.Inf, 1000), logger)
	inserter := &recordingInserter{}
	probeWorker.SetJobInserter(inserter)
	return &workerEnv{
		t:        t,
		pool:     pool,
		worker:   probeWorker,
		ownerID:  ownerID,
		inserter: inserter,
	}
}

func (e *workerEnv) createMonitor(t *testing.T, rawURL string, confirm, recover int, paused, suspended bool) (uuid.UUID, uuid.UUID) {
	t.Helper()
	wsvc := workspaces.NewService(e.pool, sqlc.New(e.pool))
	ws, err := wsvc.Create(context.Background(), e.ownerID, "worker "+uuid.NewString())
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new monitor id: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO monitors
			(id, workspace_id, name, url, method, expected_status,
			 interval_seconds, timeout_seconds, confirmation_threshold,
			 recovery_threshold, paused, created_by)
		VALUES ($1, $2, 'worker monitor', $3, 'GET', '200-299', 60, 5, $4, $5, $6, $7)`,
		id, ws.ID, rawURL, confirm, recover, paused, e.ownerID); err != nil {
		t.Fatalf("insert monitor: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO monitor_state (monitor_id) VALUES ($1)`, id); err != nil {
		t.Fatalf("insert monitor state: %v", err)
	}
	if suspended {
		if _, err := e.pool.Exec(context.Background(),
			`UPDATE workspaces SET suspended_at = now(), suspended_by = $1 WHERE id = $2`,
			e.ownerID, ws.ID); err != nil {
			t.Fatalf("suspend workspace: %v", err)
		}
	}
	return id, ws.ID
}

func (e *workerEnv) resultCount(t *testing.T, monitorID uuid.UUID) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM probe_results WHERE monitor_id = $1`, monitorID).Scan(&n); err != nil {
		t.Fatalf("count results: %v", err)
	}
	return n
}

func (e *workerEnv) attachEmailChannel(t *testing.T, monitorID, workspaceID uuid.UUID, recipients ...string) uuid.UUID {
	t.Helper()
	channelID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new channel id: %v", err)
	}
	config, err := json.Marshal(map[string][]string{"recipients": recipients})
	if err != nil {
		t.Fatalf("encode channel config: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO alert_channels (id, workspace_id, kind, name, config)
		VALUES ($1, $2, 'email', 'Email', $3)`,
		channelID, workspaceID, config); err != nil {
		t.Fatalf("insert alert channel: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO monitor_alert_channels (monitor_id, channel_id)
		VALUES ($1, $2)`, monitorID, channelID); err != nil {
		t.Fatalf("attach alert channel: %v", err)
	}
	return channelID
}

// blockingTarget holds each probe request until want requests are in flight.
// Tests use it to guarantee that concurrent probe jobs actually overlap instead
// of serializing behind a fast handler.
func blockingTarget(t *testing.T, want int32) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == want {
			once.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-time.After(10 * time.Second):
			t.Errorf("timed out waiting for %d concurrent probe requests", want)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	return target
}

// waitForWorkspaceLockWait blocks until some session is waiting for a row lock
// on workspaces, which proves the probe transaction reached its workspace lock.
func waitForWorkspaceLockWait(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND pid <> pg_backend_pid()`).Scan(&waiting); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("probe did not block on the workspace lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProcess_RecordsResultAndTransitionsOnce(t *testing.T) {
	env := newWorkerEnv(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	monitorID, _ := env.createMonitor(t, target.URL, 1, 1, false, false)

	args := jobs.ProbeArgs{MonitorID: monitorID, ScheduledFor: time.Now().UTC().Truncate(time.Second)}
	if err := env.worker.Process(context.Background(), args); err != nil {
		t.Fatalf("Process: %v", err)
	}
	// Replay must be a no-op.
	if err := env.worker.Process(context.Background(), args); err != nil {
		t.Fatalf("replay: %v", err)
	}

	var status string
	var successes int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT status, consecutive_successes FROM monitor_state WHERE monitor_id = $1`, monitorID).
		Scan(&status, &successes); err != nil {
		t.Fatalf("state: %v", err)
	}
	if results := env.resultCount(t, monitorID); results != 1 || status != "up" || successes != 1 {
		t.Fatalf("results/status/successes = %d/%s/%d", results, status, successes)
	}
}

func TestProcess_OpensIncidentAndEnqueuesAlertOnce(t *testing.T) {
	env := newWorkerEnv(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer target.Close()
	monitorID, workspaceID := env.createMonitor(t, target.URL, 1, 1, false, false)
	env.attachEmailChannel(t, monitorID, workspaceID, "one@example.com", "two@example.com")

	args := jobs.ProbeArgs{MonitorID: monitorID, ScheduledFor: time.Now().UTC().Truncate(time.Second)}
	if err := env.worker.Process(context.Background(), args); err != nil {
		t.Fatalf("Process: %v", err)
	}

	var status string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT status FROM monitor_state WHERE monitor_id = $1`, monitorID).Scan(&status); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if status != "down" {
		t.Fatalf("status = %q, want down", status)
	}
	assertIncidentAlertCounts(t, env, monitorID, 1, 2, 2)

	// Replaying the same probe job must not duplicate the incident, delivery,
	// or alert job.
	if err := env.worker.Process(context.Background(), args); err != nil {
		t.Fatalf("replay: %v", err)
	}
	assertIncidentAlertCounts(t, env, monitorID, 1, 2, 2)

	// A later probe that stays down is not a new transition.
	next := args
	next.ScheduledFor = args.ScheduledFor.Add(time.Minute)
	if err := env.worker.Process(context.Background(), next); err != nil {
		t.Fatalf("second down probe: %v", err)
	}
	assertIncidentAlertCounts(t, env, monitorID, 1, 2, 2)
	if results := env.resultCount(t, monitorID); results != 2 {
		t.Fatalf("results = %d, want 2", results)
	}
}

func assertIncidentAlertCounts(t *testing.T, env *workerEnv, monitorID uuid.UUID, incidents, deliveries, jobsWanted int) {
	t.Helper()
	var gotIncidents, gotDeliveries int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM incidents WHERE monitor_id = $1`, monitorID).Scan(&gotIncidents); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if err := env.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM alert_deliveries d
		JOIN incidents i ON i.id = d.incident_id
		WHERE i.monitor_id = $1`, monitorID).Scan(&gotDeliveries); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	gotJobs, opts := env.inserter.snapshot()
	if gotIncidents != incidents || gotDeliveries != deliveries || len(gotJobs) != jobsWanted {
		t.Fatalf("incidents/deliveries/jobs = %d/%d/%d, want %d/%d/%d",
			gotIncidents, gotDeliveries, len(gotJobs), incidents, deliveries, jobsWanted)
	}
	for i, args := range gotJobs {
		if _, ok := args.(jobs.AlertArgs); !ok {
			t.Fatalf("job %d type = %T, want jobs.AlertArgs", i, args)
		}
		if opts[i] == nil || opts[i].MaxAttempts != 5 || opts[i].Queue != "alert" {
			t.Fatalf("job %d opts = %+v, want max 5 on alert queue", i, opts[i])
		}
	}
}

func TestProcess_ReplayDoesNotProbeAgain(t *testing.T) {
	env := newWorkerEnv(t)
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	monitorID, _ := env.createMonitor(t, target.URL, 1, 1, false, false)

	args := jobs.ProbeArgs{MonitorID: monitorID, ScheduledFor: time.Now().UTC().Truncate(time.Second)}
	if err := env.worker.Process(context.Background(), args); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if err := env.worker.Process(context.Background(), args); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("target calls = %d, want 1", got)
	}
	if results := env.resultCount(t, monitorID); results != 1 {
		t.Fatalf("results = %d, want 1", results)
	}
}

func TestProcess_SkipsPausedAndSuspended(t *testing.T) {
	env := newWorkerEnv(t)
	callCount := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	pausedID, _ := env.createMonitor(t, target.URL, 1, 1, true, false)
	suspendedID, _ := env.createMonitor(t, target.URL, 1, 1, false, true)
	args := func(id uuid.UUID) jobs.ProbeArgs {
		return jobs.ProbeArgs{MonitorID: id, ScheduledFor: time.Now().UTC().Truncate(time.Second)}
	}
	if err := env.worker.Process(context.Background(), args(pausedID)); err != nil {
		t.Fatalf("paused Process: %v", err)
	}
	if err := env.worker.Process(context.Background(), args(suspendedID)); err != nil {
		t.Fatalf("suspended Process: %v", err)
	}
	if callCount != 0 {
		t.Fatalf("probed %d times, want 0", callCount)
	}
	if env.resultCount(t, pausedID) != 0 || env.resultCount(t, suspendedID) != 0 {
		t.Fatal("paused/suspended monitors recorded results")
	}
}

func TestProcess_TLSFailureIsDownNotJobFailure(t *testing.T) {
	env := newWorkerEnv(t)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	monitorID, _ := env.createMonitor(t, target.URL, 1, 1, false, false)

	args := jobs.ProbeArgs{MonitorID: monitorID, ScheduledFor: time.Now().UTC().Truncate(time.Second)}
	if err := env.worker.Process(context.Background(), args); err != nil {
		t.Fatalf("Process: %v", err)
	}
	var status string
	var errText *string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT status, error FROM probe_results WHERE monitor_id = $1`, monitorID).
		Scan(&status, &errText); err != nil {
		t.Fatalf("result: %v", err)
	}
	if status != "down" || errText == nil || !strings.Contains(*errText, "tls") {
		t.Fatalf("status/error = %s/%v", status, errText)
	}
}

func TestProcess_MissingMonitorIsNoOp(t *testing.T) {
	env := newWorkerEnv(t)
	if err := env.worker.Process(context.Background(), jobs.ProbeArgs{
		MonitorID:    uuid.New(),
		ScheduledFor: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Process missing monitor: %v", err)
	}
}

func TestProcess_ConcurrentSchedulesBothAdvanceState(t *testing.T) {
	env := newWorkerEnv(t)
	target := blockingTarget(t, 2)
	monitorID, _ := env.createMonitor(t, target.URL, 1, 1, false, false)

	base := time.Now().UTC().Truncate(time.Second)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = env.worker.Process(context.Background(), jobs.ProbeArgs{
				MonitorID:    monitorID,
				ScheduledFor: base.Add(-time.Duration(i) * time.Minute),
			})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Process %d: %v", i, err)
		}
	}

	var successes int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT consecutive_successes FROM monitor_state WHERE monitor_id = $1`, monitorID).
		Scan(&successes); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if successes != 2 {
		t.Fatalf("successes = %d, want 2", successes)
	}
	if results := env.resultCount(t, monitorID); results != 2 {
		t.Fatalf("results = %d, want 2", results)
	}
}

func TestRecord_ConcurrentSameScheduleRecordsOnce(t *testing.T) {
	env := newWorkerEnv(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	monitorID, _ := env.createMonitor(t, target.URL, 1, 1, false, false)

	q := sqlc.New(env.pool)
	monitor, err := q.GetMonitorForProbe(context.Background(), monitorID)
	if err != nil {
		t.Fatalf("load monitor: %v", err)
	}
	scheduledFor := time.Now().UTC().Truncate(time.Second)
	result := probe.Result{Up: true, StatusCode: http.StatusOK, LatencyMS: 1}

	// Call the record step directly so both transactions are guaranteed to
	// race on the same schedule; the pre-probe replay check is covered by
	// TestProcess_ReplayDoesNotProbeAgain.
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = env.worker.record(context.Background(), monitor, scheduledFor, time.Now().UTC(), result)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	var successes int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT consecutive_successes FROM monitor_state WHERE monitor_id = $1`, monitorID).
		Scan(&successes); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if successes != 1 {
		t.Fatalf("successes = %d, want 1", successes)
	}
	if results := env.resultCount(t, monitorID); results != 1 {
		t.Fatalf("results = %d, want 1", results)
	}
}

func TestRecord_PausedStoresResultWithoutStateOrIncident(t *testing.T) {
	env := newWorkerEnv(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	monitorID, _ := env.createMonitor(t, target.URL, 1, 1, false, false)

	q := sqlc.New(env.pool)
	monitor, err := q.GetMonitorForProbe(context.Background(), monitorID)
	if err != nil {
		t.Fatalf("load monitor: %v", err)
	}
	// Simulate a pause committing after the probe request was sent.
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE monitors SET paused = true WHERE id = $1`, monitorID); err != nil {
		t.Fatalf("pause monitor: %v", err)
	}

	scheduledFor := time.Now().UTC().Truncate(time.Second)
	result := probe.Result{Up: false, StatusCode: http.StatusInternalServerError, LatencyMS: 1}
	if err := env.worker.record(context.Background(), monitor, scheduledFor, time.Now().UTC(), result); err != nil {
		t.Fatalf("record: %v", err)
	}

	if results := env.resultCount(t, monitorID); results != 1 {
		t.Fatalf("results = %d, want 1 stored for audit", results)
	}
	var status string
	var failures int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT status, consecutive_failures FROM monitor_state WHERE monitor_id = $1`, monitorID).
		Scan(&status, &failures); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if status != "unknown" || failures != 0 {
		t.Fatalf("state = %s/%d, want unchanged unknown/0", status, failures)
	}
	var incidents int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM incidents WHERE monitor_id = $1`, monitorID).Scan(&incidents); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if incidents != 0 {
		t.Fatalf("incidents = %d, want 0 while paused", incidents)
	}
}

// TestProcess_LockOrderMatchesMonitorMutations overlaps a probe result write
// with each monitor mutation while holding the workspace row lock the mutation
// would take. Before the worker locked the workspace first, the mutation
// deadlocked with the probe transaction (SQLSTATE 40P01).
func TestProcess_LockOrderMatchesMonitorMutations(t *testing.T) {
	env := newWorkerEnv(t)
	ctx := context.Background()

	mutations := []struct {
		name    string
		deleted bool
		run     func(context.Context, pgx.Tx, uuid.UUID) error
	}{
		{
			name: "update",
			run: func(ctx context.Context, tx pgx.Tx, monitorID uuid.UUID) error {
				if _, err := tx.Exec(ctx, `UPDATE monitors SET updated_at = now() WHERE id = $1`, monitorID); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `UPDATE monitor_state SET updated_at = now() WHERE monitor_id = $1`, monitorID)
				return err
			},
		},
		{
			name: "resume",
			run: func(ctx context.Context, tx pgx.Tx, monitorID uuid.UUID) error {
				if _, err := tx.Exec(ctx, `UPDATE monitors SET paused = false, updated_at = now() WHERE id = $1`, monitorID); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `UPDATE monitor_state SET next_check_at = now(), updated_at = now() WHERE monitor_id = $1`, monitorID)
				return err
			},
		},
		{
			name:    "delete",
			deleted: true,
			run: func(ctx context.Context, tx pgx.Tx, monitorID uuid.UUID) error {
				_, err := tx.Exec(ctx, `DELETE FROM monitors WHERE id = $1`, monitorID)
				return err
			},
		},
	}

	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer target.Close()
			monitorID, workspaceID := env.createMonitor(t, target.URL, 1, 1, false, false)

			tx, err := env.pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin mutation tx: %v", err)
			}
			defer func() {
				_ = tx.Rollback(ctx)
			}()
			if _, err := tx.Exec(ctx, `SELECT id FROM workspaces WHERE id = $1 FOR UPDATE`, workspaceID); err != nil {
				t.Fatalf("lock workspace: %v", err)
			}

			done := make(chan error, 1)
			go func() {
				done <- env.worker.Process(ctx, jobs.ProbeArgs{
					MonitorID:    monitorID,
					ScheduledFor: time.Now().UTC().Truncate(time.Second),
				})
			}()

			waitForWorkspaceLockWait(t, env.pool)

			if err := tc.run(ctx, tx, monitorID); err != nil {
				t.Fatalf("mutation was blocked by the probe transaction: %v", err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatalf("commit mutation: %v", err)
			}

			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Process: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Process did not finish after the mutation committed")
			}

			want := 1
			if tc.deleted {
				want = 0
			}
			if results := env.resultCount(t, monitorID); results != want {
				t.Fatalf("results = %d, want %d", results, want)
			}
		})
	}
}

func TestProcess_SSRFBlockRecordsSecurityEvent(t *testing.T) {
	env := newWorkerEnv(t)
	env.worker.prober = probe.NewProber(probe.NewGuardWithResolver(probe.Policy{
		AllowedPorts:    map[int]struct{}{80: {}, 443: {}},
		MaxRedirects:    5,
		MaxResponseBody: 1 << 20,
	}, staticResolver{}))
	monitorID, wsID := env.createMonitor(t, "http://example.com/health?token=super-secret", 1, 1, false, false)

	args := jobs.ProbeArgs{MonitorID: monitorID, ScheduledFor: time.Now().UTC().Truncate(time.Second)}
	if err := env.worker.Process(context.Background(), args); err != nil {
		t.Fatalf("Process: %v", err)
	}

	var class string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT error FROM probe_results WHERE monitor_id = $1`, monitorID).Scan(&class); err != nil {
		t.Fatalf("read result: %v", err)
	}
	if !strings.HasPrefix(class, "ssrf_blocked") {
		t.Fatalf("result error = %q, want ssrf_blocked", class)
	}

	var kind string
	var detail []byte
	if err := env.pool.QueryRow(context.Background(),
		`SELECT kind, detail FROM security_events
		 WHERE workspace_id = $1 AND kind = 'ssrf_blocked'
		 ORDER BY created_at DESC, id DESC LIMIT 1`, wsID).Scan(&kind, &detail); err != nil {
		t.Fatalf("read security event: %v", err)
	}
	if kind != "ssrf_blocked" {
		t.Fatalf("kind = %q", kind)
	}
	var payload struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(detail, &payload); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if payload.URL != "http://example.com/health" {
		t.Fatalf("event url = %q, want query stripped", payload.URL)
	}
	if strings.Contains(string(detail), "super-secret") {
		t.Fatalf("security event leaked the query string: %s", detail)
	}
}

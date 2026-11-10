package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"golang.org/x/time/rate"

	"o1/internal/api"
	"o1/internal/db/sqlc"
	"o1/internal/jobs"
	"o1/internal/monitors"
	"o1/internal/probe"
	"o1/internal/scheduler"
	"o1/internal/secretbox"
	"o1/internal/testutil"
	"o1/internal/worker"
	"o1/internal/workspaces"
)

// fullStackEnv wires the HTTP API, scheduler, and probe worker against one
// database and one guarded prober.
type fullStackEnv struct {
	*monitorAPIEnv
	sched  *scheduler.Scheduler
	worker *worker.ProbeWorker
}

// newFullStackEnv builds a complete probe pipeline. The guard permits the
// supplied loopback port so an httptest target can be probed.
func newFullStackEnv(t *testing.T, allowedPort int) *fullStackEnv {
	t.Helper()
	pool, cleanup := testutil.NewPostgres(t)
	t.Cleanup(cleanup)

	cfg := testutil.TestConfig(t)
	cfg.MonitorCreateLimit = 10
	cfg.MonitorMinInterval = 60 * time.Second
	cfg.MonitorMaxInterval = 24 * time.Hour
	cfg.ProbeMaxRedirects = 5
	cfg.ProbeMaxBodyBytes = 1 << 20

	ports := map[int]struct{}{80: {}, 443: {}}
	if allowedPort > 0 {
		ports[allowedPort] = struct{}{}
	}
	guard := probe.NewGuardWithResolver(probe.Policy{
		AllowedPorts:         ports,
		MaxRedirects:         5,
		MaxResponseBody:      1 << 20,
		AllowPrivateNetworks: true,
	}, staticResolver{ip: "127.0.0.1"})

	box, err := secretbox.New(cfg.EncryptionKey)
	if err != nil {
		t.Fatalf("secretbox: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	wsSvc := workspaces.NewService(pool, sqlc.New(pool))
	monitorSvc := monitors.NewService(pool, sqlc.New(pool), wsSvc, box, guard, cfg)

	router := api.NewRouter(api.Dependencies{
		Logger:           logger,
		Pool:             pool,
		Config:           cfg,
		EmailSender:      testutil.NewFakeSender(),
		WorkspaceService: wsSvc,
		MonitorService:   monitorSvc,
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: logger})
	if err != nil {
		t.Fatalf("new river client: %v", err)
	}
	probeWorker := worker.NewProbeWorker(
		pool,
		sqlc.New(pool),
		box,
		probe.NewProber(guard),
		rate.NewLimiter(rate.Inf, 1000),
		logger,
		nil,
	)

	return &fullStackEnv{
		monitorAPIEnv: &monitorAPIEnv{t: t, pool: pool, cfg: cfg, router: router, server: server},
		sched:         scheduler.New(pool, sqlc.New(pool), riverClient, cfg, logger, nil, nil),
		worker:        probeWorker,
	}
}

func (e *fullStackEnv) loginAndWorkspace(t *testing.T, emailAddr, workspaceName string) (*apiClient, string) {
	t.Helper()
	client := e.login(emailAddr)
	return client, client.createWorkspace(t, workspaceName)
}

func TestAPI_MonitorProbedBySchedulerAndWorker(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	parsed, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target URL: %v", err)
	}
	_, portRaw, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split target port: %v", err)
	}
	var port int
	if port, err = strconv.Atoi(portRaw); err != nil {
		t.Fatalf("parse target port: %v", err)
	}

	env := newFullStackEnv(t, port)
	owner, ws := env.loginAndWorkspace(t, "flow@example.com", "Flow")

	body := validMonitorBody(0)
	body["url"] = target.URL
	resp, raw := owner.do(http.MethodPost, "/api/v1/workspaces/"+ws+"/monitors", body)
	requireStatus(t, resp, http.StatusCreated)
	monitor := decodeJSON[monitorJSON](t, raw)

	if _, err := env.sched.Tick(context.Background()); err != nil {
		t.Fatalf("scheduler Tick: %v", err)
	}
	var jobCount int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = 'probe'`).Scan(&jobCount); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobCount != 1 {
		t.Fatalf("jobs = %d, want 1", jobCount)
	}

	// Execute the enqueued job directly so the test does not depend on River
	// fetcher timing.
	var rawArgs []byte
	if err := env.pool.QueryRow(context.Background(),
		`SELECT args FROM river_job WHERE kind = 'probe'`).Scan(&rawArgs); err != nil {
		t.Fatalf("read args: %v", err)
	}
	var args jobs.ProbeArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		t.Fatalf("decode args: %v", err)
	}
	if err := env.worker.Process(context.Background(), args); err != nil {
		t.Fatalf("worker Process: %v", err)
	}

	resp, raw = owner.do(http.MethodGet, "/api/v1/workspaces/"+ws+"/monitors/"+monitor.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	got := decodeJSON[monitorJSON](t, raw)
	if got.Status != "up" {
		t.Fatalf("monitor status = %q, want up (%+v)", got.Status, got)
	}
	if got.LastCheck == nil || got.LastCheck.StatusCode == nil || *got.LastCheck.StatusCode != http.StatusOK {
		t.Fatalf("monitor last_check = %+v", got.LastCheck)
	}

	var resultCount int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM probe_results WHERE monitor_id = $1`, monitor.ID).Scan(&resultCount); err != nil {
		t.Fatalf("count results: %v", err)
	}
	if resultCount != 1 {
		t.Fatalf("probe results = %d, want 1", resultCount)
	}
}

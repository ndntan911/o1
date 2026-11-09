package monitors_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/auth"
	"o1/internal/config"
	"o1/internal/db/sqlc"
	"o1/internal/monitors"
	"o1/internal/probe"
	"o1/internal/secretbox"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

type staticResolver struct {
	ip string
}

func (r staticResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if r.ip == "" {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return []net.IPAddr{{IP: net.ParseIP(r.ip)}}, nil
}

type testEnv struct {
	t       *testing.T
	pool    *pgxpool.Pool
	svc     *monitors.Service
	wsvc    *workspaces.Service
	box     *secretbox.Box
	ownerID uuid.UUID
	cfg     config.Config
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvWithCreateLimit(t, 10)
}

func newTestEnvWithCreateLimit(t *testing.T, createLimit int) *testEnv {
	t.Helper()
	pool, _ := testutil.NewPostgres(t)

	box, err := secretbox.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatalf("new secretbox: %v", err)
	}
	guard := probe.NewGuardWithResolver(probe.Policy{
		AllowedPorts:    map[int]struct{}{80: {}, 443: {}},
		MaxRedirects:    5,
		MaxResponseBody: 1 << 20,
	}, staticResolver{ip: "93.184.216.34"})

	cfg := config.Config{
		Env:                "test",
		MonitorMinInterval: 60 * time.Second,
		MonitorMaxInterval: 24 * time.Hour,
		MonitorCreateLimit: createLimit,
	}
	q := sqlc.New(pool)
	wsvc := workspaces.NewService(pool, q)
	svc := monitors.NewService(pool, q, wsvc, box, guard, cfg)

	ownerID := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	return &testEnv{t: t, pool: pool, svc: svc, wsvc: wsvc, box: box, ownerID: ownerID, cfg: cfg}
}

func (e *testEnv) createWorkspace(t *testing.T, _ string) uuid.UUID {
	t.Helper()
	ws, err := e.wsvc.Create(context.Background(), e.ownerID, "Test Workspace")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	return ws.ID
}

func (e *testEnv) createUser(t *testing.T, email string) uuid.UUID {
	t.Helper()
	return testutil.CreateVerifiedUser(t, e.pool, email, "correct horse battery staple")
}

func (e *testEnv) monitorID(t *testing.T, workspaceID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(context.Background(),
		`SELECT id FROM monitors WHERE workspace_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`,
		workspaceID).Scan(&id); err != nil {
		t.Fatalf("load monitor id: %v", err)
	}
	return id
}

func validInput(i int) monitors.Input {
	return monitors.Input{
		Name:                  fmt.Sprintf("Monitor %d", i),
		URL:                   "https://example.com/health",
		Method:                "GET",
		ExpectedStatus:        "2xx",
		IntervalSeconds:       60,
		TimeoutSeconds:        10,
		ConfirmationThreshold: 2,
		RecoveryThreshold:     2,
	}
}

func strPtr(s string) *string { return &s }

func TestCreate_Quota(t *testing.T) {
	env := newTestEnv(t) // default MonitorCreateLimit 10, so this test exercises quota only
	ws := env.createWorkspace(t, "owner@example.com")

	for i := 0; i < 5; i++ {
		if _, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(i)); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(5)); !errors.Is(err, monitors.ErrQuotaExceeded) {
		t.Fatalf("6th create err = %v, want ErrQuotaExceeded", err)
	}

	// Deleting a monitor frees a quota slot.
	if err := env.svc.Delete(context.Background(), env.ownerID, ws, env.monitorID(t, ws)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(6)); err != nil {
		t.Fatalf("create after delete: %v", err)
	}
}

func TestCreate_RateLimit(t *testing.T) {
	env := newTestEnvWithCreateLimit(t, 3) // quota 5, so the rate limit fires first
	ws := env.createWorkspace(t, "owner@example.com")

	for i := 0; i < 3; i++ {
		if _, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(i)); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	_, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(3))
	var rl *monitors.RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter <= 0 {
		t.Fatalf("4th create err = %v, want RateLimitError with RetryAfter", err)
	}
}

func TestCreate_RateLimitRecordsEvent(t *testing.T) {
	env := newTestEnvWithCreateLimit(t, 1)
	ws := env.createWorkspace(t, "owner@example.com")

	if _, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(0)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(1)); err == nil {
		t.Fatal("second create succeeded, want rate limit")
	}

	var detail string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT detail::text FROM security_events WHERE workspace_id = $1 AND kind = 'monitor_create_rate_limited'`,
		ws).Scan(&detail); err != nil {
		t.Fatalf("load rate limit event: %v", err)
	}
	if !strings.Contains(detail, "limit") {
		t.Fatalf("event detail = %s, want limit", detail)
	}
}

func TestCreate_EncryptsSecretsAtRest(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "owner@example.com")
	in := validInput(0)
	in.Headers = map[string]string{"Authorization": "Bearer super-secret"}
	body := `{"token":"super-secret"}`
	in.Body = &body
	in.Method = "POST"
	created, err := env.svc.Create(context.Background(), env.ownerID, ws, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var headers, rawBody string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT headers_enc, body_enc FROM monitors WHERE id = $1`, created.ID).Scan(&headers, &rawBody); err != nil {
		t.Fatalf("read encrypted columns: %v", err)
	}
	if strings.Contains(headers, "super-secret") || strings.Contains(rawBody, "super-secret") {
		t.Fatal("plaintext secret found at rest")
	}
}

func TestCreate_RecordsRedactedSecurityEvent(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "owner@example.com")
	in := validInput(0)
	in.URL = "https://example.com/health?token=super-secret"

	if _, err := env.svc.Create(context.Background(), env.ownerID, ws, in); err != nil {
		t.Fatalf("create: %v", err)
	}

	var detail string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT detail::text FROM security_events WHERE workspace_id = $1 AND kind = 'monitor_created'`,
		ws).Scan(&detail); err != nil {
		t.Fatalf("load event: %v", err)
	}
	if strings.Contains(detail, "super-secret") {
		t.Fatalf("event detail leaked query string: %s", detail)
	}
	if !strings.Contains(detail, "https://example.com/health") {
		t.Fatalf("event detail = %s, want redacted url", detail)
	}
}

func TestCreate_SSRFBlockedRecordsEvent(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "owner@example.com")
	in := validInput(0)
	in.URL = "http://127.0.0.1/health"

	_, err := env.svc.Create(context.Background(), env.ownerID, ws, in)
	var verr *monitors.ValidationError
	if !errors.As(err, &verr) || verr.Fields["url"] == "" {
		t.Fatalf("err = %v, want url validation error", err)
	}

	var count int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM security_events WHERE workspace_id = $1 AND kind = 'ssrf_blocked'`,
		ws).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 1 {
		t.Fatalf("ssrf_blocked events = %d, want 1", count)
	}
}

func TestCreate_QuotaSerialized(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "owner@example.com")

	const attempts = 10
	errs := make([]error, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = env.svc.Create(context.Background(), env.ownerID, ws, validInput(i))
		}(i)
	}
	close(start)
	wg.Wait()

	var ok, quota int
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, monitors.ErrQuotaExceeded):
			quota++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 5 || quota != 5 {
		t.Fatalf("ok = %d, quota = %d; want 5 and 5", ok, quota)
	}
}

func TestGetAndList_MaskHeaders(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "owner@example.com")
	in := validInput(0)
	in.Headers = map[string]string{"Authorization": "Bearer super-secret"}
	created, err := env.svc.Create(context.Background(), env.ownerID, ws, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	get, err := env.svc.Get(context.Background(), env.ownerID, ws, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := get.Headers["Authorization"]; got != "***" {
		t.Fatalf("get header = %q, want ***", got)
	}
	if len(get.Headers) != 1 {
		t.Fatalf("get headers = %v", get.Headers)
	}
	if get.HasBody {
		t.Fatal("HasBody = true, want false")
	}
	if get.Summary24h != nil {
		t.Fatalf("Summary24h = %+v, want nil", get.Summary24h)
	}
	if get.Status != "unknown" {
		t.Fatalf("status = %q, want unknown", get.Status)
	}

	list, err := env.svc.List(context.Background(), env.ownerID, ws, nil, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d", len(list))
	}
	if got := list[0].Headers["Authorization"]; got != "***" {
		t.Fatalf("list header = %q, want ***", got)
	}
}

func TestGet_IncludesStateAndSummary(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "owner@example.com")
	created, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(0))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	resultID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new result id: %v", err)
	}
	if _, err := env.pool.Exec(context.Background(),
		`INSERT INTO probe_results (id, monitor_id, workspace_id, scheduled_for, checked_at, status, status_code, latency_ms)
		 VALUES ($1, $2, $3, now(), now(), 'up', 200, 123)`,
		resultID, created.ID, ws); err != nil {
		t.Fatalf("insert probe result: %v", err)
	}
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE monitor_state
		 SET status = 'up', consecutive_successes = 1, last_checked_at = now(),
		     last_status_code = 200, last_latency_ms = 123
		 WHERE monitor_id = $1`, created.ID); err != nil {
		t.Fatalf("update state: %v", err)
	}

	got, err := env.svc.Get(context.Background(), env.ownerID, ws, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "up" {
		t.Fatalf("status = %q, want up", got.Status)
	}
	if got.LastCheck == nil || got.LastCheck.StatusCode == nil || *got.LastCheck.StatusCode != 200 {
		t.Fatalf("last check = %+v", got.LastCheck)
	}
	if got.LastCheck.LatencyMS == nil || *got.LastCheck.LatencyMS != 123 {
		t.Fatalf("last check latency = %+v", got.LastCheck)
	}
	if got.Summary24h == nil {
		t.Fatal("Summary24h = nil, want summary")
	}
	if got.Summary24h.Checks != 1 || got.Summary24h.Up != 1 || got.Summary24h.Down != 0 {
		t.Fatalf("summary = %+v", got.Summary24h)
	}
	if got.Summary24h.Uptime == nil || *got.Summary24h.Uptime != 1 {
		t.Fatalf("uptime = %v", got.Summary24h.Uptime)
	}

	list, err := env.svc.List(context.Background(), env.ownerID, ws, nil, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Summary24h == nil || list[0].Summary24h.Checks != 1 {
		t.Fatalf("list summary = %+v", list)
	}

	// Pausing keeps the preserved state and summary but reports paused.
	paused, err := env.svc.SetPaused(context.Background(), env.ownerID, ws, created.ID, true)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if paused.Status != "paused" || paused.Summary24h == nil || paused.Summary24h.Checks != 1 {
		t.Fatalf("paused monitor = %+v", paused)
	}
}

func TestList_Pagination(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "owner@example.com")
	for i := 0; i < 3; i++ {
		if _, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(i)); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	first, err := env.svc.List(context.Background(), env.ownerID, ws, nil, 2)
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first page len = %d, want 2", len(first))
	}
	last := first[len(first)-1]
	rest, err := env.svc.List(context.Background(), env.ownerID, ws, &monitors.Cursor{Time: last.CreatedAt, ID: last.ID}, 2)
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(rest) != 1 {
		t.Fatalf("second page len = %d, want 1", len(rest))
	}
	if rest[0].ID == first[0].ID || rest[0].ID == first[1].ID {
		t.Fatalf("second page repeated first page item")
	}
}

func TestUpdate_MergesAndReencrypts(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "owner@example.com")
	in := validInput(0)
	in.Method = "POST"
	in.Headers = map[string]string{"Authorization": "Bearer one"}
	in.Body = strPtr(`{"a":1}`)
	in.ContentType = strPtr("application/json")
	created, err := env.svc.Create(context.Background(), env.ownerID, ws, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Renaming only must preserve the encrypted secrets and body.
	updated, err := env.svc.Update(context.Background(), env.ownerID, ws, created.ID, monitors.UpdateInput{Name: strPtr("Renamed")})
	if err != nil {
		t.Fatalf("update name: %v", err)
	}
	if updated.Name != "Renamed" {
		t.Fatalf("name = %q", updated.Name)
	}
	if updated.Method != "POST" || !updated.HasBody || updated.ContentType == nil || *updated.ContentType != "application/json" {
		t.Fatalf("update lost secrets/body: %+v", updated)
	}
	if got := decryptHeaders(t, env, created.ID); got["Authorization"] != "Bearer one" {
		t.Fatalf("stored headers = %v", got)
	}

	// Clearing the body also clears the content type.
	updated, err = env.svc.Update(context.Background(), env.ownerID, ws, created.ID, monitors.UpdateInput{Body: strPtr("")})
	if err != nil {
		t.Fatalf("clear body: %v", err)
	}
	if updated.HasBody || updated.ContentType != nil {
		t.Fatalf("body not cleared: hasBody=%v contentType=%v", updated.HasBody, updated.ContentType)
	}

	// Replacing headers replaces the whole map.
	updated, err = env.svc.Update(context.Background(), env.ownerID, ws, created.ID, monitors.UpdateInput{
		Headers: map[string]string{"X-Api-Key": "two"},
	})
	if err != nil {
		t.Fatalf("replace headers: %v", err)
	}
	if got := decryptHeaders(t, env, created.ID); got["X-Api-Key"] != "two" || len(got) != 1 {
		t.Fatalf("stored headers = %v", got)
	}
	if len(updated.Headers) != 1 || updated.Headers["X-Api-Key"] != "***" {
		t.Fatalf("masked headers = %v", updated.Headers)
	}
}

func TestUpdate_ValidatesAndBlocksSSRF(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "owner@example.com")
	created, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(0))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = env.svc.Update(context.Background(), env.ownerID, ws, created.ID, monitors.UpdateInput{
		URL: strPtr("http://127.0.0.1/health"),
	})
	var verr *monitors.ValidationError
	if !errors.As(err, &verr) || verr.Fields["url"] == "" {
		t.Fatalf("err = %v, want url validation error", err)
	}
	if !errors.Is(err, probe.ErrBlocked) {
		t.Fatalf("err = %v, want wrapped probe.ErrBlocked", err)
	}

	var count int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM security_events WHERE workspace_id = $1 AND kind = 'ssrf_blocked'`,
		ws).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 1 {
		t.Fatalf("ssrf_blocked events = %d, want 1", count)
	}

	// A failed update must not change the stored URL.
	get, err := env.svc.Get(context.Background(), env.ownerID, ws, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if get.URL != "https://example.com/health" {
		t.Fatalf("url = %q, want unchanged", get.URL)
	}
}

func TestSetPaused_Idempotent(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "owner@example.com")
	created, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(0))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	paused, err := env.svc.SetPaused(context.Background(), env.ownerID, ws, created.ID, true)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if !paused.Paused || paused.Status != "paused" {
		t.Fatalf("paused = %v, status = %q", paused.Paused, paused.Status)
	}
	if _, err := env.svc.SetPaused(context.Background(), env.ownerID, ws, created.ID, true); err != nil {
		t.Fatalf("pause twice: %v", err)
	}

	// Force the next check far into the future so resume must reset it.
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE monitor_state SET next_check_at = now() + interval '1 hour' WHERE monitor_id = $1`,
		created.ID); err != nil {
		t.Fatalf("set next check: %v", err)
	}
	resumed, err := env.svc.SetPaused(context.Background(), env.ownerID, ws, created.ID, false)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Paused || resumed.Status != "unknown" {
		t.Fatalf("resumed = %v, status = %q", resumed.Paused, resumed.Status)
	}
	if _, err := env.svc.SetPaused(context.Background(), env.ownerID, ws, created.ID, false); err != nil {
		t.Fatalf("resume twice: %v", err)
	}

	var due bool
	if err := env.pool.QueryRow(context.Background(),
		`SELECT next_check_at <= now() FROM monitor_state WHERE monitor_id = $1`,
		created.ID).Scan(&due); err != nil {
		t.Fatalf("read next check: %v", err)
	}
	if !due {
		t.Fatal("resume did not set next_check_at to now")
	}
}

func TestDelete_CascadesState(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "owner@example.com")
	created, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(0))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := env.svc.Delete(context.Background(), env.ownerID, ws, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := env.svc.Get(context.Background(), env.ownerID, ws, created.ID); !errors.Is(err, monitors.ErrNotFound) {
		t.Fatalf("get after delete err = %v, want ErrNotFound", err)
	}
	var stateCount int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM monitor_state WHERE monitor_id = $1`, created.ID).Scan(&stateCount); err != nil {
		t.Fatalf("count state: %v", err)
	}
	if stateCount != 0 {
		t.Fatalf("state rows = %d, want 0", stateCount)
	}
}

func TestCrossTenant_ReturnsNotFound(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "owner@example.com")
	created, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(0))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	outsider := env.createUser(t, "outsider@example.com")
	ctx := context.Background()

	if _, err := env.svc.Get(ctx, outsider, ws, created.ID); !errors.Is(err, monitors.ErrNotFound) {
		t.Fatalf("get err = %v", err)
	}
	if _, err := env.svc.List(ctx, outsider, ws, nil, 50); !errors.Is(err, monitors.ErrNotFound) {
		t.Fatalf("list err = %v", err)
	}
	if _, err := env.svc.Update(ctx, outsider, ws, created.ID, monitors.UpdateInput{Name: strPtr("x")}); !errors.Is(err, monitors.ErrNotFound) {
		t.Fatalf("update err = %v", err)
	}
	if err := env.svc.Delete(ctx, outsider, ws, created.ID); !errors.Is(err, monitors.ErrNotFound) {
		t.Fatalf("delete err = %v", err)
	}
	if _, err := env.svc.SetPaused(ctx, outsider, ws, created.ID, true); !errors.Is(err, monitors.ErrNotFound) {
		t.Fatalf("pause err = %v", err)
	}
	if _, err := env.svc.Create(ctx, outsider, ws, validInput(1)); !errors.Is(err, monitors.ErrNotFound) {
		t.Fatalf("create err = %v", err)
	}
}

func TestViewer_CannotMutate(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "owner@example.com")
	created, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(0))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	viewer := env.createUser(t, "viewer@example.com")
	if err := env.wsvc.AddMember(context.Background(), env.ownerID, ws, "viewer@example.com", workspaces.RoleViewer); err != nil {
		t.Fatalf("add viewer: %v", err)
	}
	ctx := context.Background()

	if _, err := env.svc.Get(ctx, viewer, ws, created.ID); err != nil {
		t.Fatalf("viewer get: %v", err)
	}
	if _, err := env.svc.List(ctx, viewer, ws, nil, 50); err != nil {
		t.Fatalf("viewer list: %v", err)
	}
	if _, err := env.svc.Create(ctx, viewer, ws, validInput(1)); !errors.Is(err, monitors.ErrForbidden) {
		t.Fatalf("viewer create err = %v", err)
	}
	if _, err := env.svc.Update(ctx, viewer, ws, created.ID, monitors.UpdateInput{Name: strPtr("x")}); !errors.Is(err, monitors.ErrForbidden) {
		t.Fatalf("viewer update err = %v", err)
	}
	if err := env.svc.Delete(ctx, viewer, ws, created.ID); !errors.Is(err, monitors.ErrForbidden) {
		t.Fatalf("viewer delete err = %v", err)
	}
	if _, err := env.svc.SetPaused(ctx, viewer, ws, created.ID, true); !errors.Is(err, monitors.ErrForbidden) {
		t.Fatalf("viewer pause err = %v", err)
	}
}

func TestSuspendedWorkspace_ReadsAllowedMutationsRejected(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "owner@example.com")
	created, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(0))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE workspaces SET suspended_at = now() WHERE id = $1`, ws); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	ctx := context.Background()

	if _, err := env.svc.Get(ctx, env.ownerID, ws, created.ID); err != nil {
		t.Fatalf("get on suspended workspace: %v", err)
	}
	if _, err := env.svc.List(ctx, env.ownerID, ws, nil, 50); err != nil {
		t.Fatalf("list on suspended workspace: %v", err)
	}
	if _, err := env.svc.Create(ctx, env.ownerID, ws, validInput(1)); !errors.Is(err, monitors.ErrSuspended) {
		t.Fatalf("create err = %v", err)
	}
	if _, err := env.svc.Update(ctx, env.ownerID, ws, created.ID, monitors.UpdateInput{Name: strPtr("x")}); !errors.Is(err, monitors.ErrSuspended) {
		t.Fatalf("update err = %v", err)
	}
	if err := env.svc.Delete(ctx, env.ownerID, ws, created.ID); !errors.Is(err, monitors.ErrSuspended) {
		t.Fatalf("delete err = %v", err)
	}
	if _, err := env.svc.SetPaused(ctx, env.ownerID, ws, created.ID, true); !errors.Is(err, monitors.ErrSuspended) {
		t.Fatalf("pause err = %v", err)
	}
}

func decryptHeaders(t *testing.T, env *testEnv, monitorID uuid.UUID) map[string]string {
	t.Helper()
	var enc *string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT headers_enc FROM monitors WHERE id = $1`, monitorID).Scan(&enc); err != nil {
		t.Fatalf("read headers_enc: %v", err)
	}
	if enc == nil {
		return nil
	}
	raw, err := env.box.Open(*enc)
	if err != nil {
		t.Fatalf("open headers: %v", err)
	}
	var headers map[string]string
	if err := json.Unmarshal(raw, &headers); err != nil {
		t.Fatalf("unmarshal headers: %v", err)
	}
	return headers
}

func TestCreate_AttachesAllAlertChannels(t *testing.T) {
	env := newTestEnv(t)
	ws := env.createWorkspace(t, "owner@example.com")

	// Creating a monitor without a workspace channel still succeeds.
	before, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(0))
	if err != nil {
		t.Fatalf("create monitor without channel: %v", err)
	}
	var joins int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM monitor_alert_channels WHERE monitor_id = $1`, before.ID).Scan(&joins); err != nil {
		t.Fatalf("count join rows: %v", err)
	}
	if joins != 0 {
		t.Fatalf("join rows without channel = %d, want 0", joins)
	}

	emailID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new email channel id: %v", err)
	}
	if _, err := env.pool.Exec(context.Background(), `
		INSERT INTO alert_channels (id, workspace_id, kind, name, config)
		VALUES ($1, $2, 'email', 'Email', '{"recipients":["ops@example.com"]}')`,
		emailID, ws); err != nil {
		t.Fatalf("insert email channel: %v", err)
	}
	webhookID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new webhook channel id: %v", err)
	}
	if _, err := env.pool.Exec(context.Background(), `
		INSERT INTO alert_channels (id, workspace_id, kind, name, config, enabled)
		VALUES ($1, $2, 'webhook', 'Deploy hook', '{"url_enc":"v1:x","url_preview":"https://example.com/…"}', false)`,
		webhookID, ws); err != nil {
		t.Fatalf("insert webhook channel: %v", err)
	}

	created, err := env.svc.Create(context.Background(), env.ownerID, ws, validInput(1))
	if err != nil {
		t.Fatalf("create monitor with channels: %v", err)
	}
	for _, channelID := range []uuid.UUID{emailID, webhookID} {
		var enabled bool
		if err := env.pool.QueryRow(context.Background(),
			`SELECT enabled FROM monitor_alert_channels WHERE monitor_id = $1 AND channel_id = $2`,
			created.ID, channelID).Scan(&enabled); err != nil {
			t.Fatalf("load join row for %s: %v", channelID, err)
		}
		if !enabled {
			t.Fatalf("monitor alert channel %s enabled = false, want true", channelID)
		}
	}
}

func TestAuditMonitorDeleteAndKeyAttribution(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	ws := env.createWorkspace(t, "owner@example.com")

	keyID := uuid.New()
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO api_keys (id, workspace_id, name, prefix, token_hash, role, created_by)
		 VALUES ($1, $2, 'deploy', 'abcdefghijkl', $3, 'member', $4)`,
		keyID, ws, []byte("hash"), env.ownerID); err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	keyCtx := auth.ContextWithAPIKey(ctx, auth.APIKeyPrincipal{
		ID: keyID, WorkspaceID: ws, Name: "deploy", Prefix: "abcdefghijkl",
		Role: "member", CreatedBy: env.ownerID,
	})

	created, err := env.svc.Create(keyCtx, env.ownerID, ws, validInput(0))
	if err != nil {
		t.Fatalf("create via key: %v", err)
	}
	var createdKey string
	if err := env.pool.QueryRow(ctx,
		`SELECT COALESCE(actor_api_key_id::text, '') FROM security_events WHERE workspace_id=$1 AND kind='monitor_created'`,
		ws).Scan(&createdKey); err != nil {
		t.Fatalf("load monitor_created: %v", err)
	}
	if createdKey != keyID.String() {
		t.Fatalf("monitor_created actor_api_key_id = %q, want %q", createdKey, keyID)
	}

	if err := env.svc.Delete(keyCtx, env.ownerID, ws, created.ID); err != nil {
		t.Fatalf("delete via key: %v", err)
	}
	var kind, detail, deletedKey string
	if err := env.pool.QueryRow(ctx,
		`SELECT kind, detail::text, COALESCE(actor_api_key_id::text, '')
		 FROM security_events WHERE workspace_id=$1 AND kind='monitor_deleted'`,
		ws).Scan(&kind, &detail, &deletedKey); err != nil {
		t.Fatalf("load monitor_deleted: %v", err)
	}
	if kind != "monitor_deleted" || !strings.Contains(detail, created.Name) ||
		!strings.Contains(detail, created.ID.String()) {
		t.Fatalf("monitor_deleted event = %q %s", kind, detail)
	}
	if deletedKey != keyID.String() {
		t.Fatalf("monitor_deleted actor_api_key_id = %q, want %q", deletedKey, keyID)
	}
}

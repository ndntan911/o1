package statuspage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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

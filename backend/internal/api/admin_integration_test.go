package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"o1/internal/db/sqlc"
	"o1/internal/scheduler"
)

func (e *monitorAPIEnv) promoteAdmin(t *testing.T, emailAddr string) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(),
		"UPDATE users SET is_admin = true WHERE email = $1", emailAddr); err != nil {
		t.Fatalf("promote admin: %v", err)
	}
}

func newSchedulerForTest(t *testing.T, e *monitorAPIEnv) *scheduler.Scheduler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client, err := river.NewClient(riverpgxv5.New(e.pool), &river.Config{Logger: logger})
	if err != nil {
		t.Fatalf("new river client: %v", err)
	}
	return scheduler.New(e.pool, sqlc.New(e.pool), client, e.cfg, logger)
}

type adminWorkspaceJSON struct {
	ID              string  `json:"id"`
	SuspendedAt     *string `json:"suspended_at"`
	SuspendedReason *string `json:"suspended_reason"`
	MemberCount     int64   `json:"member_count"`
	MonitorCount    int64   `json:"monitor_count"`
}

func TestAdmin_SuspendStopsProbesAndMutations(t *testing.T) {
	env := newMonitorAPIEnv(t, 10)
	owner := env.login("owner@example.com")
	admin := env.login("admin@example.com")
	env.promoteAdmin(t, "admin@example.com")

	ws := owner.createWorkspace(t, "Abuse")
	body := validMonitorBody(0)
	body["url"] = "https://example.com/health?token=super-secret"
	resp, raw := owner.do(http.MethodPost, "/api/v1/workspaces/"+ws+"/monitors", body)
	requireStatus(t, resp, http.StatusCreated)
	monitor := decodeJSON[monitorJSON](t, raw)

	// Non-admin cannot suspend.
	resp, _ = owner.do(http.MethodPost, "/api/v1/admin/workspaces/"+ws+"/suspend", map[string]string{"reason": "abuse"})
	requireStatus(t, resp, http.StatusForbidden)

	// An invalid reason is rejected with a validation problem.
	resp, raw = admin.do(http.MethodPost, "/api/v1/admin/workspaces/"+ws+"/suspend", map[string]string{"reason": " "})
	requireStatus(t, resp, http.StatusBadRequest)
	if p := decodeJSON[problemJSON](t, raw); p.Code != "validation_error" {
		t.Fatalf("problem = %+v", p)
	}

	resp, raw = admin.do(http.MethodPost, "/api/v1/admin/workspaces/"+ws+"/suspend", map[string]string{"reason": "abuse"})
	requireStatus(t, resp, http.StatusOK)
	suspended := decodeJSON[adminWorkspaceJSON](t, raw)
	if suspended.SuspendedAt == nil || suspended.SuspendedReason == nil || *suspended.SuspendedReason != "abuse" {
		t.Fatalf("suspended workspace = %+v", suspended)
	}

	// Mutations are blocked while reads stay available.
	resp, raw = owner.do(http.MethodPost, "/api/v1/workspaces/"+ws+"/monitors/"+monitor.ID+"/pause", nil)
	requireStatus(t, resp, http.StatusForbidden)
	if p := decodeJSON[problemJSON](t, raw); p.Code != "workspace_suspended" {
		t.Fatalf("pause problem = %+v", p)
	}
	resp, _ = owner.do(http.MethodGet, "/api/v1/workspaces/"+ws+"/monitors", nil)
	requireStatus(t, resp, http.StatusOK)

	// The scheduler skips suspended workspaces.
	if n, err := newSchedulerForTest(t, env).Tick(context.Background()); err != nil || n != 0 {
		t.Fatalf("Tick = %d, %v; want 0, nil", n, err)
	}

	// Admin lists expose suspension fields, counts, and redacted URLs.
	resp, raw = admin.do(http.MethodGet, "/api/v1/admin/workspaces?q=Abuse", nil)
	requireStatus(t, resp, http.StatusOK)
	workspaces := decodeJSON[struct {
		Workspaces []adminWorkspaceJSON `json:"workspaces"`
	}](t, raw)
	if len(workspaces.Workspaces) != 1 || workspaces.Workspaces[0].MemberCount != 1 || workspaces.Workspaces[0].MonitorCount != 1 {
		t.Fatalf("admin workspaces = %+v", workspaces)
	}
	if workspaces.Workspaces[0].SuspendedAt == nil {
		t.Fatalf("admin workspace missing suspension: %+v", workspaces.Workspaces[0])
	}

	resp, raw = admin.do(http.MethodGet, "/api/v1/admin/monitors?workspace_id="+url.QueryEscape(ws), nil)
	requireStatus(t, resp, http.StatusOK)
	monitors := decodeJSON[struct {
		Monitors []struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"monitors"`
	}](t, raw)
	if len(monitors.Monitors) != 1 || monitors.Monitors[0].ID != monitor.ID {
		t.Fatalf("admin monitors = %+v", monitors)
	}
	if monitors.Monitors[0].URL != "https://example.com/health" {
		t.Fatalf("url = %q, want query and fragment stripped", monitors.Monitors[0].URL)
	}

	resp, raw = admin.do(http.MethodGet, "/api/v1/admin/security-events?kind=workspace_suspended", nil)
	requireStatus(t, resp, http.StatusOK)
	events := decodeJSON[struct {
		Events []struct {
			Kind        string  `json:"kind"`
			WorkspaceID *string `json:"workspace_id"`
			ActorUserID *string `json:"actor_user_id"`
		} `json:"events"`
	}](t, raw)
	if len(events.Events) != 1 || events.Events[0].Kind != "workspace_suspended" ||
		events.Events[0].WorkspaceID == nil || events.Events[0].ActorUserID == nil {
		t.Fatalf("events = %+v", events)
	}

	// Unsuspending restores mutations and probing.
	resp, raw = admin.do(http.MethodPost, "/api/v1/admin/workspaces/"+ws+"/unsuspend", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeJSON[adminWorkspaceJSON](t, raw); got.SuspendedAt != nil {
		t.Fatalf("workspace after unsuspend = %+v", got)
	}
	time.Sleep(10 * time.Millisecond)
	if n, err := newSchedulerForTest(t, env).Tick(context.Background()); err != nil || n != 1 {
		t.Fatalf("Tick after unsuspend = %d, %v; want 1, nil", n, err)
	}
	resp, _ = owner.do(http.MethodPost, "/api/v1/workspaces/"+ws+"/monitors/"+monitor.ID+"/pause", nil)
	requireStatus(t, resp, http.StatusOK)
}

func TestAdmin_NonAdminCannotReadAdminLists(t *testing.T) {
	env := newMonitorAPIEnv(t, 10)
	owner := env.login("owner@example.com")
	resp, raw := owner.do(http.MethodGet, "/api/v1/admin/workspaces", nil)
	requireStatus(t, resp, http.StatusForbidden)
	if p := decodeJSON[problemJSON](t, raw); p.Code != "forbidden" {
		t.Fatalf("problem = %+v", p)
	}
}

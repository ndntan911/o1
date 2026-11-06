package incidents_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/api"
	"o1/internal/config"
	"o1/internal/db/sqlc"
	"o1/internal/incidents"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

const incidentHandlerPassword = "correct horse battery staple"

type incidentJSON struct {
	ID             string     `json:"id"`
	WorkspaceID    string     `json:"workspace_id"`
	MonitorID      string     `json:"monitor_id"`
	MonitorName    string     `json:"monitor_name"`
	Status         string     `json:"status"`
	StartedAt      time.Time  `json:"started_at"`
	AcknowledgedAt *time.Time `json:"acknowledged_at"`
	AcknowledgedBy *string    `json:"acknowledged_by"`
	ResolvedAt     *time.Time `json:"resolved_at"`
	LastNotifiedAt *time.Time `json:"last_notified_at"`
	ReminderCount  int        `json:"reminder_count"`
}

type incidentEventJSON struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	ActorUserID *string         `json:"actor_user_id"`
	Detail      json.RawMessage `json:"detail"`
	CreatedAt   time.Time       `json:"created_at"`
}

type incidentDetailJSON struct {
	Incident incidentJSON        `json:"incident"`
	Events   []incidentEventJSON `json:"events"`
}

type incidentListJSON struct {
	Incidents  []incidentJSON `json:"incidents"`
	NextCursor string         `json:"next_cursor"`
}

type incidentDeliveryJSON struct {
	ID        string     `json:"id"`
	Event     string     `json:"event"`
	Sequence  int        `json:"sequence"`
	Recipient string     `json:"recipient"`
	Status    string     `json:"status"`
	Attempts  int        `json:"attempts"`
	LastError *string    `json:"last_error"`
	SentAt    *time.Time `json:"sent_at"`
	CreatedAt time.Time  `json:"created_at"`
}

type incidentDeliveryListJSON struct {
	Deliveries []incidentDeliveryJSON `json:"deliveries"`
}

type incidentProblemJSON struct {
	Code   string            `json:"code"`
	Fields map[string]string `json:"fields"`
}

type incidentAPIEnv struct {
	t      *testing.T
	pool   *pgxpool.Pool
	cfg    config.Config
	wsSvc  *workspaces.Service
	server *httptest.Server
}

func newIncidentAPIEnv(t *testing.T) *incidentAPIEnv {
	t.Helper()
	pool, cleanup := testutil.NewPostgres(t)
	t.Cleanup(cleanup)

	cfg := testutil.TestConfig(t)
	wsSvc := workspaces.NewService(pool, sqlc.New(pool))
	incidentSvc := incidents.NewService(pool, sqlc.New(pool), wsSvc)

	router := api.NewRouter(api.Dependencies{
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Pool:             pool,
		Config:           cfg,
		EmailSender:      testutil.NewFakeSender(),
		WorkspaceService: wsSvc,
		IncidentService:  incidentSvc,
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &incidentAPIEnv{t: t, pool: pool, cfg: cfg, wsSvc: wsSvc, server: server}
}

func (e *incidentAPIEnv) client(emailAddr string) *http.Client {
	e.t.Helper()
	token := testutil.SessionToken(e.t, e.pool, e.cfg, emailAddr, incidentHandlerPassword)
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatalf("create cookie jar: %v", err)
	}
	base, err := url.Parse(e.server.URL)
	if err != nil {
		e.t.Fatalf("parse server URL: %v", err)
	}
	jar.SetCookies(base, []*http.Cookie{{Name: "o1_session", Value: token}})
	return &http.Client{Jar: jar}
}

func (e *incidentAPIEnv) do(c *http.Client, method, path string) (*http.Response, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.server.URL+path, nil)
	if err != nil {
		e.t.Fatalf("create request: %v", err)
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", e.cfg.AppBaseURL)
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("read response body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		e.t.Fatalf("close response body: %v", err)
	}
	return resp, body
}

func requireIncidentStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("%s %s = %d, want %d", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, want)
	}
}

func decodeIncidentJSON[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode JSON %q: %v", string(data), err)
	}
	return v
}

func incidentProblem(t *testing.T, data []byte) incidentProblemJSON {
	t.Helper()
	return decodeIncidentJSON[incidentProblemJSON](t, data)
}

func (e *incidentAPIEnv) createWorkspace(name string) (uuid.UUID, uuid.UUID) {
	e.t.Helper()
	ownerID := testutil.CreateVerifiedUser(e.t, e.pool, name+"-owner@example.com", incidentHandlerPassword)
	ws, err := e.wsSvc.Create(context.Background(), ownerID, name)
	if err != nil {
		e.t.Fatalf("create workspace: %v", err)
	}
	return ws.ID, ownerID
}

func (e *incidentAPIEnv) addMember(workspaceID uuid.UUID, emailAddr, role string) uuid.UUID {
	e.t.Helper()
	userID := testutil.CreateVerifiedUser(e.t, e.pool, emailAddr, incidentHandlerPassword)
	if err := sqlc.New(e.pool).UpsertWorkspaceMember(context.Background(), sqlc.UpsertWorkspaceMemberParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
		Role:        role,
	}); err != nil {
		e.t.Fatalf("add member: %v", err)
	}
	return userID
}

func (e *incidentAPIEnv) insertMonitor(workspaceID uuid.UUID, name string, createdBy uuid.UUID) uuid.UUID {
	e.t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		e.t.Fatalf("new monitor id: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO monitors
			(id, workspace_id, name, url, method, expected_status,
			 interval_seconds, timeout_seconds, confirmation_threshold,
			 recovery_threshold, paused, created_by)
		VALUES ($1, $2, $3, 'https://example.com/health', 'GET', '200-299', 60, 5, 1, 1, false, $4)`,
		id, workspaceID, name, createdBy); err != nil {
		e.t.Fatalf("insert monitor: %v", err)
	}
	return id
}

func (e *incidentAPIEnv) insertIncident(workspaceID, monitorID uuid.UUID, status string, startedAt time.Time, acknowledgedAt, resolvedAt *time.Time, acknowledgedBy *uuid.UUID) uuid.UUID {
	e.t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		e.t.Fatalf("new incident id: %v", err)
	}
	var ackBy pgtype.UUID
	if acknowledgedBy != nil {
		ackBy = pgtype.UUID{Bytes: *acknowledgedBy, Valid: true}
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO incidents
			(id, workspace_id, monitor_id, status, started_at, acknowledged_at,
			 acknowledged_by, resolved_at, last_notified_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $5)`,
		id, workspaceID, monitorID, status, startedAt, acknowledgedAt, ackBy, resolvedAt); err != nil {
		e.t.Fatalf("insert incident: %v", err)
	}
	return id
}

func (e *incidentAPIEnv) insertEvent(incidentID uuid.UUID, kind string, actorID *uuid.UUID, detail string, at time.Time) {
	e.t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		e.t.Fatalf("new event id: %v", err)
	}
	var actor pgtype.UUID
	if actorID != nil {
		actor = pgtype.UUID{Bytes: *actorID, Valid: true}
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO incident_events (id, incident_id, kind, actor_user_id, detail, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, incidentID, kind, actor, []byte(detail), at); err != nil {
		e.t.Fatalf("insert incident event: %v", err)
	}
}

func (e *incidentAPIEnv) insertDelivery(workspaceID, incidentID uuid.UUID) uuid.UUID {
	e.t.Helper()
	channelID, err := uuid.NewV7()
	if err != nil {
		e.t.Fatalf("new channel id: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO alert_channels (id, workspace_id, kind, name, config)
		VALUES ($1, $2, 'email', 'Email', '{"recipients":["ops@example.com"]}')`,
		channelID, workspaceID); err != nil {
		e.t.Fatalf("insert alert channel: %v", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		e.t.Fatalf("new delivery id: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO alert_deliveries
			(id, incident_id, channel_id, event, sequence, recipient, status, attempts, sent_at)
		VALUES ($1, $2, $3, 'opened', 0, 'ops@example.com', 'sent', 1, now())`,
		id, incidentID, channelID); err != nil {
		e.t.Fatalf("insert alert delivery: %v", err)
	}
	return id
}

func incidentPath(workspaceID uuid.UUID, suffix string) string {
	return "/api/v1/workspaces/" + workspaceID.String() + "/incidents" + suffix
}

func TestIncidentHandlers(t *testing.T) {
	env := newIncidentAPIEnv(t)
	ctx := context.Background()

	wsID, _ := env.createWorkspace("Incidents")
	_ = env.addMember(wsID, "viewer@example.com", "viewer")
	memberID := env.addMember(wsID, "member@example.com", "member")
	_ = env.addMember(wsID, "admin@example.com", "admin")

	monitorA := env.insertMonitor(wsID, "API A", memberID)
	monitorB := env.insertMonitor(wsID, "API B", memberID)
	monitorC := env.insertMonitor(wsID, "API C", memberID)

	base := time.Now().UTC().Truncate(time.Millisecond)
	resolvedAt := base.Add(-4 * time.Hour)
	acknowledgedAt := base.Add(-2 * time.Hour)
	resolvedIncident := env.insertIncident(wsID, monitorB, "resolved", base.Add(-5*time.Hour), nil, &resolvedAt, nil)
	ackIncident := env.insertIncident(wsID, monitorA, "acknowledged", base.Add(-3*time.Hour), &acknowledgedAt, nil, &memberID)
	openIncident := env.insertIncident(wsID, monitorC, "open", base.Add(-1*time.Hour), nil, nil, nil)

	env.insertEvent(resolvedIncident, "opened", nil, `{"previous_status":"unknown","new_status":"down"}`, base.Add(-5*time.Hour))
	env.insertEvent(resolvedIncident, "resolved", nil, `{"previous_status":"down","new_status":"up"}`, resolvedAt)
	env.insertEvent(ackIncident, "opened", nil, `{"previous_status":"unknown","new_status":"down"}`, base.Add(-3*time.Hour))
	env.insertEvent(ackIncident, "acknowledged", &memberID, `{"previous_status":"open","new_status":"acknowledged"}`, acknowledgedAt)
	env.insertEvent(openIncident, "opened", nil, `{}`, base.Add(-1*time.Hour))

	deliveryID := env.insertDelivery(wsID, ackIncident)

	otherWS, otherOwnerID := env.createWorkspace("Other")
	otherMonitor := env.insertMonitor(otherWS, "Other monitor", otherOwnerID)
	otherIncident := env.insertIncident(otherWS, otherMonitor, "open", base, nil, nil, nil)

	viewer := env.client("viewer@example.com")
	member := env.client("member@example.com")
	admin := env.client("admin@example.com")

	t.Run("list defaults to active and filters", func(t *testing.T) {
		resp, body := env.do(viewer, http.MethodGet, incidentPath(wsID, ""))
		requireIncidentStatus(t, resp, http.StatusOK)
		list := decodeIncidentJSON[incidentListJSON](t, body)
		if len(list.Incidents) != 2 {
			t.Fatalf("active incidents = %+v, want 2", list.Incidents)
		}
		if list.Incidents[0].ID != openIncident.String() || list.Incidents[1].ID != ackIncident.String() {
			t.Fatalf("active order = %+v, want open then acknowledged", list.Incidents)
		}
		if list.Incidents[0].MonitorName != "API C" {
			t.Fatalf("monitor_name = %q, want API C", list.Incidents[0].MonitorName)
		}

		resp, body = env.do(viewer, http.MethodGet, incidentPath(wsID, "?status=resolved"))
		requireIncidentStatus(t, resp, http.StatusOK)
		resolved := decodeIncidentJSON[incidentListJSON](t, body)
		if len(resolved.Incidents) != 1 || resolved.Incidents[0].ID != resolvedIncident.String() {
			t.Fatalf("resolved incidents = %+v", resolved.Incidents)
		}

		resp, body = env.do(viewer, http.MethodGet, incidentPath(wsID, "?status=all"))
		requireIncidentStatus(t, resp, http.StatusOK)
		all := decodeIncidentJSON[incidentListJSON](t, body)
		if len(all.Incidents) != 3 {
			t.Fatalf("all incidents = %+v, want 3", all.Incidents)
		}

		resp, body = env.do(viewer, http.MethodGet, incidentPath(wsID, "?status=all&monitor_id="+monitorB.String()))
		requireIncidentStatus(t, resp, http.StatusOK)
		filtered := decodeIncidentJSON[incidentListJSON](t, body)
		if len(filtered.Incidents) != 1 || filtered.Incidents[0].ID != resolvedIncident.String() {
			t.Fatalf("monitor filter = %+v", filtered.Incidents)
		}

		resp, body = env.do(viewer, http.MethodGet, incidentPath(wsID, "?status=all&limit=2"))
		requireIncidentStatus(t, resp, http.StatusOK)
		first := decodeIncidentJSON[incidentListJSON](t, body)
		if len(first.Incidents) != 2 || first.NextCursor == "" {
			t.Fatalf("first page = %+v, want two rows and a cursor", first)
		}
		resp, body = env.do(viewer, http.MethodGet, incidentPath(wsID, "?status=all&limit=2&cursor="+url.QueryEscape(first.NextCursor)))
		requireIncidentStatus(t, resp, http.StatusOK)
		second := decodeIncidentJSON[incidentListJSON](t, body)
		if len(second.Incidents) != 1 {
			t.Fatalf("second page = %+v, want one row", second)
		}
		if second.Incidents[0].ID == first.Incidents[0].ID || second.Incidents[0].ID == first.Incidents[1].ID {
			t.Fatalf("pages overlap: %+v then %+v", first.Incidents, second.Incidents)
		}
	})

	t.Run("get returns ordered events and hides other workspaces", func(t *testing.T) {
		resp, body := env.do(viewer, http.MethodGet, incidentPath(wsID, "/"+ackIncident.String()))
		requireIncidentStatus(t, resp, http.StatusOK)
		detail := decodeIncidentJSON[incidentDetailJSON](t, body)
		if detail.Incident.ID != ackIncident.String() || detail.Incident.Status != "acknowledged" {
			t.Fatalf("incident = %+v", detail.Incident)
		}
		if detail.Incident.AcknowledgedAt == nil || detail.Incident.AcknowledgedBy == nil || *detail.Incident.AcknowledgedBy != memberID.String() {
			t.Fatalf("acknowledgement fields = %+v", detail.Incident)
		}
		if len(detail.Events) != 2 {
			t.Fatalf("events = %+v, want 2", detail.Events)
		}
		if detail.Events[0].Kind != "opened" || detail.Events[1].Kind != "acknowledged" {
			t.Fatalf("event order = %+v", detail.Events)
		}
		if detail.Events[1].ActorUserID == nil || *detail.Events[1].ActorUserID != memberID.String() {
			t.Fatalf("acknowledged actor = %+v", detail.Events[1].ActorUserID)
		}

		resp, _ = env.do(viewer, http.MethodGet, incidentPath(wsID, "/"+otherIncident.String()))
		requireIncidentStatus(t, resp, http.StatusNotFound)
	})

	t.Run("acknowledge enforces roles suspension and state", func(t *testing.T) {
		resp, body := env.do(viewer, http.MethodPost, incidentPath(wsID, "/"+openIncident.String()+"/acknowledge"))
		requireIncidentStatus(t, resp, http.StatusForbidden)
		if got := incidentProblem(t, body); got.Code != "forbidden" {
			t.Fatalf("viewer problem = %+v", got)
		}

		if _, err := env.pool.Exec(ctx, `UPDATE workspaces SET suspended_at = now() WHERE id = $1`, wsID); err != nil {
			t.Fatalf("suspend workspace: %v", err)
		}
		resp, body = env.do(member, http.MethodPost, incidentPath(wsID, "/"+openIncident.String()+"/acknowledge"))
		requireIncidentStatus(t, resp, http.StatusForbidden)
		if got := incidentProblem(t, body); got.Code != "workspace_suspended" {
			t.Fatalf("suspended problem = %+v", got)
		}
		if _, err := env.pool.Exec(ctx, `UPDATE workspaces SET suspended_at = NULL WHERE id = $1`, wsID); err != nil {
			t.Fatalf("unsuspend workspace: %v", err)
		}

		resp, _ = env.do(member, http.MethodPost, incidentPath(wsID, "/"+openIncident.String()+"/acknowledge"))
		requireIncidentStatus(t, resp, http.StatusNoContent)

		resp, body = env.do(viewer, http.MethodGet, incidentPath(wsID, "/"+openIncident.String()))
		requireIncidentStatus(t, resp, http.StatusOK)
		detail := decodeIncidentJSON[incidentDetailJSON](t, body)
		if detail.Incident.Status != "acknowledged" {
			t.Fatalf("incident status = %q, want acknowledged", detail.Incident.Status)
		}
		if detail.Incident.AcknowledgedBy == nil || *detail.Incident.AcknowledgedBy != memberID.String() {
			t.Fatalf("acknowledged_by = %+v", detail.Incident.AcknowledgedBy)
		}
		if len(detail.Events) != 2 || detail.Events[1].Kind != "acknowledged" {
			t.Fatalf("events after ack = %+v", detail.Events)
		}

		resp, _ = env.do(member, http.MethodPost, incidentPath(wsID, "/"+openIncident.String()+"/acknowledge"))
		requireIncidentStatus(t, resp, http.StatusNoContent)
		resp, body = env.do(viewer, http.MethodGet, incidentPath(wsID, "/"+openIncident.String()))
		requireIncidentStatus(t, resp, http.StatusOK)
		again := decodeIncidentJSON[incidentDetailJSON](t, body)
		if len(again.Events) != 2 {
			t.Fatalf("events after second ack = %+v, want 2", again.Events)
		}

		resp, body = env.do(member, http.MethodPost, incidentPath(wsID, "/"+resolvedIncident.String()+"/acknowledge"))
		requireIncidentStatus(t, resp, http.StatusConflict)
		if got := incidentProblem(t, body); got.Code != "incident_resolved" {
			t.Fatalf("resolved problem = %+v", got)
		}

		resp, _ = env.do(member, http.MethodPost, incidentPath(wsID, "/"+otherIncident.String()+"/acknowledge"))
		requireIncidentStatus(t, resp, http.StatusNotFound)
	})

	t.Run("deliveries are admin only and tenant scoped", func(t *testing.T) {
		resp, body := env.do(admin, http.MethodGet, incidentPath(wsID, "/"+ackIncident.String()+"/deliveries"))
		requireIncidentStatus(t, resp, http.StatusOK)
		list := decodeIncidentJSON[incidentDeliveryListJSON](t, body)
		if len(list.Deliveries) != 1 {
			t.Fatalf("deliveries = %+v, want 1", list.Deliveries)
		}
		delivery := list.Deliveries[0]
		if delivery.ID != deliveryID.String() || delivery.Event != "opened" || delivery.Recipient != "ops@example.com" ||
			delivery.Status != "sent" || delivery.Attempts != 1 || delivery.SentAt == nil {
			t.Fatalf("delivery = %+v", delivery)
		}

		resp, body = env.do(member, http.MethodGet, incidentPath(wsID, "/"+ackIncident.String()+"/deliveries"))
		requireIncidentStatus(t, resp, http.StatusForbidden)
		if got := incidentProblem(t, body); got.Code != "forbidden" {
			t.Fatalf("member problem = %+v", got)
		}

		resp, _ = env.do(admin, http.MethodGet, incidentPath(wsID, "/"+otherIncident.String()+"/deliveries"))
		requireIncidentStatus(t, resp, http.StatusNotFound)
	})
}

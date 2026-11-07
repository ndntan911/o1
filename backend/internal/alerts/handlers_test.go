package alerts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/alerts"
	"o1/internal/api"
	"o1/internal/config"
	"o1/internal/db/sqlc"
	"o1/internal/incidents"
	"o1/internal/probe"
	"o1/internal/secretbox"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

const alertsHandlerPassword = "correct horse battery staple"

type staticResolver struct{}

func (staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
}

type fakeNotifier struct {
	mu      sync.Mutex
	err     error
	targets []alerts.Target
	events  []alerts.Event
}

func (n *fakeNotifier) Send(_ context.Context, target alerts.Target, event alerts.Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.err != nil {
		return n.err
	}
	n.targets = append(n.targets, target)
	n.events = append(n.events, event)
	return nil
}

type problemJSON struct {
	Type   string            `json:"type"`
	Title  string            `json:"title"`
	Status int               `json:"status"`
	Code   string            `json:"code"`
	Detail string            `json:"detail"`
	Fields map[string]string `json:"fields"`
}

type alertsAPIEnv struct {
	t      *testing.T
	pool   *pgxpool.Pool
	cfg    config.Config
	wsSvc  *workspaces.Service
	svc    *alerts.Service
	server *httptest.Server
	notify *fakeNotifier
	sender *testutil.FakeSender
	wsID   uuid.UUID
	owner  uuid.UUID
	admin  uuid.UUID
	member uuid.UUID
	viewer uuid.UUID
}

func newAlertsAPIEnv(t *testing.T) *alertsAPIEnv {
	t.Helper()
	pool, cleanup := testutil.NewPostgres(t)
	t.Cleanup(cleanup)

	cfg := testutil.TestConfig(t)
	wsSvc := workspaces.NewService(pool, sqlc.New(pool))
	box, err := secretbox.New(cfg.EncryptionKey)
	if err != nil {
		t.Fatalf("new secretbox: %v", err)
	}
	guard := probe.NewGuardWithResolver(probe.Policy{
		AllowedPorts:         map[int]struct{}{80: {}, 443: {}},
		AllowPrivateNetworks: true,
	}, staticResolver{})
	notify := &fakeNotifier{}
	sender := testutil.NewFakeSender()
	svc := alerts.NewService(pool, sqlc.New(pool), wsSvc, box, guard, notify, sender, cfg.AppBaseURL)

	owner := testutil.CreateVerifiedUser(t, pool, "alerts-api-owner@example.com", alertsHandlerPassword)
	ws, err := wsSvc.Create(context.Background(), owner, "Alerts API")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	env := &alertsAPIEnv{
		t: t, pool: pool, cfg: cfg, wsSvc: wsSvc, svc: svc, notify: notify, sender: sender,
		wsID: ws.ID, owner: owner,
	}
	env.admin = env.addMember("alerts-api-admin@example.com", "admin")
	env.member = env.addMember("alerts-api-member@example.com", "member")
	env.viewer = env.addMember("alerts-api-viewer@example.com", "viewer")

	router := api.NewRouter(api.Dependencies{
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Pool:             pool,
		Config:           cfg,
		EmailSender:      testutil.NewFakeSender(),
		WorkspaceService: wsSvc,
		IncidentService:  incidents.NewService(pool, sqlc.New(pool), wsSvc),
		AlertService:     svc,
	})
	env.server = httptest.NewServer(router)
	t.Cleanup(env.server.Close)
	return env
}

func (e *alertsAPIEnv) addMember(emailAddr, role string) uuid.UUID {
	e.t.Helper()
	id := testutil.CreateVerifiedUser(e.t, e.pool, emailAddr, alertsHandlerPassword)
	if err := sqlc.New(e.pool).UpsertWorkspaceMember(context.Background(), sqlc.UpsertWorkspaceMemberParams{
		WorkspaceID: e.wsID, UserID: id, Role: role,
	}); err != nil {
		e.t.Fatalf("add member: %v", err)
	}
	return id
}

func (e *alertsAPIEnv) client(emailAddr string) *http.Client {
	e.t.Helper()
	token := testutil.SessionToken(e.t, e.pool, e.cfg, emailAddr, alertsHandlerPassword)
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatalf("cookie jar: %v", err)
	}
	base, err := url.Parse(e.server.URL)
	if err != nil {
		e.t.Fatalf("parse server url: %v", err)
	}
	jar.SetCookies(base, []*http.Cookie{{Name: "o1_session", Value: token}})
	return &http.Client{Jar: jar}
}

func (e *alertsAPIEnv) do(c *http.Client, method, path string, body any) (*http.Response, []byte) {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		e.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatalf("do request: %v", err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		e.t.Fatalf("read body: %v", err)
	}
	return resp, data
}

func (e *alertsAPIEnv) channelsPath(suffix string) string {
	return "/api/v1/workspaces/" + e.wsID.String() + "/alert-channels" + suffix
}

func decodeJSON[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode JSON %q: %v", string(data), err)
	}
	return v
}

func decodeProblem(t *testing.T, data []byte) problemJSON {
	t.Helper()
	return decodeJSON[problemJSON](t, data)
}

func (e *alertsAPIEnv) createWebhook(c *http.Client, name string) alerts.Channel {
	e.t.Helper()
	resp, body := e.do(c, http.MethodPost, e.channelsPath(""), map[string]any{
		"kind": "webhook", "name": name, "url": "https://example.com/o1",
	})
	if resp.StatusCode != http.StatusCreated {
		e.t.Fatalf("create webhook = %d: %s", resp.StatusCode, body)
	}
	return decodeJSON[alerts.Channel](e.t, body)
}

func TestAlertChannelHandlers_CRUD(t *testing.T) {
	env := newAlertsAPIEnv(t)
	admin := env.client("alerts-api-owner@example.com")

	resp, body := env.do(admin, http.MethodPost, env.channelsPath(""), map[string]any{
		"kind": "webhook", "name": "Deploy hook", "url": "https://example.com/o1",
		"signing_secret": "0123456789abcdef",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST = %d: %s", resp.StatusCode, body)
	}
	created := decodeJSON[alerts.Channel](t, body)
	if created.ID == uuid.Nil || created.Name != "Deploy hook" || created.URLPreview != "https://example.com/…" {
		t.Fatalf("created = %+v", created)
	}
	if !created.SigningSecretSet {
		t.Fatal("signing_secret_set = false")
	}

	resp, body = env.do(admin, http.MethodGet, env.channelsPath(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d: %s", resp.StatusCode, body)
	}
	list := decodeJSON[struct {
		Channels []alerts.Channel `json:"channels"`
	}](t, body)
	if len(list.Channels) != 1 || list.Channels[0].ID != created.ID {
		t.Fatalf("channels = %+v", list.Channels)
	}

	name := "Deploy hook renamed"
	resp, body = env.do(admin, http.MethodPatch, env.channelsPath("/"+created.ID.String()), map[string]any{
		"name": name,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", resp.StatusCode, body)
	}
	updated := decodeJSON[alerts.Channel](t, body)
	if updated.Name != name {
		t.Fatalf("updated name = %q", updated.Name)
	}

	resp, body = env.do(admin, http.MethodDelete, env.channelsPath("/"+created.ID.String()), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", resp.StatusCode, body)
	}

	member := env.client("alerts-api-member@example.com")
	if resp, body := env.do(member, http.MethodPost, env.channelsPath(""), map[string]any{
		"kind": "webhook", "name": "Member hook", "url": "https://example.com/o1",
	}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member POST = %d: %s", resp.StatusCode, body)
	}
	viewer := env.client("alerts-api-viewer@example.com")
	if resp, body := env.do(viewer, http.MethodGet, env.channelsPath(""), nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer GET = %d: %s", resp.StatusCode, body)
	}
}

func TestAlertChannelHandlers_SecretsNeverReturned(t *testing.T) {
	env := newAlertsAPIEnv(t)
	admin := env.client("alerts-api-owner@example.com")

	const (
		webhookURL    = "https://example.com/hook?token=supersecret"
		signingSecret = "signingsecret0123"
	)
	resp, body := env.do(admin, http.MethodPost, env.channelsPath(""), map[string]any{
		"kind": "webhook", "name": "Secret hook", "url": webhookURL, "signing_secret": signingSecret,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST = %d: %s", resp.StatusCode, body)
	}
	for _, leak := range []string{"supersecret", signingSecret, "hook?token"} {
		if strings.Contains(string(body), leak) {
			t.Fatalf("response leaks %q: %s", leak, body)
		}
	}

	resp, body = env.do(admin, http.MethodPost, env.channelsPath(""), map[string]any{
		"kind": "telegram", "name": "Bad Telegram", "bot_token": "not-a-token", "chat_id": "1",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad telegram = %d: %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "not-a-token") {
		t.Fatalf("problem leaks token: %s", body)
	}
}

func TestAlertChannelHandlers_EmailIsSpecial(t *testing.T) {
	env := newAlertsAPIEnv(t)
	admin := env.client("alerts-api-owner@example.com")

	resp, body := env.do(admin, http.MethodPut, env.channelsPath("/email"), map[string]any{
		"recipients": []string{"ops@example.com"}, "enabled": true,
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT email = %d: %s", resp.StatusCode, body)
	}
	resp, body = env.do(admin, http.MethodGet, env.channelsPath(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d: %s", resp.StatusCode, body)
	}
	list := decodeJSON[struct {
		Channels []alerts.Channel `json:"channels"`
	}](t, body)
	var email alerts.Channel
	for _, channel := range list.Channels {
		if channel.Kind == alerts.KindEmail {
			email = channel
		}
	}
	if email.ID == uuid.Nil {
		t.Fatalf("email channel missing: %+v", list.Channels)
	}

	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		resp, body := env.do(admin, method, env.channelsPath("/"+email.ID.String()), map[string]any{"name": "Nope"})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%s email = %d: %s", method, resp.StatusCode, body)
		}
		problem := decodeProblem(t, body)
		if problem.Code != "email_channel_managed_elsewhere" {
			t.Fatalf("%s email code = %q", method, problem.Code)
		}
	}
}

func TestAlertChannelHandlers_ValidationAndConflict(t *testing.T) {
	env := newAlertsAPIEnv(t)
	admin := env.client("alerts-api-owner@example.com")

	resp, body := env.do(admin, http.MethodPost, env.channelsPath(""), map[string]any{
		"kind": "email", "name": "Email",
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("email kind = %d: %s", resp.StatusCode, body)
	}
	if problem := decodeProblem(t, body); problem.Code != "email_channel_managed_elsewhere" {
		t.Fatalf("email kind code = %q", problem.Code)
	}

	resp, body = env.do(admin, http.MethodPost, env.channelsPath(""), map[string]any{
		"kind": "pagerduty", "name": "Pager",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown kind = %d: %s", resp.StatusCode, body)
	}
	if problem := decodeProblem(t, body); problem.Fields["kind"] == "" {
		t.Fatalf("unknown kind fields = %+v", problem.Fields)
	}

	resp, body = env.do(admin, http.MethodPost, env.channelsPath(""), map[string]any{
		"kind": "webhook", "name": "Hook", "url": "not a url",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad url = %d: %s", resp.StatusCode, body)
	}
	if problem := decodeProblem(t, body); problem.Fields["url"] == "" {
		t.Fatalf("bad url fields = %+v", problem.Fields)
	}

	env.createWebhook(admin, "Duplicate")
	resp, body = env.do(admin, http.MethodPost, env.channelsPath(""), map[string]any{
		"kind": "webhook", "name": "Duplicate", "url": "https://example.com/other",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate = %d: %s", resp.StatusCode, body)
	}
	if problem := decodeProblem(t, body); problem.Code != "channel_name_taken" {
		t.Fatalf("duplicate code = %q", problem.Code)
	}

	for i := 0; i < 9; i++ {
		env.createWebhook(admin, "Filler "+string(rune('A'+i)))
	}
	resp, body = env.do(admin, http.MethodPost, env.channelsPath(""), map[string]any{
		"kind": "webhook", "name": "Overflow", "url": "https://example.com/o1",
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("limit = %d: %s", resp.StatusCode, body)
	}
	if problem := decodeProblem(t, body); problem.Code != "channel_limit" {
		t.Fatalf("limit code = %q", problem.Code)
	}
}

func TestAlertChannelHandlers_CrossWorkspace(t *testing.T) {
	env := newAlertsAPIEnv(t)
	admin := env.client("alerts-api-owner@example.com")

	otherOwner := testutil.CreateVerifiedUser(t, env.pool, "alerts-api-other@example.com", alertsHandlerPassword)
	other, err := env.wsSvc.Create(context.Background(), otherOwner, "Other")
	if err != nil {
		t.Fatalf("create other workspace: %v", err)
	}
	foreign, err := env.svc.Create(context.Background(), otherOwner, other.ID, alerts.CreateInput{
		Kind: alerts.KindWebhook, Name: "Foreign", URL: "https://example.com/o1",
	})
	if err != nil {
		t.Fatalf("create foreign channel: %v", err)
	}

	resp, body := env.do(admin, http.MethodPatch, env.channelsPath("/"+foreign.ID.String()), map[string]any{
		"name": "Renamed",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-workspace PATCH = %d: %s", resp.StatusCode, body)
	}
}

func (e *alertsAPIEnv) insertMonitor(name string) uuid.UUID {
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
		id, e.wsID, name, e.owner); err != nil {
		e.t.Fatalf("insert monitor: %v", err)
	}
	return id
}

func (e *alertsAPIEnv) monitorChannelsPath(monitorID uuid.UUID, suffix string) string {
	return "/api/v1/workspaces/" + e.wsID.String() + "/monitors/" + monitorID.String() + "/alert-channels" + suffix
}

func (e *alertsAPIEnv) testChannelPath(channelID uuid.UUID) string {
	return "/api/v1/workspaces/" + e.wsID.String() + "/alert-channels/" + channelID.String() + "/test"
}

func TestAlertChannelHandlers_MonitorChannels(t *testing.T) {
	env := newAlertsAPIEnv(t)
	admin := env.client("alerts-api-owner@example.com")
	monitorID := env.insertMonitor("API")
	channel := env.createWebhook(admin, "Deploy hook")

	member := env.client("alerts-api-member@example.com")
	resp, body := env.do(member, http.MethodGet, env.monitorChannelsPath(monitorID, ""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("member GET = %d: %s", resp.StatusCode, body)
	}
	list := decodeJSON[struct {
		Channels []alerts.MonitorChannel `json:"channels"`
	}](t, body)
	if len(list.Channels) != 1 || list.Channels[0].ID != channel.ID || !list.Channels[0].Enabled || !list.Channels[0].ChannelEnabled {
		t.Fatalf("monitor channels = %+v", list.Channels)
	}

	viewer := env.client("alerts-api-viewer@example.com")
	if resp, body := env.do(viewer, http.MethodGet, env.monitorChannelsPath(monitorID, ""), nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer GET = %d: %s", resp.StatusCode, body)
	}

	if resp, body := env.do(member, http.MethodGet, env.monitorChannelsPath(uuid.New(), ""), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown monitor GET = %d: %s", resp.StatusCode, body)
	}

	resp, body = env.do(member, http.MethodPatch, env.monitorChannelsPath(monitorID, "/"+channel.ID.String()), map[string]any{
		"enabled": false,
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PATCH = %d: %s", resp.StatusCode, body)
	}
	resp, body = env.do(member, http.MethodGet, env.monitorChannelsPath(monitorID, ""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after toggle = %d: %s", resp.StatusCode, body)
	}
	list = decodeJSON[struct {
		Channels []alerts.MonitorChannel `json:"channels"`
	}](t, body)
	if list.Channels[0].Enabled {
		t.Fatal("switch still enabled after toggle")
	}

	if resp, body := env.do(member, http.MethodPatch, env.monitorChannelsPath(monitorID, "/"+uuid.New().String()), map[string]any{
		"enabled": true,
	}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown channel PATCH = %d: %s", resp.StatusCode, body)
	}
}

func TestAlertChannelHandlers_TestSend(t *testing.T) {
	env := newAlertsAPIEnv(t)
	admin := env.client("alerts-api-owner@example.com")
	channel := env.createWebhook(admin, "Deploy hook")

	resp, body := env.do(admin, http.MethodPost, env.testChannelPath(channel.ID), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("test send = %d: %s", resp.StatusCode, body)
	}
	if len(env.notify.events) != 1 || env.notify.events[0].Kind != "test" {
		t.Fatalf("notifier events = %+v", env.notify.events)
	}

	member := env.client("alerts-api-member@example.com")
	if resp, body := env.do(member, http.MethodPost, env.testChannelPath(channel.ID), nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member test send = %d: %s", resp.StatusCode, body)
	}
	if resp, body := env.do(admin, http.MethodPost, env.testChannelPath(uuid.New()), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown channel test send = %d: %s", resp.StatusCode, body)
	}

	env.notify.err = &alerts.DeliveryError{Message: "webhook returned HTTP 500"}
	resp, body = env.do(admin, http.MethodPost, env.testChannelPath(channel.ID), nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("failed test send = %d: %s", resp.StatusCode, body)
	}
	problem := decodeProblem(t, body)
	if problem.Code != "test_send_failed" {
		t.Fatalf("failed test send code = %q", problem.Code)
	}
	if !strings.Contains(problem.Detail, "webhook returned HTTP 500") {
		t.Fatalf("detail = %q", problem.Detail)
	}
	if strings.Contains(problem.Detail, "example.com") || strings.Contains(problem.Detail, "signingsecret") {
		t.Fatalf("detail leaks destination: %q", problem.Detail)
	}
}

func TestAlertChannelHandlers_TestEmailSendsAllRecipients(t *testing.T) {
	env := newAlertsAPIEnv(t)
	admin := env.client("alerts-api-owner@example.com")

	resp, body := env.do(admin, http.MethodPut, env.channelsPath("/email"), map[string]any{
		"recipients": []string{"ops@example.com", "oncall@example.com"}, "enabled": true,
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT email = %d: %s", resp.StatusCode, body)
	}
	resp, body = env.do(admin, http.MethodGet, env.channelsPath(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d: %s", resp.StatusCode, body)
	}
	list := decodeJSON[struct {
		Channels []alerts.Channel `json:"channels"`
	}](t, body)
	var email alerts.Channel
	for _, channel := range list.Channels {
		if channel.Kind == alerts.KindEmail {
			email = channel
		}
	}
	if email.ID == uuid.Nil {
		t.Fatal("email channel missing")
	}

	resp, body = env.do(admin, http.MethodPost, env.testChannelPath(email.ID), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("email test send = %d: %s", resp.StatusCode, body)
	}
	if got := len(env.sender.Messages()); got != 2 {
		t.Fatalf("messages = %d, want 2", got)
	}
}

func TestAlertChannelHandlers_Suspended(t *testing.T) {
	env := newAlertsAPIEnv(t)
	admin := env.client("alerts-api-owner@example.com")
	channel := env.createWebhook(admin, "Suspended hook")

	if _, err := env.pool.Exec(context.Background(), `UPDATE workspaces SET suspended_at = now() WHERE id = $1`, env.wsID); err != nil {
		t.Fatalf("suspend workspace: %v", err)
	}

	resp, body := env.do(admin, http.MethodPost, env.channelsPath(""), map[string]any{
		"kind": "webhook", "name": "Another", "url": "https://example.com/o1",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("suspended POST = %d: %s", resp.StatusCode, body)
	}
	if problem := decodeProblem(t, body); problem.Code != "workspace_suspended" {
		t.Fatalf("suspended code = %q", problem.Code)
	}

	resp, body = env.do(admin, http.MethodPatch, env.channelsPath("/"+channel.ID.String()), map[string]any{
		"name": "Nope",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("suspended PATCH = %d: %s", resp.StatusCode, body)
	}
}

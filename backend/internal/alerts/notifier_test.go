package alerts

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"o1/internal/probe"
)

// permissiveGuard returns a guard that allows the private host and port used by
// an httptest server.
func permissiveGuard(t *testing.T, rawURL string) *probe.Guard {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	return probe.NewGuard(probe.Policy{
		AllowedPorts:         map[int]struct{}{port: {}},
		MaxRedirects:         3,
		MaxResponseBody:      1 << 20,
		AllowPrivateNetworks: true,
	})
}

func testEvent(kind string) Event {
	started := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	return Event{
		Kind:          kind,
		Sequence:      0,
		WorkspaceName: "Acme",
		WorkspaceSlug: "acme",
		MonitorID:     uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		MonitorName:   "API",
		MonitorURL:    "https://example.com/…",
		IncidentID:    uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		StartedAt:     started,
		Link:          "https://app.example.com/w/acme/incidents/2222",
	}
}

func TestNotifier_WebhookPayloadAndSignature(t *testing.T) {
	secret := "0123456789abcdef"
	var gotBody []byte
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(permissiveGuard(t, srv.URL), "")
	err := n.Send(context.Background(), Target{
		Kind: KindWebhook, URL: srv.URL, SigningSecret: secret,
	}, testEvent("opened"))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["event"] != "opened" {
		t.Fatalf("event = %v", payload["event"])
	}
	monitor := payload["monitor"].(map[string]any)
	if monitor["status"] != "down" {
		t.Fatalf("monitor.status = %v", monitor["status"])
	}
	incident := payload["incident"].(map[string]any)
	if incident["status"] != "open" {
		t.Fatalf("incident.status = %v", incident["status"])
	}
	if gotHeaders.Get("X-O1-Event") != "opened" {
		t.Fatalf("X-O1-Event = %q", gotHeaders.Get("X-O1-Event"))
	}
	if gotHeaders.Get("User-Agent") != "o1-alerts/1.0" {
		t.Fatalf("User-Agent = %q", gotHeaders.Get("User-Agent"))
	}
	if gotHeaders.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q", gotHeaders.Get("Content-Type"))
	}

	timestamp := gotHeaders.Get("X-O1-Timestamp")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + string(gotBody)))
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if gotHeaders.Get("X-O1-Signature") != want {
		t.Fatalf("signature = %q, want %q", gotHeaders.Get("X-O1-Signature"), want)
	}
}

func TestNotifier_WebhookTimestampIsUnixSeconds(t *testing.T) {
	fixed := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	var gotBody []byte
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(permissiveGuard(t, srv.URL), "")
	n.now = func() time.Time { return fixed }
	const secret = "0123456789abcdef"
	if err := n.Send(context.Background(), Target{
		Kind: KindWebhook, URL: srv.URL, SigningSecret: secret,
	}, testEvent("opened")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	timestamp := gotHeaders.Get("X-O1-Timestamp")
	if timestamp != strconv.FormatInt(fixed.Unix(), 10) {
		t.Fatalf("X-O1-Timestamp = %q, want unix seconds %d", timestamp, fixed.Unix())
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + string(gotBody)))
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if gotHeaders.Get("X-O1-Signature") != want {
		t.Fatalf("signature = %q, want %q", gotHeaders.Get("X-O1-Signature"), want)
	}
}

func TestNotifier_WebhookWithoutSecretHasNoSignature(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	n := NewNotifier(permissiveGuard(t, srv.URL), "")
	if err := n.Send(context.Background(), Target{Kind: KindWebhook, URL: srv.URL}, testEvent("opened")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotHeaders.Get("X-O1-Timestamp") != "" || gotHeaders.Get("X-O1-Signature") != "" {
		t.Fatalf("unsigned webhook carried signature headers: %v", gotHeaders)
	}
}

func TestNotifier_ResolvedPayload(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	event := testEvent("resolved")
	resolved := time.Date(2026, 11, 6, 10, 5, 0, 0, time.UTC)
	event.ResolvedAt = &resolved
	n := NewNotifier(permissiveGuard(t, srv.URL), "")
	if err := n.Send(context.Background(), Target{Kind: KindWebhook, URL: srv.URL}, event); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatal(err)
	}
	monitor := payload["monitor"].(map[string]any)
	if monitor["status"] != "up" {
		t.Fatalf("monitor.status = %v", monitor["status"])
	}
	incident := payload["incident"].(map[string]any)
	if incident["status"] != "resolved" {
		t.Fatalf("incident.status = %v", incident["status"])
	}
	if incident["resolved_at"] == nil {
		t.Fatal("resolved_at = nil, want a timestamp")
	}
}

func TestNotifier_TestPayload(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(permissiveGuard(t, srv.URL), "")
	if err := n.Send(context.Background(), Target{Kind: KindWebhook, URL: srv.URL}, testEvent("test")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["event"] != "test" {
		t.Fatalf("event = %v", payload["event"])
	}
	if payload["monitor"] != nil {
		t.Fatalf("monitor = %v, want null", payload["monitor"])
	}
	if payload["incident"] != nil {
		t.Fatalf("incident = %v, want null", payload["incident"])
	}
}

func TestNotifier_SlackPayload(t *testing.T) {
	event := testEvent("opened")
	body := slackPayload(event)
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal slack payload: %v", err)
	}
	for _, want := range []string{"Acme", "API", event.MonitorURL, event.Link, "🔴"} {
		if !strings.Contains(payload.Text, want) {
			t.Fatalf("slack text %q missing %q", payload.Text, want)
		}
	}

	resolved := slackPayload(testEvent("resolved"))
	var resolvedPayload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(resolved, &resolvedPayload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resolvedPayload.Text, "🟢") {
		t.Fatalf("resolved slack text %q missing green status", resolvedPayload.Text)
	}
}

func TestNotifier_TelegramPayload(t *testing.T) {
	var gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(permissiveGuard(t, srv.URL), srv.URL)
	err := n.Send(context.Background(), Target{
		Kind: KindTelegram, BotToken: "123:abc", ChatID: "42",
	}, testEvent("opened"))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if gotPath != "/bot123:abc/sendMessage" {
		t.Fatalf("path = %q, want /bot123:abc/sendMessage", gotPath)
	}
	var payload struct {
		ChatID                string `json:"chat_id"`
		Text                  string `json:"text"`
		DisableWebPagePreview bool   `json:"disable_web_page_preview"`
	}
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("unmarshal telegram payload: %v", err)
	}
	if payload.ChatID != "42" {
		t.Fatalf("chat_id = %q, want 42", payload.ChatID)
	}
	if !payload.DisableWebPagePreview {
		t.Fatal("disable_web_page_preview = false, want true")
	}
	if !strings.Contains(payload.Text, "Acme") || !strings.Contains(payload.Text, "API") ||
		!strings.Contains(payload.Text, "/w/acme/incidents/2222") {
		t.Fatalf("telegram text = %q, want workspace, monitor, and link", payload.Text)
	}
}

func TestNotifier_TelegramTestPayload(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(permissiveGuard(t, srv.URL), srv.URL)
	if err := n.Send(context.Background(), Target{
		Kind: KindTelegram, BotToken: "123:abc", ChatID: "42",
	}, testEvent("test")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(payload.Text, "Test notification from o1 for") {
		t.Fatalf("telegram test text = %q", payload.Text)
	}
}

func TestNotifier_StatusClassification(t *testing.T) {
	cases := []struct {
		status    int
		permanent bool
	}{
		{http.StatusOK, false},
		{http.StatusNoContent, false},
		{http.StatusFound, true},
		{http.StatusBadRequest, true},
		{http.StatusRequestTimeout, false},
		{http.StatusTooEarly, false},
		{http.StatusTooManyRequests, false},
		{http.StatusInternalServerError, false},
		{http.StatusServiceUnavailable, false},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			targetURL := srv.URL + "/hook?token=supersecret"
			n := NewNotifier(permissiveGuard(t, srv.URL), "")
			err := n.Send(context.Background(), Target{Kind: KindWebhook, URL: targetURL}, testEvent("opened"))
			if tc.status/100 == 2 {
				if err != nil {
					t.Fatalf("Send status %d = %v, want nil", tc.status, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Send status %d = nil, want error", tc.status)
			}
			if got := IsPermanent(err); got != tc.permanent {
				t.Fatalf("IsPermanent(%d) = %v, want %v (err %v)", tc.status, got, tc.permanent, err)
			}
			message := err.Error()
			for _, leak := range []string{srv.URL, "/hook", "token=supersecret", "supersecret"} {
				if strings.Contains(message, leak) {
					t.Fatalf("error %q leaks %q", message, leak)
				}
			}
		})
	}
}

func TestNotifier_TelegramErrorSanitized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	const token = "123:supersecrettoken"
	n := NewNotifier(permissiveGuard(t, srv.URL), srv.URL)
	err := n.Send(context.Background(), Target{
		Kind: KindTelegram, BotToken: token, ChatID: "42",
	}, testEvent("opened"))
	if err == nil {
		t.Fatal("Send = nil, want error")
	}
	if IsPermanent(err) {
		t.Fatal("500 classified as permanent")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error %q leaks the bot token", err.Error())
	}
}

func TestNotifier_WebhookPrivateBlocked(t *testing.T) {
	n := NewNotifier(probe.NewGuardWithResolver(
		probe.Policy{AllowedPorts: map[int]struct{}{80: {}, 443: {}}, MaxRedirects: 3},
		staticResolver{ips: []string{"127.0.0.1"}},
	), "")
	err := n.Send(context.Background(), Target{
		Kind: KindWebhook, URL: "https://internal.example/hook",
	}, testEvent("opened"))
	if !IsPermanent(err) {
		t.Fatalf("Send = %v, want permanent policy error", err)
	}
	if strings.Contains(err.Error(), "internal.example") {
		t.Fatalf("error %q leaks the destination", err.Error())
	}
}

func TestNotifier_SlackHostRejected(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(permissiveGuard(t, srv.URL), "")
	err := n.Send(context.Background(), Target{
		Kind: KindSlack, URL: srv.URL + "/slack",
	}, testEvent("opened"))
	if !IsPermanent(err) {
		t.Fatalf("Send = %v, want permanent Slack host error", err)
	}
	if calls.Load() != 0 {
		t.Fatal("Slack host rejection made a request")
	}
}

func TestNotifier_UnknownKindPermanent(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(permissiveGuard(t, srv.URL), "")
	err := n.Send(context.Background(), Target{
		Kind: "pagerduty", URL: srv.URL,
	}, testEvent("opened"))
	if !IsPermanent(err) {
		t.Fatalf("Send = %v, want permanent unknown-kind error", err)
	}
	if calls.Load() != 0 {
		t.Fatal("unknown kind made a request")
	}
}

func TestNotifier_NoRedirects(t *testing.T) {
	var secondCalls atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()

	var firstCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls.Add(1)
		http.Redirect(w, r, second.URL, http.StatusFound)
	}))
	defer first.Close()

	n := NewNotifier(permissiveGuard(t, first.URL), "")
	err := n.Send(context.Background(), Target{Kind: KindWebhook, URL: first.URL}, testEvent("opened"))
	if !IsPermanent(err) {
		t.Fatalf("Send = %v, want permanent redirect error", err)
	}
	if firstCalls.Load() != 1 {
		t.Fatalf("first server calls = %d, want 1", firstCalls.Load())
	}
	if secondCalls.Load() != 0 {
		t.Fatalf("second server calls = %d, want 0", secondCalls.Load())
	}
}

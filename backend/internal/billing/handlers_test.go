package billing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhookAppliesAndRejectsBadSignatures(t *testing.T) {
	svc, q, wsID, _ := newBillingEnv(t)
	ctx := context.Background()

	ev := startedEvent(wsID, "pro")
	body, sig, err := NewSandbox([]byte("sandbox-secret")).SignEvent(ev)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/billing/webhook/sandbox", strings.NewReader(string(body)))
	req.SetPathValue("provider", "sandbox")
	req.Header.Set("X-O1-Sandbox-Signature", sig)
	rec := httptest.NewRecorder()
	svc.Webhook(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid webhook = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	sub, err := q.GetSubscription(ctx, wsID)
	if err != nil {
		t.Fatalf("get subscription: %v", err)
	}
	if sub.PlanKey != "pro" {
		t.Fatalf("plan = %s", sub.PlanKey)
	}

	bad := httptest.NewRequest(http.MethodPost, "/api/v1/billing/webhook/sandbox", strings.NewReader(string(body)))
	bad.SetPathValue("provider", "sandbox")
	bad.Header.Set("X-O1-Sandbox-Signature", "sha256=deadbeef")
	rec = httptest.NewRecorder()
	svc.Webhook(rec, bad)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad signature = %d, want 400", rec.Code)
	}

	unknown := httptest.NewRequest(http.MethodPost, "/api/v1/billing/webhook/stripe", strings.NewReader("{}"))
	unknown.SetPathValue("provider", "stripe")
	rec = httptest.NewRecorder()
	svc.Webhook(rec, unknown)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown provider = %d, want 404", rec.Code)
	}
}

func TestWebhookDuplicateIsIdempotent(t *testing.T) {
	svc, _, wsID, _ := newBillingEnv(t)
	ev := startedEvent(wsID, "pro")
	body, sig, _ := NewSandbox([]byte("sandbox-secret")).SignEvent(ev)

	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/billing/webhook/sandbox", strings.NewReader(string(body)))
		req.SetPathValue("provider", "sandbox")
		req.Header.Set("X-O1-Sandbox-Signature", sig)
		rec := httptest.NewRecorder()
		svc.Webhook(rec, req)
		return rec.Code
	}
	if code := send(); code != http.StatusOK {
		t.Fatalf("first = %d", code)
	}
	if code := send(); code != http.StatusOK {
		t.Fatalf("duplicate = %d", code)
	}
	var events int
	if err := svc.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM billing_events`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("events = %d (err %v)", events, err)
	}
}

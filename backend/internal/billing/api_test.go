package billing_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"o1/internal/api"
	"o1/internal/billing"
	"o1/internal/db/sqlc"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

func TestBillingRoutes(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	cfg := testutil.TestConfig(t)
	ctx := context.Background()

	wsSvc := workspaces.NewService(pool, sqlc.New(pool))
	ownerEmail := "owner@example.com"
	const password = "correct horse battery staple"
	ownerID := testutil.CreateVerifiedUser(t, pool, ownerEmail, password)
	ws, err := wsSvc.Create(ctx, ownerID, "Route Billing")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	svc := billing.NewService(pool, sqlc.New(pool), billing.NewSandbox([]byte("route-secret")))
	router := api.NewRouter(api.Dependencies{
		Pool: pool, Config: cfg, WorkspaceService: wsSvc, BillingService: svc,
	})
	token := testutil.SessionToken(t, pool, cfg, ownerEmail, password)

	do := func(method, path string, body any) *httptest.ResponseRecorder {
		var reader *bytes.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		} else {
			reader = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, reader)
		req.AddCookie(&http.Cookie{Name: "o1_session", Value: token})
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	if rec := do(http.MethodGet, "/api/v1/plans", nil); rec.Code != http.StatusOK {
		t.Fatalf("plans = %d: %s", rec.Code, rec.Body.String())
	}

	base := "/api/v1/workspaces/" + ws.ID.String()
	rec := do(http.MethodGet, base+"/billing", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get billing = %d: %s", rec.Code, rec.Body.String())
	}
	var view struct {
		Plan struct {
			Key string `json:"key"`
		} `json:"plan"`
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &view)
	if view.Plan.Key != "free" || view.Mode != "sandbox" {
		t.Fatalf("view = %+v", view)
	}

	rec = do(http.MethodPost, base+"/billing/checkout", map[string]string{"plan": "pro"})
	if rec.Code != http.StatusOK {
		t.Fatalf("checkout = %d: %s", rec.Code, rec.Body.String())
	}
	var session struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &session)
	parsed, _ := url.Parse(session.URL)
	checkoutToken := parsed.Query().Get("token")
	if checkoutToken == "" {
		t.Fatalf("checkout URL = %q", session.URL)
	}

	rec = do(http.MethodPost, base+"/billing/sandbox/complete",
		map[string]string{"token": checkoutToken, "outcome": "success"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("complete = %d: %s", rec.Code, rec.Body.String())
	}

	rec = do(http.MethodGet, base+"/billing", nil)
	var after struct {
		Plan struct {
			Key string `json:"key"`
		} `json:"plan"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &after)
	if after.Plan.Key != "pro" || after.Status != "trialing" {
		t.Fatalf("after checkout = %+v", after)
	}

	// A viewer is read-only.
	viewerEmail := "viewer@example.com"
	viewerID := testutil.CreateVerifiedUser(t, pool, viewerEmail, password)
	if err := wsSvc.AddMember(ctx, ownerID, ws.ID, viewerEmail, workspaces.RoleViewer); err != nil {
		t.Fatalf("add viewer: %v", err)
	}
	_ = viewerID
	viewerToken := testutil.SessionToken(t, pool, cfg, viewerEmail, password)
	req := httptest.NewRequest(http.MethodPost, base+"/billing/checkout",
		bytes.NewReader([]byte(`{"plan":"team"}`)))
	req.AddCookie(&http.Cookie{Name: "o1_session", Value: viewerToken})
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer checkout = %d, want 403", rec.Code)
	}

}

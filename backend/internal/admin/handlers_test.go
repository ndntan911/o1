package admin_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"o1/internal/admin"
)

// serveHandler invokes h directly, bypassing the router middleware.
func serveHandler(t *testing.T, h http.HandlerFunc, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) struct {
	Code   string            `json:"code"`
	Fields map[string]string `json:"fields"`
} {
	t.Helper()
	var p struct {
		Code   string            `json:"code"`
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem %q: %v", rec.Body.String(), err)
	}
	return p
}

func TestHandlers_ServiceUnavailable(t *testing.T) {
	rec := serveHandler(t, admin.WorkspacesHandler(nil), "/api/v1/admin/workspaces")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if p := decodeProblem(t, rec); p.Code != "internal_error" {
		t.Fatalf("code = %q", p.Code)
	}
}

func TestWorkspacesHandler_RejectsBadQuery(t *testing.T) {
	env := newTestEnv(t)
	tests := []struct {
		query string
		field string
	}{
		{"?suspended=maybe", "suspended"},
		{"?limit=0", "limit"},
		{"?limit=abc", "limit"},
		{"?cursor=not-base64!", "cursor"},
	}
	for _, tc := range tests {
		rec := serveHandler(t, admin.WorkspacesHandler(env.svc), "/api/v1/admin/workspaces"+tc.query)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, body = %s", tc.query, rec.Code, rec.Body.String())
			continue
		}
		if p := decodeProblem(t, rec); p.Fields[tc.field] == "" {
			t.Errorf("%s fields = %+v", tc.query, p.Fields)
		}
	}
}

func TestMonitorsHandler_RejectsBadWorkspaceID(t *testing.T) {
	env := newTestEnv(t)
	rec := serveHandler(t, admin.MonitorsHandler(env.svc), "/api/v1/admin/monitors?workspace_id=nope")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if p := decodeProblem(t, rec); p.Fields["workspace_id"] == "" {
		t.Fatalf("fields = %+v", p.Fields)
	}
}

func TestSuspendHandler_RequiresPrincipal(t *testing.T) {
	env := newTestEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/workspaces/01234567-89ab-cdef-0123-456789abcdef/suspend",
		bytes.NewReader([]byte(`{"reason":"abuse"}`)))
	req.SetPathValue("workspaceID", "01234567-89ab-cdef-0123-456789abcdef")
	rec := httptest.NewRecorder()
	admin.SuspendHandler(env.svc)(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestSecurityEventsHandler_ReturnsEmptyPage(t *testing.T) {
	env := newTestEnv(t)
	rec := serveHandler(t, admin.SecurityEventsHandler(env.svc), "/api/v1/admin/security-events")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Events     []json.RawMessage `json:"events"`
		NextCursor string            `json:"next_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Events == nil || len(body.Events) != 0 || body.NextCursor != "" {
		t.Fatalf("body = %+v", body)
	}
}

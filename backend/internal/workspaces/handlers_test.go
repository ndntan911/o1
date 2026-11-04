package workspaces_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/auth"
	"o1/internal/db/sqlc"
	"o1/internal/httpx"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

const handlerPassword = "correct horse battery staple"

type workspaceJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Role string `json:"role"`
}

type workspaceListJSON struct {
	Workspaces []workspaceJSON `json:"workspaces"`
}

type memberJSON struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

type memberListJSON struct {
	Members []memberJSON `json:"members"`
}

func newWorkspaceService(t *testing.T, pool *pgxpool.Pool) (*workspaces.Service, *auth.Service) {
	t.Helper()
	wsSvc := workspaces.NewService(pool, sqlc.New(pool))
	authSvc := auth.NewService(pool, sqlc.New(pool), testutil.NewFakeSender(), testutil.TestConfig(t))
	t.Cleanup(authSvc.Close)
	return wsSvc, authSvc
}

func loginAs(t *testing.T, authSvc *auth.Service, emailAddr string) *http.Cookie {
	t.Helper()
	raw, _, err := authSvc.Login(context.Background(), emailAddr, handlerPassword, "", "")
	if err != nil {
		t.Fatalf("login %s: %v", emailAddr, err)
	}
	return &http.Cookie{Name: "o1_session", Value: raw}
}

func doRequest(t *testing.T, h http.Handler, method, target, body string, cookie *http.Cookie, workspaceID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if workspaceID != "" {
		req.SetPathValue("workspaceID", workspaceID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode body %s: %v", rec.Body.String(), err)
	}
	return v
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type = %q, body = %s", ct, rec.Body.String())
	}
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem %s: %v", rec.Body.String(), err)
	}
	return p.Code
}

func TestListHandler_ReturnsOnlyCallersWorkspaces(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	wsSvc, authSvc := newWorkspaceService(t, pool)
	owner := testutil.CreateVerifiedUser(t, pool, "owner@example.com", handlerPassword)
	other := testutil.CreateVerifiedUser(t, pool, "other@example.com", handlerPassword)
	if _, err := wsSvc.Create(context.Background(), owner, "Acme Status"); err != nil {
		t.Fatal(err)
	}
	if _, err := wsSvc.Create(context.Background(), other, "Other"); err != nil {
		t.Fatal(err)
	}

	h := auth.RequireSession(authSvc)(workspaces.ListHandler(wsSvc))
	rec := doRequest(t, h, http.MethodGet, "/api/v1/workspaces", "", loginAs(t, authSvc, "owner@example.com"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp := decodeBody[workspaceListJSON](t, rec)
	if len(resp.Workspaces) != 1 {
		t.Fatalf("workspaces = %+v", resp.Workspaces)
	}
	ws := resp.Workspaces[0]
	if ws.Name != "Acme Status" || ws.Slug != "acme-status" || ws.Role != "owner" || ws.ID == "" {
		t.Fatalf("workspace = %+v", ws)
	}
}

func TestListHandler_RequiresSession(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	wsSvc, authSvc := newWorkspaceService(t, pool)

	h := auth.RequireSession(authSvc)(workspaces.ListHandler(wsSvc))
	rec := doRequest(t, h, http.MethodGet, "/api/v1/workspaces", "", nil, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if code := problemCode(t, rec); code != "unauthenticated" {
		t.Fatalf("code = %q", code)
	}
}

func TestCreateHandler_CreatesWorkspace(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	wsSvc, authSvc := newWorkspaceService(t, pool)
	testutil.CreateVerifiedUser(t, pool, "owner@example.com", handlerPassword)

	h := auth.RequireSession(authSvc)(workspaces.CreateHandler(wsSvc))
	rec := doRequest(t, h, http.MethodPost, "/api/v1/workspaces", `{"name":"Acme Status"}`, loginAs(t, authSvc, "owner@example.com"), "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	ws := decodeBody[workspaceJSON](t, rec)
	if ws.ID == "" || ws.Name != "Acme Status" || ws.Slug != "acme-status" || ws.Role != "owner" {
		t.Fatalf("workspace = %+v", ws)
	}
}

func TestCreateHandler_ValidationErrors(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	wsSvc, authSvc := newWorkspaceService(t, pool)
	testutil.CreateVerifiedUser(t, pool, "owner@example.com", handlerPassword)
	cookie := loginAs(t, authSvc, "owner@example.com")

	h := auth.RequireSession(authSvc)(workspaces.CreateHandler(wsSvc))
	for name, body := range map[string]string{
		"bad json": "{not json",
		"unknown":  `{"name":"Acme","extra":1}`,
		"empty":    `{"name":"   "}`,
		"too long": `{"name":"` + strings.Repeat("a", 81) + `"}`,
		"no name":  `{}`,
	} {
		rec := doRequest(t, h, http.MethodPost, "/api/v1/workspaces", body, cookie, "")
		if rec.Code != http.StatusBadRequest || problemCode(t, rec) != "validation_error" {
			t.Errorf("%s: status = %d, body = %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestGetHandler_ReturnsRoleAndHidesOtherTenants(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	wsSvc, authSvc := newWorkspaceService(t, pool)
	owner := testutil.CreateVerifiedUser(t, pool, "owner@example.com", handlerPassword)
	testutil.CreateVerifiedUser(t, pool, "outsider@example.com", handlerPassword)
	ws, err := wsSvc.Create(context.Background(), owner, "Private")
	if err != nil {
		t.Fatal(err)
	}

	h := auth.RequireSession(authSvc)(workspaces.GetHandler(wsSvc))
	rec := doRequest(t, h, http.MethodGet, "/api/v1/workspaces/"+ws.ID.String(), "", loginAs(t, authSvc, "owner@example.com"), ws.ID.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("owner status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got := decodeBody[workspaceJSON](t, rec)
	if got.ID != ws.ID.String() || got.Name != "Private" || got.Role != "owner" {
		t.Fatalf("workspace = %+v", got)
	}

	rec = doRequest(t, h, http.MethodGet, "/api/v1/workspaces/"+ws.ID.String(), "", loginAs(t, authSvc, "outsider@example.com"), ws.ID.String())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("outsider status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if code := problemCode(t, rec); code != "not_found" {
		t.Fatalf("code = %q", code)
	}
}

func TestMembersHandler_ListsMembers(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	wsSvc, authSvc := newWorkspaceService(t, pool)
	owner := testutil.CreateVerifiedUser(t, pool, "owner@example.com", handlerPassword)
	testutil.CreateVerifiedUser(t, pool, "viewer@example.com", handlerPassword)
	testutil.CreateVerifiedUser(t, pool, "outsider@example.com", handlerPassword)
	ws, err := wsSvc.Create(context.Background(), owner, "Team")
	if err != nil {
		t.Fatal(err)
	}
	if err := wsSvc.AddMember(context.Background(), owner, ws.ID, "viewer@example.com", workspaces.RoleViewer); err != nil {
		t.Fatal(err)
	}

	h := auth.RequireSession(authSvc)(workspaces.MembersHandler(wsSvc))
	rec := doRequest(t, h, http.MethodGet, "/api/v1/workspaces/"+ws.ID.String()+"/members", "", loginAs(t, authSvc, "owner@example.com"), ws.ID.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp := decodeBody[memberListJSON](t, rec)
	if len(resp.Members) != 2 {
		t.Fatalf("members = %+v", resp.Members)
	}
	roles := map[string]string{}
	for _, m := range resp.Members {
		roles[m.Email] = m.Role
	}
	if roles["owner@example.com"] != "owner" || roles["viewer@example.com"] != "viewer" {
		t.Fatalf("roles = %+v", roles)
	}

	rec = doRequest(t, h, http.MethodGet, "/api/v1/workspaces/"+ws.ID.String()+"/members", "", loginAs(t, authSvc, "outsider@example.com"), ws.ID.String())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("outsider status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAddMemberHandler_AddsMemberAndMapsErrors(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	wsSvc, authSvc := newWorkspaceService(t, pool)
	owner := testutil.CreateVerifiedUser(t, pool, "owner@example.com", handlerPassword)
	testutil.CreateVerifiedUser(t, pool, "member@example.com", handlerPassword)
	testutil.CreateVerifiedUser(t, pool, "viewer@example.com", handlerPassword)
	ws, err := wsSvc.Create(context.Background(), owner, "Team")
	if err != nil {
		t.Fatal(err)
	}

	h := auth.RequireSession(authSvc)(workspaces.AddMemberHandler(wsSvc))
	ownerCookie := loginAs(t, authSvc, "owner@example.com")
	path := "/api/v1/workspaces/" + ws.ID.String() + "/members"

	rec := doRequest(t, h, http.MethodPost, path, `{"email":"MEMBER@Example.com","role":"viewer"}`, ownerCookie, ws.ID.String())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("add status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// The upsert updates an existing membership and uses normalized email.
	rec = doRequest(t, h, http.MethodPost, path, `{"email":"member@example.com","role":"member"}`, ownerCookie, ws.ID.String())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("update status = %d, body = %s", rec.Code, rec.Body.String())
	}
	members, err := wsSvc.ListMembers(context.Background(), owner, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range members {
		if m.Email == "member@example.com" && m.Role == workspaces.RoleMember {
			found = true
		}
	}
	if !found {
		t.Fatalf("members = %+v, want member@example.com as member", members)
	}

	// Owner grants are validation errors before the target email is looked up.
	rec = doRequest(t, h, http.MethodPost, path, `{"email":"missing@example.com","role":"owner"}`, ownerCookie, ws.ID.String())
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != "validation_error" {
		t.Fatalf("owner grant status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Unknown email -> 404.
	rec = doRequest(t, h, http.MethodPost, path, `{"email":"missing@example.com","role":"viewer"}`, ownerCookie, ws.ID.String())
	if rec.Code != http.StatusNotFound || problemCode(t, rec) != "not_found" {
		t.Fatalf("missing user status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Unknown role -> 400.
	rec = doRequest(t, h, http.MethodPost, path, `{"email":"viewer@example.com","role":"superuser"}`, ownerCookie, ws.ID.String())
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != "validation_error" {
		t.Fatalf("bad role status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// A viewer cannot add members -> 403.
	if err := wsSvc.AddMember(context.Background(), owner, ws.ID, "viewer@example.com", workspaces.RoleViewer); err != nil {
		t.Fatal(err)
	}
	viewerCookie := loginAs(t, authSvc, "viewer@example.com")
	rec = doRequest(t, h, http.MethodPost, path, `{"email":"member@example.com","role":"viewer"}`, viewerCookie, ws.ID.String())
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != "forbidden" {
		t.Fatalf("viewer add status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Malformed JSON/unknown fields -> 400.
	for name, body := range map[string]string{
		"bad json": "{not json",
		"unknown":  `{"email":"member@example.com","role":"viewer","extra":1}`,
	} {
		rec = doRequest(t, h, http.MethodPost, path, body, ownerCookie, ws.ID.String())
		if rec.Code != http.StatusBadRequest || problemCode(t, rec) != "validation_error" {
			t.Errorf("%s: status = %d, body = %s", name, rec.Code, rec.Body.String())
		}
	}
}

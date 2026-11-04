package workspaces_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"o1/internal/db/sqlc"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

func TestCreateWorkspace_CreatorIsOwner(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	userID := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	svc := workspaces.NewService(pool, sqlc.New(pool))
	ws, err := svc.Create(context.Background(), userID, "Acme Status")
	if err != nil {
		t.Fatal(err)
	}
	if ws.Role != workspaces.RoleOwner {
		t.Fatalf("role = %v", ws.Role)
	}
	if ws.Slug != "acme-status" {
		t.Fatalf("slug = %q", ws.Slug)
	}
}

func TestListForUser_OnlyMemberships(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	a := testutil.CreateVerifiedUser(t, pool, "a@example.com", "correct horse battery staple")
	b := testutil.CreateVerifiedUser(t, pool, "b@example.com", "correct horse battery staple")
	svc := workspaces.NewService(pool, sqlc.New(pool))
	ctx := context.Background()
	if _, err := svc.Create(ctx, a, "A Workspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, b, "B Workspace"); err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListForUser(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "A Workspace" {
		t.Fatalf("list = %+v", list)
	}
}

func TestAuthorize_CrossTenantDenied(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	owner := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	outsider := testutil.CreateVerifiedUser(t, pool, "outsider@example.com", "correct horse battery staple")
	svc := workspaces.NewService(pool, sqlc.New(pool))
	ws, err := svc.Create(context.Background(), owner, "Private")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authorize(context.Background(), outsider, ws.ID, workspaces.RoleViewer); !errors.Is(err, workspaces.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestAddMember_RequiresAdminAndCapsRole(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	owner := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	viewer := testutil.CreateVerifiedUser(t, pool, "viewer@example.com", "correct horse battery staple")
	admin := testutil.CreateVerifiedUser(t, pool, "admin@example.com", "correct horse battery staple")
	svc := workspaces.NewService(pool, sqlc.New(pool))
	ctx := context.Background()
	ws, err := svc.Create(ctx, owner, "Team")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AddMember(ctx, owner, ws.ID, "viewer@example.com", workspaces.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddMember(ctx, viewer, ws.ID, "admin@example.com", workspaces.RoleAdmin); !errors.Is(err, workspaces.ErrForbidden) {
		t.Fatalf("viewer add err = %v", err)
	}
	if err := svc.AddMember(ctx, owner, ws.ID, "admin@example.com", workspaces.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddMember(ctx, admin, ws.ID, "viewer@example.com", workspaces.RoleAdmin); !errors.Is(err, workspaces.ErrForbidden) {
		t.Fatalf("admin cap err = %v", err)
	}
	if err := svc.AddMember(ctx, owner, ws.ID, "owner2@example.com", workspaces.RoleOwner); !errors.Is(err, workspaces.ErrValidation) {
		t.Fatalf("owner grant err = %v", err)
	}
}

func TestCreateWorkspace_SlugCollisionAddsSuffix(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	user := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	svc := workspaces.NewService(pool, sqlc.New(pool))
	ctx := context.Background()

	first, err := svc.Create(ctx, user, "Acme Status")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Create(ctx, user, "Acme Status")
	if err != nil {
		t.Fatal(err)
	}
	if second.Slug == first.Slug {
		t.Fatalf("slugs collide: %q", second.Slug)
	}
	if !strings.HasPrefix(second.Slug, "acme-status-") {
		t.Fatalf("slug = %q, want acme-status- prefix", second.Slug)
	}
	if len(second.Slug) > 60 {
		t.Fatalf("slug length = %d, want <= 60", len(second.Slug))
	}
}

func TestCreateWorkspace_ValidatesName(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	user := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	svc := workspaces.NewService(pool, sqlc.New(pool))
	ctx := context.Background()

	for name, value := range map[string]string{
		"empty":      "",
		"whitespace": "   ",
		"too long":   strings.Repeat("a", 81),
	} {
		if _, err := svc.Create(ctx, user, value); !errors.Is(err, workspaces.ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
	}
}

func TestCreateWorkspace_RejectsUnknownUser(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	svc := workspaces.NewService(pool, sqlc.New(pool))
	missing := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	// Delete the user so the workspace's created_by foreign key fails.
	if _, err := pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", missing); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Create(context.Background(), missing, "Orphan"); err == nil {
		t.Fatal("expected create to fail for a missing user")
	}
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM workspaces").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("workspaces after failed create = %d, want 0", count)
	}
}

func TestGet_ReturnsCallerRoleAndHidesOtherTenants(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	owner := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	outsider := testutil.CreateVerifiedUser(t, pool, "outsider@example.com", "correct horse battery staple")
	svc := workspaces.NewService(pool, sqlc.New(pool))
	ctx := context.Background()

	ws, err := svc.Create(ctx, owner, "Private")
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, owner, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != ws.ID || got.Role != workspaces.RoleOwner {
		t.Fatalf("get = %+v", got)
	}
	if _, err := svc.Get(ctx, outsider, ws.ID); !errors.Is(err, workspaces.ErrNotFound) {
		t.Fatalf("cross-tenant get err = %v", err)
	}
}

func TestListMembers_IncludesOwnerAndUpdatesRoles(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	owner := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	testutil.CreateVerifiedUser(t, pool, "member@example.com", "correct horse battery staple")
	outsider := testutil.CreateVerifiedUser(t, pool, "outsider@example.com", "correct horse battery staple")
	svc := workspaces.NewService(pool, sqlc.New(pool))
	ctx := context.Background()

	ws, err := svc.Create(ctx, owner, "Team")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AddMember(ctx, owner, ws.ID, "member@example.com", workspaces.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddMember(ctx, owner, ws.ID, "member@example.com", workspaces.RoleMember); err != nil {
		t.Fatal(err)
	}

	members, err := svc.ListMembers(ctx, owner, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Fatalf("members = %+v", members)
	}
	roles := map[string]workspaces.Role{}
	for _, m := range members {
		roles[m.Email] = m.Role
	}
	if roles["owner@example.com"] != workspaces.RoleOwner || roles["member@example.com"] != workspaces.RoleMember {
		t.Fatalf("roles = %+v", roles)
	}
	if _, err := svc.ListMembers(ctx, outsider, ws.ID); !errors.Is(err, workspaces.ErrNotFound) {
		t.Fatalf("cross-tenant list members err = %v", err)
	}
}

func TestAddMember_RejectsInvalidAndOwnerRolesBeforeUserLookup(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	owner := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	svc := workspaces.NewService(pool, sqlc.New(pool))
	ctx := context.Background()

	ws, err := svc.Create(ctx, owner, "Team")
	if err != nil {
		t.Fatal(err)
	}
	for name, role := range map[string]workspaces.Role{
		"owner":   workspaces.RoleOwner,
		"unknown": workspaces.Role(99),
	} {
		err := svc.AddMember(ctx, owner, ws.ID, "nobody@example.com", role)
		if !errors.Is(err, workspaces.ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
	}
}

func TestAddMember_CrossTenantIsNotFound(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	owner := testutil.CreateVerifiedUser(t, pool, "owner@example.com", "correct horse battery staple")
	outsider := testutil.CreateVerifiedUser(t, pool, "outsider@example.com", "correct horse battery staple")
	svc := workspaces.NewService(pool, sqlc.New(pool))
	ctx := context.Background()

	ws, err := svc.Create(ctx, owner, "Private")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AddMember(ctx, outsider, ws.ID, "owner@example.com", workspaces.RoleViewer); !errors.Is(err, workspaces.ErrNotFound) {
		t.Fatalf("cross-tenant add err = %v", err)
	}
}

func TestRole_ParseAndAtLeast(t *testing.T) {
	for _, want := range []workspaces.Role{
		workspaces.RoleViewer,
		workspaces.RoleMember,
		workspaces.RoleAdmin,
		workspaces.RoleOwner,
	} {
		got, err := workspaces.ParseRole(want.String())
		if err != nil {
			t.Fatalf("parse %q: %v", want.String(), err)
		}
		if got != want {
			t.Fatalf("parse %q = %v, want %v", want.String(), got, want)
		}
	}
	if _, err := workspaces.ParseRole("superuser"); !errors.Is(err, workspaces.ErrValidation) {
		t.Fatalf("parse invalid err = %v", err)
	}
	if !workspaces.RoleOwner.AtLeast(workspaces.RoleAdmin) {
		t.Fatal("owner should be at least admin")
	}
	if workspaces.RoleViewer.AtLeast(workspaces.RoleMember) {
		t.Fatal("viewer should not be at least member")
	}
}

package apikeys

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/auth"
	"o1/internal/db/sqlc"
	"o1/internal/testutil"
	"o1/internal/workspaces"
)

const serviceTestPassword = "correct horse battery staple"

var (
	secretPattern = regexp.MustCompile(`^o1_[a-z0-9]{12}_[A-Za-z0-9_-]{43}$`)
	prefixPattern = regexp.MustCompile(`^o1_[a-z0-9]{12}$`)
)

type serviceEnv struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *sqlc.Queries
	ws     *workspaces.Service
	svc    *Service
	wsA    uuid.UUID
	wsB    uuid.UUID
	owner  uuid.UUID
	admin  uuid.UUID
	member uuid.UUID
	viewer uuid.UUID
	ownerB uuid.UUID
}

func newServiceEnv(t *testing.T) *serviceEnv {
	t.Helper()
	pool, cleanup := testutil.NewPostgres(t)
	t.Cleanup(cleanup)

	q := sqlc.New(pool)
	wsSvc := workspaces.NewService(pool, q)
	env := &serviceEnv{
		t: t, pool: pool, q: q, ws: wsSvc,
		svc: NewService(pool, q, wsSvc),
	}

	env.owner = testutil.CreateVerifiedUser(t, pool, "keys-owner@example.com", serviceTestPassword)
	ws, err := wsSvc.Create(context.Background(), env.owner, "Keys A")
	if err != nil {
		t.Fatalf("create workspace A: %v", err)
	}
	env.wsA = ws.ID
	env.admin = env.addMember("keys-admin@example.com", "admin")
	env.member = env.addMember("keys-member@example.com", "member")
	env.viewer = env.addMember("keys-viewer@example.com", "viewer")

	env.ownerB = testutil.CreateVerifiedUser(t, pool, "keys-owner-b@example.com", serviceTestPassword)
	wsB, err := wsSvc.Create(context.Background(), env.ownerB, "Keys B")
	if err != nil {
		t.Fatalf("create workspace B: %v", err)
	}
	env.wsB = wsB.ID
	return env
}

func (e *serviceEnv) addMember(emailAddr, role string) uuid.UUID {
	e.t.Helper()
	id := testutil.CreateVerifiedUser(e.t, e.pool, emailAddr, serviceTestPassword)
	if err := e.q.UpsertWorkspaceMember(context.Background(), sqlc.UpsertWorkspaceMemberParams{
		WorkspaceID: e.wsA,
		UserID:      id,
		Role:        role,
	}); err != nil {
		e.t.Fatalf("add member: %v", err)
	}
	return id
}

func (e *serviceEnv) createKey(name, role string) CreatedKey {
	e.t.Helper()
	created, err := e.svc.Create(context.Background(), e.owner, e.wsA, CreateInput{
		Name: name, Role: role,
	})
	if err != nil {
		e.t.Fatalf("create key %q: %v", name, err)
	}
	return created
}

func (e *serviceEnv) suspend(workspaceID uuid.UUID) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE workspaces SET suspended_at = now() WHERE id = $1`, workspaceID); err != nil {
		e.t.Fatalf("suspend workspace: %v", err)
	}
}

func (e *serviceEnv) storedKey(t *testing.T, id uuid.UUID) sqlc.APIKey {
	t.Helper()
	row, err := e.q.GetAPIKey(context.Background(), sqlc.GetAPIKeyParams{ID: id, WorkspaceID: e.wsA})
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	return row
}

func TestService_CreateKey(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()

	created, err := env.svc.Create(ctx, env.owner, env.wsA, CreateInput{Name: "  CI deploy  ", Role: "viewer"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if created.Key.Name != "CI deploy" {
		t.Fatalf("name = %q, want trimmed %q", created.Key.Name, "CI deploy")
	}
	if created.Key.Role != "viewer" {
		t.Fatalf("role = %q, want viewer", created.Key.Role)
	}
	if !prefixPattern.MatchString(created.Key.Prefix) {
		t.Fatalf("prefix = %q, want o1_<12 lowercase alnum>", created.Key.Prefix)
	}
	if !secretPattern.MatchString(created.Secret) {
		t.Fatalf("secret does not match the documented shape: %q", created.Secret)
	}
	if !strings.HasPrefix(created.Secret, created.Key.Prefix+"_") {
		t.Fatalf("secret %q does not embed prefix %q", created.Secret, created.Key.Prefix)
	}
	if created.Key.CreatedBy != env.owner {
		t.Fatalf("created_by = %s, want %s", created.Key.CreatedBy, env.owner)
	}
	if created.Key.CreatedAt.IsZero() {
		t.Fatal("created_at is zero")
	}

	stored := env.storedKey(t, created.Key.ID)
	if !bytes.Equal(stored.TokenHash, auth.HashToken(created.Secret)) {
		t.Fatal("stored token_hash is not HashToken(secret)")
	}
	if bytes.Equal(stored.TokenHash, []byte(created.Secret)) {
		t.Fatal("token_hash stores the plaintext secret")
	}
	if stored.LastUsedAt != nil {
		t.Fatalf("last_used_at = %v, want nil", stored.LastUsedAt)
	}
	if stored.RevokedAt != nil {
		t.Fatalf("revoked_at = %v, want nil", stored.RevokedAt)
	}
	if stored.Prefix != strings.TrimPrefix(created.Key.Prefix, "o1_") {
		t.Fatalf("stored prefix = %q, want %q", stored.Prefix, strings.TrimPrefix(created.Key.Prefix, "o1_"))
	}
}

func TestService_ListAndRevoke(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()

	first := env.createKey("first", "viewer")
	second := env.createKey("second", "member")
	third := env.createKey("third", "viewer")

	keys, err := env.svc.List(ctx, env.owner, env.wsA)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("len(keys) = %d, want 3", len(keys))
	}
	wantOrder := []uuid.UUID{third.Key.ID, second.Key.ID, first.Key.ID}
	for i, want := range wantOrder {
		if keys[i].ID != want {
			t.Fatalf("keys[%d].ID = %s, want %s (newest first)", i, keys[i].ID, want)
		}
	}

	raw, err := json.Marshal(keys)
	if err != nil {
		t.Fatalf("marshal keys: %v", err)
	}
	for _, created := range []CreatedKey{first, second, third} {
		if strings.Contains(string(raw), created.Secret) {
			t.Fatal("List response contains a plaintext secret")
		}
	}
	if strings.Contains(strings.ToLower(string(raw)), "token_hash") {
		t.Fatal("List response contains a token hash field")
	}

	if err := env.svc.Revoke(ctx, env.owner, env.wsA, second.Key.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	keys, err = env.svc.List(ctx, env.owner, env.wsA)
	if err != nil {
		t.Fatalf("List after revoke: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("len(keys) after revoke = %d, want 2", len(keys))
	}
	for _, key := range keys {
		if key.ID == second.Key.ID {
			t.Fatal("revoked key is still listed")
		}
	}
	count, err := env.q.CountActiveAPIKeys(ctx, env.wsA)
	if err != nil {
		t.Fatalf("count active keys: %v", err)
	}
	if count != 2 {
		t.Fatalf("CountActiveAPIKeys = %d, want 2", count)
	}
	if stored := env.storedKey(t, second.Key.ID); stored.RevokedAt == nil {
		t.Fatal("revoked_at is nil after revoke")
	}

	// Revoking an already revoked key is idempotent.
	if err := env.svc.Revoke(ctx, env.owner, env.wsA, second.Key.ID); err != nil {
		t.Fatalf("second Revoke = %v, want nil", err)
	}
}

func TestService_CreateValidation(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()

	t.Run("blank name", func(t *testing.T) {
		_, err := env.svc.Create(ctx, env.owner, env.wsA, CreateInput{Name: "   ", Role: "viewer"})
		requireFieldError(t, err, "name")
	})

	t.Run("61 rune name", func(t *testing.T) {
		_, err := env.svc.Create(ctx, env.owner, env.wsA, CreateInput{
			Name: strings.Repeat("a", 61), Role: "viewer",
		})
		requireFieldError(t, err, "name")
	})

	t.Run("admin role", func(t *testing.T) {
		_, err := env.svc.Create(ctx, env.owner, env.wsA, CreateInput{Name: "admin-key", Role: "admin"})
		requireFieldError(t, err, "role")
		_, err = env.svc.Create(ctx, env.owner, env.wsA, CreateInput{Name: "owner-key", Role: "owner"})
		requireFieldError(t, err, "role")
	})

	t.Run("21st active key", func(t *testing.T) {
		limitEnv := newServiceEnv(t)
		for i := 0; i < 20; i++ {
			limitEnv.createKey("key-"+string(rune('a'+i)), "viewer")
		}
		_, err := limitEnv.svc.Create(context.Background(), limitEnv.owner, limitEnv.wsA, CreateInput{
			Name: "key-21", Role: "viewer",
		})
		if !errors.Is(err, ErrLimit) {
			t.Fatalf("21st Create error = %v, want ErrLimit", err)
		}
	})

	t.Run("duplicate active name", func(t *testing.T) {
		dupEnv := newServiceEnv(t)
		dupEnv.createKey("deploy", "viewer")
		_, err := dupEnv.svc.Create(context.Background(), dupEnv.owner, dupEnv.wsA, CreateInput{
			Name: "deploy", Role: "member",
		})
		if !errors.Is(err, ErrNameTaken) {
			t.Fatalf("duplicate Create error = %v, want ErrNameTaken", err)
		}
	})

	t.Run("revoked name reusable", func(t *testing.T) {
		reuseEnv := newServiceEnv(t)
		created := reuseEnv.createKey("reuse", "viewer")
		if err := reuseEnv.svc.Revoke(context.Background(), reuseEnv.owner, reuseEnv.wsA, created.Key.ID); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
		again := reuseEnv.createKey("reuse", "member")
		if again.Key.Name != "reuse" {
			t.Fatalf("reused name = %q, want reuse", again.Key.Name)
		}
	})
}

func TestService_RolesAndSuspension(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()

	for _, actor := range []uuid.UUID{env.member, env.viewer} {
		if _, err := env.svc.Create(ctx, actor, env.wsA, CreateInput{Name: "nope", Role: "viewer"}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("Create as %s error = %v, want ErrForbidden", actor, err)
		}
		if _, err := env.svc.List(ctx, actor, env.wsA); !errors.Is(err, ErrForbidden) {
			t.Fatalf("List as %s error = %v, want ErrForbidden", actor, err)
		}
		if err := env.svc.Revoke(ctx, actor, env.wsA, uuid.New()); !errors.Is(err, ErrForbidden) {
			t.Fatalf("Revoke as %s error = %v, want ErrForbidden", actor, err)
		}
	}

	if _, err := env.svc.Create(ctx, env.admin, env.wsA, CreateInput{Name: "admin-made", Role: "member"}); err != nil {
		t.Fatalf("admin Create: %v", err)
	}
	ownerKey := env.createKey("owner-made", "viewer")

	env.suspend(env.wsA)
	if _, err := env.svc.Create(ctx, env.owner, env.wsA, CreateInput{Name: "while-suspended", Role: "viewer"}); !errors.Is(err, ErrSuspended) {
		t.Fatalf("suspended Create error = %v, want ErrSuspended", err)
	}
	if err := env.svc.Revoke(ctx, env.owner, env.wsA, ownerKey.Key.ID); !errors.Is(err, ErrSuspended) {
		t.Fatalf("suspended Revoke error = %v, want ErrSuspended", err)
	}

	keys, err := env.svc.List(ctx, env.owner, env.wsA)
	if err != nil {
		t.Fatalf("List while suspended: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("len(keys) while suspended = %d, want 2", len(keys))
	}
}

func TestService_CrossWorkspace(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()

	other, err := env.svc.Create(ctx, env.ownerB, env.wsB, CreateInput{Name: "b-key", Role: "viewer"})
	if err != nil {
		t.Fatalf("create key in workspace B: %v", err)
	}

	if err := env.svc.Revoke(ctx, env.owner, env.wsA, other.Key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace Revoke error = %v, want ErrNotFound", err)
	}
	if _, err := env.svc.List(ctx, env.owner, env.wsB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("List workspace B as non-member error = %v, want ErrNotFound", err)
	}
}

func TestService_AuditEvents(t *testing.T) {
	env := newServiceEnv(t)
	ctx := context.Background()

	created := env.createKey("audited", "member")
	var (
		kind        string
		detail      string
		actorUser   uuid.UUID
		actorAPIKey *uuid.UUID
	)
	if err := env.pool.QueryRow(ctx,
		`SELECT kind, detail::text, actor_user_id, actor_api_key_id
		   FROM security_events
		  WHERE workspace_id = $1 AND kind = 'api_key_created'`,
		env.wsA).Scan(&kind, &detail, &actorUser, &actorAPIKey); err != nil {
		t.Fatalf("load api_key_created event: %v", err)
	}
	if kind != "api_key_created" {
		t.Fatalf("kind = %q", kind)
	}
	if actorUser != env.owner {
		t.Fatalf("actor_user_id = %s, want %s", actorUser, env.owner)
	}
	if actorAPIKey != nil {
		t.Fatalf("actor_api_key_id = %s, want nil for a session actor", actorAPIKey)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(detail), &payload); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if payload["api_key_id"] != created.Key.ID.String() {
		t.Fatalf("detail api_key_id = %v, want %s", payload["api_key_id"], created.Key.ID)
	}
	if payload["name"] != "audited" {
		t.Fatalf("detail name = %v, want audited", payload["name"])
	}
	if payload["role"] != "member" {
		t.Fatalf("detail role = %v, want member", payload["role"])
	}
	if payload["prefix"] != created.Key.Prefix {
		t.Fatalf("detail prefix = %v, want %s", payload["prefix"], created.Key.Prefix)
	}
	if strings.Contains(detail, created.Secret) {
		t.Fatal("api_key_created detail contains the secret")
	}
	if bytes.Contains([]byte(detail), auth.HashToken(created.Secret)) {
		t.Fatal("api_key_created detail contains the token hash")
	}

	if err := env.svc.Revoke(ctx, env.owner, env.wsA, created.Key.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := env.pool.QueryRow(ctx,
		`SELECT detail::text FROM security_events
		  WHERE workspace_id = $1 AND kind = 'api_key_revoked'`,
		env.wsA).Scan(&detail); err != nil {
		t.Fatalf("load api_key_revoked event: %v", err)
	}
	var revoked map[string]any
	if err := json.Unmarshal([]byte(detail), &revoked); err != nil {
		t.Fatalf("decode revoked detail: %v", err)
	}
	if revoked["api_key_id"] != created.Key.ID.String() || revoked["name"] != "audited" {
		t.Fatalf("revoked detail = %v", revoked)
	}
	if strings.Contains(detail, created.Secret) {
		t.Fatal("api_key_revoked detail contains the secret")
	}
}

// requireFieldError asserts err is a validation error carrying field.
func requireFieldError(t *testing.T, err error, field string) {
	t.Helper()
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("error = %v, want ErrValidation", err)
	}
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T, want *ValidationError", err)
	}
	if validation.Fields[field] == "" {
		t.Fatalf("fields = %v, want a message for %q", validation.Fields, field)
	}
}

// Package workspaces implements multi-tenant workspaces, memberships, and
// role-based access control.
package workspaces

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/db/sqlc"
)

// Sentinel errors returned by the service. Handlers map them onto HTTP
// responses; errors.Is callers can branch on them.
var (
	// ErrForbidden is returned when the caller is a member but lacks the
	// required role.
	ErrForbidden = errors.New("forbidden")
	// ErrNotFound is returned when the workspace or target user does not
	// exist, including for workspaces the caller cannot see. Cross-tenant
	// lookups must not reveal whether a workspace exists.
	ErrNotFound = errors.New("not found")
	// ErrValidation is returned for invalid input, such as an empty name or
	// an unacceptable role.
	ErrValidation = errors.New("validation error")
)

const (
	// maxSlugLen caps generated slugs.
	maxSlugLen = 60
	// slugSuffixLen is the number of random letters appended to a taken slug.
	slugSuffixLen = 6
	// maxSlugRetries is how many random suffixes are tried after the base slug
	// collides.
	maxSlugRetries = 5
	// maxNameLen is the maximum workspace name length in characters.
	maxNameLen = 80
)

// Role is a workspace member's permission level. Higher values grant strictly
// more access.
type Role int

const (
	// RoleViewer can read workspace resources.
	RoleViewer Role = iota
	// RoleMember can modify workspace resources.
	RoleMember
	// RoleAdmin can manage members below their own role.
	RoleAdmin
	// RoleOwner has full control over the workspace.
	RoleOwner
)

// String returns the database and API representation of r.
func (r Role) String() string {
	switch r {
	case RoleViewer:
		return "viewer"
	case RoleMember:
		return "member"
	case RoleAdmin:
		return "admin"
	case RoleOwner:
		return "owner"
	default:
		return "unknown"
	}
}

// ParseRole parses a role name. Unknown names return ErrValidation.
func ParseRole(s string) (Role, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "viewer":
		return RoleViewer, nil
	case "member":
		return RoleMember, nil
	case "admin":
		return RoleAdmin, nil
	case "owner":
		return RoleOwner, nil
	default:
		return RoleViewer, fmt.Errorf("%w: invalid role", ErrValidation)
	}
}

// AtLeast reports whether r grants at least as much access as min.
func (r Role) AtLeast(min Role) bool {
	return r >= min
}

// valid reports whether r is one of the defined roles.
func (r Role) valid() bool {
	return r >= RoleViewer && r <= RoleOwner
}

// Workspace is a tenant visible to a user, together with that user's role.
type Workspace struct {
	ID   uuid.UUID
	Name string
	Slug string
	Role Role
}

// Member is a user's membership in a workspace.
type Member struct {
	UserID uuid.UUID
	Email  string
	Role   Role
}

// Service implements workspace creation, lookup, membership, and
// authorization on top of the workspace schema.
type Service struct {
	pool *pgxpool.Pool
	q    *sqlc.Queries
}

// NewService wires a Service together.
func NewService(pool *pgxpool.Pool, q *sqlc.Queries) *Service {
	return &Service{pool: pool, q: q}
}

// Create validates name, allocates a unique slug, and inserts the workspace
// and its owner membership in one transaction. If the slug is already taken,
// it retries with a random suffix.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, name string) (Workspace, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameLen {
		return Workspace{}, fmt.Errorf("%w: name must be between 1 and %d characters", ErrValidation, maxNameLen)
	}

	base := slugify(name)
	for attempt := 0; attempt <= maxSlugRetries; attempt++ {
		slug := base
		if attempt > 0 {
			suffix, err := randomSlugSuffix()
			if err != nil {
				return Workspace{}, fmt.Errorf("generate slug suffix: %w", err)
			}
			prefix := base
			if max := maxSlugLen - len(suffix) - 1; len(prefix) > max {
				prefix = strings.TrimRight(prefix[:max], "-")
			}
			slug = prefix + "-" + suffix
		}

		ws, err := s.create(ctx, userID, name, slug)
		if err == nil {
			return Workspace{ID: ws.ID, Name: ws.Name, Slug: ws.Slug, Role: RoleOwner}, nil
		}
		if isUniqueViolation(err) {
			continue
		}
		return Workspace{}, fmt.Errorf("create workspace: %w", err)
	}
	return Workspace{}, errors.New("create workspace: could not allocate a unique slug")
}

// create inserts one workspace and its owner membership atomically.
func (s *Service) create(ctx context.Context, userID uuid.UUID, name, slug string) (sqlc.Workspace, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return sqlc.Workspace{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	id, err := uuid.NewV7()
	if err != nil {
		return sqlc.Workspace{}, fmt.Errorf("generate workspace id: %w", err)
	}
	ws, err := qtx.CreateWorkspace(ctx, sqlc.CreateWorkspaceParams{
		ID:        id,
		Name:      name,
		Slug:      slug,
		CreatedBy: userID,
	})
	if err != nil {
		return sqlc.Workspace{}, err
	}
	if err := qtx.UpsertWorkspaceMember(ctx, sqlc.UpsertWorkspaceMemberParams{
		WorkspaceID: ws.ID,
		UserID:      userID,
		Role:        RoleOwner.String(),
	}); err != nil {
		return sqlc.Workspace{}, fmt.Errorf("create owner membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlc.Workspace{}, fmt.Errorf("commit workspace: %w", err)
	}
	return ws, nil
}

// Get returns a workspace after checking the caller has at least viewer
// access. Non-members get ErrNotFound.
func (s *Service) Get(ctx context.Context, userID, workspaceID uuid.UUID) (Workspace, error) {
	role, err := s.Authorize(ctx, userID, workspaceID, RoleViewer)
	if err != nil {
		return Workspace{}, err
	}
	ws, err := s.q.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Workspace{}, ErrNotFound
		}
		return Workspace{}, fmt.Errorf("load workspace: %w", err)
	}
	return Workspace{ID: ws.ID, Name: ws.Name, Slug: ws.Slug, Role: role}, nil
}

// ListForUser returns every workspace the user belongs to, with their role in
// each one.
func (s *Service) ListForUser(ctx context.Context, userID uuid.UUID) ([]Workspace, error) {
	rows, err := s.q.ListWorkspacesForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	out := make([]Workspace, 0, len(rows))
	for _, row := range rows {
		role, err := ParseRole(row.Role)
		if err != nil {
			return nil, fmt.Errorf("parse stored role: %w", err)
		}
		out = append(out, Workspace{ID: row.ID, Name: row.Name, Slug: row.Slug, Role: role})
	}
	return out, nil
}

// ListMembers returns the members of a workspace. Only viewers and above may
// list members; non-members get ErrNotFound.
func (s *Service) ListMembers(ctx context.Context, userID, workspaceID uuid.UUID) ([]Member, error) {
	if _, err := s.Authorize(ctx, userID, workspaceID, RoleViewer); err != nil {
		return nil, err
	}
	rows, err := s.q.ListWorkspaceMembers(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list workspace members: %w", err)
	}
	out := make([]Member, 0, len(rows))
	for _, row := range rows {
		role, err := ParseRole(row.Role)
		if err != nil {
			return nil, fmt.Errorf("parse stored role: %w", err)
		}
		out = append(out, Member{UserID: row.UserID, Email: row.Email, Role: role})
	}
	return out, nil
}

// AddMember adds or updates a workspace membership. The actor must be an
// admin; only an owner may assign a role at or above their own. Owner grants
// are always rejected because ownership transfer is out of scope for V1.
func (s *Service) AddMember(ctx context.Context, actorID, workspaceID uuid.UUID, email string, role Role) error {
	actorRole, err := s.Authorize(ctx, actorID, workspaceID, RoleAdmin)
	if err != nil {
		return err
	}
	// Validate the requested role before looking up the target account so a
	// rejected owner grant cannot leak whether the email exists.
	if !role.valid() || role == RoleOwner {
		return fmt.Errorf("%w: cannot assign role %q", ErrValidation, role)
	}
	if actorRole != RoleOwner && role.AtLeast(actorRole) {
		return ErrForbidden
	}

	user, err := s.q.GetUserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load user by email: %w", err)
	}
	if err := s.q.UpsertWorkspaceMember(ctx, sqlc.UpsertWorkspaceMemberParams{
		WorkspaceID: workspaceID,
		UserID:      user.ID,
		Role:        role.String(),
	}); err != nil {
		return fmt.Errorf("upsert workspace member: %w", err)
	}
	return nil
}

// Authorize returns the caller's role when it is at least min. Non-members get
// ErrNotFound so cross-tenant probes cannot distinguish missing workspaces
// from forbidden ones; members with an insufficient role get ErrForbidden.
func (s *Service) Authorize(ctx context.Context, userID, workspaceID uuid.UUID, min Role) (Role, error) {
	member, err := s.q.GetWorkspaceMember(ctx, sqlc.GetWorkspaceMemberParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RoleViewer, ErrNotFound
		}
		return RoleViewer, fmt.Errorf("load workspace membership: %w", err)
	}
	role, err := ParseRole(member.Role)
	if err != nil {
		return RoleViewer, fmt.Errorf("parse stored role: %w", err)
	}
	if !role.AtLeast(min) {
		return role, ErrForbidden
	}
	return role, nil
}

// slugify converts a workspace name to a URL-safe slug: lowercase ASCII
// letters and digits, with every other run replaced by a single dash. The
// result is trimmed and capped at maxSlugLen.
func slugify(name string) string {
	var b strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteRune(r)
		default:
			pendingDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > maxSlugLen {
		slug = strings.TrimRight(slug[:maxSlugLen], "-")
	}
	if slug == "" {
		return "workspace"
	}
	return slug
}

// randomSlugSuffix returns slugSuffixLen random lowercase ASCII letters.
func randomSlugSuffix() (string, error) {
	var buf [slugSuffixLen]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = 'a' + buf[i]%26
	}
	return string(buf[:]), nil
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

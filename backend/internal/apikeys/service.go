package apikeys

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/audit"
	"o1/internal/auth"
	"o1/internal/db/sqlc"
	"o1/internal/workspaces"
)

const (
	// keyVersionPrefix is the fixed scheme marker on every API key.
	keyVersionPrefix = "o1"
	// prefixBytes is the entropy source for the public key prefix.
	prefixBytes = 9
	// prefixLen is the exact length of the stored, publicly visible prefix.
	prefixLen = 12
	// secretBytes is the entropy source for the secret half of the key.
	secretBytes = 32
	// maxPrefixRetries is how many fresh prefixes are tried after a collision.
	maxPrefixRetries = 5
	// maxActiveKeys caps active keys per workspace.
	maxActiveKeys = 20
	// maxNameLen is the maximum key name length in characters.
	maxNameLen = 60

	// unique constraint names used to classify insert failures.
	prefixConstraint    = "api_keys_prefix_key"
	tokenHashConstraint = "api_keys_token_hash_key" //nolint:gosec // PostgreSQL constraint name, not a credential.
	nameConstraint      = "api_keys_workspace_active_name_key"
)

// Service owns API key lifecycle management. Authentication with an existing
// key lives in the auth package.
type Service struct {
	pool *pgxpool.Pool
	q    *sqlc.Queries
	ws   *workspaces.Service
}

// NewService wires a Service together.
func NewService(pool *pgxpool.Pool, q *sqlc.Queries, ws *workspaces.Service) *Service {
	return &Service{pool: pool, q: q, ws: ws}
}

// List returns the active keys of a workspace, newest first, with prefixes
// rendered as o1_<prefix>. It never returns secrets or token hashes and keeps
// working while the workspace is suspended.
func (s *Service) List(ctx context.Context, actorID, workspaceID uuid.UUID) ([]Key, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleAdmin); err != nil {
		return nil, err
	}
	rows, err := s.q.ListActiveAPIKeys(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	out := make([]Key, 0, len(rows))
	for _, row := range rows {
		out = append(out, keyView(row))
	}
	return out, nil
}

// Create validates the request, allocates a fresh key, and returns its
// plaintext secret exactly once. Only admins and owners may create keys, and
// suspended workspaces are rejected.
func (s *Service) Create(ctx context.Context, actorID, workspaceID uuid.UUID, in CreateInput) (CreatedKey, error) {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleAdmin); err != nil {
		return CreatedKey{}, err
	}
	name, err := validateName(in.Name)
	if err != nil {
		return CreatedKey{}, err
	}
	role, err := validateRole(in.Role)
	if err != nil {
		return CreatedKey{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CreatedKey{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	locked, err := qtx.LockWorkspace(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CreatedKey{}, ErrNotFound
		}
		return CreatedKey{}, fmt.Errorf("lock workspace: %w", err)
	}
	if locked.SuspendedAt != nil {
		return CreatedKey{}, ErrSuspended
	}

	active, err := qtx.CountActiveAPIKeys(ctx, workspaceID)
	if err != nil {
		return CreatedKey{}, fmt.Errorf("count api keys: %w", err)
	}
	if active >= maxActiveKeys {
		return CreatedKey{}, ErrLimit
	}

	created, secret, err := insertKey(ctx, tx, qtx, workspaceID, actorID, name, role)
	if err != nil {
		return CreatedKey{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CreatedKey{}, fmt.Errorf("commit transaction: %w", err)
	}

	view := keyView(created)
	audit.RecordBestEffort(ctx, s.q, audit.Event{
		WorkspaceID:   &workspaceID,
		ActorUserID:   &actorID,
		ActorAPIKeyID: auth.APIKeyIDFrom(ctx),
		Kind:          audit.KindAPIKeyCreated,
		Detail: map[string]any{
			"api_key_id": view.ID.String(),
			"name":       view.Name,
			"role":       view.Role,
			"prefix":     view.Prefix,
		},
	})
	return CreatedKey{Key: view, Secret: secret}, nil
}

// insertKey tries fresh key material until the prefix is unique. A name
// conflict reports ErrNameTaken. Savepoints keep a prefix collision from
// aborting the surrounding transaction.
func insertKey(
	ctx context.Context,
	tx pgx.Tx,
	qtx *sqlc.Queries,
	workspaceID, actorID uuid.UUID,
	name, role string,
) (sqlc.APIKey, string, error) {
	var lastErr error
	for attempt := 0; attempt <= maxPrefixRetries; attempt++ {
		prefix, _, fullKey, hash, err := newKeyMaterial()
		if err != nil {
			return sqlc.APIKey{}, "", err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return sqlc.APIKey{}, "", fmt.Errorf("generate api key id: %w", err)
		}

		sp, err := tx.Begin(ctx)
		if err != nil {
			return sqlc.APIKey{}, "", fmt.Errorf("begin savepoint: %w", err)
		}
		created, err := qtx.WithTx(sp).CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{
			ID:          id,
			WorkspaceID: workspaceID,
			Name:        name,
			Prefix:      prefix,
			TokenHash:   hash,
			Role:        role,
			CreatedBy:   actorID,
		})
		if err == nil {
			if err := sp.Commit(ctx); err != nil {
				return sqlc.APIKey{}, "", fmt.Errorf("commit savepoint: %w", err)
			}
			return created, fullKey, nil
		}
		_ = sp.Rollback(ctx)
		lastErr = err

		switch uniqueConstraint(err) {
		case nameConstraint:
			return sqlc.APIKey{}, "", ErrNameTaken
		case prefixConstraint, tokenHashConstraint:
			// Try fresh key material.
			continue
		default:
			return sqlc.APIKey{}, "", fmt.Errorf("create api key: %w", err)
		}
	}
	return sqlc.APIKey{}, "", fmt.Errorf("create api key: could not allocate a unique prefix: %w", lastErr)
}

// Revoke marks a key revoked. Non-admins are forbidden, unknown or
// cross-workspace keys are not found, and revoking an already revoked key is
// an idempotent success.
func (s *Service) Revoke(ctx context.Context, actorID, workspaceID, keyID uuid.UUID) error {
	if _, err := s.ws.Authorize(ctx, actorID, workspaceID, workspaces.RoleAdmin); err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := s.q.WithTx(tx)

	locked, err := qtx.LockWorkspace(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock workspace: %w", err)
	}
	if locked.SuspendedAt != nil {
		return ErrSuspended
	}

	existing, err := qtx.GetAPIKey(ctx, sqlc.GetAPIKeyParams{ID: keyID, WorkspaceID: workspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load api key: %w", err)
	}
	rows, err := qtx.RevokeAPIKey(ctx, sqlc.RevokeAPIKeyParams{ID: keyID, WorkspaceID: workspaceID})
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	if rows == 0 {
		// Already revoked earlier: idempotent success without a duplicate event.
		return nil
	}

	audit.RecordBestEffort(ctx, s.q, audit.Event{
		WorkspaceID:   &workspaceID,
		ActorUserID:   &actorID,
		ActorAPIKeyID: auth.APIKeyIDFrom(ctx),
		Kind:          audit.KindAPIKeyRevoked,
		Detail: map[string]any{
			"api_key_id": keyID.String(),
			"name":       existing.Name,
		},
	})
	return nil
}

// keyView maps a stored row to the client-facing representation.
func keyView(row sqlc.APIKey) Key {
	return Key{
		ID:         row.ID,
		Name:       row.Name,
		Prefix:     keyVersionPrefix + "_" + row.Prefix,
		Role:       row.Role,
		CreatedBy:  row.CreatedBy,
		CreatedAt:  row.CreatedAt,
		LastUsedAt: row.LastUsedAt,
	}
}

// validateName trims raw and checks that it is 1–60 characters.
func validateName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameLen {
		return "", &ValidationError{Fields: map[string]string{
			"name": fmt.Sprintf("name must be between 1 and %d characters", maxNameLen),
		}}
	}
	return name, nil
}

// validateRole accepts only the viewer and member roles.
func validateRole(raw string) (string, error) {
	switch role := strings.ToLower(strings.TrimSpace(raw)); role {
	case "viewer", "member":
		return role, nil
	default:
		return "", &ValidationError{Fields: map[string]string{
			"role": "role must be viewer or member",
		}}
	}
}

// newKeyMaterial generates a prefix and secret. The full key is
// o1_<prefix>_<secret>; only the prefix and the hash are stored.
func newKeyMaterial() (prefix, secret, fullKey string, hash []byte, err error) {
	var raw [prefixBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", "", nil, fmt.Errorf("generate api key prefix: %w", err)
	}
	prefix = new(big.Int).SetBytes(raw[:]).Text(36)
	if len(prefix) > prefixLen {
		prefix = prefix[len(prefix)-prefixLen:]
	}
	for len(prefix) < prefixLen {
		prefix = "0" + prefix
	}

	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", nil, fmt.Errorf("generate api key secret: %w", err)
	}
	secret = base64.RawURLEncoding.EncodeToString(buf)
	fullKey = keyVersionPrefix + "_" + prefix + "_" + secret
	return prefix, secret, fullKey, auth.HashToken(fullKey), nil
}

// uniqueConstraint returns the violated unique constraint name, or "" when
// err is not a PostgreSQL unique violation.
func uniqueConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName
	}
	return ""
}

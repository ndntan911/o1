package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"o1/internal/db/sqlc"
	"o1/internal/testutil"
)

func TestCreateUser_DuplicateEmailIsCaseInsensitive(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	q := sqlc.New(pool)
	ctx := context.Background()
	id, _ := uuid.NewV7()
	if _, err := q.CreateUser(ctx, sqlc.CreateUserParams{ID: id, Email: "User@Example.com", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	id2, _ := uuid.NewV7()
	if _, err := q.CreateUser(ctx, sqlc.CreateUserParams{ID: id2, Email: "user@example.com", PasswordHash: "x"}); err == nil {
		t.Fatal("expected unique violation for case-insensitive duplicate email")
	}
}

func TestGetUserByEmail_NormalizesCase(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	q := sqlc.New(pool)
	ctx := context.Background()
	id, _ := uuid.NewV7()
	if _, err := q.CreateUser(ctx, sqlc.CreateUserParams{ID: id, Email: "user@example.com", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetUserByEmail(ctx, "User@Example.com"); err != nil {
		t.Fatalf("expected case-insensitive lookup: %v", err)
	}
}

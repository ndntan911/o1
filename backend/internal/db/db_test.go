package db_test

import (
	"context"
	"testing"

	"o1/internal/db"
	"o1/internal/testutil"
)

func TestMigrate_IsIdempotent(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()
	if err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("second migrate failed: %v", err)
	}
}

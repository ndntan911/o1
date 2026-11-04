// Package testutil provides helpers shared by integration tests. Helpers
// that need external services skip themselves in short test mode.
package testutil

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"o1/internal/db"
)

const postgresImage = "postgres:16-alpine"

// NewPostgres starts a disposable Postgres 16 container, opens a connection
// pool, and applies all migrations. In short test mode it skips: unit tests
// must not require Docker. The caller must invoke the returned cleanup
// function (or rely on the registered t.Cleanup) to stop the container.
func NewPostgres(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}

	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        postgresImage,
			Env:          map[string]string{"POSTGRES_USER": "postgres", "POSTGRES_PASSWORD": "postgres", "POSTGRES_DB": "postgres"},
			ExposedPorts: []string{"5432/tcp"},
			AutoRemove:   true,
			WaitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}

	var terminateOnce sync.Once
	terminate := func() {
		terminateOnce.Do(func() {
			if err := ctr.Terminate(ctx); err != nil {
				t.Logf("terminate postgres container: %v", err)
			}
		})
	}

	host, err := ctr.Host(ctx)
	if err != nil {
		terminate()
		t.Fatalf("get container host: %v", err)
	}
	port, err := ctr.MappedPort(ctx, "5432/tcp")
	if err != nil {
		terminate()
		t.Fatalf("get mapped port: %v", err)
	}
	databaseURL := fmt.Sprintf("postgres://postgres:postgres@%s:%s/postgres?sslmode=disable", host, port.Port())

	pool, err := db.Open(ctx, databaseURL)
	if err != nil {
		terminate()
		t.Fatalf("open pool: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		terminate()
		t.Fatalf("migrate: %v", err)
	}

	cleanup := func() {
		pool.Close()
		terminate()
	}
	t.Cleanup(cleanup)
	return pool, cleanup
}

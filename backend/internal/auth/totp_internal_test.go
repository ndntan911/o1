package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"o1/internal/db"
	"o1/internal/db/sqlc"
)

// newTOTPTestPool starts a disposable Postgres for internal tests. It cannot
// import internal/testutil because that package imports auth, which would form
// an import cycle for a package auth test file.
func newTOTPTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:16-alpine",
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
	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatalf("get container host: %v", err)
	}
	port, err := ctr.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("get mapped port: %v", err)
	}
	databaseURL := fmt.Sprintf("postgres://postgres:postgres@%s:%s/postgres?sslmode=disable", host, port.Port())
	pool, err := db.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if err := ctr.Terminate(context.Background()); err != nil {
			t.Logf("terminate postgres container: %v", err)
		}
	})
	return pool
}

func TestTOTPVerify_SkewAndReplay(t *testing.T) {
	ctx := context.Background()
	pool := newTOTPTestPool(t)
	q := sqlc.New(pool)

	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	userID, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateUser(ctx, sqlc.CreateUserParams{
		ID:           userID,
		Email:        "totp-internal@example.com",
		PasswordHash: hash,
	}); err != nil {
		t.Fatal(err)
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "o1 uptime",
		AccountName: "totp-internal@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	step := now.Unix() / 30

	opts := totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}
	for _, offset := range []int64{-1, 0, 1} {
		code, err := totp.GenerateCodeCustom(key.Secret(), time.Unix((step+offset)*30, 0), opts)
		if err != nil {
			t.Fatal(err)
		}
		got, ok, err := matchTOTPStep(key.Secret(), code, now)
		if err != nil {
			t.Fatal(err)
		}
		if !ok || got != step+offset {
			t.Fatalf("offset %d: got (%d, %v), want (%d, true)", offset, got, ok, step+offset)
		}
	}

	for _, offset := range []int64{-2, 2} {
		code, err := totp.GenerateCodeCustom(key.Secret(), time.Unix((step+offset)*30, 0), opts)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, err := matchTOTPStep(key.Secret(), code, now); err != nil || ok {
			t.Fatalf("offset %d: accepted=%v err=%v, want rejected", offset, ok, err)
		}
	}

	used, err := useTOTPStep(ctx, q, userID, step)
	if err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Fatal("first use of step was rejected")
	}
	used, err = useTOTPStep(ctx, q, userID, step)
	if err != nil {
		t.Fatal(err)
	}
	if used {
		t.Fatal("replayed step was accepted")
	}
}

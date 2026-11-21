package db

import (
	"strings"
	"testing"
)

// TestEmbeddedMigrationsUseGooseHeaders guards the file format itself.
//
// goose only parses a migration when it runs it, so a file without the
// "-- +goose Up" header is discovered by whichever deployment starts next —
// with an error that names the statement, not the file. This check fails in CI
// instead. It complements TestEmbeddedMigrationsParse, which proves the files
// can be collected.
func TestEmbeddedMigrationsUseGooseHeaders(t *testing.T) {
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}

	checked := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		checked++

		raw, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		body := strings.ReplaceAll(string(raw), "\r\n", "\n")

		if !strings.HasPrefix(strings.TrimSpace(body), "-- +goose Up") {
			t.Errorf("%s must start with a \"-- +goose Up\" header", entry.Name())
		}
		if !strings.Contains(body, "-- +goose Down") {
			t.Errorf("%s must contain a \"-- +goose Down\" section", entry.Name())
		}
	}

	if checked == 0 {
		t.Fatal("no migrations were checked")
	}
}

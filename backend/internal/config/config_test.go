package config_test

import (
	"encoding/base64"
	"testing"

	"o1/internal/config"
)

func TestLoad_RequiresDatabaseURL(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if _, err := config.Load(); err == nil {
		t.Fatal("expected error when O1_DATABASE_URL is empty")
	}
}

func TestLoad_RejectsShortSecretKey(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://localhost/o1")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString([]byte("short")))
	if _, err := config.Load(); err == nil {
		t.Fatal("expected error when secret key is not 32 bytes")
	}
}

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://localhost/o1")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_ENV", "development")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.SessionCookieName != "o1_session" {
		t.Fatalf("cookie = %q", cfg.SessionCookieName)
	}
	if cfg.SessionTTL.String() != "720h0m0s" {
		t.Fatalf("ttl = %v", cfg.SessionTTL)
	}
}

func TestLoad_RejectsUnknownEnv(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://localhost/o1")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_ENV", "prod")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected error for O1_ENV=prod")
	}
}

func TestLoad_AcceptsKnownEnvs(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://localhost/o1")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	for _, env := range []string{"development", "test", "production"} {
		t.Setenv("O1_ENV", env)
		if _, err := config.Load(); err != nil {
			t.Fatalf("O1_ENV=%s: %v", env, err)
		}
	}
}

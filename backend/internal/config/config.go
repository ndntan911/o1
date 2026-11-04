package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config holds all runtime configuration for the application. Values come
// from environment variables with the O1_ prefix.
type Config struct {
	Env               string
	HTTPAddr          string
	DatabaseURL       string
	AppBaseURL        string
	AllowedOrigins    []string
	SessionCookieName string
	SessionTTL        time.Duration
	SecretKey         []byte
	SMTPAddr          string
	EmailFrom         string
	ResendAPIKey      string
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// Load reads configuration from the environment. It enforces
// O1_DATABASE_URL and O1_SECRET_KEY (base64-encoded, exactly 32 bytes).
func Load() (Config, error) {
	cfg := Config{
		Env:               env("O1_ENV", "development"),
		HTTPAddr:          env("O1_HTTP_ADDR", ":8080"),
		DatabaseURL:       env("O1_DATABASE_URL", ""),
		AppBaseURL:        env("O1_APP_BASE_URL", "http://localhost:5173"),
		SessionCookieName: env("O1_SESSION_COOKIE_NAME", "o1_session"),
		SessionTTL:        30 * 24 * time.Hour,
		SMTPAddr:          env("O1_SMTP_ADDR", "localhost:1025"),
		EmailFrom:         env("O1_EMAIL_FROM", "o1@localhost"),
		ResendAPIKey:      os.Getenv("O1_RESEND_API_KEY"),
	}

	if cfg.DatabaseURL == "" {
		return cfg, fmt.Errorf("missing required environment variable O1_DATABASE_URL")
	}

	raw := env("O1_ALLOWED_ORIGINS", "")
	for _, origin := range strings.Split(raw, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			cfg.AllowedOrigins = append(cfg.AllowedOrigins, origin)
		}
	}

	if v := env("O1_SESSION_TTL", "720h"); v != "" {
		ttl, err := time.ParseDuration(v)
		if err != nil {
			return cfg, fmt.Errorf("invalid O1_SESSION_TTL %q: %w", v, err)
		}
		cfg.SessionTTL = ttl
	}

	rawKey := env("O1_SECRET_KEY", "")
	key, err := base64.StdEncoding.DecodeString(rawKey)
	if err != nil {
		return cfg, fmt.Errorf("invalid O1_SECRET_KEY: must be base64-encoded: %w", err)
	}
	if len(key) != 32 {
		return cfg, fmt.Errorf("invalid O1_SECRET_KEY: expected 32 bytes after base64 decoding, got %d", len(key))
	}
	cfg.SecretKey = key

	return cfg, nil
}

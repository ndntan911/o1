package config

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
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
	EncryptionKey     []byte
	// EncryptionKeyDerived is true when no O1_ENCRYPTION_KEY was supplied and
	// the key was derived from O1_SECRET_KEY (development and tests only).
	EncryptionKeyDerived bool
	SMTPAddr             string
	EmailFrom            string
	ResendAPIKey         string

	MonitorMinInterval time.Duration
	MonitorMaxInterval time.Duration
	MonitorCreateLimit int

	ProbeAllowedPorts []int
	ProbeMaxRedirects int
	ProbeMaxBodyBytes int64
	ProbeGlobalRPS    float64

	SchedulerTick       time.Duration
	SchedulerBatch      int
	WorkerConcurrency   int
	SchedulerHealthAddr string
	WorkerHealthAddr    string
	BootstrapAdminEmail string
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	v := env(key, "")
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", key, v, err)
	}
	return n, nil
}

func envInt64(key string, fallback int64) (int64, error) {
	v := env(key, "")
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", key, v, err)
	}
	return n, nil
}

func envFloat(key string, fallback float64) (float64, error) {
	v := env(key, "")
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", key, v, err)
	}
	return n, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := env(key, "")
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", key, v, err)
	}
	return d, nil
}

func envPorts(key string, fallback []int) ([]int, error) {
	raw := env(key, "")
	if raw == "" {
		return fallback, nil
	}
	parts := strings.Split(raw, ",")
	ports := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("invalid %s %q: empty port", key, raw)
		}
		port, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", key, raw, err)
		}
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid %s %q: port %d out of range 1-65535", key, raw, port)
		}
		ports = append(ports, port)
	}
	return ports, nil
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

		SchedulerHealthAddr: env("O1_SCHEDULER_HEALTH_ADDR", ":8081"),
		WorkerHealthAddr:    env("O1_WORKER_HEALTH_ADDR", ":8082"),
		BootstrapAdminEmail: env("O1_BOOTSTRAP_ADMIN_EMAIL", ""),
	}

	switch cfg.Env {
	case "development", "test", "production":
	default:
		return cfg, fmt.Errorf("invalid O1_ENV %q: must be one of development, test, production", cfg.Env)
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

	if cfg.EncryptionKey, cfg.EncryptionKeyDerived, err = loadEncryptionKey(cfg); err != nil {
		return cfg, err
	}

	if cfg.MonitorMinInterval, err = envDuration("O1_MONITOR_MIN_INTERVAL", 60*time.Second); err != nil {
		return cfg, err
	}
	if cfg.MonitorMaxInterval, err = envDuration("O1_MONITOR_MAX_INTERVAL", 24*time.Hour); err != nil {
		return cfg, err
	}
	if cfg.MonitorCreateLimit, err = envInt("O1_MONITOR_CREATE_LIMIT", 10); err != nil {
		return cfg, err
	}
	if cfg.ProbeAllowedPorts, err = envPorts("O1_PROBE_ALLOWED_PORTS", []int{80, 443}); err != nil {
		return cfg, err
	}
	if cfg.ProbeMaxRedirects, err = envInt("O1_PROBE_MAX_REDIRECTS", 5); err != nil {
		return cfg, err
	}
	if cfg.ProbeMaxBodyBytes, err = envInt64("O1_PROBE_MAX_BODY_BYTES", 1<<20); err != nil {
		return cfg, err
	}
	if cfg.ProbeGlobalRPS, err = envFloat("O1_PROBE_GLOBAL_RPS", 50); err != nil {
		return cfg, err
	}
	if cfg.SchedulerTick, err = envDuration("O1_SCHEDULER_TICK", 10*time.Second); err != nil {
		return cfg, err
	}
	if cfg.SchedulerBatch, err = envInt("O1_SCHEDULER_BATCH", 500); err != nil {
		return cfg, err
	}
	if cfg.WorkerConcurrency, err = envInt("O1_WORKER_CONCURRENCY", 50); err != nil {
		return cfg, err
	}

	if err := cfg.validate(); err != nil {
		return cfg, err
	}

	return cfg, nil
}

func loadEncryptionKey(cfg Config) ([]byte, bool, error) {
	if raw := env("O1_ENCRYPTION_KEY", ""); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, false, fmt.Errorf("invalid O1_ENCRYPTION_KEY: must be base64-encoded: %w", err)
		}
		if len(key) != 32 {
			return nil, false, fmt.Errorf("invalid O1_ENCRYPTION_KEY: expected 32 bytes after base64 decoding, got %d", len(key))
		}
		return key, false, nil
	}
	if cfg.Env == "production" {
		return nil, false, fmt.Errorf("missing required environment variable O1_ENCRYPTION_KEY in production")
	}
	key, err := hkdf.Key(sha256.New, cfg.SecretKey, nil, "o1:tenant-secrets:v1", 32)
	if err != nil {
		return nil, false, fmt.Errorf("derive encryption key from O1_SECRET_KEY: %w", err)
	}
	return key, true, nil
}

func (cfg Config) validate() error {
	if cfg.MonitorMinInterval < 60*time.Second || cfg.MonitorMaxInterval > 24*time.Hour {
		return fmt.Errorf("invalid monitor intervals: must be between 60s and 24h")
	}
	if cfg.MonitorMinInterval > cfg.MonitorMaxInterval {
		return fmt.Errorf("invalid monitor intervals: minimum %v exceeds maximum %v", cfg.MonitorMinInterval, cfg.MonitorMaxInterval)
	}
	if cfg.MonitorCreateLimit <= 0 {
		return fmt.Errorf("invalid O1_MONITOR_CREATE_LIMIT: must be positive")
	}
	if cfg.ProbeMaxRedirects < 0 || cfg.ProbeMaxRedirects > 10 {
		return fmt.Errorf("invalid O1_PROBE_MAX_REDIRECTS: must be between 0 and 10")
	}
	if cfg.ProbeMaxBodyBytes <= 0 {
		return fmt.Errorf("invalid O1_PROBE_MAX_BODY_BYTES: must be positive")
	}
	if cfg.ProbeGlobalRPS <= 0 {
		return fmt.Errorf("invalid O1_PROBE_GLOBAL_RPS: must be positive")
	}
	if cfg.SchedulerTick <= 0 {
		return fmt.Errorf("invalid O1_SCHEDULER_TICK: must be positive")
	}
	if cfg.SchedulerBatch <= 0 {
		return fmt.Errorf("invalid O1_SCHEDULER_BATCH: must be positive")
	}
	if cfg.WorkerConcurrency <= 0 {
		return fmt.Errorf("invalid O1_WORKER_CONCURRENCY: must be positive")
	}
	return nil
}

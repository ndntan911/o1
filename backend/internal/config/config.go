package config

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"
)

// Supported transactional email providers.
const (
	// EmailProviderMailpit delivers over the local Mailpit SMTP server.
	EmailProviderMailpit = "mailpit"
	// EmailProviderResend delivers through the Resend HTTP API.
	EmailProviderResend = "resend"
)

// defaultResendAPIURL is used when O1_RESEND_API_URL is unset.
const defaultResendAPIURL = "https://api.resend.com"

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
	// EmailProvider is the resolved transactional email provider, either
	// EmailProviderMailpit or EmailProviderResend.
	EmailProvider string
	// EmailReplyTo is the optional Reply-To mailbox; empty means none.
	EmailReplyTo string
	// ResendAPIURL is the Resend API base URL.
	ResendAPIURL string
	// ResendRPS and ResendBurst configure the per-process Resend rate limiter.
	ResendRPS   float64
	ResendBurst int

	// AuthRatePerMinute and AuthRateBurst configure the per-IP token bucket
	// guarding the credential-handling auth endpoints. Compose raises them
	// for local testing; production keeps the 10/10 defaults.
	AuthRatePerMinute int
	AuthRateBurst     int

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

	// Observability. Empty endpoints/DSNs disable the corresponding
	// integration; defaults keep all three binaries fully functional.
	APIHealthAddr        string
	OTELExporterEndpoint string
	OTELExporterHeaders  map[string]string
	OTELSampleRatio      float64
	SentryDSN            string
	SentryEnvironment    string
	Release              string
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

// envHeaders parses a comma-separated list of key=value pairs. Values are
// secret (OTLP auth headers), so error messages never include them.
func envHeaders(key string) (map[string]string, error) {
	raw := env(key, "")
	if raw == "" {
		return nil, nil
	}
	headers := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			return nil, fmt.Errorf("invalid %s: empty header pair", key)
		}
		name, value, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("invalid %s: header pair missing '='", key)
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" || value == "" {
			return nil, fmt.Errorf("invalid %s: header name and value must be non-empty", key)
		}
		headers[name] = value
	}
	return headers, nil
}

// envSampleRatio parses a trace sampling ratio, rejects non-finite values, and
// clamps it to [0, 1].
func envSampleRatio(key string, fallback float64) (float64, error) {
	v := env(key, "")
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", key, v, err)
	}
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, fmt.Errorf("invalid %s %q: must be finite", key, v)
	}
	if n < 0 {
		return 0, nil
	}
	if n > 1 {
		return 1, nil
	}
	return n, nil
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
		EmailProvider:     strings.ToLower(strings.TrimSpace(env("O1_EMAIL_PROVIDER", ""))),
		EmailReplyTo:      strings.TrimSpace(env("O1_EMAIL_REPLY_TO", "")),
		ResendAPIURL:      env("O1_RESEND_API_URL", defaultResendAPIURL),

		SchedulerHealthAddr: env("O1_SCHEDULER_HEALTH_ADDR", ":8081"),
		WorkerHealthAddr:    env("O1_WORKER_HEALTH_ADDR", ":8082"),
		BootstrapAdminEmail: env("O1_BOOTSTRAP_ADMIN_EMAIL", ""),

		APIHealthAddr:        env("O1_API_HEALTH_ADDR", ":8083"),
		OTELExporterEndpoint: env("O1_OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		SentryDSN:            env("O1_SENTRY_DSN", ""),
		SentryEnvironment:    env("O1_SENTRY_ENVIRONMENT", ""),
		Release:              env("O1_RELEASE", "dev"),
	}

	switch cfg.Env {
	case "development", "test", "production":
	default:
		return cfg, fmt.Errorf("invalid O1_ENV %q: must be one of development, test, production", cfg.Env)
	}
	// The o1@localhost sender is a development convenience. Production must
	// configure a real address, so a missing O1_EMAIL_FROM fails validation
	// instead of silently starting with an undeliverable mailbox.
	if cfg.Env == "production" && strings.TrimSpace(os.Getenv("O1_EMAIL_FROM")) == "" {
		cfg.EmailFrom = ""
	}
	if cfg.SentryEnvironment == "" {
		cfg.SentryEnvironment = cfg.Env
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
	if len(cfg.AllowedOrigins) == 0 && cfg.AppBaseURL != "" {
		cfg.AllowedOrigins = []string{cfg.AppBaseURL}
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
	if cfg.AuthRatePerMinute, err = envInt("O1_AUTH_RATE_PER_MINUTE", 10); err != nil {
		return cfg, err
	}
	if cfg.AuthRateBurst, err = envInt("O1_AUTH_RATE_BURST", 10); err != nil {
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
	if cfg.OTELExporterHeaders, err = envHeaders("O1_OTEL_EXPORTER_OTLP_HEADERS"); err != nil {
		return cfg, err
	}
	if cfg.OTELSampleRatio, err = envSampleRatio("O1_OTEL_SAMPLE_RATIO", 0.05); err != nil {
		return cfg, err
	}
	if cfg.ResendRPS, err = envFloat("O1_RESEND_RPS", 2); err != nil {
		return cfg, err
	}
	if cfg.ResendBurst, err = envInt("O1_RESEND_BURST", 2); err != nil {
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

func (cfg *Config) validate() error {
	if cfg.MonitorMinInterval < 60*time.Second || cfg.MonitorMaxInterval > 24*time.Hour {
		return fmt.Errorf("invalid monitor intervals: must be between 60s and 24h")
	}
	if cfg.MonitorMinInterval > cfg.MonitorMaxInterval {
		return fmt.Errorf("invalid monitor intervals: minimum %v exceeds maximum %v", cfg.MonitorMinInterval, cfg.MonitorMaxInterval)
	}
	if cfg.MonitorCreateLimit <= 0 {
		return fmt.Errorf("invalid O1_MONITOR_CREATE_LIMIT: must be positive")
	}
	if cfg.AuthRatePerMinute < 1 {
		return fmt.Errorf("invalid O1_AUTH_RATE_PER_MINUTE: must be at least 1, got %d", cfg.AuthRatePerMinute)
	}
	if cfg.AuthRateBurst < 1 {
		return fmt.Errorf("invalid O1_AUTH_RATE_BURST: must be at least 1, got %d", cfg.AuthRateBurst)
	}
	if cfg.ProbeMaxRedirects < 0 || cfg.ProbeMaxRedirects > 10 {
		return fmt.Errorf("invalid O1_PROBE_MAX_REDIRECTS: must be between 0 and 10")
	}
	if cfg.ProbeMaxBodyBytes <= 0 {
		return fmt.Errorf("invalid O1_PROBE_MAX_BODY_BYTES: must be positive")
	}
	if cfg.ProbeGlobalRPS <= 0 || math.IsNaN(cfg.ProbeGlobalRPS) || math.IsInf(cfg.ProbeGlobalRPS, 0) {
		return fmt.Errorf("invalid O1_PROBE_GLOBAL_RPS: must be a finite positive number")
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

	provider, err := cfg.ResolveEmailProvider()
	if err != nil {
		return err
	}
	cfg.EmailProvider = provider
	if cfg.ResendRPS <= 0 || math.IsNaN(cfg.ResendRPS) || math.IsInf(cfg.ResendRPS, 0) {
		return fmt.Errorf("invalid O1_RESEND_RPS: must be a finite positive number")
	}
	if cfg.ResendBurst < 1 {
		return fmt.Errorf("invalid O1_RESEND_BURST: must be at least 1, got %d", cfg.ResendBurst)
	}
	return nil
}

// ResolveEmailProvider returns the effective email provider for cfg, applying
// the defaults (mailpit outside production, resend in production) and checking
// that the provider has the credentials and addresses it needs. It does not
// modify cfg; callers that need the resolved value should assign it back to
// EmailProvider. Error messages never include credentials or addresses.
func (cfg Config) ResolveEmailProvider() (string, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.EmailProvider))
	switch provider {
	case "":
		if cfg.Env == "production" {
			provider = EmailProviderResend
		} else {
			provider = EmailProviderMailpit
		}
	case EmailProviderMailpit, EmailProviderResend:
	default:
		return "", fmt.Errorf("invalid O1_EMAIL_PROVIDER %q: must be one of mailpit, resend", cfg.EmailProvider)
	}

	switch provider {
	case EmailProviderMailpit:
		if cfg.Env == "production" {
			return "", fmt.Errorf("invalid O1_EMAIL_PROVIDER: mailpit is not allowed in production; use resend")
		}
		if strings.TrimSpace(cfg.SMTPAddr) == "" {
			return "", fmt.Errorf("missing required environment variable O1_SMTP_ADDR for the mailpit email provider")
		}
	case EmailProviderResend:
		if strings.TrimSpace(cfg.ResendAPIKey) == "" {
			return "", fmt.Errorf("missing required environment variable O1_RESEND_API_KEY for the resend email provider")
		}
	}

	if err := validateMailbox("O1_EMAIL_FROM", cfg.EmailFrom, true); err != nil {
		return "", err
	}
	if err := validateMailbox("O1_EMAIL_REPLY_TO", cfg.EmailReplyTo, false); err != nil {
		return "", err
	}
	return provider, nil
}

// validateMailbox checks that value parses as a single RFC 5322 mailbox. The
// underlying parser error is not surfaced because it can echo the address.
func validateMailbox(name, value string, required bool) error {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return fmt.Errorf("missing required environment variable %s", name)
		}
		return nil
	}
	if _, err := mail.ParseAddress(value); err != nil {
		return fmt.Errorf("invalid %s: must be a valid email mailbox", name)
	}
	return nil
}

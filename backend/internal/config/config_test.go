package config_test

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestLoad_DefaultsForMonitoring(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_ENCRYPTION_KEY", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MonitorCreateLimit != 10 {
		t.Fatalf("create limit = %d, want 10", cfg.MonitorCreateLimit)
	}
	if cfg.MonitorMinInterval != 60*time.Second || cfg.MonitorMaxInterval != 24*time.Hour {
		t.Fatalf("intervals = %v/%v", cfg.MonitorMinInterval, cfg.MonitorMaxInterval)
	}
	if len(cfg.ProbeAllowedPorts) != 2 || cfg.ProbeAllowedPorts[0] != 80 || cfg.ProbeAllowedPorts[1] != 443 {
		t.Fatalf("ports = %v, want [80 443]", cfg.ProbeAllowedPorts)
	}
	if cfg.ProbeMaxRedirects != 5 || cfg.ProbeMaxBodyBytes != 1<<20 || cfg.ProbeGlobalRPS != 50 {
		t.Fatalf("probe config = %d/%d/%v", cfg.ProbeMaxRedirects, cfg.ProbeMaxBodyBytes, cfg.ProbeGlobalRPS)
	}
	if cfg.SchedulerTick != 10*time.Second || cfg.SchedulerBatch != 500 || cfg.WorkerConcurrency != 50 {
		t.Fatalf("runtime config = %v/%d/%d", cfg.SchedulerTick, cfg.SchedulerBatch, cfg.WorkerConcurrency)
	}
	if len(cfg.EncryptionKey) != 32 {
		t.Fatalf("derived encryption key length = %d, want 32", len(cfg.EncryptionKey))
	}
}

func TestLoad_ProductionRequiresEncryptionKey(t *testing.T) {
	t.Setenv("O1_ENV", "production")
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_ENCRYPTION_KEY", "")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() succeeded without O1_ENCRYPTION_KEY in production")
	}
}

func TestLoad_RejectsInvalidProbePort(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_PROBE_ALLOWED_PORTS", "80,70000")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() accepted an out-of-range port")
	}
}

func TestLoad_AcceptsKnownEnvs(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://localhost/o1")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_RESEND_API_KEY", "re_test_key")
	t.Setenv("O1_EMAIL_FROM", "o1@example.com")
	for _, env := range []string{"development", "test", "production"} {
		t.Setenv("O1_ENV", env)
		if _, err := config.Load(); err != nil {
			t.Fatalf("O1_ENV=%s: %v", env, err)
		}
	}
}

func TestLoad_TracksDerivedEncryptionKey(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_ENV", "development")
	t.Setenv("O1_ENCRYPTION_KEY", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.EncryptionKeyDerived {
		t.Fatal("EncryptionKeyDerived = false, want true")
	}

	t.Setenv("O1_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load with explicit key: %v", err)
	}
	if cfg.EncryptionKeyDerived {
		t.Fatal("EncryptionKeyDerived = true, want false for explicit key")
	}
}

func TestLoad_RejectsNonFiniteProbeRPS(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	for _, value := range []string{"NaN", "+Inf", "-Inf"} {
		t.Setenv("O1_PROBE_GLOBAL_RPS", value)
		if _, err := config.Load(); err == nil {
			t.Errorf("Load() accepted O1_PROBE_GLOBAL_RPS=%s", value)
		}
	}
}

func TestLoad_RejectsIntervalsOutsideSchema(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	for _, tc := range []struct{ name, value string }{
		{"O1_MONITOR_MIN_INTERVAL", "30s"},
		{"O1_MONITOR_MAX_INTERVAL", "48h"},
	} {
		t.Setenv("O1_MONITOR_MIN_INTERVAL", "60s")
		t.Setenv("O1_MONITOR_MAX_INTERVAL", "24h")
		t.Setenv(tc.name, tc.value)
		if _, err := config.Load(); err == nil {
			t.Errorf("Load() accepted %s=%s", tc.name, tc.value)
		}
	}
}

func TestObservabilityConfig_Defaults(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_ENV", "development")
	t.Setenv("O1_API_HEALTH_ADDR", "")
	t.Setenv("O1_OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("O1_OTEL_EXPORTER_OTLP_HEADERS", "")
	t.Setenv("O1_OTEL_SAMPLE_RATIO", "")
	t.Setenv("O1_SENTRY_DSN", "")
	t.Setenv("O1_SENTRY_ENVIRONMENT", "")
	t.Setenv("O1_RELEASE", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIHealthAddr != ":8083" {
		t.Errorf("APIHealthAddr = %q, want :8083", cfg.APIHealthAddr)
	}
	if cfg.OTELExporterEndpoint != "" {
		t.Errorf("OTELExporterEndpoint = %q, want empty", cfg.OTELExporterEndpoint)
	}
	if len(cfg.OTELExporterHeaders) != 0 {
		t.Errorf("OTELExporterHeaders = %v, want empty", cfg.OTELExporterHeaders)
	}
	if cfg.OTELSampleRatio != 0.05 {
		t.Errorf("OTELSampleRatio = %v, want 0.05", cfg.OTELSampleRatio)
	}
	if cfg.SentryDSN != "" {
		t.Errorf("SentryDSN = %q, want empty", cfg.SentryDSN)
	}
	if cfg.SentryEnvironment != cfg.Env {
		t.Errorf("SentryEnvironment = %q, want Env %q", cfg.SentryEnvironment, cfg.Env)
	}
	if cfg.Release != "dev" {
		t.Errorf("Release = %q, want dev", cfg.Release)
	}
}

func TestObservabilityConfig_ClampsSampleRatio(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_OTEL_SAMPLE_RATIO", "2")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load with ratio 2: %v", err)
	}
	if cfg.OTELSampleRatio != 1 {
		t.Errorf("ratio 2 clamped to %v, want 1", cfg.OTELSampleRatio)
	}

	t.Setenv("O1_OTEL_SAMPLE_RATIO", "-1")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load with ratio -1: %v", err)
	}
	if cfg.OTELSampleRatio != 0 {
		t.Errorf("ratio -1 clamped to %v, want 0", cfg.OTELSampleRatio)
	}
}

func TestObservabilityConfig_RejectsInvalidSampleRatio(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	for _, value := range []string{"NaN", "+Inf", "-Inf", "not-a-number"} {
		t.Setenv("O1_OTEL_SAMPLE_RATIO", value)
		if _, err := config.Load(); err == nil {
			t.Errorf("Load() accepted O1_OTEL_SAMPLE_RATIO=%s", value)
		}
	}
}

func TestObservabilityConfig_ReadsEnv(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_ENV", "production")
	t.Setenv("O1_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_RESEND_API_KEY", "re_test_key")
	t.Setenv("O1_EMAIL_FROM", "o1@example.com")
	t.Setenv("O1_API_HEALTH_ADDR", ":9090")
	t.Setenv("O1_OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector:4318")
	t.Setenv("O1_OTEL_EXPORTER_OTLP_HEADERS", "Authorization=Bearer token,X-Scope=o1")
	t.Setenv("O1_OTEL_SAMPLE_RATIO", "0.5")
	t.Setenv("O1_SENTRY_DSN", "https://key@sentry.example/1")
	t.Setenv("O1_SENTRY_ENVIRONMENT", "staging")
	t.Setenv("O1_RELEASE", "v1.2.3")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIHealthAddr != ":9090" {
		t.Errorf("APIHealthAddr = %q", cfg.APIHealthAddr)
	}
	if cfg.OTELExporterEndpoint != "http://collector:4318" {
		t.Errorf("OTELExporterEndpoint = %q", cfg.OTELExporterEndpoint)
	}
	wantHeaders := map[string]string{"Authorization": "Bearer token", "X-Scope": "o1"}
	if !reflect.DeepEqual(cfg.OTELExporterHeaders, wantHeaders) {
		t.Errorf("OTELExporterHeaders = %v, want %v", cfg.OTELExporterHeaders, wantHeaders)
	}
	if cfg.OTELSampleRatio != 0.5 {
		t.Errorf("OTELSampleRatio = %v", cfg.OTELSampleRatio)
	}
	if cfg.SentryDSN != "https://key@sentry.example/1" {
		t.Errorf("SentryDSN = %q", cfg.SentryDSN)
	}
	if cfg.SentryEnvironment != "staging" {
		t.Errorf("SentryEnvironment = %q", cfg.SentryEnvironment)
	}
	if cfg.Release != "v1.2.3" {
		t.Errorf("Release = %q", cfg.Release)
	}
}

func TestObservabilityConfig_RejectsMalformedHeaders(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	for _, value := range []string{"no-equals", "=missing-key", "missing-value=", "a=b,,c=d"} {
		t.Setenv("O1_OTEL_EXPORTER_OTLP_HEADERS", value)
		if _, err := config.Load(); err == nil {
			t.Errorf("Load() accepted malformed O1_OTEL_EXPORTER_OTLP_HEADERS=%q", value)
		}
	}
}

func TestLoad_DefaultsAllowedOriginsToAppBaseURL(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_APP_BASE_URL", "http://localhost:5173")
	t.Setenv("O1_ALLOWED_ORIGINS", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://localhost:5173"}
	if !reflect.DeepEqual(cfg.AllowedOrigins, want) {
		t.Fatalf("allowed origins = %v, want %v", cfg.AllowedOrigins, want)
	}
}

func TestLoad_UsesExplicitAllowedOrigins(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_APP_BASE_URL", "http://localhost:5173")
	t.Setenv("O1_ALLOWED_ORIGINS", "http://a.example, http://b.example")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://a.example", "http://b.example"}
	if !reflect.DeepEqual(cfg.AllowedOrigins, want) {
		t.Fatalf("allowed origins = %v, want %v", cfg.AllowedOrigins, want)
	}
}

func TestLoad_AuthRateDefaults(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_AUTH_RATE_PER_MINUTE", "")
	t.Setenv("O1_AUTH_RATE_BURST", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AuthRatePerMinute != 10 {
		t.Errorf("AuthRatePerMinute = %d, want 10", cfg.AuthRatePerMinute)
	}
	if cfg.AuthRateBurst != 10 {
		t.Errorf("AuthRateBurst = %d, want 10", cfg.AuthRateBurst)
	}
}

func TestLoad_AuthRateOverrides(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_AUTH_RATE_PER_MINUTE", "120")
	t.Setenv("O1_AUTH_RATE_BURST", "60")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AuthRatePerMinute != 120 {
		t.Errorf("AuthRatePerMinute = %d, want 120", cfg.AuthRatePerMinute)
	}
	if cfg.AuthRateBurst != 60 {
		t.Errorf("AuthRateBurst = %d, want 60", cfg.AuthRateBurst)
	}
}

// setEmailEnv applies a minimal valid environment for email configuration
// tests. Individual tests override the variables they exercise.
func setEmailEnv(t *testing.T, envName string) {
	t.Helper()
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_ENV", envName)
	t.Setenv("O1_EMAIL_PROVIDER", "")
	t.Setenv("O1_SMTP_ADDR", "localhost:1025")
	t.Setenv("O1_EMAIL_FROM", "o1@example.com")
	t.Setenv("O1_EMAIL_REPLY_TO", "")
	t.Setenv("O1_RESEND_API_KEY", "")
	t.Setenv("O1_RESEND_API_URL", "")
	t.Setenv("O1_RESEND_RPS", "")
	t.Setenv("O1_RESEND_BURST", "")
}

func TestEmailProvider_ResolvesDefaults(t *testing.T) {
	for _, envName := range []string{"development", "test"} {
		t.Run(envName, func(t *testing.T) {
			setEmailEnv(t, envName)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.EmailProvider != "mailpit" {
				t.Fatalf("EmailProvider = %q, want mailpit", cfg.EmailProvider)
			}
		})
	}

	t.Run("production", func(t *testing.T) {
		setEmailEnv(t, "production")
		t.Setenv("O1_RESEND_API_KEY", "re_test_key")
		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.EmailProvider != "resend" {
			t.Fatalf("EmailProvider = %q, want resend", cfg.EmailProvider)
		}
	})
}

func TestEmailProvider_ExplicitResendOutsideProduction(t *testing.T) {
	setEmailEnv(t, "development")
	t.Setenv("O1_EMAIL_PROVIDER", "resend")
	t.Setenv("O1_RESEND_API_KEY", "re_test_key")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EmailProvider != "resend" {
		t.Fatalf("EmailProvider = %q, want resend", cfg.EmailProvider)
	}
}

func TestEmailProvider_RejectsMailpitInProduction(t *testing.T) {
	setEmailEnv(t, "production")
	t.Setenv("O1_EMAIL_PROVIDER", "mailpit")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() accepted mailpit in production")
	}
}

func TestEmailProvider_ProductionRequiresExplicitFrom(t *testing.T) {
	setEmailEnv(t, "production")
	t.Setenv("O1_RESEND_API_KEY", "re_test_key")
	t.Setenv("O1_EMAIL_FROM", "")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() accepted production without O1_EMAIL_FROM")
	}
}

func TestEmailProvider_DevelopmentDefaultsMissingFrom(t *testing.T) {
	setEmailEnv(t, "development")
	t.Setenv("O1_EMAIL_FROM", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EmailFrom != "o1@localhost" {
		t.Fatalf("EmailFrom = %q, want o1@localhost", cfg.EmailFrom)
	}
}

func TestEmailProvider_RejectsResendWithoutKey(t *testing.T) {
	setEmailEnv(t, "development")
	t.Setenv("O1_EMAIL_PROVIDER", "resend")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() accepted resend without O1_RESEND_API_KEY")
	}
}

func TestEmailProvider_RejectsInvalidFrom(t *testing.T) {
	setEmailEnv(t, "development")
	t.Setenv("O1_EMAIL_PROVIDER", "resend")
	t.Setenv("O1_RESEND_API_KEY", "re_super_secret")
	t.Setenv("O1_EMAIL_FROM", "not-an-email")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() accepted a malformed O1_EMAIL_FROM")
	}
	if strings.Contains(err.Error(), "re_super_secret") {
		t.Fatalf("error leaks the API key: %v", err)
	}
}

func TestEmailProvider_RejectsInvalidReplyTo(t *testing.T) {
	setEmailEnv(t, "development")
	t.Setenv("O1_EMAIL_REPLY_TO", "not-an-email")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() accepted a malformed O1_EMAIL_REPLY_TO")
	}
}

func TestEmailProvider_AcceptsDisplayNameReplyTo(t *testing.T) {
	setEmailEnv(t, "development")
	t.Setenv("O1_EMAIL_REPLY_TO", "O1 Support <support@example.com>")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EmailReplyTo != "O1 Support <support@example.com>" {
		t.Fatalf("EmailReplyTo = %q", cfg.EmailReplyTo)
	}
}

func TestEmailProvider_RejectsUnknownProvider(t *testing.T) {
	for _, provider := range []string{"sendgrid", "resendd", "mailpit2"} {
		t.Run(provider, func(t *testing.T) {
			setEmailEnv(t, "development")
			t.Setenv("O1_EMAIL_PROVIDER", provider)
			t.Setenv("O1_RESEND_API_KEY", "re_test_key")

			if _, err := config.Load(); err == nil {
				t.Fatalf("Load() accepted O1_EMAIL_PROVIDER=%q", provider)
			}
		})
	}
}

func TestEmailProvider_ResendDefaults(t *testing.T) {
	setEmailEnv(t, "development")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ResendAPIURL != "https://api.resend.com" {
		t.Errorf("ResendAPIURL = %q, want https://api.resend.com", cfg.ResendAPIURL)
	}
	if cfg.ResendRPS != 2 {
		t.Errorf("ResendRPS = %v, want 2", cfg.ResendRPS)
	}
	if cfg.ResendBurst != 2 {
		t.Errorf("ResendBurst = %d, want 2", cfg.ResendBurst)
	}
	if cfg.EmailReplyTo != "" {
		t.Errorf("EmailReplyTo = %q, want empty", cfg.EmailReplyTo)
	}
}

func TestEmailProvider_ResendOverrides(t *testing.T) {
	setEmailEnv(t, "development")
	t.Setenv("O1_RESEND_API_URL", "https://resend.test")
	t.Setenv("O1_RESEND_RPS", "5.5")
	t.Setenv("O1_RESEND_BURST", "7")
	t.Setenv("O1_EMAIL_REPLY_TO", "support@example.com")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ResendAPIURL != "https://resend.test" {
		t.Errorf("ResendAPIURL = %q", cfg.ResendAPIURL)
	}
	if cfg.ResendRPS != 5.5 {
		t.Errorf("ResendRPS = %v, want 5.5", cfg.ResendRPS)
	}
	if cfg.ResendBurst != 7 {
		t.Errorf("ResendBurst = %d, want 7", cfg.ResendBurst)
	}
	if cfg.EmailReplyTo != "support@example.com" {
		t.Errorf("EmailReplyTo = %q", cfg.EmailReplyTo)
	}
}

func TestEmailProvider_RejectsInvalidResendLimits(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"O1_RESEND_RPS", "0"},
		{"O1_RESEND_RPS", "-1"},
		{"O1_RESEND_RPS", "NaN"},
		{"O1_RESEND_RPS", "+Inf"},
		{"O1_RESEND_RPS", "not-a-number"},
		{"O1_RESEND_BURST", "0"},
		{"O1_RESEND_BURST", "-1"},
		{"O1_RESEND_BURST", "not-a-number"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			setEmailEnv(t, "development")
			t.Setenv(tc.name, tc.value)

			if _, err := config.Load(); err == nil {
				t.Fatalf("Load() accepted %s=%s", tc.name, tc.value)
			}
		})
	}
}

func TestRetentionConfig_Defaults(t *testing.T) {
	setEmailEnv(t, "development")
	for _, key := range []string{
		"O1_RAW_RETENTION_DAYS", "O1_ROLLUP_RETENTION_MONTHS",
		"O1_AUDIT_RETENTION_MONTHS", "O1_RETENTION_DRY_RUN",
		"O1_RETENTION_MAX_PARTITION_DROPS", "O1_ROLLUP_INTERVAL",
		"O1_ROLLUP_MAX_HOURS_PER_RUN",
	} {
		t.Setenv(key, "")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RawRetentionDays != 90 || cfg.RollupRetentionMonths != 13 ||
		cfg.AuditRetentionMonths != 12 || !cfg.RetentionDryRun ||
		cfg.RetentionMaxPartitionDrops != 1 || cfg.RollupInterval != 15*time.Minute ||
		cfg.RollupMaxHoursPerRun != 24 {
		t.Fatalf("retention defaults = %+v", cfg)
	}
}

func TestRetentionConfig_RejectsInvalid(t *testing.T) {
	cases := []struct{ key, value string }{
		{"O1_RAW_RETENTION_DAYS", "6"},
		{"O1_ROLLUP_RETENTION_MONTHS", "3"},
		{"O1_AUDIT_RETENTION_MONTHS", "0"},
		{"O1_RETENTION_DRY_RUN", "sometimes"},
		{"O1_RETENTION_MAX_PARTITION_DROPS", "0"},
		{"O1_ROLLUP_INTERVAL", "500ms"},
		// A long interval cannot satisfy the 2-hour late-row contract.
		{"O1_ROLLUP_INTERVAL", "2h"},
		{"O1_ROLLUP_MAX_HOURS_PER_RUN", "0"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			setEmailEnv(t, "development")
			t.Setenv(tc.key, tc.value)
			if _, err := config.Load(); err == nil {
				t.Fatalf("Load accepted %s=%s", tc.key, tc.value)
			}
		})
	}
}

func TestLoad_RejectsInvalidAuthRate(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	for _, variable := range []string{"O1_AUTH_RATE_PER_MINUTE", "O1_AUTH_RATE_BURST"} {
		for _, value := range []string{"0", "-1", "not-a-number"} {
			t.Setenv("O1_AUTH_RATE_PER_MINUTE", "")
			t.Setenv("O1_AUTH_RATE_BURST", "")
			t.Setenv(variable, value)
			if _, err := config.Load(); err == nil {
				t.Errorf("Load() accepted %s=%s", variable, value)
			}
		}
	}
}

func TestLoad_DBMaxConnsDefault(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_DB_MAX_CONNS", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBMaxConns != 10 {
		t.Fatalf("DBMaxConns = %d, want 10", cfg.DBMaxConns)
	}
}

func TestLoad_DBMaxConnsExplicit(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_DB_MAX_CONNS", "7")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBMaxConns != 7 {
		t.Fatalf("DBMaxConns = %d, want 7", cfg.DBMaxConns)
	}
}

func TestLoad_RejectsInvalidDBMaxConns(t *testing.T) {
	for _, value := range []string{"0", "101", "abc"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("O1_DATABASE_URL", "postgres://example")
			t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
			t.Setenv("O1_DB_MAX_CONNS", value)
			if _, err := config.Load(); err == nil {
				t.Fatalf("Load() accepted O1_DB_MAX_CONNS=%q", value)
			}
		})
	}
}

func TestResolveEmailProvider_SMTP(t *testing.T) {
	cfg := config.Config{
		Env:           "production",
		EmailProvider: "smtp",
		SMTPAddr:      "smtp.example.net:587",
		SMTPTLS:       "starttls",
		SMTPUsername:  "user",
		SMTPPassword:  "pass",
		EmailFrom:     "o1 <noreply@example.net>",
	}
	got, err := cfg.ResolveEmailProvider()
	if err != nil {
		t.Fatal(err)
	}
	if got != config.EmailProviderSMTP {
		t.Fatalf("provider = %q", got)
	}
}

func TestResolveEmailProvider_SMTPNeedsNoResendKey(t *testing.T) {
	cfg := config.Config{
		Env:           "production",
		EmailProvider: "smtp",
		SMTPAddr:      "smtp.example.net:587",
		SMTPTLS:       "starttls",
		EmailFrom:     "o1 <noreply@example.net>",
	}
	if _, err := cfg.ResolveEmailProvider(); err != nil {
		t.Fatalf("smtp should not require a Resend key: %v", err)
	}
}

func TestResolveEmailProvider_SMTPRejectsHalfCredentials(t *testing.T) {
	for _, tc := range []struct{ user, pass string }{{"user", ""}, {"", "pass"}} {
		cfg := config.Config{
			Env:           "production",
			EmailProvider: "smtp",
			SMTPAddr:      "smtp.example.net:587",
			SMTPTLS:       "starttls",
			SMTPUsername:  tc.user,
			SMTPPassword:  tc.pass,
			EmailFrom:     "o1 <noreply@example.net>",
		}
		if _, err := cfg.ResolveEmailProvider(); err == nil {
			t.Fatalf("accepted half credentials user=%q", tc.user)
		}
	}
}

func TestResolveEmailProvider_SMTPRejectsInsecureTLS(t *testing.T) {
	base := config.Config{
		Env:           "production",
		EmailProvider: "smtp",
		SMTPAddr:      "smtp.example.net:587",
		EmailFrom:     "o1 <noreply@example.net>",
	}
	for _, tc := range []struct {
		tls  string
		user string
		pass string
	}{
		{"none", "", ""},
		{"none", "user", "pass"},
		{"bogus", "user", "pass"},
	} {
		cfg := base
		cfg.SMTPTLS, cfg.SMTPUsername, cfg.SMTPPassword = tc.tls, tc.user, tc.pass
		if _, err := cfg.ResolveEmailProvider(); err == nil {
			t.Fatalf("accepted tls=%q with credentials=%v", tc.tls, tc.user != "")
		}
	}
}

func TestResolveEmailProvider_SMTPRejectsMailpitAddrInProduction(t *testing.T) {
	cfg := config.Config{
		Env:           "production",
		EmailProvider: "smtp",
		SMTPAddr:      "localhost:1025",
		SMTPTLS:       "starttls",
		EmailFrom:     "o1 <noreply@example.net>",
	}
	if _, err := cfg.ResolveEmailProvider(); err == nil {
		t.Fatal("accepted the Mailpit address in production")
	}
}

func TestLoad_SMTPTLSDefaultsToStartTLS(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_SMTP_TLS", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMTPTLS != "starttls" {
		t.Fatalf("SMTPTLS = %q, want starttls", cfg.SMTPTLS)
	}
}

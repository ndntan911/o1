package config_test

import (
	"encoding/base64"
	"reflect"
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

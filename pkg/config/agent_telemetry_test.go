package config

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/samber/do/v2"
)

func telemetryTestEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"AGENT_TELEMETRY_OTLP_ENDPOINT", "AGENT_TELEMETRY_OTLP_HEADERS", "AGENT_TELEMETRY_CAPTURE_CONTENT"} {
		t.Setenv(key, "")
	}
}

func TestAgentTelemetryConfig(t *testing.T) {
	telemetryTestEnv(t)
	cfg, err := loadAgentTelemetryConfig()
	if err != nil || cfg.Endpoint != "" || cfg.CaptureContent {
		t.Fatalf("unexpected defaults: %+v, %v", cfg, err)
	}
	t.Setenv("AGENT_TELEMETRY_OTLP_ENDPOINT", "https://collector.example/otlp/")
	t.Setenv("AGENT_TELEMETRY_OTLP_HEADERS", "Authorization=Bearer%20test%2Btoken,X-Org=a%2Fb")
	t.Setenv("AGENT_TELEMETRY_CAPTURE_CONTENT", "true")
	cfg, err = loadAgentTelemetryConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "https://collector.example/otlp" || !cfg.CaptureContent || cfg.Headers["authorization"] != "Bearer test+token" || cfg.Headers["x-org"] != "a/b" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestAgentTelemetryRejectsInvalidConfigWithoutSecrets(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"scheme", "AGENT_TELEMETRY_OTLP_ENDPOINT", "ftp://collector"},
		{"host", "AGENT_TELEMETRY_OTLP_ENDPOINT", "http:///path"},
		{"credentials", "AGENT_TELEMETRY_OTLP_ENDPOINT", "https://secret@collector"},
		{"query", "AGENT_TELEMETRY_OTLP_ENDPOINT", "https://collector?token=secret"},
		{"signal path", "AGENT_TELEMETRY_OTLP_ENDPOINT", "https://collector/v1/logs"},
		{"invalid boolean", "AGENT_TELEMETRY_CAPTURE_CONTENT", "secret"},
		{"header syntax", "AGENT_TELEMETRY_OTLP_HEADERS", "secret"},
		{"header injection", "AGENT_TELEMETRY_OTLP_HEADERS", "Authorization=secret%0d%0aX:bad"},
		{"invalid escape", "AGENT_TELEMETRY_OTLP_HEADERS", "Authorization=secret%XX"},
		{"duplicate", "AGENT_TELEMETRY_OTLP_HEADERS", "Authorization=secret,authorization=other"},
		{"provider header name", "AGENT_TELEMETRY_OTLP_HEADERS", "x.y=secret"},
		{"provider header value", "AGENT_TELEMETRY_OTLP_HEADERS", "Authorization=a%2Cb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			telemetryTestEnv(t)
			t.Setenv("AGENT_TELEMETRY_OTLP_ENDPOINT", "https://collector")
			t.Setenv(tc.key, tc.value)
			_, err := loadAgentTelemetryConfig()
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("expected sanitized error, got %v", err)
			}
		})
	}
	t.Run("headers require destination", func(t *testing.T) {
		telemetryTestEnv(t)
		t.Setenv("AGENT_TELEMETRY_OTLP_HEADERS", "Authorization=secret")
		if _, err := loadAgentTelemetryConfig(); err == nil {
			t.Fatal("expected missing endpoint error")
		}
	})
}

func TestIntegrationAgentTelemetryDaemonConfig(t *testing.T) {
	telemetryTestEnv(t)
	t.Setenv("DATA_ROOT", t.TempDir())
	t.Setenv("AGENT_TELEMETRY_OTLP_ENDPOINT", "http://collector:4318")
	di := do.New()
	do.ProvideValue(di, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cfg, err := NewConfig(di)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentTelemetry.Endpoint != "http://collector:4318" {
		t.Fatalf("telemetry was not loaded: %+v", cfg.AgentTelemetry)
	}
	t.Setenv("AGENT_TELEMETRY_OTLP_ENDPOINT", "not-a-url")
	if _, err := NewConfig(di); err == nil {
		t.Fatal("daemon accepted invalid telemetry destination")
	}
}

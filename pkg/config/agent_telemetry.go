package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// AgentTelemetryConfig controls native provider export from guest processes.
// Endpoint is an OTLP/HTTP base URL, not a signal-specific URL.
type AgentTelemetryConfig struct {
	Endpoint       string            `json:"endpoint,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	CaptureContent bool              `json:"captureContent,omitempty"`
}

func loadAgentTelemetryConfig() (AgentTelemetryConfig, error) {
	cfg := AgentTelemetryConfig{Endpoint: strings.TrimRight(strings.TrimSpace(os.Getenv("AGENT_TELEMETRY_OTLP_ENDPOINT")), "/")}
	if cfg.Endpoint != "" {
		u, err := url.Parse(cfg.Endpoint)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return cfg, fmt.Errorf("AGENT_TELEMETRY_OTLP_ENDPOINT must be an HTTP(S) base URL without credentials, query or fragment")
		}
		for _, suffix := range []string{"/v1/logs", "/v1/traces", "/v1/metrics"} {
			if strings.HasSuffix(u.Path, suffix) {
				return cfg, fmt.Errorf("AGENT_TELEMETRY_OTLP_ENDPOINT must not include a signal path")
			}
		}
	}
	if raw := strings.TrimSpace(os.Getenv("AGENT_TELEMETRY_CAPTURE_CONTENT")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return cfg, fmt.Errorf("AGENT_TELEMETRY_CAPTURE_CONTENT must be a boolean")
		}
		cfg.CaptureContent = value
	}
	raw := strings.TrimSpace(os.Getenv("AGENT_TELEMETRY_OTLP_HEADERS"))
	if raw != "" {
		cfg.Headers = make(map[string]string)
		for _, entry := range strings.Split(raw, ",") {
			name, encoded, ok := strings.Cut(strings.TrimSpace(entry), "=")
			value, err := url.PathUnescape(encoded)
			if !ok || err != nil || !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) || strings.ContainsAny(value, "\x00\r\n") {
				return cfg, fmt.Errorf("AGENT_TELEMETRY_OTLP_HEADERS must contain comma-separated name=percent-encoded-value pairs")
			}
			name = strings.ToLower(name)
			if strings.Contains(name, ".") {
				return cfg, fmt.Errorf("AGENT_TELEMETRY_OTLP_HEADERS must not contain dots in header names")
			}
			if strings.Contains(value, ",") {
				return cfg, fmt.Errorf("AGENT_TELEMETRY_OTLP_HEADERS must not contain commas in header values")
			}
			if _, exists := cfg.Headers[name]; exists {
				return cfg, fmt.Errorf("AGENT_TELEMETRY_OTLP_HEADERS contains duplicate header names")
			}
			cfg.Headers[name] = value
		}
	}
	if cfg.Endpoint == "" && (len(cfg.Headers) > 0 || cfg.CaptureContent) {
		return cfg, fmt.Errorf("AGENT_TELEMETRY_OTLP_ENDPOINT is required when configuring agent telemetry")
	}
	return cfg, nil
}

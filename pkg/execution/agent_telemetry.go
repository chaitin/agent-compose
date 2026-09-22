package execution

import (
	"context"
	"encoding/json"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

const (
	traceparentLength        = 55
	traceparentTraceIDLength = 32
	traceparentSpanIDLength  = 16
	traceparentZeroTraceID   = "00000000000000000000000000000000"
	traceparentZeroSpanID    = "0000000000000000"
	// tracestateLimit is the W3C Trace Context limit on the tracestate header.
	tracestateLimit = 512
)

// ApplyAgentTelemetryEnv passes daemon-owned telemetry settings to this execution
// only. It never persists collector credentials in sandbox configuration.
func ApplyAgentTelemetryEnv(ctx context.Context, config *appconfig.Config, sandbox *domain.Sandbox, env map[string]string) {
	// An empty value also shadows stale values in a resumed guest's environment.
	env["AGENT_COMPOSE_TELEMETRY"] = ""
	if config.AgentTelemetry.Endpoint == "" {
		return
	}
	traceContext := domain.TraceContextFromContext(ctx)
	payload := struct {
		appconfig.AgentTelemetryConfig
		Traceparent string            `json:"traceparent,omitempty"`
		Tracestate  string            `json:"tracestate,omitempty"`
		Attributes  map[string]string `json:"attributes"`
	}{
		AgentTelemetryConfig: config.AgentTelemetry,
		Traceparent:          validTraceparent(traceContext.Traceparent),
		Tracestate:           validTracestate(traceContext.Tracestate),
		Attributes:           map[string]string{"agent_compose.sandbox.id": sandbox.Summary.ID},
	}
	// The payload contains only strings, booleans and maps of strings; JSON
	// marshaling cannot fail for these types.
	encoded, _ := json.Marshal(payload) //nolint:errchkjson // Only strings, booleans and string maps; see invariant above.
	env["AGENT_COMPOSE_TELEMETRY"] = string(encoded)
}

// validTraceparent returns value when it is a W3C Trace Context version 00
// traceparent the guest runtime can consume, and "" otherwise. The daemon only
// relays the caller's context, so an unknown or malformed value is dropped
// rather than rewritten.
func validTraceparent(value string) string {
	value = strings.TrimSpace(value)
	if len(value) != traceparentLength {
		return ""
	}
	if value[2] != '-' || value[35] != '-' || value[52] != '-' {
		return ""
	}
	version, traceID, spanID, flags := value[:2], value[3:35], value[36:52], value[53:55]
	// Version 00 is the only defined W3C Trace Context version; later versions
	// may change the field layout, so they are not relayed as version 00.
	if version != "00" {
		return ""
	}
	if !isLowerHex(traceID) || traceID == traceparentZeroTraceID {
		return ""
	}
	if !isLowerHex(spanID) || spanID == traceparentZeroSpanID {
		return ""
	}
	if !isLowerHex(flags) {
		return ""
	}
	return value
}

// validTracestate returns value when it is a plausible W3C Trace Context
// tracestate header value and "" otherwise. The daemon does not interpret the
// vendor entries; it only rejects non-printable, oversized, or injection-prone
// values before relaying them into the guest environment.
func validTracestate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > tracestateLimit {
		return ""
	}
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b < 0x20 || b > 0x7e {
			return ""
		}
	}
	return value
}

func isLowerHex(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

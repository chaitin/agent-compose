package agentcompose

import (
	"encoding/json"
	"testing"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestRedactRuntimeContextJSON(t *testing.T) {
	context := &agentcomposev2.RuntimeContext{
		Source: "api",
		Metadata: map[string]string{
			"trace.label": "visible",
			"api_token":   "secret-token",
		},
		Env: map[string]string{
			"OPENAI_API_KEY": "sk-test",
			"MODE":           "test",
		},
		IdentityContext: map[string]string{
			"user":        "user-1",
			"auth_header": "bearer secret",
		},
		CapabilityScope: &agentcomposev2.CapabilityScope{
			CapsetIds: []string{"capset-a"},
			Metadata:  map[string]string{"x-octobus-ext-user": "alice", "x-octobus-ext-token": "token"},
		},
	}
	raw, err := json.Marshal(context)
	if err != nil {
		t.Fatalf("marshal context: %v", err)
	}
	redacted := redactRuntimeContextJSON(string(raw))
	var got agentcomposev2.RuntimeContext
	if err := json.Unmarshal([]byte(redacted), &got); err != nil {
		t.Fatalf("decode redacted context: %v", err)
	}
	if got.Metadata["trace.label"] != "visible" || got.Metadata["api_token"] != redactedValue {
		t.Fatalf("metadata redaction = %#v", got.Metadata)
	}
	if got.Env["OPENAI_API_KEY"] != redactedValue || got.Env["MODE"] != "test" {
		t.Fatalf("env redaction = %#v", got.Env)
	}
	if got.IdentityContext["auth_header"] != redactedValue || got.IdentityContext["user"] != "user-1" {
		t.Fatalf("identity redaction = %#v", got.IdentityContext)
	}
	if got.CapabilityScope.Metadata["x-octobus-ext-token"] != redactedValue || got.CapabilityScope.Metadata["x-octobus-ext-user"] != "alice" {
		t.Fatalf("capability metadata redaction = %#v", got.CapabilityScope.Metadata)
	}
}

func TestRuntimeContextResponseRedactsSecrets(t *testing.T) {
	raw := `{"env":{"ANTHROPIC_AUTH_TOKEN":"secret","SAFE":"ok"}}`
	got := runtimeContextResponse(raw)
	if got == nil {
		t.Fatal("runtimeContextResponse returned nil")
	}
	if got.Env["ANTHROPIC_AUTH_TOKEN"] != redactedValue || got.Env["SAFE"] != "ok" {
		t.Fatalf("runtime context response env = %#v", got.Env)
	}
}

func TestRuntimeContextResponseMarksInvalidJSON(t *testing.T) {
	got := runtimeContextResponse(`{"env":`)
	if got == nil {
		t.Fatal("runtimeContextResponse returned nil for invalid JSON")
	}
	if got.Metadata[runtimeContextRedactionErrorKey] != "invalid runtime context JSON" {
		t.Fatalf("runtime context response metadata = %#v", got.Metadata)
	}
	if _, ok := got.Metadata["env"]; ok {
		t.Fatalf("runtime context response leaked raw context: %#v", got.Metadata)
	}
}

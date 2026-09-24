package driver

import "testing"

func TestParseEnvEntry(t *testing.T) {
	tests := []struct {
		input     string
		wantKey   string
		wantValue string
		wantOK    bool
	}{
		{"KEY=VALUE", "KEY", "VALUE", true},
		{"KEY=VAL=UE", "KEY", "VAL=UE", true},
		{"KEY=", "KEY", "", true},
		{"=VALUE", "", "", false},
		{"NODELIM", "", "", false},
		{"  KEY  =  VALUE  ", "KEY", "  VALUE  ", true},
		{"", "", "", false},
	}
	for _, tt := range tests {
		key, value, ok := parseEnvEntry(tt.input)
		if ok != tt.wantOK || key != tt.wantKey || value != tt.wantValue {
			t.Errorf("parseEnvEntry(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.input, key, value, ok, tt.wantKey, tt.wantValue, tt.wantOK)
		}
	}
}

func TestSandboxEnvMapKeepsNonLLMSecretEnv(t *testing.T) {
	env := sandboxEnvMap([]SandboxEnvVar{
		{Name: "DATABASE_PASSWORD", Value: "db-secret", Secret: true},
		{Name: "OPENAI_API_KEY", Value: "provider-key", Secret: true},
		{Name: "OPENAI_BASE_URL", Value: "https://upstream.example", Secret: false},
		{Name: "LLM_API_HEADERS", Value: `{"X-Gateway-Token":"long-lived"}`, Secret: true},
	}, []SandboxEnvVar{
		{Name: "OPENAI_API_KEY", Value: "facade-token", Secret: false},
		{Name: "OPENAI_BASE_URL", Value: "http://daemon.test/llm/openai/v1", Secret: false},
	})
	if env["DATABASE_PASSWORD"] != "db-secret" {
		t.Fatalf("DATABASE_PASSWORD = %q, want non-LLM secret env to be preserved", env["DATABASE_PASSWORD"])
	}
	if env["OPENAI_API_KEY"] != "facade-token" {
		t.Fatalf("OPENAI_API_KEY = %q, want managed facade token", env["OPENAI_API_KEY"])
	}
	if env["OPENAI_BASE_URL"] != "http://daemon.test/llm/openai/v1" {
		t.Fatalf("OPENAI_BASE_URL = %q, want the managed facade address", env["OPENAI_BASE_URL"])
	}
	if _, ok := env["LLM_API_HEADERS"]; ok {
		t.Fatal("LLM_API_HEADERS must not be passed to the guest runtime")
	}
}

// TestSandboxEnvMapDropsDeclaredProviderEndpoints pins the half of the denylist
// that is not a credential. A declared upstream address is daemon configuration
// too: the guest is given the facade address, so a surviving declared endpoint
// would point the harness at an upstream its facade token cannot authenticate
// against.
func TestSandboxEnvMapDropsDeclaredProviderEndpoints(t *testing.T) {
	declared := []SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://upstream.example/v1"},
		{Name: "LLM_API_PROTOCOL", Value: "chat_completions"},
		{Name: "ANTHROPIC_BASE_URL", Value: "https://upstream.example/anthropic"},
		{Name: "ANTHROPIC_API_ENDPOINT", Value: "https://upstream.example/anthropic"},
		{Name: "DEEPSEEK_BASE_URL", Value: "https://upstream.example"},
		{Name: "OPENROUTER_BASE_URL", Value: "https://upstream.example/api"},
	}
	env := sandboxEnvMap(declared, nil)
	for _, item := range declared {
		if _, ok := env[item.Name]; ok {
			t.Errorf("%s reached the guest runtime, but it is daemon-owned provider configuration", item.Name)
		}
	}

	// The managed layer writes the same names, and those values must survive.
	managed := sandboxEnvMap(declared, []SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "http://daemon.test/llm/openai/v1"},
		{Name: "LLM_API_PROTOCOL", Value: "responses"},
	})
	if managed["LLM_API_ENDPOINT"] != "http://daemon.test/llm/openai/v1" ||
		managed["LLM_API_PROTOCOL"] != "responses" {
		t.Fatalf("managed facade values were dropped: %#v", managed)
	}
}

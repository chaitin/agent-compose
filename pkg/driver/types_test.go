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

// TestLLMProviderCredentialEnvNameCoversEveryVendorAlias pins the credential
// denylist itself. pkg/llms recognizes a superset of spellings per vendor, and a
// name the recognition table knows but this list does not would let a real
// upstream credential reach the guest under its own name while the run was being
// proxied — the exact gap a review found in CODEX_API_KEY and DEEPSEEK_API_KEY.
func TestLLMProviderCredentialEnvNameCoversEveryVendorAlias(t *testing.T) {
	denied := []string{
		"LLM_API_KEY", "LLM_API_HEADERS",
		"OPENAI_API_KEY", "CODEX_API_KEY",
		"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
		"DEEPSEEK_API_KEY", "OPENROUTER_API_KEY",
		"AZURE_OPENAI_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY",
	}
	for _, name := range denied {
		if !LLMProviderCredentialEnvName(name) {
			t.Errorf("LLMProviderCredentialEnvName(%q) = false, want it denied", name)
		}
		if !LLMProviderEnvName(name) {
			t.Errorf("LLMProviderEnvName(%q) = false, want it denied", name)
		}
	}
	// An address is provider configuration but not a credential: the view keeps
	// showing it, so the two predicates must not agree here.
	if LLMProviderCredentialEnvName("OPENAI_BASE_URL") {
		t.Error("OPENAI_BASE_URL is an address, not a credential")
	}
	if !LLMProviderEnvName("OPENAI_BASE_URL") {
		t.Error("OPENAI_BASE_URL must still be kept off the guest")
	}
	// A name the daemon does not know is passed through, so it must not be
	// reported as credential material the daemon holds.
	for _, name := range []string{"MYCORP_API_KEY", "MYCORP_AUTH_TOKEN", "MODE", ""} {
		if LLMProviderCredentialEnvName(name) {
			t.Errorf("LLMProviderCredentialEnvName(%q) = true, want an unrecognized name left visible", name)
		}
	}
}

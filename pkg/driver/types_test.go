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
	}, []SandboxEnvVar{
		{Name: "OPENAI_API_KEY", Value: "facade-token", Secret: false},
	})
	if env["DATABASE_PASSWORD"] != "db-secret" {
		t.Fatalf("DATABASE_PASSWORD = %q, want non-LLM secret env to be preserved", env["DATABASE_PASSWORD"])
	}
	if env["OPENAI_API_KEY"] != "facade-token" {
		t.Fatalf("OPENAI_API_KEY = %q, want managed facade token", env["OPENAI_API_KEY"])
	}
}

package driver

import "testing"

func TestSessionEnvMapKeepsNonLLMSecretEnv(t *testing.T) {
	env := sessionEnvMap([]SessionEnvVar{
		{Name: "DATABASE_PASSWORD", Value: "db-secret", Secret: true},
		{Name: "OPENAI_API_KEY", Value: "provider-key", Secret: true},
	}, []SessionEnvVar{
		{Name: "OPENAI_API_KEY", Value: "facade-token", Secret: false},
	})
	if env["DATABASE_PASSWORD"] != "db-secret" {
		t.Fatalf("DATABASE_PASSWORD = %q, want non-LLM secret env to be preserved", env["DATABASE_PASSWORD"])
	}
	if env["OPENAI_API_KEY"] != "facade-token" {
		t.Fatalf("OPENAI_API_KEY = %q, want managed facade token", env["OPENAI_API_KEY"])
	}
}

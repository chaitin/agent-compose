package capmatrix

import (
	"testing"

	"github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// TestCredentialPlaceholderDeclarationMatchesManagedFacadeEnv cross-asserts the
// driver declarations against the engine mechanism that actually enforces the
// dimension. The declarations live in pkg/driver, but the enforcement lives in
// pkg/llms, so the assertion belongs here: if the managed facade stops replacing
// a declared provider credential with the run-scoped token, the "enforced" claim
// becomes false and this test fails.
func TestCredentialPlaceholderDeclarationMatchesManagedFacadeEnv(t *testing.T) {
	const declaredValue = "sk-real-upstream-credential"
	if !driver.LLMProviderCredentialEnvName("OPENAI_API_KEY") {
		t.Fatal("OPENAI_API_KEY is no longer recognized as an LLM provider credential; the credential placeholder declaration is stale")
	}

	merged := llms.MergeManagedExecEnv(
		map[string]string{"OPENAI_API_KEY": declaredValue, "UNRECOGNIZED_SECRET": "kept"},
		map[string]string{"OPENAI_API_KEY": "ac_llm_run_scoped_token"},
	)
	if merged["OPENAI_API_KEY"] != "ac_llm_run_scoped_token" {
		t.Fatalf("guest OPENAI_API_KEY = %q, want the run-scoped facade token", merged["OPENAI_API_KEY"])
	}
	if merged["UNRECOGNIZED_SECRET"] != "kept" {
		t.Fatalf("unrecognized secret = %q, want it passed through unchanged", merged["UNRECOGNIZED_SECRET"])
	}

	persisted := llms.FilterPersistedRuntimeEnv([]domain.SandboxEnvVar{
		{Name: "OPENAI_API_KEY", Value: declaredValue},
		{Name: "UNRECOGNIZED_SECRET", Value: "kept"},
	})
	if len(persisted) != 1 || persisted[0].Name != "UNRECOGNIZED_SECRET" {
		t.Fatalf("persisted runtime env = %+v, want only the unrecognized secret", persisted)
	}

	for _, name := range []string{
		driver.RuntimeDriverDocker,
		driver.RuntimeDriverK8s,
		driver.RuntimeDriverBoxlite,
		driver.RuntimeDriverMicrosandbox,
	} {
		facts, err := driver.RuntimeCapabilityFactsFor(name)
		if err != nil {
			t.Fatalf("RuntimeCapabilityFactsFor(%q) error = %v", name, err)
		}
		declaration := driverCapabilitiesFromFacts(facts)
		capability, ok := declaration.Capability(DimensionCredentialPlaceholder)
		if !ok {
			t.Fatalf("driver %q does not declare %q", name, DimensionCredentialPlaceholder)
		}
		if !capability.Enforced || capability.Mechanism != "runtime_llm_facade_token" {
			t.Fatalf("driver %q credential placeholder = %+v, want enforced via the runtime LLM facade token", name, capability)
		}
		if len(capability.Preconditions) == 0 {
			t.Fatalf("driver %q credential placeholder declares no precondition; the recognized-name scope must stay explicit", name)
		}
	}
}

package projects

import (
	"reflect"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestNormalizeAgentDefinitionAcceptsPiAndRejectsUnknownProvider(t *testing.T) {
	definition := domain.AgentDefinition{ID: "pi-agent", Name: "reviewer", Provider: "pi-agent", Model: " openai/gpt-5.4 ", ProjectID: "project-1", AgentName: "reviewer"}
	normalized, err := NormalizeAgentDefinition(definition, false)
	if err != nil {
		t.Fatalf("NormalizeAgentDefinition returned error: %v", err)
	}
	if normalized.Provider != "pi" || normalized.Model != "openai/gpt-5.4" {
		t.Fatalf("normalized definition = %#v", normalized)
	}

	definition.Provider = "unknown"
	if _, err := NormalizeAgentDefinition(definition, false); err == nil {
		t.Fatal("NormalizeAgentDefinition accepted an unknown provider")
	}
}

func TestNormalizeAgentDefinitionAcceptsDsh(t *testing.T) {
	definition := domain.AgentDefinition{ID: "dsh-agent", Name: "reviewer", Provider: "deepseek-harness", Model: " deepseek-official/deepseek-v4-flash ", ProjectID: "project-1", AgentName: "reviewer"}
	normalized, err := NormalizeAgentDefinition(definition, false)
	if err != nil {
		t.Fatalf("NormalizeAgentDefinition returned error: %v", err)
	}
	if normalized.Provider != "dsh" || normalized.Model != "deepseek-official/deepseek-v4-flash" {
		t.Fatalf("normalized definition = %#v", normalized)
	}
}

// TestNormalizeAgentDefinitionRejectsRemovedGeminiProvider pins the deliberate
// non-backward-compatible removal: every historical spelling is rejected as an
// unsupported provider instead of being normalized to a runner that no longer
// exists.
func TestNormalizeAgentDefinitionRejectsRemovedGeminiProvider(t *testing.T) {
	for _, provider := range []string{"gemini", "gemini-cli", "gemini_cli", "GEMINI"} {
		definition := domain.AgentDefinition{ID: "gemini-agent", Name: "reviewer", Provider: provider, ProjectID: "project-1", AgentName: "reviewer"}
		if _, err := NormalizeAgentDefinition(definition, false); err == nil {
			t.Fatalf("NormalizeAgentDefinition accepted removed provider %q", provider)
		}
	}
}

func TestNormalizeAgentDefinitionNormalizesCapsetIDs(t *testing.T) {
	definition := domain.AgentDefinition{
		ID: "agent-1", Name: "reviewer", Provider: "codex",
		CapsetIDs: []string{" beta ", "", "alpha", "beta", "  ", "gamma", "alpha"},
	}
	normalized, err := NormalizeAgentDefinition(definition, false)
	if err != nil {
		t.Fatalf("NormalizeAgentDefinition returned error: %v", err)
	}
	want := []string{"beta", "alpha", "gamma"}
	if !reflect.DeepEqual(normalized.CapsetIDs, want) {
		t.Fatalf("normalized CapsetIDs = %#v, want %#v", normalized.CapsetIDs, want)
	}
}

func TestNormalizeAgentDefinitionNilCapsetIDsNormalizesToEmpty(t *testing.T) {
	definition := domain.AgentDefinition{ID: "agent-1", Name: "reviewer", Provider: "codex"}
	normalized, err := NormalizeAgentDefinition(definition, false)
	if err != nil {
		t.Fatalf("NormalizeAgentDefinition returned error: %v", err)
	}
	if normalized.CapsetIDs == nil || len(normalized.CapsetIDs) != 0 {
		t.Fatalf("normalized CapsetIDs = %#v, want non-nil empty slice", normalized.CapsetIDs)
	}
}

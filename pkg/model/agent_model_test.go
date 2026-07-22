package model

import "testing"

func TestNormalizeAgentKindPiAliases(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "pi", want: "pi"},
		{input: " pi-agent ", want: "pi"},
		{input: "PI_AGENT", want: "pi"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			if got := NormalizeAgentKind(test.input); got != test.want {
				t.Fatalf("NormalizeAgentKind(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestNormalizeAgentDefinitionAcceptsPiAndRejectsUnknownProvider(t *testing.T) {
	definition := AgentDefinition{ID: "pi-agent", Name: "reviewer", Provider: "pi-agent", Model: " openai/gpt-5.4 "}
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

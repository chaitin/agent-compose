package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

func TestCellArtifactsAndAgentFilesWorkflows(t *testing.T) {
	root := t.TempDir()
	cellDir := filepath.Join(root, "cell")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script, command, args := CellExecSpec(CellTypePython, "/guest/cell")
	if script != "cell.py" || command != "python3" || len(args) != 2 {
		t.Fatalf("python spec %q %q %#v", script, command, args)
	}
	if err := WriteCellArtifacts(cellDir, "source", domain.ExecResult{Stdout: "out", Stderr: "err", Output: "", ExitCode: 2}); err != nil {
		t.Fatalf("WriteCellArtifacts returned error: %v", err)
	}
	recovered := RecoverExecResultFromCellArtifacts(cellDir, domain.ExecResult{})
	if recovered.Stdout != "out" || recovered.Stderr != "err" || recovered.Output != "outerr" || recovered.ExitCode != 2 || recovered.Success {
		t.Fatalf("recovered = %#v", recovered)
	}
	if err := WriteJSONArtifact(filepath.Join(cellDir, "value.json"), map[string]string{"ok": "true"}); err != nil {
		t.Fatalf("WriteJSONArtifact returned error: %v", err)
	}
	if FirstNonZeroInt(0, 0, 7) != 7 {
		t.Fatalf("FirstNonZeroInt failed")
	}
	for _, cellType := range []string{"", " JavaScript ", CellTypeShell, CellTypePython} {
		if normalized, err := NormalizeCellType(cellType); err != nil || normalized == "" {
			t.Fatalf("NormalizeCellType(%q) = %q/%v", cellType, normalized, err)
		}
	}
	if _, err := NormalizeCellType(CellTypeAgent); err == nil {
		t.Fatalf("NormalizeCellType agent returned nil error")
	}
	if config := AgentConfigFromDefinition(domain.AgentDefinition{ID: " agent-1 ", Provider: " ", Model: " model "}, " codex "); config.Provider != "codex" || config.AgentDefinitionID != "agent-1" || config.Model != "model" {
		t.Fatalf("AgentConfigFromDefinition fallback = %#v", config)
	}
	if config := AgentConfigFromDefinition(domain.AgentDefinition{Provider: "opencode", Model: "ignored", EnvItems: []domain.SandboxEnvVar{{Name: "OPENCODE_MODEL", Value: "env-model"}}}, "codex"); config.Model != "env-model" {
		t.Fatalf("AgentConfigFromDefinition opencode = %#v", config)
	}
	ApplyAgentProviderEnv(nil, []domain.SandboxEnvVar{{Name: "A", Value: "1"}})
	sessionEnvTarget := &domain.Sandbox{EnvItems: []domain.SandboxEnvVar{{Name: "A", Value: "session"}}}
	ApplyAgentProviderEnv(sessionEnvTarget, []domain.SandboxEnvVar{{Name: "A", Value: "agent"}, {Name: "B", Value: "agent"}})
	if env := domain.SandboxEnvMap(sessionEnvTarget.ExecutionProviderEnvItems); env["A"] != "agent" || env["B"] != "agent" {
		t.Fatalf("ApplyAgentProviderEnv session env = %#v", sessionEnvTarget.ExecutionProviderEnvItems)
	}
	providerEnvTarget := &domain.Sandbox{EnvItems: []domain.SandboxEnvVar{{Name: "A", Value: "session"}}, ExecutionProviderEnvItems: []domain.SandboxEnvVar{{Name: "A", Value: "provider"}}}
	ApplyAgentProviderEnv(providerEnvTarget, []domain.SandboxEnvVar{{Name: "A", Value: "agent"}})
	if env := domain.SandboxEnvMap(providerEnvTarget.ExecutionProviderEnvItems); env["A"] != "provider" {
		t.Fatalf("ApplyAgentProviderEnv provider env = %#v", providerEnvTarget.ExecutionProviderEnvItems)
	}
	if SessionTagValue([]domain.SandboxTag{{Name: " agent ", Value: " codex "}}, " agent ") != "" || SessionTagValue([]domain.SandboxTag{{Name: "agent", Value: " codex "}}, "agent") != "codex" {
		t.Fatalf("SessionTagValue returned unexpected value")
	}

	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "session", "workspace")}}
	config := &appconfig.Config{GuestStateRoot: "/guest/state"}
	promptPath, err := WriteAgentPromptFile(config, session, "codex", "hello")
	if err != nil || !strings.HasPrefix(promptPath, "/guest/state/agents/prompts/") {
		t.Fatalf("WriteAgentPromptFile path=%q err=%v", promptPath, err)
	}
	if err := WriteAgentSystemPromptFile(session, "system prompt"); err != nil {
		t.Fatalf("WriteAgentSystemPromptFile returned error: %v", err)
	}
	if data, err := os.ReadFile(HostAgentSystemPromptPath(session)); err != nil || string(data) != "system prompt" {
		t.Fatalf("system prompt data=%q err=%v", string(data), err)
	}
	if err := WriteAgentSystemPromptFile(session, ""); err != nil {
		t.Fatalf("remove system prompt returned error: %v", err)
	}
	if err := WriteAgentSystemPromptFile(&domain.Sandbox{}, "system"); err == nil {
		t.Fatalf("expected missing workspace path error")
	}
	schemaPath, err := WriteAgentOutputSchemaFile(config, session, "codex", `{"type":"object"}`)
	if err != nil || !strings.HasPrefix(schemaPath, "/guest/state/agents/schemas/") {
		t.Fatalf("WriteAgentOutputSchemaFile path=%q err=%v", schemaPath, err)
	}
	if _, err := WriteAgentOutputSchemaFile(config, session, "codex", `[]`); err == nil {
		t.Fatalf("expected non-object schema error")
	}
}

func TestIntegrationCellArtifactsAndAgentFilesWorkflows(t *testing.T) {
	TestCellArtifactsAndAgentFilesWorkflows(t)
}

func TestE2ECellArtifactsAndAgentFilesWorkflows(t *testing.T) {
	TestCellArtifactsAndAgentFilesWorkflows(t)
}

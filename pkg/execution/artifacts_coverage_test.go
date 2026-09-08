package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
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
	// A configured `model:` wins for opencode as it does for every other
	// provider; OPENCODE_MODEL is only the fallback for agents written before
	// `model:` was read at all.
	if config := AgentConfigFromDefinition(domain.AgentDefinition{Provider: "opencode", Model: "configured-model", EnvItems: []domain.SandboxEnvVar{{Name: "OPENCODE_MODEL", Value: "env-model"}}}, "codex"); config.Model != "configured-model" {
		t.Fatalf("AgentConfigFromDefinition opencode = %#v", config)
	}
	if config := AgentConfigFromDefinition(domain.AgentDefinition{Provider: "opencode", EnvItems: []domain.SandboxEnvVar{{Name: "OPENCODE_MODEL", Value: "env-model"}}}, "codex"); config.Model != "env-model" {
		t.Fatalf("AgentConfigFromDefinition opencode env fallback = %#v", config)
	}
	ApplyAgentProviderEnv(nil, []domain.SandboxEnvVar{{Name: "A", Value: "1"}})
	sessionEnvTarget := &domain.Sandbox{EnvItems: []domain.SandboxEnvVar{{Name: "A", Value: "session"}}}
	ApplyAgentProviderEnv(sessionEnvTarget, []domain.SandboxEnvVar{{Name: "A", Value: "agent"}, {Name: "B", Value: "agent"}})
	if env := domain.SandboxEnvMap(sessionEnvTarget.ProviderEnvItems); env["A"] != "session" || env["B"] != "agent" {
		t.Fatalf("ApplyAgentProviderEnv session env = %#v", sessionEnvTarget.ProviderEnvItems)
	}
	providerEnvTarget := &domain.Sandbox{EnvItems: []domain.SandboxEnvVar{{Name: "A", Value: "session"}}, ProviderEnvItems: []domain.SandboxEnvVar{{Name: "A", Value: "provider"}}}
	ApplyAgentProviderEnv(providerEnvTarget, []domain.SandboxEnvVar{{Name: "A", Value: "agent"}})
	if env := domain.SandboxEnvMap(providerEnvTarget.ProviderEnvItems); env["A"] != "provider" {
		t.Fatalf("ApplyAgentProviderEnv provider env = %#v", providerEnvTarget.ProviderEnvItems)
	}
	if SessionTagValue([]domain.SandboxTag{{Name: " agent ", Value: " codex "}}, " agent ") != "" || SessionTagValue([]domain.SandboxTag{{Name: "agent", Value: " codex "}}, "agent") != "codex" {
		t.Fatalf("SessionTagValue returned unexpected value")
	}

	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "session", "workspace")}}
	config := &appconfig.Config{GuestStateRoot: "/guest/state"}
	promptPath, err := WriteAgentPromptFile(context.Background(), AgentPromptFileRequest{Config: config, Sandbox: session, Agent: "codex", Message: "hello"})
	if err != nil || !strings.HasPrefix(promptPath, "/guest/state/agents/prompts/") {
		t.Fatalf("WriteAgentPromptFile path=%q err=%v", promptPath, err)
	}

	// No-shared-mount path (k8s - see design doc §2.1): the content must
	// also be pushed via the GuestFileWriterFunc, at the same guest path
	// the mount-based drivers would have made it appear at for free.
	var pushedPath string
	var pushedContent []byte
	pushSession := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "push-session", "workspace")}}
	pushedPromptPath, err := WriteAgentPromptFile(context.Background(), AgentPromptFileRequest{
		Config: config, Sandbox: pushSession, Agent: "codex", Message: "pushed hello",
		WriteGuestFile: func(_ context.Context, guestPath string, content []byte) error {
			pushedPath = guestPath
			pushedContent = content
			return nil
		},
	})
	if err != nil {
		t.Fatalf("WriteAgentPromptFile with writer returned error: %v", err)
	}
	if pushedPath != pushedPromptPath {
		t.Fatalf("pushed guest path = %q, want it to match the returned path %q", pushedPath, pushedPromptPath)
	}
	if string(pushedContent) != "pushed hello" {
		t.Fatalf("pushed content = %q, want %q", pushedContent, "pushed hello")
	}
	if err := WriteAgentSystemPromptFile(context.Background(), config, session, "system prompt", nil); err != nil {
		t.Fatalf("WriteAgentSystemPromptFile returned error: %v", err)
	}
	if data, err := os.ReadFile(HostAgentSystemPromptPath(session)); err != nil || string(data) != "system prompt" {
		t.Fatalf("system prompt data=%q err=%v", string(data), err)
	}
	if err := WriteAgentSystemPromptFile(context.Background(), config, session, "", nil); err != nil {
		t.Fatalf("remove system prompt returned error: %v", err)
	}
	if err := WriteAgentSystemPromptFile(context.Background(), config, &domain.Sandbox{}, "system", nil); err == nil {
		t.Fatalf("expected missing workspace path error")
	}
	var pushedSystemPromptPath string
	var pushedSystemPromptContent []byte
	if err := WriteAgentSystemPromptFile(context.Background(), config, session, "pushed system prompt", func(_ context.Context, guestPath string, content []byte) error {
		pushedSystemPromptPath = guestPath
		pushedSystemPromptContent = content
		return nil
	}); err != nil {
		t.Fatalf("WriteAgentSystemPromptFile with writer returned error: %v", err)
	}
	if pushedSystemPromptPath != "/guest/state/agents/system-prompts/"+AgentSystemPromptFileName {
		t.Fatalf("pushed system prompt guest path = %q", pushedSystemPromptPath)
	}
	if string(pushedSystemPromptContent) != "pushed system prompt" {
		t.Fatalf("pushed system prompt content = %q", pushedSystemPromptContent)
	}
	schemaPath, err := WriteAgentOutputSchemaFile(context.Background(), AgentOutputSchemaFileRequest{Config: config, Sandbox: session, Agent: "codex", SchemaJSON: `{"type":"object"}`})
	if err != nil || !strings.HasPrefix(schemaPath, "/guest/state/agents/schemas/") {
		t.Fatalf("WriteAgentOutputSchemaFile path=%q err=%v", schemaPath, err)
	}
	if _, err := WriteAgentOutputSchemaFile(context.Background(), AgentOutputSchemaFileRequest{Config: config, Sandbox: session, Agent: "codex", SchemaJSON: `[]`}); err == nil {
		t.Fatalf("expected non-object schema error")
	}
}

func TestIntegrationCellArtifactsAndAgentFilesWorkflows(t *testing.T) {
	TestCellArtifactsAndAgentFilesWorkflows(t)
}

func TestE2ECellArtifactsAndAgentFilesWorkflows(t *testing.T) {
	TestCellArtifactsAndAgentFilesWorkflows(t)
}

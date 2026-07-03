package execution

import (
	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const AgentSystemPromptFileName = "system-prompt.txt"

func HostAgentSystemPromptPath(session *domain.Session) string {
	if session == nil || strings.TrimSpace(session.Summary.WorkspacePath) == "" {
		return ""
	}
	return filepath.Join(HostSessionDir(session), "state", "agents", "system-prompts", AgentSystemPromptFileName)
}

func WriteAgentPromptFile(config *appconfig.Config, session *domain.Session, agent, message string) (string, error) {
	hostSessionDir := filepath.Dir(session.Summary.WorkspacePath)
	promptDir := filepath.Join(hostSessionDir, "state", "agents", "prompts")
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		return "", fmt.Errorf("create agent prompt dir: %w", err)
	}
	name := fmt.Sprintf("%s-%d.txt", domain.NormalizeAgentKind(agent), time.Now().UTC().UnixNano())
	hostPath := filepath.Join(promptDir, name)
	if err := os.WriteFile(hostPath, []byte(message), 0o644); err != nil {
		return "", fmt.Errorf("write agent prompt file: %w", err)
	}
	return filepath.Join(config.GuestStateRoot, "agents", "prompts", name), nil
}

// WriteAgentSystemPromptFile materializes agent identity for the guest runtime at a
// fixed convention path under the session state tree.
func WriteAgentSystemPromptFile(session *domain.Session, systemPrompt string) error {
	systemPrompt = strings.TrimSpace(systemPrompt)
	hostPath := HostAgentSystemPromptPath(session)
	if hostPath == "" {
		if systemPrompt == "" {
			return nil
		}
		return fmt.Errorf("session workspace path is required to write agent system prompt")
	}
	if systemPrompt == "" {
		if err := os.Remove(hostPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove agent system prompt file: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
		return fmt.Errorf("create agent system prompt dir: %w", err)
	}
	if err := os.WriteFile(hostPath, []byte(systemPrompt), 0o644); err != nil {
		return fmt.Errorf("write agent system prompt file: %w", err)
	}
	return nil
}

func WriteAgentOutputSchemaFile(config *appconfig.Config, session *domain.Session, agent, schemaJSON string) (string, error) {
	schemaJSON = strings.TrimSpace(schemaJSON)
	if schemaJSON == "" {
		return "", nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(schemaJSON), &decoded); err != nil {
		return "", fmt.Errorf("decode agent output schema json: %w", err)
	}
	if _, ok := decoded.(map[string]any); !ok {
		return "", fmt.Errorf("agent output schema must be a JSON object")
	}
	hostSessionDir := filepath.Dir(session.Summary.WorkspacePath)
	schemaDir := filepath.Join(hostSessionDir, "state", "agents", "schemas")
	if err := os.MkdirAll(schemaDir, 0o755); err != nil {
		return "", fmt.Errorf("create agent schema dir: %w", err)
	}
	name := fmt.Sprintf("%s-%d.json", domain.NormalizeAgentKind(agent), time.Now().UTC().UnixNano())
	hostPath := filepath.Join(schemaDir, name)
	if err := os.WriteFile(hostPath, []byte(schemaJSON), 0o644); err != nil {
		return "", fmt.Errorf("write agent schema file: %w", err)
	}
	return filepath.Join(config.GuestStateRoot, "agents", "schemas", name), nil
}

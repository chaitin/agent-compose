package execution

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
)

type AgentMCPConfigPayload struct {
	MCPServers map[string]compose.NormalizedMCPServerSpec `json:"mcp_servers,omitempty"`
}

func HostAgentMCPConfigPath(session *domain.Sandbox) string {
	if session == nil || strings.TrimSpace(session.Summary.WorkspacePath) == "" {
		return ""
	}
	return filepath.Join(HostSandboxDir(session), "state", "agents", "mcp", "config.json")
}

func WriteAgentMCPConfigFile(session *domain.Sandbox, mcps map[string]compose.NormalizedMCPServerSpec) error {
	hostPath := HostAgentMCPConfigPath(session)
	if hostPath == "" {
		if len(mcps) == 0 {
			return nil
		}
		return fmt.Errorf("sandbox workspace path is required to write agent mcp config")
	}
	if len(mcps) == 0 {
		if err := os.Remove(hostPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove agent mcp config file: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
		return fmt.Errorf("create agent mcp config dir: %w", err)
	}
	if err := WriteJSONArtifact(hostPath, AgentMCPConfigPayload{MCPServers: mcps}); err != nil {
		return fmt.Errorf("write agent mcp config file: %w", err)
	}
	return nil
}

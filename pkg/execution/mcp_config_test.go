package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
)

func TestWriteAgentMCPConfigFile(t *testing.T) {
	root := t.TempDir()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "workspace")}}
	mcps := map[string]compose.NormalizedMCPServerSpec{
		"filesystem": {Type: "local", Command: "npx", Args: []string{"-y", "server"}},
	}
	if err := WriteAgentMCPConfigFile(session, mcps); err != nil {
		t.Fatalf("WriteAgentMCPConfigFile returned error: %v", err)
	}
	path := HostAgentMCPConfigPath(session)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if !strings.Contains(string(data), `"mcp_servers"`) || !strings.Contains(string(data), `"filesystem"`) {
		t.Fatalf("config = %q", string(data))
	}
	if err := WriteAgentMCPConfigFile(session, nil); err != nil {
		t.Fatalf("WriteAgentMCPConfigFile remove returned error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected config file removed, stat err=%v", err)
	}
}

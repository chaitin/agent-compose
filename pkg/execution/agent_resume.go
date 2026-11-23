package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type storedAgentThreadState struct {
	ThreadID        string `json:"threadId"`
	LegacySessionID string `json:"sessionId"`
}

// AgentThreadIDRequest identifies the provider state a finished agent
// execution left behind, so the thread ID can be recovered when the runtime
// result did not report one.
type AgentThreadIDRequest struct {
	Config        *appconfig.Config
	Sandbox       *domain.Sandbox
	Agent         string
	ThreadID      string
	ReadGuestFile GuestFileReaderFunc
}

func LoadStoredAgentThreadID(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return parseStoredAgentThreadID(data)
}

func parseStoredAgentThreadID(data []byte) string {
	var state storedAgentThreadState
	if err := json.Unmarshal(data, &state); err != nil {
		return ""
	}
	if threadID := strings.TrimSpace(state.ThreadID); threadID != "" {
		return threadID
	}
	return strings.TrimSpace(state.LegacySessionID)
}

// ResolveAgentThreadID returns the thread ID the runtime reported, falling
// back to the provider state file the guest runtime writes under
// state/agents/providers/<provider>.json. Drivers without a shared filesystem
// pull that file through ReadGuestFile; the others read the host copy.
func ResolveAgentThreadID(ctx context.Context, req AgentThreadIDRequest) string {
	if threadID := strings.TrimSpace(req.ThreadID); threadID != "" {
		return threadID
	}
	provider := domain.NormalizeAgentKind(req.Agent)
	if provider == "" {
		return ""
	}
	if req.ReadGuestFile != nil {
		config := req.Config
		if config == nil {
			config = &appconfig.Config{}
		}
		appconfig.ApplyDefaultGuestPaths(config)
		guestStatePath := filepath.Join(config.GuestStateRoot, "agents", "providers", provider+".json")
		data, err := req.ReadGuestFile(ctx, guestStatePath)
		if err != nil {
			return ""
		}
		return parseStoredAgentThreadID(data)
	}
	if req.Sandbox == nil {
		return ""
	}
	return LoadStoredAgentThreadID(filepath.Join(HostSandboxDir(req.Sandbox), "state", "agents", "providers", provider+".json"))
}

func HostSandboxDir(session *domain.Sandbox) string {
	return filepath.Dir(session.Summary.WorkspacePath)
}

func HostSandboxHome(session *domain.Sandbox) string {
	return filepath.Join(HostSandboxDir(session), "home")
}

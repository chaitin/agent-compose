package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func WriteAgentThreadArtifact(path string, info *domain.AgentResumeInfo) error {
	if info == nil {
		return nil
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent thread artifact: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write agent thread artifact: %w", err)
	}
	return nil
}

type storedAgentThreadState struct {
	ThreadID        string `json:"threadId"`
	LegacySessionID string `json:"sessionId"`
}

// AgentResumeInfoRequest identifies the provider state used to reconstruct
// resumable agent metadata after an execution.
type AgentResumeInfoRequest struct {
	Config        *appconfig.Config
	Sandbox       *domain.Sandbox
	Agent         string
	ThreadID      string
	ManifestPath  string
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

func CollectAgentResumeInfo(ctx context.Context, req AgentResumeInfoRequest) *domain.AgentResumeInfo {
	config, session := req.Config, req.Sandbox
	agent, threadID, manifestPath := req.Agent, req.ThreadID, req.ManifestPath
	readGuestFile := req.ReadGuestFile
	provider := domain.NormalizeAgentKind(agent)
	info := &domain.AgentResumeInfo{
		Provider:           provider,
		ThreadID:           strings.TrimSpace(threadID),
		ThreadManifestPath: manifestPath,
		UpdatedAt:          time.Now().UTC(),
	}
	if readGuestFile != nil {
		// No shared filesystem: only resolve the thread ID, by pulling the
		// provider's state file over Exec when the caller didn't already
		// report one. ThreadStatePath/ProviderLogPaths need a host-local
		// path to mean anything to a later reader and are deliberately left
		// unset here rather than reporting a path nothing exists at - see
		// design doc §6 for the FindAgentThreadLogPaths directory-scan gap
		// this doesn't attempt to close.
		if info.ThreadID == "" {
			appconfig.ApplyDefaultGuestPaths(config)
			guestStatePath := filepath.Join(config.GuestStateRoot, "agents", "providers", provider+".json")
			if data, err := readGuestFile(ctx, guestStatePath); err == nil {
				info.ThreadID = parseStoredAgentThreadID(data)
			}
		}
	} else {
		statePath := filepath.Join(HostSandboxDir(session), "state", "agents", "providers", provider+".json")
		if stat, err := os.Stat(statePath); err == nil && !stat.IsDir() {
			info.ThreadStatePath = statePath
			if info.ThreadID == "" {
				info.ThreadID = LoadStoredAgentThreadID(statePath)
			}
		}
		info.ProviderLogPaths = FindAgentThreadLogPaths(HostSandboxHome(session), provider, info.ThreadID)
	}
	if info.Provider == "" && info.ThreadID == "" && info.ThreadStatePath == "" && info.ThreadManifestPath == "" && len(info.ProviderLogPaths) == 0 {
		return nil
	}
	return info
}

func FindAgentThreadLogPaths(homeDir, provider, threadID string) []string {
	roots := AgentThreadLogRoots(homeDir, provider)
	if len(roots) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var paths []string
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		info, err := os.Stat(root)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			if ShouldIncludeAgentJSONL(root, provider, threadID) {
				if _, ok := seen[root]; !ok {
					seen[root] = struct{}{}
					paths = append(paths, root)
				}
			}
			continue
		}
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry == nil || entry.IsDir() {
				return nil
			}
			if !ShouldIncludeAgentJSONL(path, provider, threadID) {
				return nil
			}
			if _, ok := seen[path]; ok {
				return nil
			}
			seen[path] = struct{}{}
			paths = append(paths, path)
			return nil
		})
	}
	sort.Strings(paths)
	return paths
}

func AgentThreadLogRoots(homeDir, provider string) []string {
	switch provider {
	case "codex":
		return []string{
			filepath.Join(homeDir, ".codex", "history.jsonl"),
			filepath.Join(homeDir, ".codex", "sessions"),
		}
	case "claude":
		return []string{
			filepath.Join(homeDir, ".claude"),
			filepath.Join(homeDir, ".config", "claude"),
			filepath.Join(homeDir, ".config", "Claude"),
		}
	default:
		return nil
	}
}

func ShouldIncludeAgentJSONL(path, provider, threadID string) bool {
	if filepath.Ext(path) != ".jsonl" {
		return false
	}
	if provider == "codex" && threadID != "" && strings.Contains(path, string(filepath.Separator)+"sessions"+string(filepath.Separator)) {
		return strings.Contains(filepath.Base(path), threadID)
	}
	return true
}

func HostSandboxDir(session *domain.Sandbox) string {
	return filepath.Dir(session.Summary.WorkspacePath)
}

func HostSandboxHome(session *domain.Sandbox) string {
	return filepath.Join(HostSandboxDir(session), "home")
}

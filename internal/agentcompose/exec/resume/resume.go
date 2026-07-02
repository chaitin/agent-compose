package resume

import (
	agentdomain "agent-compose/internal/agentcompose/agent"
	sessiondomain "agent-compose/internal/agentcompose/session"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func WriteAgentSessionArtifact(path string, info *sessiondomain.AgentResumeInfo) error {
	if info == nil {
		return nil
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent session artifact: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write agent session artifact: %w", err)
	}
	return nil
}

func CollectAgentResumeInfo(session *sessiondomain.Session, agent, agentSessionID, manifestPath string) *sessiondomain.AgentResumeInfo {
	provider := agentdomain.NormalizeKind(agent)
	info := &sessiondomain.AgentResumeInfo{
		Provider:            provider,
		SessionID:           strings.TrimSpace(agentSessionID),
		SessionManifestPath: manifestPath,
		UpdatedAt:           time.Now().UTC(),
	}
	statePath := filepath.Join(HostSessionDir(session), "state", "agents", "providers", provider+".json")
	if stat, err := os.Stat(statePath); err == nil && !stat.IsDir() {
		info.SessionStatePath = statePath
		if info.SessionID == "" {
			info.SessionID = LoadStoredAgentSessionID(statePath)
		}
	}
	info.SessionJSONLPaths = FindAgentSessionJSONLPaths(HostSessionHome(session), provider, info.SessionID)
	if info.Provider == "" && info.SessionID == "" && info.SessionStatePath == "" && info.SessionManifestPath == "" && len(info.SessionJSONLPaths) == 0 {
		return nil
	}
	return info
}

func LoadStoredAgentSessionID(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var state struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return ""
	}
	return strings.TrimSpace(state.SessionID)
}

func FindAgentSessionJSONLPaths(homeDir, provider, sessionID string) []string {
	roots := AgentSessionJSONLRoots(homeDir, provider)
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
			if ShouldIncludeAgentJSONL(root, provider, sessionID) {
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
			if !ShouldIncludeAgentJSONL(path, provider, sessionID) {
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

func AgentSessionJSONLRoots(homeDir, provider string) []string {
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
	case "gemini":
		return []string{
			filepath.Join(homeDir, ".gemini"),
			filepath.Join(homeDir, ".config", "gemini"),
			filepath.Join(homeDir, ".local", "share", "gemini"),
		}
	default:
		return nil
	}
}

func ShouldIncludeAgentJSONL(path, provider, sessionID string) bool {
	if filepath.Ext(path) != ".jsonl" {
		return false
	}
	if provider == "codex" && sessionID != "" && strings.Contains(path, string(filepath.Separator)+"sessions"+string(filepath.Separator)) {
		return strings.Contains(filepath.Base(path), sessionID)
	}
	return true
}

func HostSessionDir(session *sessiondomain.Session) string {
	if session == nil {
		return ""
	}
	return filepath.Dir(session.Summary.WorkspacePath)
}

func HostSessionHome(session *sessiondomain.Session) string {
	return filepath.Join(HostSessionDir(session), "home")
}

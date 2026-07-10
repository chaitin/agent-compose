package execution

import (
	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/workspaces"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const AgentSystemPromptFileName = "system-prompt.txt"
const agentSkillsManifestFileName = ".agent-compose-skills.json"
const claudeSkillsManagedMarkerFileName = ".agent-compose-managed"

type ResolvedAgentSkill struct {
	Name     string `json:"name"`
	LocalDir string `json:"local_dir"`
}

type agentSkillsManifest struct {
	Names []string `json:"names"`
}

func HostAgentSystemPromptPath(session *domain.Sandbox) string {
	if session == nil || strings.TrimSpace(session.Summary.WorkspacePath) == "" {
		return ""
	}
	return filepath.Join(HostSandboxDir(session), "state", "agents", "system-prompts", AgentSystemPromptFileName)
}

func HostAgentSkillsDir(session *domain.Sandbox) string {
	if session == nil || strings.TrimSpace(session.Summary.WorkspacePath) == "" {
		return ""
	}
	return filepath.Join(HostSandboxDir(session), "home", ".agents", "skills")
}

func WriteAgentPromptFile(config *appconfig.Config, session *domain.Sandbox, agent, message string) (string, error) {
	hostSandboxDir := filepath.Dir(session.Summary.WorkspacePath)
	promptDir := filepath.Join(hostSandboxDir, "state", "agents", "prompts")
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

func WriteAgentSkills(session *domain.Sandbox, skills []ResolvedAgentSkill) ([]string, error) {
	skillsDir := HostAgentSkillsDir(session)
	if skillsDir == "" {
		if len(skills) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("session workspace path is required to write agent skills")
	}
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create agent skills dir: %w", err)
	}
	current := make(map[string]ResolvedAgentSkill, len(skills))
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		name := strings.TrimSpace(skill.Name)
		if err := validateAgentSkillName(name); err != nil {
			return nil, err
		}
		localDir := strings.TrimSpace(skill.LocalDir)
		if localDir == "" {
			return nil, fmt.Errorf("agent skill %s local dir is required", name)
		}
		if _, ok := current[name]; ok {
			return nil, fmt.Errorf("duplicate agent skill %s", name)
		}
		current[name] = ResolvedAgentSkill{Name: name, LocalDir: localDir}
		names = append(names, name)
	}
	previous := readAgentSkillsManifest(skillsDir)
	for _, name := range previous.Names {
		if err := validateAgentSkillName(name); err != nil {
			continue
		}
		if _, ok := current[name]; ok {
			continue
		}
		if err := os.RemoveAll(filepath.Join(skillsDir, name)); err != nil {
			return nil, fmt.Errorf("remove stale agent skill %s: %w", name, err)
		}
	}
	for _, name := range names {
		if err := copyAgentSkill(current[name], filepath.Join(skillsDir, name)); err != nil {
			return nil, err
		}
	}
	if err := writeAgentSkillsManifest(skillsDir, agentSkillsManifest{Names: names}); err != nil {
		return nil, err
	}
	if err := reconcileClaudeSkillsLink(session, skillsDir, len(names) > 0); err != nil {
		return nil, err
	}
	return names, nil
}

func validateAgentSkillName(name string) error {
	if name == "" {
		return fmt.Errorf("agent skill name is required")
	}
	if filepath.IsAbs(name) || name == "." || name == ".." || name != filepath.Base(name) {
		return fmt.Errorf("agent skill name %q is not a valid path segment", name)
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("agent skill name %q is not a valid path segment", name)
	}
	return nil
}

func readAgentSkillsManifest(skillsDir string) agentSkillsManifest {
	data, err := os.ReadFile(filepath.Join(skillsDir, agentSkillsManifestFileName))
	if err != nil {
		return agentSkillsManifest{}
	}
	var manifest agentSkillsManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return agentSkillsManifest{}
	}
	return manifest
}

func writeAgentSkillsManifest(skillsDir string, manifest agentSkillsManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal agent skills manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(skillsDir, agentSkillsManifestFileName), data, 0o644); err != nil {
		return fmt.Errorf("write agent skills manifest: %w", err)
	}
	return nil
}

func copyAgentSkill(skill ResolvedAgentSkill, dst string) error {
	srcRoot, err := os.OpenRoot(skill.LocalDir)
	if err != nil {
		return fmt.Errorf("open agent skill %s: %w", skill.Name, err)
	}
	defer func() { _ = srcRoot.Close() }()
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("remove agent skill destination %s: %w", skill.Name, err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("create agent skill destination %s: %w", skill.Name, err)
	}
	if err := workspaces.CopyRootDirectoryContents(srcRoot, dst); err != nil {
		return fmt.Errorf("copy agent skill %s: %w", skill.Name, err)
	}
	return nil
}

func reconcileClaudeSkillsLink(session *domain.Sandbox, skillsDir string, enabled bool) error {
	claudeSkills := filepath.Join(HostSandboxDir(session), "home", ".claude", "skills")
	if !enabled {
		managed, err := managedClaudeSkillsPath(claudeSkills)
		if err != nil {
			return err
		}
		if managed {
			if err := os.RemoveAll(claudeSkills); err != nil {
				return fmt.Errorf("remove managed claude skills path: %w", err)
			}
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(claudeSkills), 0o755); err != nil {
		return fmt.Errorf("create claude skills parent: %w", err)
	}
	managed, err := managedClaudeSkillsPath(claudeSkills)
	if err != nil {
		return err
	}
	if !managed {
		if _, err := os.Lstat(claudeSkills); err == nil {
			return fmt.Errorf("claude skills path %s already exists and is not managed by agent-compose", claudeSkills)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat claude skills path: %w", err)
		}
	}
	if managed {
		if err := os.RemoveAll(claudeSkills); err != nil {
			return fmt.Errorf("remove managed claude skills path: %w", err)
		}
	}
	if err := os.Symlink("../.agents/skills", claudeSkills); err == nil {
		return nil
	}
	srcRoot, err := os.OpenRoot(skillsDir)
	if err != nil {
		return fmt.Errorf("open projected skills for claude fallback: %w", err)
	}
	defer func() { _ = srcRoot.Close() }()
	if err := os.MkdirAll(claudeSkills, 0o755); err != nil {
		return fmt.Errorf("create claude skills fallback: %w", err)
	}
	if err := os.WriteFile(filepath.Join(claudeSkills, claudeSkillsManagedMarkerFileName), []byte("agent-compose\n"), 0o644); err != nil {
		return fmt.Errorf("write claude skills fallback marker: %w", err)
	}
	if err := workspaces.CopyRootDirectoryContents(srcRoot, claudeSkills); err != nil {
		return fmt.Errorf("copy claude skills fallback: %w", err)
	}
	return nil
}

func managedClaudeSkillsPath(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat claude skills path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return false, fmt.Errorf("read claude skills link: %w", err)
		}
		return filepath.Clean(target) == filepath.Clean("../.agents/skills"), nil
	}
	if !info.IsDir() {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(path, claudeSkillsManagedMarkerFileName)); err == nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat claude skills marker: %w", err)
	}
	return false, nil
}

// WriteAgentSystemPromptFile materializes agent identity for the guest runtime at a
// fixed convention path under the sandbox state tree.
func WriteAgentSystemPromptFile(session *domain.Sandbox, systemPrompt string) error {
	systemPrompt = strings.TrimSpace(systemPrompt)
	hostPath := HostAgentSystemPromptPath(session)
	if hostPath == "" {
		if systemPrompt == "" {
			return nil
		}
		return fmt.Errorf("sandbox workspace path is required to write agent system prompt")
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

func WriteAgentOutputSchemaFile(config *appconfig.Config, session *domain.Sandbox, agent, schemaJSON string) (string, error) {
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
	hostSandboxDir := filepath.Dir(session.Summary.WorkspacePath)
	schemaDir := filepath.Join(hostSandboxDir, "state", "agents", "schemas")
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

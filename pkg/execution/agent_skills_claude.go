package execution

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/chaitin/agent-compose/pkg/workspaces"
)

func validateClaudeSkillsPath(path string, enabled bool) error {
	managed, err := managedClaudeSkillsPath(path)
	if err != nil {
		return err
	}
	if !enabled || managed {
		return nil
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("claude skills path %s already exists and is not managed by agent-compose", path)
	} else if !isAgentSkillMissing(err) {
		return err
	}
	return nil
}

func stageClaudeSkillsProjection(ctx context.Context, update *agentSkillsUpdate, projection agentSkillsProjection, operations agentSkillsFileOperations) error {
	names := projection.names
	path := filepath.Join(HostSandboxDir(update.session), "home", ".claude", "skills")
	managed, err := managedClaudeSkillsPath(path)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		if managed {
			return update.addOperation("claude", "claude")
		}
		return nil
	}
	if target, err := os.Readlink(path); err == nil && filepath.Clean(target) == "../.agents/skills" {
		return nil
	}
	if managed && len(update.journal.Operations) == 0 {
		expected, err := AgentSkillsProjectionEntries(ctx, HostAgentSkillsDir(update.session))
		if err != nil {
			return err
		}
		actual, err := AgentSkillsProjectionEntries(ctx, path)
		if err == nil && slices.Equal(expected, withoutClaudeSkillsMarker(actual)) {
			return nil
		}
		if err != nil && !isAgentSkillMissing(err) && !errors.Is(err, errInvalidAgentSkillEntry) {
			return fmt.Errorf("inspect existing claude skills fallback: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	staged := filepath.Join(update.root, "claude")
	if err := operations.symlink("../.agents/skills", staged); err != nil {
		if err := stageClaudeSkillsFallback(ctx, update, projection, operations); err != nil {
			return err
		}
	}
	return update.addOperation("claude", "claude")
}

func stageClaudeSkillsFallback(ctx context.Context, update *agentSkillsUpdate, projection agentSkillsProjection, operations agentSkillsFileOperations) error {
	current, names, previous := projection.current, projection.names, projection.previous
	staged := filepath.Join(update.root, "claude")
	source, err := os.OpenRoot(HostAgentSkillsDir(update.session))
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	if err := os.MkdirAll(staged, 0o755); err != nil {
		return err
	}
	entries, err := fs.ReadDir(source.FS(), ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == agentSkillsManifestFileName || slices.Contains(previous.Names, name) {
			continue
		}
		if err := workspaces.CopyRootEntry(ctx, source, name, filepath.Join(staged, name)); err != nil {
			return fmt.Errorf("stage unmanaged claude skill entry: %w", err)
		}
	}
	for _, name := range names {
		target := filepath.Join(staged, name)
		if err := copyAgentSkillToStaging(ctx, current[name], target, operations); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(staged, claudeSkillsManagedMarkerFileName), []byte("agent-compose\n"), 0o644)
}

func withoutClaudeSkillsMarker(entries []AgentSkillFileEntry) []AgentSkillFileEntry {
	result := make([]AgentSkillFileEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Path != claudeSkillsManagedMarkerFileName {
			result = append(result, entry)
		}
	}
	return result
}

func managedClaudeSkillsPath(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if isAgentSkillMissing(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat claude skills path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return false, fmt.Errorf("read claude skills link: %w", err)
		}
		return filepath.Clean(target) == "../.agents/skills", nil
	}
	if !info.IsDir() {
		return false, nil
	}
	return agentSkillsOwnedMarker(filepath.Join(path, claudeSkillsManagedMarkerFileName), "agent-compose\n")
}

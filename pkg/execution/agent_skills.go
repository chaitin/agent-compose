package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

const agentSkillsManifestFileName = ".agent-compose-skills.json"
const claudeSkillsManagedMarkerFileName = ".agent-compose-managed"

// ResolvedAgentSkill is a validated, locally available skill artifact.
type ResolvedAgentSkill struct {
	Name        string `json:"name"`
	LocalDir    string `json:"local_dir"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type agentSkillsManifest struct {
	Version      int               `json:"version,omitempty"`
	Names        []string          `json:"names"`
	Fingerprints map[string]string `json:"fingerprints,omitempty"`
}

// GuestSkillsWriterFunc reconciles the canonical guest skills and provider
// aliases. It verifies guest contents before skipping an unchanged delivery.
type GuestSkillsWriterFunc func(context.Context, string) error

type agentSkillsFileOperations struct {
	copy     func(context.Context, *os.Root, string) error
	exchange func(string, string) error
	symlink  func(string, string) error
}

func defaultAgentSkillsFileOperations() agentSkillsFileOperations {
	return agentSkillsFileOperations{copy: workspaces.CopyRootDirectoryContentsContext, exchange: exchangeAgentSkillPaths, symlink: os.Symlink}
}

func HostAgentSkillsDir(session *domain.Sandbox) string {
	if session == nil || strings.TrimSpace(session.Summary.WorkspacePath) == "" {
		return ""
	}
	return filepath.Join(HostSandboxDir(session), "home", ".agents", "skills")
}

// WriteAgentSkills refreshes private skill copies only when their contents have
// changed. Staged directories are published atomically and failed updates roll
// back both the directories and their manifest.
func WriteAgentSkills(ctx context.Context, _ *appconfig.Config, session *domain.Sandbox, skills []ResolvedAgentSkill, writeGuest GuestSkillsWriterFunc) ([]string, error) {
	return writeAgentSkills(ctx, session, skills, writeGuest, defaultAgentSkillsFileOperations())
}

func writeAgentSkills(ctx context.Context, session *domain.Sandbox, skills []ResolvedAgentSkill, writeGuest GuestSkillsWriterFunc, operations agentSkillsFileOperations) ([]string, error) {
	skillsDir := HostAgentSkillsDir(session)
	if skillsDir == "" {
		if len(skills) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("session workspace path is required to write agent skills")
	}
	if err := validateAgentSkillsPrivatePaths(session); err != nil {
		return nil, err
	}
	unlock, err := lockAgentSkills(ctx, filepath.Dir(skillsDir))
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := ensureAgentSkillsDirectory(skillsDir); err != nil {
		return nil, err
	}
	if err := recoverAgentSkillsUpdates(session, operations); err != nil {
		return nil, err
	}
	current, names, err := normalizeResolvedAgentSkills(ctx, skills)
	if err != nil {
		return nil, err
	}
	previous := readAgentSkillsManifest(skillsDir)
	claudePath := filepath.Join(HostSandboxDir(session), "home", ".claude", "skills")
	if err := validateClaudeSkillsPath(claudePath, len(names) > 0); err != nil {
		return nil, err
	}
	transaction, err := prepareAgentSkillsUpdate(ctx, session, agentSkillsProjection{current: current, names: names, previous: previous}, operations)
	if err != nil {
		return nil, err
	}
	if transaction == nil {
		if writeGuest != nil && (len(names) > 0 || len(previous.Names) > 0) {
			if err := writeGuest(ctx, skillsDir); err != nil {
				return nil, err
			}
		}
		return names, nil
	}
	if err := transaction.publish(ctx, operations); err != nil {
		return nil, transaction.abort(err, operations)
	}
	if writeGuest != nil && (len(names) > 0 || len(previous.Names) > 0) {
		if err := writeGuest(ctx, skillsDir); err != nil {
			return nil, transaction.abort(fmt.Errorf("publish agent skills to guest: %w", err), operations)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, transaction.abort(err, operations)
	}
	if err := transaction.commit(); err != nil {
		return nil, transaction.abort(err, operations)
	}
	if err := transaction.cleanup(); err != nil {
		return nil, fmt.Errorf("agent skills committed; cleanup will retry on next preparation: %w", err)
	}
	return names, nil
}

func validateAgentSkillsPrivatePaths(session *domain.Sandbox) error {
	// The configured sandbox root may itself use a platform alias (/var on
	// macOS). Only its private home descendants belong to this invariant.
	root := HostSandboxDir(session)
	for _, relative := range []string{"home", "home/.agents", "home/.claude", "home/.agents/skills"} {
		path := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Lstat(path)
		if isAgentSkillMissing(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("agent skills private parent %s must be a real directory", path)
		}
	}
	for _, relative := range []string{"home/.agents/.skills.lock", "home/.agents/skills/" + agentSkillsManifestFileName} {
		if err := validateAgentSkillsControlFile(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			return err
		}
	}
	return nil
}

func validateAgentSkillsControlFile(path string) error {
	info, err := os.Lstat(path)
	if isAgentSkillMissing(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("agent skills control file %s must be a regular file", path)
	}
	return nil
}

func normalizeResolvedAgentSkills(ctx context.Context, skills []ResolvedAgentSkill) (map[string]ResolvedAgentSkill, []string, error) {
	current := make(map[string]ResolvedAgentSkill, len(skills))
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		skill.Name = strings.TrimSpace(skill.Name)
		skill.LocalDir = strings.TrimSpace(skill.LocalDir)
		if err := validateAgentSkillName(skill.Name); err != nil {
			return nil, nil, err
		}
		if skill.LocalDir == "" {
			return nil, nil, fmt.Errorf("agent skill %s local dir is required", skill.Name)
		}
		if _, ok := current[skill.Name]; ok {
			return nil, nil, fmt.Errorf("duplicate agent skill %s", skill.Name)
		}
		fingerprint, err := FingerprintAgentSkill(ctx, skill.LocalDir)
		if err != nil {
			return nil, nil, fmt.Errorf("read agent skill %s: %w", skill.Name, err)
		}
		if skill.Fingerprint != "" && skill.Fingerprint != fingerprint {
			return nil, nil, fmt.Errorf("agent skill %s artifact changed after resolution", skill.Name)
		}
		skill.Fingerprint = fingerprint
		current[skill.Name] = skill
		names = append(names, skill.Name)
	}
	return current, names, nil
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

func ensureAgentSkillsDirectory(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create agent skills directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("agent skills path %s is not a private directory", path)
	}
	return nil
}

func readAgentSkillsManifest(skillsDir string) agentSkillsManifest {
	data, err := os.ReadFile(filepath.Join(skillsDir, agentSkillsManifestFileName))
	if err != nil {
		return agentSkillsManifest{}
	}
	var manifest agentSkillsManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.Version > 2 {
		return agentSkillsManifest{}
	}
	return manifest
}

func agentSkillsManifestBytes(manifest agentSkillsManifest) ([]byte, error) {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal agent skills manifest: %w", err)
	}
	return append(data, '\n'), nil
}

func writeAgentSkillsManifest(skillsDir string, manifest agentSkillsManifest) error {
	data, err := agentSkillsManifestBytes(manifest)
	if err != nil {
		return err
	}
	path := filepath.Join(skillsDir, agentSkillsManifestFileName)
	if previous, err := os.ReadFile(path); err == nil && bytes.Equal(previous, data) {
		return nil
	}
	return writeAgentSkillsAtomicFile(path, data)
}

func writeAgentSkillsAtomicFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".skills-metadata-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer func() { _ = os.Remove(temp) }()
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func copyAgentSkillToStaging(ctx context.Context, skill ResolvedAgentSkill, dst string, operations agentSkillsFileOperations) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := os.OpenRoot(skill.LocalDir)
	if err != nil {
		return fmt.Errorf("open agent skill %s: %w", skill.Name, err)
	}
	defer func() { _ = source.Close() }()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	if err := operations.copy(ctx, source, dst); err != nil {
		return fmt.Errorf("stage agent skill %s: %w", skill.Name, err)
	}
	info, err := source.Stat(".")
	if err != nil {
		return err
	}
	if err := os.Chmod(dst, info.Mode().Perm()); err != nil {
		return err
	}
	actual, err := FingerprintAgentSkill(ctx, dst)
	if err != nil {
		return err
	}
	if actual != skill.Fingerprint {
		return fmt.Errorf("agent skill %s changed while staging", skill.Name)
	}
	return nil
}

func isAgentSkillMissing(err error) bool { return errors.Is(err, os.ErrNotExist) }

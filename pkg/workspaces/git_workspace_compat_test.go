package workspaces

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	domain "agent-compose/pkg/model"
)

func TestDecodeGitWorkspaceConfigSupportsLegacyPersistenceShape(t *testing.T) {
	cfg, err := DecodeGitWorkspaceConfig(`{
		"url":"https://example.test/repo.git",
		"branch":"release",
		"commit":"abc123",
		"path":"nested/repo",
		"credential":"legacy%20user:p%40ss%3Aword"
	}`)
	if err != nil {
		t.Fatalf("DecodeGitWorkspaceConfig returned error: %v", err)
	}
	if cfg.Provider != "git" || cfg.URL != "https://example.test/repo.git" {
		t.Fatalf("legacy Git source = %#v", cfg.Source)
	}
	if cfg.Ref != "abc123" {
		t.Fatalf("legacy Git ref = %q, want commit abc123", cfg.Ref)
	}
	if cfg.Target != "nested/repo" || cfg.Path != "" {
		t.Fatalf("legacy Git target/path = %q/%q", cfg.Target, cfg.Path)
	}
	if cfg.Username != "legacy user" || cfg.Password != "p@ss:word" {
		t.Fatalf("legacy Git credentials = %q/%q", cfg.Username, cfg.Password)
	}

	canonical, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal canonical Git workspace config: %v", err)
	}
	for _, legacyField := range []string{`"branch"`, `"commit"`, `"credential"`, `"path"`} {
		if strings.Contains(string(canonical), legacyField) {
			t.Fatalf("canonical Git workspace config retains %s: %s", legacyField, canonical)
		}
	}
}

func TestDecodeGitWorkspaceConfigFallsBackToLegacyBranch(t *testing.T) {
	cfg, err := DecodeGitWorkspaceConfig(`{
		"url":"https://example.test/repo.git",
		"branch":"release"
	}`)
	if err != nil {
		t.Fatalf("DecodeGitWorkspaceConfig returned error: %v", err)
	}
	if cfg.Ref != "release" {
		t.Fatalf("legacy branch ref = %q, want release", cfg.Ref)
	}
}

func TestDecodeGitWorkspaceConfigPrefersCanonicalFields(t *testing.T) {
	cfg, err := DecodeGitWorkspaceConfig(`{
		"provider":"git",
		"url":"https://example.test/repo.git",
		"ref":"refs/tags/v2",
		"target":"current",
		"branch":"legacy-branch",
		"commit":"legacy-commit",
		"path":"legacy-target",
		"token":"${GIT_TOKEN}"
	}`)
	if err != nil {
		t.Fatalf("DecodeGitWorkspaceConfig returned error: %v", err)
	}
	if cfg.Ref != "refs/tags/v2" || cfg.Target != "current" || cfg.Token != "${GIT_TOKEN}" {
		t.Fatalf("canonical Git workspace config = %#v", cfg)
	}
}

func TestIntegrationPrepareGitWorkspaceSupportsLegacyPersistenceShape(t *testing.T) {
	sourceRepo := createGitWorkspaceSourceRepo(t)
	workspaceRoot := t.TempDir()
	legacyConfig, err := json.Marshal(map[string]string{
		"url":    "file://" + filepath.ToSlash(sourceRepo.path),
		"branch": "main",
		"commit": sourceRepo.firstCommit,
		"path":   "nested/repo",
	})
	if err != nil {
		t.Fatalf("marshal legacy Git workspace config: %v", err)
	}
	if err := PrepareGitWorkspace(context.Background(), &domain.Sandbox{
		Summary: domain.SandboxSummary{ID: "legacy-git-session", WorkspacePath: workspaceRoot},
	}, domain.WorkspaceConfig{
		ID:         "legacy-git-workspace",
		Name:       "Legacy Git Workspace",
		Type:       "git",
		ConfigJSON: string(legacyConfig),
	}); err != nil {
		t.Fatalf("PrepareGitWorkspace returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(workspaceRoot, "nested", "repo", "README.md"), "first\n")
}

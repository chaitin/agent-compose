package workspaces

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sources"
)

func TestProvisionerGitWorkspaceReadyPreservesState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *provisionerGitStateFixture)
	}{
		{
			name: "locally modified tracked and untracked files",
			mutate: func(t *testing.T, fixture *provisionerGitStateFixture) {
				t.Helper()
				writeProvisionerGitStateFile(t, filepath.Join(fixture.workspacePath, "README.md"), "local edit\n")
				writeProvisionerGitStateFile(t, filepath.Join(fixture.workspacePath, "LOCAL.txt"), "local only\n")
			},
		},
		{
			name: "locally deleted tracked file",
			mutate: func(t *testing.T, fixture *provisionerGitStateFixture) {
				t.Helper()
				if err := os.Remove(filepath.Join(fixture.workspacePath, "README.md")); err != nil {
					t.Fatalf("remove tracked workspace file: %v", err)
				}
			},
		},
		{
			name: "user emptied workspace",
			mutate: func(t *testing.T, fixture *provisionerGitStateFixture) {
				t.Helper()
				if err := os.RemoveAll(fixture.workspacePath); err != nil {
					t.Fatalf("remove workspace contents: %v", err)
				}
				if err := os.MkdirAll(fixture.workspacePath, 0o755); err != nil {
					t.Fatalf("recreate empty workspace: %v", err)
				}
			},
		},
		{
			name: "remote advanced",
			mutate: func(t *testing.T, fixture *provisionerGitStateFixture) {
				t.Helper()
				writeProvisionerGitStateFile(t, filepath.Join(fixture.sourcePath, "REMOTE.txt"), "new remote commit\n")
				runProvisionerGitStateCommand(t, fixture.sourcePath, "add", "REMOTE.txt")
				runProvisionerGitStateCommand(t, fixture.sourcePath, "commit", "-m", "remote advance")
				advanced := strings.TrimSpace(runProvisionerGitStateCommand(t, fixture.sourcePath, "rev-parse", "HEAD"))
				if advanced == fixture.sourceTip {
					t.Fatal("remote HEAD did not advance")
				}
			},
		},
		{
			name: "remote deleted and unreachable",
			mutate: func(t *testing.T, fixture *provisionerGitStateFixture) {
				t.Helper()
				if err := os.RemoveAll(fixture.sourcePath); err != nil {
					t.Fatalf("remove remote repository: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newProvisionerGitStateFixture(t)
			fixture.ensurePendingWorkspaceReady(t)
			fixture.assertInitialClone(t)

			tt.mutate(t, fixture)
			before := provisionerGitWorkspaceManifest(t, fixture.workspacePath)
			if tt.name == "user emptied workspace" && len(before) != 0 {
				t.Fatalf("empty workspace manifest has %d entries, want 0", len(before))
			}

			configCalls := fixture.store.workspaceConfigCallCount()
			updateCalls := fixture.store.updateCallCount()
			fixture.store.setWorkspaceConfigError(errors.New("workspace source must not be resolved after ready"))
			caller := fixture.store.persistedSandbox(t)
			if err := fixture.provisioner.Ensure(context.Background(), caller); err != nil {
				t.Fatalf("Ensure ready Git workspace: %v", err)
			}

			after := provisionerGitWorkspaceManifest(t, fixture.workspacePath)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("ready Git workspace changed:\n before: %#v\n  after: %#v", before, after)
			}
			if got := fixture.store.workspaceConfigCallCount(); got != configCalls {
				t.Fatalf("GetWorkspaceConfig calls after ready Ensure = %d, want unchanged %d", got, configCalls)
			}
			if got := fixture.store.updateCallCount(); got != updateCalls {
				t.Fatalf("UpdateSandbox calls after ready Ensure = %d, want unchanged %d", got, updateCalls)
			}
			assertProvisionerGitStateReady(t, caller)
			assertProvisionerGitStateReady(t, fixture.store.persistedSandbox(t))
		})
	}
}

type provisionerGitStateFixture struct {
	store         *provisionerGitStateStore
	provisioner   *Provisioner
	sourcePath    string
	workspacePath string
	branch        string
	commit        string
	sourceTip     string
}

func newProvisionerGitStateFixture(t *testing.T) *provisionerGitStateFixture {
	t.Helper()

	fixtureRoot := t.TempDir()
	sourcePath := filepath.Join(fixtureRoot, "source")
	const branch = "resume-fixture"
	runProvisionerGitStateCommand(t, "", "init", "-b", branch, sourcePath)
	runProvisionerGitStateCommand(t, sourcePath, "config", "user.email", "agent-compose@example.test")
	runProvisionerGitStateCommand(t, sourcePath, "config", "user.name", "Agent Compose")
	writeProvisionerGitStateFile(t, filepath.Join(sourcePath, "README.md"), "first\n")
	runProvisionerGitStateCommand(t, sourcePath, "add", "README.md")
	runProvisionerGitStateCommand(t, sourcePath, "commit", "-m", "first")
	commit := strings.TrimSpace(runProvisionerGitStateCommand(t, sourcePath, "rev-parse", "HEAD"))
	writeProvisionerGitStateFile(t, filepath.Join(sourcePath, "README.md"), "second\n")
	runProvisionerGitStateCommand(t, sourcePath, "commit", "-am", "second")
	sourceTip := strings.TrimSpace(runProvisionerGitStateCommand(t, sourcePath, "rev-parse", "HEAD"))

	configJSON, err := json.Marshal(GitWorkspaceConfig{
		Source: sources.Source{Provider: sources.ProviderGit, URL: "file://" + filepath.ToSlash(sourcePath), Ref: commit},
	})
	if err != nil {
		t.Fatalf("marshal Git workspace config: %v", err)
	}

	sandboxRoot := filepath.Join(fixtureRoot, "sandbox")
	if err := os.MkdirAll(sandboxRoot, 0o755); err != nil {
		t.Fatalf("create sandbox root: %v", err)
	}
	workspacePath := filepath.Join(sandboxRoot, "workspace")
	workspaceID := "git-state-workspace"
	sandbox := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:            "git-state-sandbox",
			WorkspacePath: workspacePath,
		},
		WorkspaceID: workspaceID,
		WorkspaceProvisioning: &domain.SandboxWorkspaceProvisioning{
			Version:   domain.SandboxWorkspaceProvisioningVersion,
			Status:    domain.SandboxWorkspaceProvisioningStatusPending,
			UpdatedAt: time.Unix(1, 0).UTC(),
		},
	}
	store := &provisionerGitStateStore{
		sandboxRoot: sandboxRoot,
		sandbox:     cloneProvisionerGitStateSandbox(sandbox),
		workspace: domain.WorkspaceConfig{
			ID:         workspaceID,
			Name:       "Git State Workspace",
			Type:       "git",
			ConfigJSON: string(configJSON),
		},
	}
	return &provisionerGitStateFixture{
		store:         store,
		provisioner:   NewProvisioner(&appconfig.Config{}, store, store),
		sourcePath:    sourcePath,
		workspacePath: workspacePath,
		branch:        branch,
		commit:        commit,
		sourceTip:     sourceTip,
	}
}

func (f *provisionerGitStateFixture) ensurePendingWorkspaceReady(t *testing.T) {
	t.Helper()
	caller := f.store.persistedSandbox(t)
	if err := f.provisioner.Ensure(context.Background(), caller); err != nil {
		t.Fatalf("Ensure pending Git workspace: %v", err)
	}
	assertProvisionerGitStateReady(t, caller)
	assertProvisionerGitStateReady(t, f.store.persistedSandbox(t))
	if got := f.store.workspaceConfigCallCount(); got != 1 {
		t.Fatalf("GetWorkspaceConfig calls during initial provisioning = %d, want 1", got)
	}
	if got := f.store.updateCallCount(); got != 1 {
		t.Fatalf("UpdateSandbox calls during initial provisioning = %d, want 1", got)
	}
}

func (f *provisionerGitStateFixture) assertInitialClone(t *testing.T) {
	t.Helper()
	if got := strings.TrimSpace(runProvisionerGitStateCommand(t, f.workspacePath, "rev-parse", "HEAD")); got != f.commit {
		t.Fatalf("workspace HEAD = %q, want configured commit %q", got, f.commit)
	}
	remoteBranch := "refs/remotes/origin/" + f.branch
	if got := strings.TrimSpace(runProvisionerGitStateCommand(t, f.workspacePath, "rev-parse", remoteBranch)); got != f.sourceTip {
		t.Fatalf("workspace %s = %q, want configured branch tip %q", remoteBranch, got, f.sourceTip)
	}
	data, err := os.ReadFile(filepath.Join(f.workspacePath, "README.md"))
	if err != nil {
		t.Fatalf("read cloned README.md: %v", err)
	}
	if got := string(data); got != "first\n" {
		t.Fatalf("cloned README.md = %q, want content from configured commit", got)
	}
}

type provisionerGitStateStore struct {
	mu                 sync.Mutex
	sandboxRoot        string
	sandbox            *domain.Sandbox
	workspace          domain.WorkspaceConfig
	workspaceConfigErr error
	workspaceCalls     int
	updateCalls        int
}

var _ SandboxStore = (*provisionerGitStateStore)(nil)
var _ SandboxPathResolver = (*provisionerGitStateStore)(nil)
var _ WorkspaceConfigStore = (*provisionerGitStateStore)(nil)

func (s *provisionerGitStateStore) GetSandbox(_ context.Context, id string) (*domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sandbox == nil || s.sandbox.Summary.ID != id {
		return nil, fmt.Errorf("sandbox %q not found", id)
	}
	return cloneProvisionerGitStateSandbox(s.sandbox), nil
}

func (s *provisionerGitStateStore) UpdateSandbox(_ context.Context, sandbox *domain.Sandbox) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateCalls++
	s.sandbox = cloneProvisionerGitStateSandbox(sandbox)
	return nil
}

func (s *provisionerGitStateStore) SandboxDir(string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sandboxRoot
}

func (s *provisionerGitStateStore) GetWorkspaceConfig(_ context.Context, id string) (domain.WorkspaceConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaceCalls++
	if s.workspaceConfigErr != nil {
		return domain.WorkspaceConfig{}, s.workspaceConfigErr
	}
	if s.workspace.ID != id {
		return domain.WorkspaceConfig{}, fmt.Errorf("workspace %q not found", id)
	}
	return s.workspace, nil
}

func (s *provisionerGitStateStore) setWorkspaceConfigError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaceConfigErr = err
}

func (s *provisionerGitStateStore) persistedSandbox(t *testing.T) *domain.Sandbox {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProvisionerGitStateSandbox(s.sandbox)
}

func (s *provisionerGitStateStore) workspaceConfigCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workspaceCalls
}

func (s *provisionerGitStateStore) updateCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateCalls
}

func cloneProvisionerGitStateSandbox(sandbox *domain.Sandbox) *domain.Sandbox {
	if sandbox == nil {
		return nil
	}
	clone := *sandbox
	clone.Summary.Tags = append([]domain.SandboxTag(nil), sandbox.Summary.Tags...)
	if sandbox.Workspace != nil {
		workspace := *sandbox.Workspace
		clone.Workspace = &workspace
	}
	if sandbox.WorkspaceProvisioning != nil {
		provisioning := *sandbox.WorkspaceProvisioning
		clone.WorkspaceProvisioning = &provisioning
	}
	clone.EnvItems = append([]domain.SandboxEnvVar(nil), sandbox.EnvItems...)
	clone.VolumeMounts = append([]domain.SandboxVolumeMount(nil), sandbox.VolumeMounts...)
	clone.RuntimeEnvItems = append([]domain.SandboxEnvVar(nil), sandbox.RuntimeEnvItems...)
	clone.ProviderEnvItems = append([]domain.SandboxEnvVar(nil), sandbox.ProviderEnvItems...)
	return &clone
}

type provisionerGitManifestEntry struct {
	mode    fs.FileMode
	content string
	link    string
}

func provisionerGitWorkspaceManifest(t *testing.T, root string) map[string]provisionerGitManifestEntry {
	t.Helper()
	manifest := make(map[string]provisionerGitManifestEntry)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := provisionerGitManifestEntry{mode: info.Mode()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			item.link, err = os.Readlink(path)
		case info.Mode().IsRegular():
			var data []byte
			data, err = os.ReadFile(path)
			item.content = string(data)
		}
		if err != nil {
			return err
		}
		manifest[filepath.ToSlash(rel)] = item
		return nil
	})
	if err != nil {
		t.Fatalf("build workspace manifest for %s: %v", root, err)
	}
	return manifest
}

func assertProvisionerGitStateReady(t *testing.T, sandbox *domain.Sandbox) {
	t.Helper()
	if sandbox == nil || sandbox.WorkspaceProvisioning == nil {
		t.Fatalf("workspace provisioning = %#v, want ready", sandbox)
	}
	if got := sandbox.WorkspaceProvisioning.Status; got != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("workspace provisioning status = %q, want %q", got, domain.SandboxWorkspaceProvisioningStatusReady)
	}
}

func writeProvisionerGitStateFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runProvisionerGitStateCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output)
}

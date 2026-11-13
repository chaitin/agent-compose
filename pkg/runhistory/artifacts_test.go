package runhistory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type testSandboxDirs string

func (r testSandboxDirs) SandboxDir(id string) string { return filepath.Join(string(r), id) }

func TestRunHistoryArtifactsRejectUnsafeOrSharedPaths(t *testing.T) {
	for _, name := range []string{"wrong_recorded_path", "symlink_leaf", "symlink_parent", "shared_file", "shared_symlink_parent", "sandbox_retained", "audit_archive", "unowned_run_artifact", "outside_run_artifact"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			schedulerID := identity.NewID(identity.ResourceScheduler, "scheduler")
			runID := identity.NewID(identity.ResourceRun, "run")
			path := filepath.Join(root, "schedulers", schedulerID, "runs", runID)
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			write := func(path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(path, "payload.json"))
			write(filepath.Join(outside, "keep"))
			a := FSArtifacts{DataRoot: root, SandboxRoot: filepath.Join(root, "sandboxes"), SandboxArchiveRoot: filepath.Join(root, "archives"), SandboxDirs: testSandboxDirs(filepath.Join(root, "sandboxes"))}
			scope := Scope{SchedulerRun: &domain.SchedulerRunSummary{ID: runID, SchedulerID: schedulerID, ArtifactsDir: path}}
			switch name {
			case "wrong_recorded_path":
				scope.SchedulerRun.ArtifactsDir = outside
			case "symlink_leaf":
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "symlink_parent":
				parent := filepath.Dir(path)
				if err := os.RemoveAll(parent); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, parent); err != nil {
					t.Fatal(err)
				}
			case "shared_file":
				scope.ProtectedPaths = []string{filepath.Join(path, "payload.json")}
			case "shared_symlink_parent":
				alias := filepath.Join(root, "alias")
				if err := os.Symlink(path, alias); err != nil {
					t.Fatal(err)
				}
				scope.ProtectedPaths = []string{filepath.Join(alias, "not-created-yet.log")}
			case "sandbox_retained":
				id := identity.NewID(identity.ResourceSandbox, "shared")
				scope.SandboxIDs = []string{id}
				if err := os.MkdirAll(a.SandboxDirs.SandboxDir(id), 0o755); err != nil {
					t.Fatal(err)
				}
			case "audit_archive":
				id := identity.NewID(identity.ResourceSandbox, "archived")
				scope.SandboxIDs = []string{id}
				if err := os.MkdirAll(filepath.Join(a.SandboxArchiveRoot, id), 0o755); err != nil {
					t.Fatal(err)
				}
			case "unowned_run_artifact":
				scope.Runs = []domain.ProjectRunRecord{{LogsPath: filepath.Join(outside, "keep")}}
			case "outside_run_artifact":
				id := identity.NewID(identity.ResourceSandbox, "removed")
				scope.SandboxIDs = []string{id}
				scope.Runs = []domain.ProjectRunRecord{{SandboxID: id, LogsPath: filepath.Join(outside, "keep")}}
			}
			if err := a.Remove(context.Background(), scope); err == nil {
				t.Fatal("unsafe removal succeeded")
			}
			if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
				t.Fatal("outside file changed:", err)
			}
			if name != "symlink_leaf" && name != "symlink_parent" {
				if _, err := os.Stat(filepath.Join(path, "payload.json")); err != nil {
					t.Fatal("refused operation changed artifacts:", err)
				}
			}
		})
	}
}

func TestRunHistoryArtifactsSuccessIsIdempotent(t *testing.T) {
	root := t.TempDir()
	schedulerID := identity.NewID(identity.ResourceScheduler, "scheduler")
	runID := identity.NewID(identity.ResourceRun, "run")
	path := filepath.Join(root, "schedulers", schedulerID, "runs", runID)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "private.json"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(path), identity.NewID(identity.ResourceRun, "other"))
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	scope := Scope{SchedulerRun: &domain.SchedulerRunSummary{ID: runID, SchedulerID: schedulerID}}
	a := FSArtifacts{DataRoot: root}
	for range 2 {
		if err := a.Remove(context.Background(), scope); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path remains: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("neighbor removed:", err)
	}
}

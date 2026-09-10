package workspaces

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestProvisionerMountNeverCopiesAndRestoresFromSnapshot(t *testing.T) {
	for _, target := range []string{".", "reference"} {
		for _, readOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/read_only=%v", target, readOnly), func(t *testing.T) {
				store, sandbox, source := mountProvisionerFixture(t, target, readOnly)
				before := snapshotProvisionerFileStateManifest(t, source)
				// No materializer is installed: reaching either copy path fails.
				provisioner := NewProvisionerWithMaterializer(store, nil)
				if err := provisioner.Ensure(context.Background(), sandbox); err != nil {
					t.Fatal(err)
				}
				assertProvisionerFileStateReady(t, sandbox)
				entries, err := os.ReadDir(sandbox.Summary.WorkspacePath)
				if err != nil || len(entries) != 0 {
					t.Fatalf("mount made a working copy: %v, %v", entries, err)
				}
				writeProvisionerFileStateFile(t, filepath.Join(sandbox.Summary.WorkspacePath, "private-output"), "keep", 0o600)
				var group sync.WaitGroup
				for range 8 {
					group.Go(func() {
						caller := cloneProvisionerFileStateSandbox(sandbox)
						if err := provisioner.Ensure(context.Background(), caller); err != nil {
							t.Errorf("concurrent mount ensure: %v", err)
						}
					})
				}
				group.Wait()
				// A fresh owner reconstructs everything from the persisted snapshot.
				restarted := NewProvisionerWithMaterializer(store, nil)
				loaded := store.persistedSandbox(t)
				if err := restarted.Ensure(context.Background(), loaded); err != nil {
					t.Fatal(err)
				}
				assertProvisionerFileStateFile(t, filepath.Join(loaded.Summary.WorkspacePath, "private-output"), "keep", 0o600)
				if after := snapshotProvisionerFileStateManifest(t, source); !reflect.DeepEqual(before, after) {
					t.Fatal("mount preparation changed external source")
				}
				if _, err := os.Stat(filepath.Join(store.sandboxRoot, "state", workspaceProvisioningStateDir)); !os.IsNotExist(err) {
					t.Fatalf("mount unexpectedly allocated copy staging: %v", err)
				}
			})
		}
	}
}

func TestProvisionerMountSourceFailureAndRetry(t *testing.T) {
	for _, target := range []string{".", "reference"} {
		for _, readOnly := range []bool{false, true} {
			for _, ready := range []bool{false, true} {
				for _, failure := range []string{"missing", "file", "source-symlink", "parent-symlink"} {
					name := fmt.Sprintf("%s/read_only=%v/ready=%v/%s", target, readOnly, ready, failure)
					t.Run(name, func(t *testing.T) {
						testProvisionerMountSourceFailureAndRetry(t, target, readOnly, ready, failure)
					})
				}
			}
		}
	}
}

func testProvisionerMountSourceFailureAndRetry(t *testing.T, target string, readOnly, ready bool, failure string) {
	t.Helper()
	store, sandbox, source := mountProvisionerFixture(t, target, readOnly)
	provisioner := NewProvisionerWithMaterializer(store, nil)
	if ready {
		if err := provisioner.Ensure(context.Background(), sandbox); err != nil {
			t.Fatal(err)
		}
	}
	// The external root contains both the replaced path and its moved original;
	// snapshotting it catches changes through symlinks as well as lost content.
	externalRoot := filepath.Dir(filepath.Dir(source))
	original := snapshotProvisionerFileStateManifest(t, externalRoot)
	restore := replaceMountProvisionerSource(t, source, failure)
	replaced := snapshotProvisionerFileStateManifest(t, externalRoot)
	if err := provisioner.Ensure(context.Background(), sandbox); err == nil {
		t.Fatalf("%s live source accepted", failure)
	}
	if after := snapshotProvisionerFileStateManifest(t, externalRoot); !reflect.DeepEqual(replaced, after) {
		t.Fatalf("rejected mount changed the external tree:\nbefore=%#v\nafter=%#v", replaced, after)
	}
	if failure == "missing" {
		if _, err := os.Lstat(source); !os.IsNotExist(err) {
			t.Fatalf("missing source was recreated: %v", err)
		}
	}
	wantStatus := domain.SandboxWorkspaceProvisioningStatusFailed
	if ready {
		wantStatus = domain.SandboxWorkspaceProvisioningStatusReady
	}
	persisted := store.persistedSandbox(t)
	if sandbox.WorkspaceProvisioning.Status != wantStatus || persisted.WorkspaceProvisioning.Status != wantStatus {
		t.Fatalf("rejection status: caller=%#v persisted=%#v, want %s", sandbox.WorkspaceProvisioning, persisted.WorkspaceProvisioning, wantStatus)
	}
	restore()
	if err := provisioner.Ensure(context.Background(), sandbox); err != nil {
		t.Fatalf("retry with restored source: %v", err)
	}
	assertProvisionerFileStateReady(t, sandbox)
	assertProvisionerFileStateReady(t, store.persistedSandbox(t))
	if after := snapshotProvisionerFileStateManifest(t, externalRoot); !reflect.DeepEqual(original, after) {
		t.Fatalf("retry changed the restored external tree:\nbefore=%#v\nafter=%#v", original, after)
	}
	entries, err := os.ReadDir(sandbox.Summary.WorkspacePath)
	if err != nil || len(entries) != 0 {
		t.Fatalf("retry made a working copy: %v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(store.sandboxRoot, "state", workspaceProvisioningStateDir)); !os.IsNotExist(err) {
		t.Fatalf("retry allocated copy staging: %v", err)
	}
}

func replaceMountProvisionerSource(t *testing.T, source, failure string) func() {
	t.Helper()
	path := source
	if failure == "parent-symlink" {
		path = filepath.Dir(source)
	}
	moved := path + "-moved"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	switch failure {
	case "missing":
	case "file":
		writeProvisionerFileStateFile(t, path, "replacement file", 0o600)
	case "source-symlink", "parent-symlink":
		if err := os.Symlink(moved, path); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown source replacement %q", failure)
	}
	return func() {
		t.Helper()
		if failure != "missing" {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Rename(moved, path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProvisionerMountRejectsChangedOwnershipAndCancellation(t *testing.T) {
	store, sandbox, source := mountProvisionerFixture(t, ".", false)
	provisioner := NewProvisionerWithMaterializer(store, nil)
	if err := os.Symlink(source, sandbox.Summary.WorkspacePath); err != nil {
		t.Fatal(err)
	}
	if err := provisioner.Ensure(context.Background(), sandbox); err == nil {
		t.Fatal("external symlink accepted as owned workspace")
	}
	if err := os.Remove(sandbox.Summary.WorkspacePath); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := provisioner.Ensure(ctx, sandbox); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Ensure = %v", err)
	}
	assertProvisionerFileStateFile(t, filepath.Join(source, "marker"), "source", 0o640)
}

func mountProvisionerFixture(t *testing.T, target string, readOnly bool) (*provisionerFileStateSandboxStore, *domain.Sandbox, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "external", "project", "source")
	writeProvisionerFileStateFile(t, filepath.Join(source, "marker"), "source", 0o640)
	raw, err := NewFileWorkspaceMountConfig(domain.ProjectRecord{SourcePath: filepath.Dir(source)}, "source", target, readOnly)
	if err != nil {
		t.Fatal(err)
	}
	sandboxRoot := filepath.Join(root, "sandbox")
	if err := os.Mkdir(sandboxRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	sandbox := &domain.Sandbox{
		Summary:   domain.SandboxSummary{ID: "mounted", Driver: "docker", WorkspacePath: filepath.Join(sandboxRoot, "workspace")},
		Workspace: &domain.SandboxWorkspace{ID: "source", Type: "file", ConfigJSON: raw},
		WorkspaceProvisioning: &domain.SandboxWorkspaceProvisioning{
			Version: domain.SandboxWorkspaceProvisioningVersion, Status: domain.SandboxWorkspaceProvisioningStatusPending,
		},
	}
	return newProvisionerFileStateSandboxStore(sandboxRoot, sandbox), sandbox, source
}

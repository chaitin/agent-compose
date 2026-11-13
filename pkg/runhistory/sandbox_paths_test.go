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

func TestRunHistoryDatedSandboxWithoutRecordedPaths(t *testing.T) {
	root := t.TempDir()
	sandboxID := identity.NewID(identity.ResourceSandbox, "unregistered")
	residue := filepath.Join(root, "2026", "11", "12", sandboxID, "home", "private.txt")
	if err := os.MkdirAll(filepath.Dir(residue), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(residue, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := FSArtifacts{SandboxRoot: root, SandboxDirs: testSandboxDirs(root)}
	scope := Scope{SandboxIDs: []string{sandboxID}, Runs: []domain.ProjectRunRecord{{SandboxID: sandboxID}}}
	if err := a.Remove(context.Background(), scope); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("unregistered dated sandbox must block history removal: %v", err)
	}
	if data, err := os.ReadFile(residue); err != nil || string(data) != "private" {
		t.Fatalf("refusal changed sandbox data: %q, %v", data, err)
	}
	if err := os.RemoveAll(filepath.Join(root, "2026", "11", "12", sandboxID)); err != nil {
		t.Fatal(err)
	}
	if err := a.Remove(context.Background(), scope); err != nil {
		t.Fatalf("retry after sandbox removal: %v", err)
	}
}

func TestRunHistoryDatedSandboxArtifactsAfterRemoval(t *testing.T) {
	root := t.TempDir()
	sandboxID := identity.NewID(identity.ResourceSandbox, "dated")
	oldBase := filepath.Join(root, "2026", "11", "12", sandboxID)
	artifacts := FSArtifacts{SandboxRoot: root, SandboxDirs: testSandboxDirs(root)}
	scope := Scope{
		SandboxIDs: []string{sandboxID},
		Runs:       []domain.ProjectRunRecord{{SandboxID: sandboxID, LogsPath: filepath.Join(oldBase, "state", "cells", "cell-1", "output.txt")}},
	}
	// A current SandboxDir lookup falls back to the legacy flat layout after
	// removal; the stored dated path must still be verifiable on a retry.
	if err := artifacts.Remove(context.Background(), scope); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(oldBase, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := artifacts.Remove(context.Background(), scope); err == nil {
		t.Fatal("a residual dated sandbox must block history removal")
	}
}

func TestRunHistoryDatedSandboxScanRejectsSymlinkPartitions(t *testing.T) {
	for _, partition := range []string{"2026", "2026/11", "2026/11/12"} {
		t.Run(partition, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, filepath.FromSlash(partition))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), path); err != nil {
				t.Fatal(err)
			}
			id := identity.NewID(identity.ResourceSandbox, "removed")
			a := FSArtifacts{SandboxRoot: root, SandboxDirs: testSandboxDirs(root)}
			if err := a.Remove(context.Background(), Scope{SandboxIDs: []string{id}}); !errors.Is(err, domain.ErrFailedPrecondition) {
				t.Fatalf("unverifiable partition must block removal: %v", err)
			}
		})
	}
}

func TestRunHistoryDatedSandboxScanCancellationAndUnrelatedData(t *testing.T) {
	root := t.TempDir()
	id := identity.NewID(identity.ResourceSandbox, "removed")
	other := identity.NewID(identity.ResourceSandbox, "unrelated")
	path := filepath.Join(root, "2026", "11", "12", other, "home")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	a := FSArtifacts{SandboxRoot: root, SandboxDirs: testSandboxDirs(root)}
	scope := Scope{SandboxIDs: []string{id}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Remove(ctx, scope); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled check: %v", err)
	}
	if err := a.Remove(context.Background(), scope); err != nil {
		t.Fatalf("unrelated sandbox blocked removal: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("unrelated sandbox changed: %v", err)
	}
}

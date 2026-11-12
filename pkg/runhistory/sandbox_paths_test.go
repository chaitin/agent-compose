package runhistory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

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

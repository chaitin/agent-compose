package runhistory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

func TestRunHistoryArtifactsRemoveTheRunsOwnLogDirectory(t *testing.T) {
	root := t.TempDir()
	runID := identity.NewID(identity.ResourceRun, "run")
	logPath := runlog.RunPath(root, runID)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifacts := FSArtifacts{DataRoot: root, SandboxRoot: filepath.Join(root, "sandboxes")}
	// A run that failed before it got a sandbox still owns its log.
	scope := Scope{Runs: []domain.ProjectRunRecord{{RunID: runID, LogFormat: runlog.FormatJSONL, LogsPath: logPath}}}
	if err := artifacts.Remove(context.Background(), scope); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(runlog.RunDir(root, runID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run directory remains: %v", err)
	}
	if err := artifacts.Remove(context.Background(), scope); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
}

func TestRunHistoryArtifactsRejectARunLogOutsideItsCanonicalPath(t *testing.T) {
	root := t.TempDir()
	runID := identity.NewID(identity.ResourceRun, "run")
	other := identity.NewID(identity.ResourceRun, "other")
	artifacts := FSArtifacts{DataRoot: root}
	scope := Scope{Runs: []domain.ProjectRunRecord{{RunID: runID, LogFormat: runlog.FormatJSONL, LogsPath: runlog.RunPath(root, other)}}}
	if err := artifacts.Remove(context.Background(), scope); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("Remove err = %v, want failed precondition", err)
	}
}

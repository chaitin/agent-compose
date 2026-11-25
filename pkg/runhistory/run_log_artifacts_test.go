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
	logPath, err := runlog.RunPath(root, runID)
	if err != nil {
		t.Fatal(err)
	}
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
	runDir, err := runlog.RunDir(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runDir); !errors.Is(err, os.ErrNotExist) {
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
	otherPath, err := runlog.RunPath(root, other)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Runs: []domain.ProjectRunRecord{{RunID: runID, LogFormat: runlog.FormatJSONL, LogsPath: otherPath}}}
	if err := artifacts.Remove(context.Background(), scope); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("Remove err = %v, want failed precondition", err)
	}
}

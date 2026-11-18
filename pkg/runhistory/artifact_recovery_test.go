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

func TestRunHistoryIsolatedDirectorySurvivesCancellationAndReopen(t *testing.T) {
	root := t.TempDir()
	schedulerID := identity.NewID(identity.ResourceScheduler, t.Name())
	runID := identity.NewID(identity.ResourceRun, t.Name())
	parent := filepath.Join(root, "schedulers", schedulerID, "runs")
	path := filepath.Join(parent, runID)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "private.json"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := FSArtifacts{DataRoot: root}
	scope := Scope{SchedulerRun: &domain.SchedulerRunSummary{ID: runID, SchedulerID: schedulerID}}
	p, err := a.Prepare(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Detach(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canonical directory remains: %v", err)
	}
	pending := filepath.Join(parent, pendingHistoryName(runID))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Remove(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cleanup: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(pending, "private.json")); err != nil || string(data) != "private" {
		t.Fatalf("pending retry material lost: %q, %v", data, err)
	}
	if err := (FSArtifacts{DataRoot: root}).Remove(context.Background(), scope); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pending); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private pending directory remains: %v", err)
	}
}

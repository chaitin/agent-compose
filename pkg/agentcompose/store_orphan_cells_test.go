package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/samber/do/v2"
)

// TestStoreConvergesOrphanedRunningCellsOnStartup verifies that a cell left
// with Running=true by an interrupted run (e.g. daemon killed mid-run) is
// converged to a finished state when the store is created at startup, so the
// session UI is not stuck on "replying". A normally finished cell must be
// left untouched.
func TestStoreConvergesOrphanedRunningCellsOnStartup(t *testing.T) {
	config := &appconfig.Config{
		SessionRoot:   filepath.Join(t.TempDir(), "sessions"),
		RuntimeDriver: driverpkg.RuntimeDriverBoxlite,
		DefaultImage:  "default-box:latest",
		GuestHomePath: "/home/agent-compose",
	}
	cellsDir := filepath.Join(config.SessionRoot, uuid.NewString(), "state")
	if err := os.MkdirAll(cellsDir, 0o755); err != nil {
		t.Fatalf("mkdir cells dir: %v", err)
	}

	now := time.Now().UTC()
	orphanID := uuid.NewString()
	normalID := uuid.NewString()
	sessionID := filepath.Base(filepath.Dir(cellsDir))
	cells := []NotebookCell{
		{ID: orphanID, Type: CellTypeAgent, Source: "interrupted run", Running: true, CreatedAt: now},
		{ID: normalID, Type: CellTypeAgent, Source: "finished run", Running: false, Success: true, ExitCode: 0, CreatedAt: now},
	}
	raw, err := json.Marshal(cells)
	if err != nil {
		t.Fatalf("marshal cells: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cellsDir, "cells.json"), raw, 0o644); err != nil {
		t.Fatalf("write cells.json: %v", err)
	}

	// NewStore runs convergeOrphanedRunningCells once at startup.
	di := do.New()
	do.ProvideValue(di, config)
	store, err := NewStore(di)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	got, err := store.ListCells(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ListCells: %v", err)
	}
	byID := make(map[string]NotebookCell, len(got))
	for _, c := range got {
		byID[c.ID] = c
	}

	orphan, ok := byID[orphanID]
	if !ok {
		t.Fatalf("orphan cell missing after recovery")
	}
	if orphan.Running {
		t.Errorf("orphan cell still Running=true after recovery")
	}
	if orphan.Success {
		t.Errorf("orphan cell marked Success=true")
	}
	if orphan.ExitCode != 1 {
		t.Errorf("orphan cell ExitCode=%d, want 1", orphan.ExitCode)
	}
	if orphan.StopReason != "run interrupted by daemon restart" {
		t.Errorf("orphan cell StopReason=%q, want interrupted note", orphan.StopReason)
	}

	normal, ok := byID[normalID]
	if !ok {
		t.Fatalf("normal cell missing after recovery")
	}
	if normal.Running || !normal.Success || normal.ExitCode != 0 {
		t.Errorf("finished cell was modified by recovery: %+v", normal)
	}
}

// TestStoreConvergeOrphanedRunningCellsIdempotent verifies that running the
// recovery twice (e.g. two consecutive daemon restarts) does not double-write
// or corrupt already-converged cells.
func TestStoreConvergeOrphanedRunningCellsIdempotent(t *testing.T) {
	config := &appconfig.Config{
		SessionRoot:   filepath.Join(t.TempDir(), "sessions"),
		RuntimeDriver: driverpkg.RuntimeDriverBoxlite,
		DefaultImage:  "default-box:latest",
		GuestHomePath: "/home/agent-compose",
	}
	cellsDir := filepath.Join(config.SessionRoot, uuid.NewString(), "state")
	if err := os.MkdirAll(cellsDir, 0o755); err != nil {
		t.Fatalf("mkdir cells dir: %v", err)
	}
	sessionID := filepath.Base(filepath.Dir(cellsDir))
	raw, err := json.Marshal([]NotebookCell{
		{ID: uuid.NewString(), Type: CellTypeAgent, Source: "interrupted", Running: true, CreatedAt: time.Now().UTC()},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cellsDir, "cells.json"), raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	di := do.New()
	do.ProvideValue(di, config)
	store, err := NewStore(di)
	if err != nil {
		t.Fatalf("NewStore first: %v", err)
	}
	// Re-run recovery directly; no cell should still be Running, so this is a no-op.
	store.convergeOrphanedRunningCells()

	got, err := store.ListCells(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ListCells: %v", err)
	}
	for _, c := range got {
		if c.Running {
			t.Errorf("cell still Running after second recovery: %+v", c)
		}
	}
}

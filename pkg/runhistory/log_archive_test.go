package runhistory

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

type fakeLogArchiveStore struct {
	runs    map[string]domain.ProjectRunRecord
	pending map[string]bool
}

func (s fakeLogArchiveStore) GetProjectRun(_ context.Context, id string) (domain.ProjectRunRecord, error) {
	run, ok := s.runs[id]
	if !ok {
		return domain.ProjectRunRecord{}, domain.ResourceError(domain.ErrNotFound, "project run", id, "not found", sql.ErrNoRows)
	}
	return run, nil
}

func (s fakeLogArchiveStore) GetProjectRunCompletion(_ context.Context, id string) (domain.ProjectRunCompletionRecord, error) {
	if s.pending[id] {
		return domain.ProjectRunCompletionRecord{RunID: id}, nil
	}
	return domain.ProjectRunCompletionRecord{}, sql.ErrNoRows
}

type archiveFixture struct {
	dataRoot, archiveRoot string
	store                 fakeLogArchiveStore
	cutoff                time.Time
}

func newArchiveFixture(t *testing.T) *archiveFixture {
	t.Helper()
	root := t.TempDir()
	return &archiveFixture{
		dataRoot:    filepath.Join(root, "data"),
		archiveRoot: filepath.Join(root, "data", "archives", "logs"),
		store:       fakeLogArchiveStore{runs: map[string]domain.ProjectRunRecord{}, pending: map[string]bool{}},
		cutoff:      time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
	}
}

// addRun records a run whose log was last written at written.
func (f *archiveFixture) addRun(t *testing.T, name, status string, completed, written time.Time) (string, string) {
	t.Helper()
	runID := identity.NewID(identity.ResourceRun, name)
	logPath, err := runlog.RunPath(f.dataRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	w, err := runlog.Open(logPath, runlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.AppendInitialPrompt("prompt of " + name); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(logPath, written, written); err != nil {
		t.Fatal(err)
	}
	f.store.runs[runID] = domain.ProjectRunRecord{
		RunID: runID, Status: status, CompletedAt: completed,
		LogFormat: runlog.FormatJSONL, LogsPath: logPath,
	}
	return runID, logPath
}

func (f *archiveFixture) archiver() *LogArchiver {
	return &LogArchiver{DataRoot: f.dataRoot, ArchiveRoot: f.archiveRoot, Store: f.store, Now: func() time.Time { return f.cutoff.Add(48 * time.Hour) }}
}

func TestLogArchiverArchivesOnlyFinishedRunsPastTheCutoff(t *testing.T) {
	f := newArchiveFixture(t)
	old := f.cutoff.Add(-24 * time.Hour)
	recent := f.cutoff.Add(time.Hour)
	due, duePath := f.addRun(t, "due", domain.ProjectRunStatusSucceeded, old, old)
	_, recentPath := f.addRun(t, "recent", domain.ProjectRunStatusFailed, recent, recent)
	_, runningPath := f.addRun(t, "running", domain.ProjectRunStatusRunning, time.Time{}, old)
	pending, pendingPath := f.addRun(t, "pending", domain.ProjectRunStatusSucceeded, old, old)
	f.store.pending[pending] = true

	result, err := f.archiver().Clean(context.Background(), f.cutoff)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if result.Removed != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want one archived", result)
	}
	if _, err := runlog.ReadEntries(duePath, 0); !errors.Is(err, runlog.ErrArchived) {
		t.Fatalf("due run read err = %v, want ErrArchived", err)
	}
	restored, err := runlog.ReadArchivedLog(RunLogArchivePath(f.archiveRoot, due))
	if err != nil || len(restored) == 0 {
		t.Fatalf("archived log = %d bytes, %v", len(restored), err)
	}
	for name, path := range map[string]string{"recent": recentPath, "running": runningPath, "pending": pendingPath} {
		if _, err := runlog.ReadEntries(path, 0); err != nil {
			t.Fatalf("%s run log was touched: %v", name, err)
		}
	}

	again, err := f.archiver().Clean(context.Background(), f.cutoff)
	if err != nil || again.Removed != 0 || again.Failed != 0 {
		t.Fatalf("second Clean = %+v, %v, want nothing left to archive", again, err)
	}
}

func TestLogArchiverLeavesAnOrphanRunDirectoryToRunHistoryRemoval(t *testing.T) {
	f := newArchiveFixture(t)
	old := f.cutoff.Add(-24 * time.Hour)
	runID, logPath := f.addRun(t, "orphan", domain.ProjectRunStatusSucceeded, old, old)
	delete(f.store.runs, runID)
	result, err := f.archiver().Clean(context.Background(), f.cutoff)
	if err != nil || result.Removed != 0 {
		t.Fatalf("Clean = %+v, %v", result, err)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("orphan log was touched: %v", err)
	}
}

func TestRemovingRunHistoryRemovesTheArchivedLog(t *testing.T) {
	f := newArchiveFixture(t)
	old := f.cutoff.Add(-24 * time.Hour)
	runID, logPath := f.addRun(t, "archived", domain.ProjectRunStatusSucceeded, old, old)
	if _, err := f.archiver().Clean(context.Background(), f.cutoff); err != nil {
		t.Fatal(err)
	}
	artifacts := FSArtifacts{DataRoot: f.dataRoot, RunLogArchiveRoot: f.archiveRoot}
	scope := Scope{Runs: []domain.ProjectRunRecord{f.store.runs[runID]}}
	if err := artifacts.Remove(context.Background(), scope); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, path := range []string{filepath.Dir(logPath), filepath.Dir(RunLogArchivePath(f.archiveRoot, runID))} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s remains: %v", path, err)
		}
	}
}

func TestRemovingRunHistoryWithoutAnyArchiveRootYet(t *testing.T) {
	f := newArchiveFixture(t)
	runID, _ := f.addRun(t, "plain", domain.ProjectRunStatusSucceeded, f.cutoff, f.cutoff)
	artifacts := FSArtifacts{DataRoot: f.dataRoot, RunLogArchiveRoot: f.archiveRoot}
	if err := artifacts.Remove(context.Background(), Scope{Runs: []domain.ProjectRunRecord{f.store.runs[runID]}}); err != nil {
		t.Fatalf("Remove with a missing archive root: %v", err)
	}
}

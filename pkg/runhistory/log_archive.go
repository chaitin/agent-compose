package runhistory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/cleanup"
	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

// LogArchiveStore reads what the archiver needs to know about a run before it
// moves the run's log.
type LogArchiveStore interface {
	GetProjectRun(context.Context, string) (domain.ProjectRunRecord, error)
	GetProjectRunCompletion(context.Context, string) (domain.ProjectRunCompletionRecord, error)
}

// LogArchiver moves the logs of runs that finished before the cutoff out of
// the data root into ArchiveRoot, compressed. The run keeps its directory,
// conversation and summary; only the log itself leaves, and readers then get
// runlog.ErrArchived. The run's age is measured from its completion, not its
// sandbox's lifecycle, because the log belongs to the run.
type LogArchiver struct {
	DataRoot    string
	ArchiveRoot string
	Store       LogArchiveStore
	Now         func() time.Time
}

func (a *LogArchiver) Name() string { return "run-log-archive" }

// RunLogArchivePath is where a run's archived log lives under archiveRoot.
func RunLogArchivePath(archiveRoot, runID string) string {
	return filepath.Join(archiveRoot, "runs", runID, runlog.ArchivedFileName)
}

func (a *LogArchiver) Clean(ctx context.Context, cutoff time.Time) (cleanup.Result, error) {
	if a == nil || a.Store == nil || strings.TrimSpace(a.DataRoot) == "" || strings.TrimSpace(a.ArchiveRoot) == "" {
		return cleanup.Result{}, fmt.Errorf("run log archiver is not configured")
	}
	runsDir := filepath.Join(a.DataRoot, "runs")
	entries, err := os.ReadDir(runsDir)
	if errors.Is(err, os.ErrNotExist) {
		return cleanup.Result{}, nil
	}
	if err != nil {
		return cleanup.Result{}, fmt.Errorf("list run directories: %w", err)
	}
	result := cleanup.Result{}
	var joined error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(joined, err)
		}
		// A symlink is not a directory entry here, and names that are not run
		// ids include RemoveRunHistory's directories being removed.
		if !entry.IsDir() || !identity.IsID(entry.Name()) {
			continue
		}
		matched, err := a.archiveRun(ctx, entry.Name(), cutoff)
		switch {
		case err != nil:
			if matched {
				result.Matched++
			}
			result.Failed++
			joined = errors.Join(joined, fmt.Errorf("archive log of run %s: %w", entry.Name(), err))
		case matched:
			result.Matched++
			result.Removed++
		default:
			result.Skipped++
		}
	}
	return result, joined
}

// archiveRun archives one run's log when it is due. It reports whether the
// run matched, so a failure can be told apart from a run left alone.
func (a *LogArchiver) archiveRun(ctx context.Context, runID string, cutoff time.Time) (bool, error) {
	logPath, err := runlog.RunPath(a.DataRoot, runID)
	if err != nil {
		return false, err
	}
	info, statErr := os.Lstat(logPath)
	_, archived, err := runlog.ReadArchiveMarker(filepath.Dir(logPath))
	if err != nil {
		return false, err
	}
	switch {
	case archived && errors.Is(statErr, os.ErrNotExist):
		return false, nil // Done earlier.
	case !archived && errors.Is(statErr, os.ErrNotExist):
		return false, nil // The run never wrote a log.
	case statErr != nil:
		return false, statErr
	case !info.Mode().IsRegular():
		return false, fmt.Errorf("run log %s is not a regular file", logPath)
	case !archived && info.ModTime().After(cutoff):
		// Written after the cutoff, so it cannot have finished before it.
		return false, nil
	}
	due, err := a.runIsDue(ctx, runID, logPath, cutoff)
	if err != nil || !due {
		return false, err
	}
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	_, err = runlog.ArchiveLog(logPath, RunLogArchivePath(a.ArchiveRoot, runID), now)
	return true, err
}

// runIsDue reports whether the run finished before cutoff and nothing may
// still write its log: it is terminal, its completion is no longer pending,
// and its recorded log is the canonical one this archiver found.
func (a *LogArchiver) runIsDue(ctx context.Context, runID, logPath string, cutoff time.Time) (bool, error) {
	run, err := a.Store.GetProjectRun(ctx, runID)
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
		// No run owns the directory; RemoveRunHistory decides its fate.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if run.LogFormat != runlog.FormatJSONL || filepath.Clean(run.LogsPath) != filepath.Clean(logPath) {
		return false, nil
	}
	if !runStatusTerminal(run.Status) || run.CompletedAt.IsZero() || run.CompletedAt.After(cutoff) {
		return false, nil
	}
	_, err = a.Store.GetProjectRunCompletion(ctx, runID)
	if err == nil {
		return false, nil // Its completion is still being finalized.
	}
	if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, domain.ErrNotFound) {
		return false, err
	}
	return true, nil
}

func runStatusTerminal(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case domain.ProjectRunStatusSucceeded, domain.ProjectRunStatusFailed, domain.ProjectRunStatusCanceled:
		return true
	default:
		return false
	}
}

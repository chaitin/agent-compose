package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	sandboxpkg "github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

// schedulerSandboxLister lists persisted sandboxes for startup reconciliation.
type schedulerSandboxLister interface {
	ListSandboxes(context.Context, domain.SandboxListOptions) (domain.SandboxListResult, error)
}

// schedulerRunReader resolves the scheduler run that owns a sandbox.
type schedulerRunReader interface {
	GetSchedulerRun(context.Context, string, string) (domain.SchedulerRunSummary, error)
	BatchGetLatestSchedulerRunsBySandboxIDs(context.Context, []string, []string) (map[string]domain.SchedulerRunSummary, error)
}

// schedulerSandboxStopper releases the runtime of an orphaned sandbox.
type schedulerSandboxStopper interface {
	StopSandbox(context.Context, string) (*domain.Sandbox, error)
}

// reconcileOrphanedSchedulerSandboxes reclaims scheduler command sandboxes
// whose owning run is terminal or gone. It runs after interrupted scheduler
// runs are marked failed and before schedulers accept new work, so a live
// sandbox can only belong to a previous daemon life. Sticky (retain-policy)
// sandboxes are left alone; sandboxes created after startup are skipped.
//
// The returned warnings are startup diagnostics; a failure to stop one sandbox
// never blocks the remaining ones and is retried on the next daemon start
// because StopSandbox is idempotent.
func reconcileOrphanedSchedulerSandboxes(
	ctx context.Context,
	sandboxStore schedulerSandboxLister,
	runs schedulerRunReader,
	stopper schedulerSandboxStopper,
	startedAt time.Time,
) []string {
	if sandboxStore == nil || runs == nil || stopper == nil {
		return nil
	}
	listed, err := sandboxStore.ListSandboxes(ctx, domain.SandboxListOptions{
		VMStatuses: []string{domain.VMStatusRunning},
		CreatedTo:  startedAt.UTC(),
		Limit:      1 << 30,
	})
	if err != nil {
		return []string{fmt.Sprintf("list sandboxes for scheduler recovery: %v", err)}
	}
	var warnings []string
	for _, sandbox := range listed.Sandboxes {
		if sandbox == nil || !sandbox.Summary.CreatedAt.Before(startedAt) {
			continue
		}
		if strings.TrimSpace(schedulerSandboxTag(sandbox, "origin")) != "scheduler" {
			continue
		}
		if sandboxpkg.EffectiveStoppedRuntimePolicy(sandbox) != domain.StoppedRuntimePolicyRemove {
			continue
		}
		schedulerID := strings.TrimSpace(schedulerSandboxTag(sandbox, "scheduler_id"))
		runID := strings.TrimSpace(schedulerSandboxTag(sandbox, "scheduler_run_id"))
		orphaned, warning := orphanedSchedulerSandbox(ctx, runs, sandbox.Summary.ID, schedulerID, runID, startedAt)
		if warning != "" {
			warnings = append(warnings, warning)
			continue
		}
		if !orphaned {
			continue
		}
		if _, err := stopper.StopSandbox(ctx, sandbox.Summary.ID); err != nil {
			warnings = append(warnings, fmt.Sprintf("stop orphaned scheduler sandbox %s: %v", sandbox.Summary.ID, err))
			continue
		}
		slog.Info("stopped orphaned scheduler sandbox",
			"sandbox_id", sandbox.Summary.ID,
			"scheduler_id", schedulerID,
			"run_id", runID,
		)
	}
	return warnings
}

// orphanedSchedulerSandbox reports whether the sandbox has lost every valid
// owner. A sandbox without a scheduler_run_id tag predates the ownership tag;
// it is resolved through its linked scheduler event instead of being stopped
// blindly, because the run status alone cannot prove which run owned it.
func orphanedSchedulerSandbox(
	ctx context.Context,
	runs schedulerRunReader,
	sandboxID, schedulerID, runID string,
	startedAt time.Time,
) (bool, string) {
	if runID != "" {
		run, err := runs.GetSchedulerRun(ctx, schedulerID, runID)
		if err == nil {
			return schedulerRunLostOwner(run, startedAt), ""
		}
		if errors.Is(err, domain.ErrNotFound) {
			return true, ""
		}
		return false, fmt.Sprintf("look up scheduler run %s/%s for sandbox %s: %v", schedulerID, runID, sandboxID, err)
	}
	if schedulerID == "" {
		return false, fmt.Sprintf("sandbox %s has neither scheduler_run_id nor scheduler_id; leaving it for manual review", sandboxID)
	}
	found, err := runs.BatchGetLatestSchedulerRunsBySandboxIDs(ctx, []string{schedulerID}, []string{sandboxID})
	if err != nil {
		return false, fmt.Sprintf("look up linked scheduler run for legacy sandbox %s: %v", sandboxID, err)
	}
	run, ok := found[sandboxID]
	if !ok {
		return false, fmt.Sprintf("sandbox %s has no scheduler_run_id tag and no linked scheduler run; leaving it for manual review", sandboxID)
	}
	return schedulerRunLostOwner(run, startedAt), ""
}

// schedulerRunLostOwner reports whether the run can no longer own its sandbox.
// A terminal run is done. A run still marked running when the daemon starts is
// stale as well: scheduler orchestration lives in daemon memory, so it cannot
// survive the restart. That second case covers manual runs without a trigger
// id, which startup recovery does not mark failed.
func schedulerRunLostOwner(run domain.SchedulerRunSummary, startedAt time.Time) bool {
	if schedulers.SchedulerRunStatusIsTerminal(run.Status) {
		return true
	}
	return !run.StartedAt.After(startedAt)
}

func schedulerSandboxTag(sandbox *domain.Sandbox, name string) string {
	if sandbox == nil {
		return ""
	}
	for _, tag := range sandbox.Summary.Tags {
		if strings.EqualFold(tag.Name, name) {
			return tag.Value
		}
	}
	return ""
}

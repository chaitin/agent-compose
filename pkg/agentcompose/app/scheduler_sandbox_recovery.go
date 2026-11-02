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
	GetSchedulerBindingForSandbox(context.Context, string) (domain.SchedulerBinding, bool, error)
	GetScheduler(context.Context, string) (domain.Scheduler, error)
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
		if binding, bound, err := runs.GetSchedulerBindingForSandbox(ctx, sandbox.Summary.ID); err != nil {
			warnings = append(warnings, fmt.Sprintf("look up scheduler binding for sandbox %s: %v", sandbox.Summary.ID, err))
			continue
		} else if bound {
			// A binding only protects the sandbox while it is still a valid
			// sticky target. A removed trigger, a policy switch to new, or a
			// changed config leaves a stale row behind and must not exempt an
			// otherwise orphaned runtime forever.
			valid, err := schedulerBindingStillValid(ctx, runs, binding)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("validate scheduler binding for sandbox %s: %v", sandbox.Summary.ID, err))
				continue
			}
			if valid {
				continue
			}
		}
		schedulerID := strings.TrimSpace(schedulerSandboxTag(sandbox, "scheduler_id"))
		runID := strings.TrimSpace(schedulerSandboxTag(sandbox, "scheduler_run_id"))
		orphaned, warning := orphanedSchedulerSandbox(ctx, runs, schedulerSandboxOwner{
			sandboxID:   sandbox.Summary.ID,
			schedulerID: schedulerID,
			runID:       runID,
		}, startedAt)
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

// schedulerSandboxOwner identifies a scheduler sandbox and the run recorded as
// its owner via the scheduler_id and scheduler_run_id tags.
type schedulerSandboxOwner struct {
	sandboxID   string
	schedulerID string
	runID       string
}

// orphanedSchedulerSandbox reports whether the sandbox has lost every valid
// owner. A sandbox without a scheduler_run_id tag predates the ownership tag;
// it is resolved through its linked scheduler event instead of being stopped
// blindly, because the run status alone cannot prove which run owned it.
func orphanedSchedulerSandbox(
	ctx context.Context,
	runs schedulerRunReader,
	owner schedulerSandboxOwner,
	startedAt time.Time,
) (bool, string) {
	if owner.runID != "" {
		run, err := runs.GetSchedulerRun(ctx, owner.schedulerID, owner.runID)
		if err == nil {
			return schedulerRunLostOwner(run, startedAt), ""
		}
		if errors.Is(err, domain.ErrNotFound) {
			return true, ""
		}
		return false, fmt.Sprintf("look up scheduler run %s/%s for sandbox %s: %v", owner.schedulerID, owner.runID, owner.sandboxID, err)
	}
	if owner.schedulerID == "" {
		return false, fmt.Sprintf("sandbox %s has neither scheduler_run_id nor scheduler_id; leaving it for manual review", owner.sandboxID)
	}
	found, err := runs.BatchGetLatestSchedulerRunsBySandboxIDs(ctx, []string{owner.schedulerID}, []string{owner.sandboxID})
	if err != nil {
		return false, fmt.Sprintf("look up linked scheduler run for legacy sandbox %s: %v", owner.sandboxID, err)
	}
	run, ok := found[owner.sandboxID]
	if !ok {
		return false, fmt.Sprintf("sandbox %s has no scheduler_run_id tag and no linked scheduler run; leaving it for manual review", owner.sandboxID)
	}
	return schedulerRunLostOwner(run, startedAt), ""
}

// schedulerBindingStillValid reports whether a binding still points at a
// sandbox the scheduler would reuse. Stale rows survive trigger removal, a
// switch from sticky to new, and config changes, so the binding alone cannot
// exempt a sandbox from reclamation.
func schedulerBindingStillValid(ctx context.Context, runs schedulerRunReader, binding domain.SchedulerBinding) (bool, error) {
	// A retiring binding marks a sandbox that is being replaced and is never a
	// valid sticky target. Its sandbox must be reclaimed rather than exempted,
	// otherwise a crash between the retirement CAS and the old sandbox's
	// shutdown leaves the sandbox running until the next reuse retires it.
	if _, retiring := schedulers.RetiringSchedulerBindingConfigHash(binding); retiring {
		return false, nil
	}
	scheduler, err := runs.GetScheduler(ctx, binding.SchedulerID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if schedulers.NormalizeSandboxPolicy(scheduler.Summary.SandboxPolicy) != domain.SchedulerSandboxPolicySticky {
		return false, nil
	}
	if triggerID := strings.TrimSpace(binding.TriggerID); triggerID != "" {
		found := false
		for _, trigger := range scheduler.Triggers {
			if trigger.ID == triggerID {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	// A legacy binding predates the persisted configuration hashes. The full
	// request-level hash is claimed on the next reuse, but the scheduler-level
	// (base) hash is not backfilled by legacy adoption, so an adopted row keeps
	// an empty base hash and this check is skipped for it — a conservative keep
	// rather than a reclaim. Only compare the base hash when the row already
	// carries one. The base hash — not the full request-level sandbox config
	// hash — is the correct scope here: the full hash folds in per-request
	// inputs unavailable at startup, while the base hash is derivable from the
	// scheduler alone.
	if hash := strings.TrimSpace(binding.SchedulerConfigHash); hash != "" {
		currentHash, err := schedulers.SchedulerSandboxConfigHash(scheduler)
		if err != nil {
			return false, err
		}
		if currentHash != hash {
			return false, nil
		}
	}
	return true, nil
}

// schedulerRunLostOwner reports whether the run can no longer own its sandbox.
// A terminal run is done. A run still marked running when the daemon starts is
// stale as well: scheduler orchestration lives in daemon memory, so it cannot
// survive the restart. Startup recovery normally marks those runs failed first;
// the StartedAt check remains a safety net for a recovery failure or a run that
// recovery missed.
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

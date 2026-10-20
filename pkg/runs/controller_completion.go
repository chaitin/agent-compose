package runs

import (
	"context"
	"errors"
	"log/slog"

	"github.com/chaitin/agent-compose/internal/projects"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func withRunWarnings(run domain.ProjectRunRecord, warnings []string) domain.ProjectRunRecord {
	run.Warnings = append([]string(nil), warnings...)
	return run
}

func markProjectRunTerminalError(ctx context.Context, coordinator *Coordinator, transition TransitionRequest, err error) (domain.ProjectRunRecord, error) {
	if errors.Is(err, context.Canceled) {
		return coordinator.MarkCanceled(ctx, transition)
	}
	return coordinator.MarkFailed(ctx, transition)
}

func (c *Controller) completeProjectRunError(ctx, executionCtx context.Context, transition TransitionRequest, err error) (domain.ProjectRunRecord, error) {
	transition.Status = domain.ProjectRunStatusFailed
	// An explicitly cancelled execution context is authoritative even when the
	// runtime adapter reports the resulting I/O failure as a different error.
	// This is how StopRun interrupts a running turn without mistaking it for an
	// execution failure.
	if errors.Is(executionCtx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		transition.Status = domain.ProjectRunStatusCanceled
		if cause := context.Cause(executionCtx); cause != nil {
			transition.Error = cause.Error()
		}
	}
	return c.completeProjectRun(ctx, transition)
}

func (c *Controller) completeProjectRun(ctx context.Context, transition TransitionRequest) (domain.ProjectRunRecord, error) {
	manager, err := c.completionManager()
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	if manager == nil {
		return c.completeProjectRunWithoutJournal(ctx, transition)
	}
	return manager.Complete(ctx, transition)
}

func (c *Controller) completeProjectRunWithoutJournal(ctx context.Context, transition TransitionRequest) (domain.ProjectRunRecord, error) {
	current, err := c.configDB.GetProjectRun(ctx, transition.RunID)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	action := CompletionCleanupAction(current.CleanupPolicy, current.SandboxID != "", current.SandboxCreated)
	if action != domain.ProjectRunCompletionActionNone {
		sandbox, loadErr := c.store.GetSandbox(ctx, current.SandboxID)
		if loadErr != nil && !completionSandboxMissing(loadErr) {
			return current, loadErr
		}
		if sandbox != nil {
			policy := agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_STOP_ON_COMPLETION
			if action == domain.ProjectRunCompletionActionRemove {
				policy = agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION
			}
			if err := c.cleanupProjectRunSandboxByPolicy(ctx, SandboxResult{Sandbox: sandbox, Created: current.SandboxCreated}, policy); err != nil {
				current.Status = domain.ProjectRunStatusRunning
				current.CleanupError = err.Error()
				applyProjectRunTransitionFields(&current, transition)
				return c.configDB.UpdateProjectRun(ctx, current)
			}
		}
	}
	return NewCoordinator(c.configDB, projects.StableProjectRunID).TransitionRun(ctx, transition)
}

type controllerCompletionStopper struct{ controller *Controller }

func (s controllerCompletionStopper) Stop(ctx context.Context, sandbox *domain.Sandbox) error {
	return s.controller.stopProjectRunSandbox(ctx, sandbox)
}

type controllerCompletionRemoval struct{ controller *Controller }

func (r controllerCompletionRemoval) Remove(ctx context.Context, sandboxID string, _ bool) (sandboxes.RemovalResult, error) {
	sandbox, err := r.controller.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return sandboxes.RemovalResult{}, err
	}
	if err := r.controller.cleanupProjectRunSandboxByPolicy(ctx, SandboxResult{Sandbox: sandbox, Created: true}, agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION); err != nil {
		return sandboxes.RemovalResult{SandboxID: sandboxID}, err
	}
	return sandboxes.RemovalResult{SandboxID: sandboxID, Stopped: true, Removed: true}, nil
}

func (c *Controller) completionManager() (*CompletionManager, error) {
	if c.completion != nil {
		return c.completion, nil
	}
	store, ok := c.configDB.(CompletionStore)
	if !ok {
		return nil, nil
	}
	removal := c.removal
	if removal == nil {
		removal = controllerCompletionRemoval{controller: c}
	}
	c.completion = NewCompletionManager(CompletionManagerDeps{
		Store:     store,
		Sandboxes: c.store,
		Lifecycle: controllerCompletionStopper{controller: c},
		Removal:   removal,
		Logger:    slog.Default(),
	})
	return c.completion, nil
}

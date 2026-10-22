package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/chaitin/agent-compose/pkg/agentcompose/adapters"
	"github.com/chaitin/agent-compose/pkg/capproxy"
	"github.com/chaitin/agent-compose/pkg/events"
	"github.com/chaitin/agent-compose/pkg/llms/runtimefacade"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

const stalePendingSandboxLastError = "sandbox startup interrupted before runtime reached running state"
const staleProjectRunError = "daemon interrupted project run before reaching terminal state"

type runtimeReconciler interface {
	ReconcileRuntimeState(context.Context, *domain.Sandbox) (*domain.Sandbox, error)
}

type backgroundSchedulerController interface {
	RecoverInterruptedRuns(context.Context, time.Time) error
	Start()
}

// backgroundManagersDeps bundles the runtime components startBackgroundManagers
// starts and reconciles on daemon startup.
type backgroundManagersDeps struct {
	Sandboxes   *sandboxstore.Store
	ConfigDB    *configstore.ConfigStore
	Bridge      runtimeReconciler
	Schedulers  backgroundSchedulerController
	Events      *events.Dispatcher
	CapProxy    *capproxy.Server
	CapTokens   *adapters.CapabilitySandboxResolver
	Completions *runs.CompletionManager
	Reattacher  detachedRunReattacher
}

// detachedRunReattacher follows a detached run left running by an earlier
// daemon. RunSupervisor implements it so a re-attached run can be stopped and
// is waited for on shutdown, like any run this daemon started.
type detachedRunReattacher interface {
	ReattachRun(run domain.ProjectRunRecord, onFailure func(error))
}

func startBackgroundManagers(ctx context.Context, deps backgroundManagersDeps) error {
	startedAt := time.Now().UTC()
	reconcileCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := reconcilePersistedSandboxes(reconcileCtx, deps.Sandboxes, deps.Bridge, startedAt); err != nil {
		slog.Warn("failed to reconcile persisted sandbox state on startup", "error", err)
	}
	if deps.CapTokens != nil {
		if err := deps.CapTokens.Rebuild(reconcileCtx); err != nil {
			slog.Warn("failed to rebuild capability sandbox token index on startup", "error", err)
		}
	}
	if err := deps.Schedulers.RecoverInterruptedRuns(reconcileCtx, startedAt); err != nil {
		slog.Warn("failed to recover interrupted scheduler runs", "error", err)
	}
	if err := deps.Completions.Start(ctx); err != nil {
		return err
	}
	if err := reconcilePersistedProjectRuns(reconcileCtx, projectRunReconcileContext{
		ConfigDB:    deps.ConfigDB,
		Completions: deps.Completions,
		Reattacher:  deps.Reattacher,
		RootCtx:     ctx,
		StartedAt:   startedAt,
	}); err != nil {
		slog.Warn("failed to reconcile persisted project runs", "error", err)
	}
	deps.Schedulers.Start()
	deps.Events.Start()
	return startCapabilityProxy(ctx, deps.CapProxy)
}

func reconcilePersistedSandboxes(ctx context.Context, store *sandboxstore.Store, bridge runtimeReconciler, startedAt time.Time) error {
	result, err := store.ListSandboxes(ctx, domain.SandboxListOptions{Limit: 1 << 30})
	if err != nil {
		return err
	}
	for _, session := range result.Sandboxes {
		reconciled, err := reconcilePendingSandboxState(ctx, store, session, startedAt)
		if err != nil {
			slog.Warn("failed to reconcile pending sandbox state", "sandbox_id", session.Summary.ID, "error", err)
			continue
		}
		if _, err := bridge.ReconcileRuntimeState(ctx, reconciled); err != nil {
			slog.Warn("failed to reconcile sandbox runtime state", "sandbox_id", session.Summary.ID, "error", err)
		}
	}
	return nil
}

func reconcilePendingSandboxState(ctx context.Context, store *sandboxstore.Store, session *domain.Sandbox, startedAt time.Time) (*domain.Sandbox, error) {
	if session == nil || session.Summary.VMStatus != domain.VMStatusPending {
		return session, nil
	}
	if !session.Summary.CreatedAt.Before(startedAt) {
		return session, nil
	}
	vmState, err := store.GetVMState(session.Summary.ID)
	if err != nil {
		return nil, err
	}
	if !vmState.StartedAt.IsZero() {
		return session, nil
	}
	now := time.Now().UTC()
	vmState.StoppedAt = now
	vmState.BoxID = ""
	if strings.TrimSpace(vmState.LastError) == "" {
		vmState.LastError = stalePendingSandboxLastError
	}
	if err := store.SaveVMState(session.Summary.ID, vmState); err != nil {
		return nil, err
	}
	session.Summary.VMStatus = domain.VMStatusFailed
	if err := store.UpdateSandbox(ctx, session); err != nil {
		return nil, err
	}
	_ = store.AddEvent(ctx, session.Summary.ID, domain.SandboxEvent{
		ID:        uuid.NewString(),
		Type:      "sandbox.startup_interrupted",
		Level:     "warn",
		Message:   "sandbox marked failed after a previous startup was interrupted before the VM became ready",
		CreatedAt: now,
	})
	return store.GetSandbox(ctx, session.Summary.ID)
}

// projectRunReconcileContext bundles the store/manager, startup timestamp, and
// the daemon root context reconcilePersistedProjectRuns needs to re-attach
// detached runs that may outlive the startup reconciliation window.
type projectRunReconcileContext struct {
	ConfigDB    *configstore.ConfigStore
	Completions *runs.CompletionManager
	Reattacher  detachedRunReattacher
	RootCtx     context.Context
	StartedAt   time.Time
}

func reconcilePersistedProjectRuns(ctx context.Context, reconcile projectRunReconcileContext) error {
	if reconcile.ConfigDB == nil {
		return nil
	}
	for _, status := range []string{domain.ProjectRunStatusPending, domain.ProjectRunStatusRunning} {
		if err := reconcilePersistedProjectRunsWithStatus(ctx, reconcile, status); err != nil {
			return err
		}
	}
	return nil
}

func reconcilePersistedProjectRunsWithStatus(ctx context.Context, reconcile projectRunReconcileContext, status string) error {
	var staleRuns []domain.ProjectRunRecord
	offset := 0
	for {
		result, err := reconcile.ConfigDB.ListProjectRunsByOptions(ctx, domain.ProjectRunListOptions{
			Status: status,
			Limit:  200,
			Offset: offset,
		})
		if err != nil {
			return err
		}
		if len(result.Runs) == 0 {
			break
		}
		for _, run := range result.Runs {
			if !run.CreatedAt.Before(reconcile.StartedAt) {
				continue
			}
			staleRuns = append(staleRuns, run)
		}
		offset += len(result.Runs)
	}
	for _, run := range staleRuns {
		if run.Detach && reconcile.Reattacher != nil {
			// A run whose result was already staged before the restart is
			// finishing, not running: the completion manager finalizes it with
			// that result. Re-attaching would race it with a second result.
			if pending, err := reconcile.Completions.Pending(ctx, run.RunID); err != nil {
				slog.Warn("failed to check staged completion of detached project run", "run_id", run.RunID, "error", err)
			} else if pending {
				continue
			}
			// A detached run survives the daemon that started it: re-attach to
			// its guest journal and complete it when the task exits instead of
			// declaring it interrupted. The follower outlives this startup
			// reconciliation, so a failure is finalized on the root context.
			run := run
			reconcile.Reattacher.ReattachRun(run, func(err error) {
				slog.Warn("failed to re-attach detached project run", "run_id", run.RunID, "error", err)
				message := fmt.Sprintf("%s: re-attach failed: %v", staleProjectRunError, err)
				if stageErr := reconcile.Completions.StageInterrupted(context.WithoutCancel(reconcile.RootCtx), run, message); stageErr != nil {
					// The run is still running with no follower; a later
					// re-attach or reconciliation finalizes it. Its credentials
					// must survive until then.
					slog.Warn("failed to stage project run completion after re-attach failure", "run_id", run.RunID, "error", stageErr)
				} else if run.DetachKind == domain.DetachKindAgent || run.DetachKind == domain.DetachKindInteractivePrompt {
					// The run is finalized as interrupted, so the per-run facade
					// tokens kept for the re-attach are no longer needed.
					if delErr := reconcile.ConfigDB.DeleteLLMFacadeTokensForRun(context.WithoutCancel(reconcile.RootCtx), run.RunID, runtimefacade.TokenSourceAgent); delErr != nil {
						slog.Warn("failed to delete retained facade tokens after re-attach failure", "run_id", run.RunID, "error", delErr)
					}
				}
			})
			continue
		}
		if err := reconcile.Completions.StageInterrupted(ctx, run, staleProjectRunError); err != nil {
			slog.Warn("failed to stage stale project run completion", "run_id", run.RunID, "error", err)
		}
	}
	return nil
}

func startCapabilityProxy(ctx context.Context, capProxy *capproxy.Server) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if capProxy.Configured() {
		go func() {
			if err := capProxy.Serve(ctx); err != nil {
				slog.Error("agent compose capability grpc proxy stopped", "error", err)
			}
		}()
	}
	return nil
}

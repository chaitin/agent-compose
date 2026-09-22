package app

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/chaitin/agent-compose/pkg/agentcompose/adapters"
	"github.com/chaitin/agent-compose/pkg/capproxy"
	"github.com/chaitin/agent-compose/pkg/events"
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
	if err := reconcilePersistedProjectRuns(reconcileCtx, deps.ConfigDB, deps.Completions, startedAt); err != nil {
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

// projectRunReconcileContext bundles the store/manager and startup timestamp
// reconcilePersistedProjectRunsWithStatus needs, shared across every status
// it's called with.
type projectRunReconcileContext struct {
	ConfigDB    *configstore.ConfigStore
	Completions *runs.CompletionManager
	StartedAt   time.Time
}

func reconcilePersistedProjectRuns(ctx context.Context, configDB *configstore.ConfigStore, completions *runs.CompletionManager, startedAt time.Time) error {
	if configDB == nil {
		return nil
	}
	reconcile := projectRunReconcileContext{ConfigDB: configDB, Completions: completions, StartedAt: startedAt}
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

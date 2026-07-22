package app

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"agent-compose/pkg/agentcompose/adapters"
	"agent-compose/pkg/capproxy"
	"agent-compose/pkg/events"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
)

const stalePendingSandboxLastError = "sandbox startup interrupted before runtime reached running state"
const staleProjectRunError = "daemon interrupted project run before reaching terminal state"

type runtimeReconciler interface {
	ReconcileRuntimeState(context.Context, *domain.Sandbox) (*domain.Sandbox, error)
}

type backgroundLoaderManager interface {
	RecoverInterruptedRuns(context.Context, time.Time) error
	Start()
}

func startBackgroundManagers(ctx context.Context, sandboxes *sessionstore.Store, configDB *configstore.ConfigStore, bridge runtimeReconciler, loaders backgroundLoaderManager, events *events.Dispatcher, capProxy *capproxy.Server, capTokens *adapters.CapabilitySandboxResolver) error {
	startedAt := time.Now().UTC()
	reconcileCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := reconcilePersistedSandboxes(reconcileCtx, sandboxes, configDB, bridge, startedAt); err != nil {
		slog.Warn("failed to reconcile persisted sandbox state on startup", "error", err)
	}
	if capTokens != nil {
		if err := capTokens.Rebuild(reconcileCtx); err != nil {
			slog.Warn("failed to rebuild capability sandbox token index on startup", "error", err)
		}
	}
	if err := loaders.RecoverInterruptedRuns(reconcileCtx, startedAt); err != nil {
		slog.Warn("failed to recover interrupted scheduler runs", "error", err)
	}
	loaders.Start()
	events.Start()
	return startCapabilityProxy(ctx, capProxy)
}

func reconcilePersistedSandboxes(ctx context.Context, store *sessionstore.Store, configDB *configstore.ConfigStore, bridge runtimeReconciler, startedAt time.Time) error {
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
	if err := reconcilePersistedProjectRuns(ctx, configDB, startedAt); err != nil {
		slog.Warn("failed to reconcile persisted project runs", "error", err)
	}
	return nil
}

func reconcilePendingSandboxState(ctx context.Context, store *sessionstore.Store, session *domain.Sandbox, startedAt time.Time) (*domain.Sandbox, error) {
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

func reconcilePersistedProjectRuns(ctx context.Context, configDB *configstore.ConfigStore, startedAt time.Time) error {
	if configDB == nil {
		return nil
	}
	coordinator := runs.NewCoordinator(configDB, domain.StableProjectRunID)
	for _, status := range []string{domain.ProjectRunStatusPending, domain.ProjectRunStatusRunning} {
		if err := reconcilePersistedProjectRunsWithStatus(ctx, configDB, coordinator, status, startedAt); err != nil {
			return err
		}
	}
	return nil
}

func reconcilePersistedProjectRunsWithStatus(ctx context.Context, configDB *configstore.ConfigStore, coordinator *runs.Coordinator, status string, startedAt time.Time) error {
	var staleRuns []domain.ProjectRunRecord
	offset := 0
	for {
		runs, err := configDB.ListProjectRunsByOptions(ctx, domain.ProjectRunListOptions{
			Status: status,
			Limit:  200,
			Offset: offset,
		})
		if err != nil {
			return err
		}
		if len(runs) == 0 {
			break
		}
		for _, run := range runs {
			if !run.CreatedAt.Before(startedAt) {
				continue
			}
			staleRuns = append(staleRuns, run)
		}
		offset += len(runs)
	}
	for _, run := range staleRuns {
		if _, err := coordinator.MarkFailed(ctx, runs.TransitionRequest{
			RunID:    run.RunID,
			ExitCode: execution.FirstNonZeroInt(run.ExitCode, 1),
			Error:    staleProjectRunError,
		}); err != nil {
			slog.Warn("failed to mark stale project run failed", "run_id", run.RunID, "error", err)
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

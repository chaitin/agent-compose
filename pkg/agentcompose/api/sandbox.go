package api

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type SandboxStore interface {
	GetSandbox(context.Context, string) (*domain.Sandbox, error)
	RemoveSandbox(context.Context, string) error
}

type SandboxStatsStore interface {
	SandboxStore
	GetVMState(string) (domain.VMState, error)
}

type SandboxStatsRuntime interface {
	Stats(context.Context, *domain.Sandbox, domain.VMState) (domain.SandboxStats, error)
}

type SandboxStatsRuntimeResolver func(*domain.Sandbox) (SandboxStatsRuntime, error)

type SandboxRuntimeRemover interface {
	RemoveSandboxVM(context.Context, *domain.Sandbox) error
}

type SandboxDashboardNotifier interface {
	Notify(string)
}

type SandboxHandler struct {
	delegate   SessionDelegate
	store      SandboxStore
	remover    SandboxRuntimeRemover
	reconciler SessionRuntimeReconciler
	dashboard  SandboxDashboardNotifier
	stats      SandboxStatsRuntimeResolver
}

func NewSandboxHandler(delegate SessionDelegate, store SandboxStore, remover SandboxRuntimeRemover, dashboard SandboxDashboardNotifier, stats ...SandboxStatsRuntimeResolver) *SandboxHandler {
	handler := &SandboxHandler{delegate: delegate, store: store, remover: remover, dashboard: dashboard}
	if reconciler, ok := delegate.(SessionRuntimeReconciler); ok {
		handler.reconciler = reconciler
	}
	if len(stats) > 0 {
		handler.stats = stats[0]
	}
	return handler
}

func (h *SandboxHandler) RemoveSandbox(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
	sandboxID := strings.TrimSpace(req.Msg.GetSandboxId())
	if err := validateSandboxID(sandboxID); err != nil {
		return nil, err
	}
	session, err := h.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if h.reconciler != nil {
		reconciled, recErr := h.reconciler.ReconcileRuntimeState(ctx, session)
		if recErr != nil {
			slog.Warn("failed to reconcile sandbox runtime state before remove", "sandbox_id", sandboxID, "error", recErr)
			return nil, connect.NewError(connect.CodeInternal, recErr)
		}
		session = reconciled
	}
	stopped := false
	if session.Summary.VMStatus == domain.VMStatusRunning {
		if !req.Msg.GetForce() {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sandbox %s is running", sandboxID))
		}
		if _, err := h.delegate.StopSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sandboxID})); err != nil {
			return nil, err
		}
		stopped = true
	}
	if h.remover == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sandbox runtime remover is required"))
	}
	if err := h.remover.RemoveSandboxVM(ctx, session); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := h.store.RemoveSandbox(ctx, sandboxID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if h.dashboard != nil {
		h.dashboard.Notify("sandbox_removed")
	}
	return connect.NewResponse(&agentcomposev2.RemoveSandboxResponse{
		SandboxId: sandboxID,
		Stopped:   stopped,
		Removed:   true,
	}), nil
}

func (h *SandboxHandler) GetSandboxStats(ctx context.Context, req *connect.Request[agentcomposev2.GetSandboxStatsRequest]) (*connect.Response[agentcomposev2.GetSandboxStatsResponse], error) {
	sandboxID := strings.TrimSpace(req.Msg.GetSandboxId())
	if err := validateSandboxID(sandboxID); err != nil {
		return nil, err
	}
	session, err := h.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if h.reconciler != nil {
		reconciled, recErr := h.reconciler.ReconcileRuntimeState(ctx, session)
		if recErr != nil {
			slog.Warn("failed to reconcile sandbox runtime state before stats", "sandbox_id", sandboxID, "error", recErr)
			return nil, connect.NewError(connect.CodeInternal, recErr)
		}
		session = reconciled
	}
	if session.Summary.VMStatus != domain.VMStatusRunning {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sandbox %s is not running", sandboxID))
	}
	statsStore, ok := h.store.(SandboxStatsStore)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sandbox stats store is required"))
	}
	if h.stats == nil {
		return nil, ConnectErrorForDomain(domain.ClassifyError(domain.ErrUnsupported, "sandbox stats are unsupported by this daemon", nil))
	}
	vmState, err := statsStore.GetVMState(sandboxID)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	runtime, err := h.stats(session)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	stats, err := runtime.Stats(ctx, session, vmState)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	stats.SandboxID = firstNonEmpty(stats.SandboxID, sandboxID)
	stats.Driver = firstNonEmpty(stats.Driver, session.Summary.Driver, vmState.Driver)
	if stats.SampledAt.IsZero() {
		stats.SampledAt = time.Now().UTC()
	}
	return connect.NewResponse(&agentcomposev2.GetSandboxStatsResponse{Stats: SandboxStatsToProto(stats)}), nil
}

func validateSandboxID(sandboxID string) error {
	if sandboxID == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("sandbox id is required"))
	}
	if !identity.IsID(sandboxID) {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid sandbox id %q", sandboxID))
	}
	if sandboxID == "." || sandboxID == ".." || filepath.Base(sandboxID) != sandboxID {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid sandbox id %q", sandboxID))
	}
	return nil
}

func SandboxStatsToProto(stats domain.SandboxStats) *agentcomposev2.SandboxStats {
	return &agentcomposev2.SandboxStats{
		SandboxId:        stats.SandboxID,
		Driver:           stats.Driver,
		SampledAt:        FormatProjectTime(stats.SampledAt),
		CpuPercent:       MetricValueToProto(stats.CPUPercent),
		MemoryUsageBytes: MetricValueToProto(stats.MemoryUsageBytes),
		MemoryLimitBytes: MetricValueToProto(stats.MemoryLimitBytes),
		MemoryPercent:    MetricValueToProto(stats.MemoryPercent),
		NetworkRxBytes:   MetricValueToProto(stats.NetworkRxBytes),
		NetworkTxBytes:   MetricValueToProto(stats.NetworkTxBytes),
		BlockReadBytes:   MetricValueToProto(stats.BlockReadBytes),
		BlockWriteBytes:  MetricValueToProto(stats.BlockWriteBytes),
		UptimeSeconds:    MetricValueToProto(stats.UptimeSeconds),
	}
}

func MetricValueToProto(metric domain.MetricValue) *agentcomposev2.MetricValue {
	status := MetricStatusToProto(metric.Status)
	if status == agentcomposev2.MetricStatus_METRIC_STATUS_UNSPECIFIED {
		status = agentcomposev2.MetricStatus_METRIC_STATUS_UNKNOWN
	}
	return &agentcomposev2.MetricValue{
		Value:   metric.Value,
		Unit:    metric.Unit,
		Status:  status,
		Message: metric.Message,
	}
}

func MetricStatusToProto(status string) agentcomposev2.MetricStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case domain.MetricStatusOK:
		return agentcomposev2.MetricStatus_METRIC_STATUS_OK
	case domain.MetricStatusUnknown:
		return agentcomposev2.MetricStatus_METRIC_STATUS_UNKNOWN
	case domain.MetricStatusUnavailable:
		return agentcomposev2.MetricStatus_METRIC_STATUS_UNAVAILABLE
	default:
		return agentcomposev2.MetricStatus_METRIC_STATUS_UNSPECIFIED
	}
}

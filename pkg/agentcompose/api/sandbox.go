package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type SandboxStore interface {
	GetSandbox(context.Context, string) (*domain.Sandbox, error)
	RemoveSandbox(context.Context, string) error
}

type SandboxStatsStore interface {
	SandboxStore
	GetVMState(string) (domain.VMState, error)
}

type SandboxListStore interface {
	ListSandboxes(context.Context, domain.SandboxListOptions) (domain.SandboxListResult, error)
}

type SandboxHistoryStore interface {
	ListCells(context.Context, string) ([]domain.NotebookCell, error)
	ListEvents(context.Context, string) ([]domain.SandboxEvent, error)
}

type SandboxProxyStateStore interface {
	GetProxyState(string) (domain.ProxyState, error)
}

type SandboxWatchSource interface {
	SubscribeSandbox(string) (<-chan sandboxes.WatchEvent, func())
}

type SandboxStatsRuntime interface {
	Stats(context.Context, *domain.Sandbox, domain.VMState) (domain.SandboxStats, error)
}

type SandboxStatsRuntimeResolver func(*domain.Sandbox) (SandboxStatsRuntime, error)

type SandboxRuntimeRemover interface {
	RemoveSandboxVM(context.Context, *domain.Sandbox) error
}

type SandboxRemovalCoordinator interface {
	Remove(context.Context, string, bool) (sandboxes.RemovalResult, error)
	Prune(context.Context, sandboxes.PruneRequest) (sandboxes.PruneResult, error)
}

type SandboxDashboardNotifier interface {
	Notify(string)
}

type SandboxLifecycleDelegate interface {
	ResumeSandbox(context.Context, string) (*domain.Sandbox, error)
	StopSandbox(context.Context, string) (*domain.Sandbox, error)
	GetSandboxProxy(context.Context, string) (SandboxProxy, error)
}

type SandboxGracefulLifecycleDelegate interface {
	StopSandboxWithOptions(context.Context, string, sandboxes.StopOptions) (sandboxes.StopOutcome, error)
}

type SandboxProxy struct {
	ProxyPath   string
	NotebookURL string
}

type SandboxRunTargetResolver interface {
	Resolve(context.Context, *domain.Sandbox) (runs.SandboxRunTarget, error)
	ResolveBatch(context.Context, []*domain.Sandbox) (map[string]runs.SandboxRunTarget, error)
}

type SandboxHandler struct {
	agentcomposev2connect.UnimplementedSandboxServiceHandler
	delegate   SandboxLifecycleDelegate
	store      SandboxStore
	remover    SandboxRuntimeRemover
	reconciler SandboxRuntimeReconciler
	dashboard  SandboxDashboardNotifier
	stats      SandboxStatsRuntimeResolver
	runTargets SandboxRunTargetResolver
	removal    SandboxRemovalCoordinator
}

func (h *SandboxHandler) WithRemovalCoordinator(coordinator SandboxRemovalCoordinator) *SandboxHandler {
	h.removal = coordinator
	return h
}

func (h *SandboxHandler) WithRunTargetResolver(resolver SandboxRunTargetResolver) *SandboxHandler {
	h.runTargets = resolver
	return h
}

// SandboxHandlerDeps bundles NewSandboxHandler's required dependencies.
type SandboxHandlerDeps struct {
	Delegate  SandboxLifecycleDelegate
	Store     SandboxStore
	Remover   SandboxRuntimeRemover
	Dashboard SandboxDashboardNotifier
}

func NewSandboxHandler(deps SandboxHandlerDeps, stats ...SandboxStatsRuntimeResolver) *SandboxHandler {
	handler := &SandboxHandler{delegate: deps.Delegate, store: deps.Store, remover: deps.Remover, dashboard: deps.Dashboard}
	if reconciler, ok := deps.Delegate.(SandboxRuntimeReconciler); ok {
		handler.reconciler = reconciler
	}
	if len(stats) > 0 {
		handler.stats = stats[0]
	}
	return handler
}

func (h *SandboxHandler) GetSandbox(ctx context.Context, req *connect.Request[agentcomposev2.GetSandboxRequest]) (*connect.Response[agentcomposev2.GetSandboxResponse], error) {
	sandbox, err := h.loadSandbox(ctx, req.Msg.GetSandboxId())
	if err != nil {
		return nil, err
	}
	result := h.sandboxToV2(ctx, sandbox)
	h.populateSandboxNotebookURL(sandbox, result)
	return connect.NewResponse(&agentcomposev2.GetSandboxResponse{Sandbox: result}), nil
}

func (h *SandboxHandler) populateSandboxNotebookURL(sandbox *domain.Sandbox, result *agentcomposev2.Sandbox) {
	store, ok := h.store.(SandboxProxyStateStore)
	if !ok || sandbox == nil || result == nil || sandbox.Summary.VMStatus != domain.VMStatusRunning {
		return
	}
	proxy, err := store.GetProxyState(sandbox.Summary.ID)
	if err != nil || !proxy.Enabled || !proxy.Exposed || strings.TrimSpace(proxy.Token) == "" {
		return
	}
	location := strings.TrimSpace(proxy.ProxyPath)
	if location == "" {
		location = strings.TrimSpace(sandbox.Summary.ProxyPath)
	}
	if location == "" {
		location = strings.TrimSpace(proxy.JupyterURL)
	}
	parsed, err := url.Parse(location)
	if err != nil || location == "" {
		return
	}
	query := parsed.Query()
	query.Set("token", proxy.Token)
	parsed.RawQuery = query.Encode()
	result.NotebookUrl = parsed.String()
}

func (h *SandboxHandler) ListSandboxes(ctx context.Context, req *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error) {
	offset, limit, err := listPagination(req.Msg.GetOffset(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}
	statusValues, err := sandboxStatusesFromProto(req.Msg.GetStatus())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	statuses, err := sandboxes.NormalizeVMStatuses(statusValues)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	store, ok := h.store.(SandboxListStore)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sandbox list store is required"))
	}
	result, err := store.ListSandboxes(ctx, domain.SandboxListOptions{
		ProjectID:  strings.TrimSpace(req.Msg.GetProjectId()),
		VMStatuses: statuses,
		Offset:     offset,
		Limit:      limit,
	})
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	response := &agentcomposev2.ListSandboxesResponse{Sandboxes: make([]*agentcomposev2.Sandbox, 0, len(result.Sandboxes)), Total: uint32(result.TotalCount)}
	targets := h.resolveSandboxTargets(ctx, result.Sandboxes)
	for _, sandbox := range result.Sandboxes {
		response.Sandboxes = append(response.Sandboxes, sandboxToV2WithTarget(sandbox, targets[sandbox.Summary.ID]))
	}
	return connect.NewResponse(response), nil
}

func (h *SandboxHandler) ListSandboxHistory(ctx context.Context, req *connect.Request[agentcomposev2.ListSandboxHistoryRequest]) (*connect.Response[agentcomposev2.ListSandboxHistoryResponse], error) {
	sandboxID := strings.TrimSpace(req.Msg.GetSandboxId())
	if err := validateSandboxID(sandboxID); err != nil {
		return nil, err
	}
	if _, err := h.store.GetSandbox(ctx, sandboxID); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	store, ok := h.store.(SandboxHistoryStore)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sandbox history store is required"))
	}
	cells, err := store.ListCells(ctx, sandboxID)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	events, err := store.ListEvents(ctx, sandboxID)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	response, err := paginateSandboxHistory(cells, events, req.Msg.GetOffset(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (h *SandboxHandler) WatchSandbox(ctx context.Context, req *connect.Request[agentcomposev2.WatchSandboxRequest], stream *connect.ServerStream[agentcomposev2.WatchSandboxResponse]) error {
	sandboxID := strings.TrimSpace(req.Msg.GetSandboxId())
	if err := validateSandboxID(sandboxID); err != nil {
		return err
	}
	source, ok := h.delegate.(SandboxWatchSource)
	if !ok {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("sandbox watch source is required"))
	}
	events, unsubscribe := source.SubscribeSandbox(sandboxID)
	defer unsubscribe()
	sandbox, err := h.loadSandbox(ctx, sandboxID)
	if err != nil {
		return err
	}
	PrepareStreamingHeaders(stream.ResponseHeader())
	if err := stream.Send(&agentcomposev2.WatchSandboxResponse{EventType: agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_SANDBOX_UPDATED, Sandbox: h.sandboxToV2(ctx, sandbox)}); err != nil {
		return connect.NewError(connect.CodeUnknown, err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if err := stream.Send(h.sandboxWatchEventToV2(ctx, event)); err != nil {
				return connect.NewError(connect.CodeUnknown, err)
			}
		}
	}
}

func sandboxWatchEventToV2(event sandboxes.WatchEvent) *agentcomposev2.WatchSandboxResponse {
	response := &agentcomposev2.WatchSandboxResponse{CellId: event.CellID, Chunk: event.Chunk, Stream: StdioStreamToProto(event.Stream)}
	switch event.EventType {
	case sandboxes.WatchEventTypeSandboxUpdated:
		response.EventType = agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_SANDBOX_UPDATED
		if event.Sandbox != nil {
			response.Sandbox = sandboxToV2(&domain.Sandbox{Summary: *event.Sandbox})
		}
	case sandboxes.WatchEventTypeCellStarted:
		response.EventType = agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_CELL_STARTED
		response.Cell = sandboxHistoryCellToV2(event.Cell)
	case sandboxes.WatchEventTypeCellOutput:
		response.EventType = agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_CELL_OUTPUT
	case sandboxes.WatchEventTypeCellCompleted:
		response.EventType = agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_CELL_COMPLETED
		response.Cell = sandboxHistoryCellToV2(event.Cell)
	case sandboxes.WatchEventTypeEventAdded:
		response.EventType = agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_EVENT_ADDED
		response.Event = sandboxHistoryEventToV2(event.Event)
	}
	return response
}

func (h *SandboxHandler) sandboxWatchEventToV2(ctx context.Context, event sandboxes.WatchEvent) *agentcomposev2.WatchSandboxResponse {
	response := sandboxWatchEventToV2(event)
	if event.EventType == sandboxes.WatchEventTypeSandboxUpdated && event.Sandbox != nil {
		response.Sandbox = h.sandboxToV2(ctx, &domain.Sandbox{Summary: *event.Sandbox})
	}
	return response
}

func sandboxHistoryTimestamp(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}

// A cell accumulates every byte its run writes, for as long as the run lasts, so
// nothing bounds its size: one verbose build an agent reran a dozen times reached
// 41MB, and because the same bytes travelled in both stdout and output, listing
// that single cell meant a 75MB response. Carry the newest bytes, which is the
// end anyone reads, and leave the rest to RunService.FollowRunLogs.
const maxSandboxHistoryCellOutputBytes = 64 << 10

func sandboxHistoryCellToV2(cell *domain.NotebookCell) *agentcomposev2.SandboxHistoryCell {
	if cell == nil {
		return nil
	}
	// Go evaluates arguments before the call, so folding this into firstNonEmpty
	// would concatenate and then discard a copy of the very stream this function
	// exists to keep out of memory.
	merged := cell.Output
	if merged == "" {
		merged = cell.Stdout + cell.Stderr
	}
	output, truncated := tailBytes(merged, maxSandboxHistoryCellOutputBytes)
	// A cell carries whatever bytes its run wrote, which for a command that cats a
	// binary is not text at all. One invalid sequence anywhere fails the marshal for
	// the entire response, not just the cell that produced it. Scrub after the cut so
	// the scan covers the tail rather than the whole stream, and so a stream that was
	// already valid - which is nearly all of them - is returned unchanged without
	// allocating. A tail that is mostly binary grows to at most three times the cap,
	// which is still four orders of magnitude below what this function exists to stop.
	output = strings.ToValidUTF8(output, "\uFFFD")
	return &agentcomposev2.SandboxHistoryCell{
		Id: cell.ID, Type: cell.Type, Source: cell.Source, Output: output, OutputTruncatedBytes: truncated,
		ExitCode: int32(cell.ExitCode), Success: cell.Success, Running: cell.Running,
		CreatedAt: sandboxHistoryTimestamp(cell.CreatedAt), Agent: cell.Agent, AgentThreadId: cell.AgentThreadID, StopReason: cell.StopReason,
	}
}

// tailBytes keeps the last limit bytes of value and reports how many it dropped.
// The cut lands on a rune boundary so it does not split a multi-byte rune that
// survived the cut; whether what the run wrote was valid UTF-8 to begin with is
// the caller's problem, and it has to scrub for that separately.
func tailBytes(value string, limit int) (string, uint64) {
	if len(value) <= limit {
		return value, 0
	}
	tail := value[len(value)-limit:]
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return tail, uint64(len(value) - len(tail))
}

func sandboxHistoryEventToV2(event *domain.SandboxEvent) *agentcomposev2.SandboxHistoryEvent {
	if event == nil {
		return nil
	}
	return &agentcomposev2.SandboxHistoryEvent{Id: event.ID, Type: event.Type, Level: event.Level, Message: event.Message, CreatedAt: sandboxHistoryTimestamp(event.CreatedAt)}
}

func (h *SandboxHandler) StopSandbox(ctx context.Context, req *connect.Request[agentcomposev2.StopSandboxRequest]) (*connect.Response[agentcomposev2.StopSandboxResponse], error) {
	options, err := sandboxStopOptions(req.Msg)
	if err != nil {
		return nil, err
	}
	sandbox, err := h.loadSandbox(ctx, req.Msg.GetSandboxId())
	if err != nil {
		return nil, err
	}
	policy := sandboxes.EffectiveStoppedRuntimePolicy(sandbox)
	state := sandboxes.EffectiveStoppedRuntimeState(sandbox)
	if sandbox.Summary.VMStatus == domain.VMStatusStopped &&
		(policy == domain.StoppedRuntimePolicyRetain || state == domain.StoppedRuntimeStateReleased) {
		outcome := agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_FORCE
		if options.Mode == sandboxes.StopModeGraceful {
			outcome = agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_GRACEFUL
		}
		return connect.NewResponse(&agentcomposev2.StopSandboxResponse{Sandbox: h.sandboxToV2(ctx, sandbox), Outcome: outcome}), nil
	}
	retryRelease := sandbox.Summary.VMStatus == domain.VMStatusStopped &&
		policy == domain.StoppedRuntimePolicyRemove && state == domain.StoppedRuntimeStateReleasePending
	if sandbox.Summary.VMStatus != domain.VMStatusRunning && !retryRelease {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sandbox %s cannot be stopped from state %s", sandbox.Summary.ID, sandbox.Summary.VMStatus))
	}
	if options.Mode != sandboxes.StopModeGraceful {
		stopped, stopErr := h.delegate.StopSandbox(ctx, sandbox.Summary.ID)
		if stopErr != nil {
			return nil, stopErr
		}
		return connect.NewResponse(&agentcomposev2.StopSandboxResponse{Sandbox: h.sandboxToV2(ctx, stopped), Outcome: agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_FORCE}), nil
	}
	graceful, ok := h.delegate.(SandboxGracefulLifecycleDelegate)
	if !ok {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("graceful sandbox stop is unsupported"))
	}
	outcome, stopErr := graceful.StopSandboxWithOptions(ctx, sandbox.Summary.ID, options)
	if stopErr != nil {
		return nil, stopErr
	}
	return connect.NewResponse(&agentcomposev2.StopSandboxResponse{Sandbox: h.sandboxToV2(ctx, outcome.Sandbox), Outcome: sandboxStopOutcomeToProto(outcome)}), nil
}

const maxSandboxGracePeriod = 5 * time.Minute

func sandboxStopOptions(request *agentcomposev2.StopSandboxRequest) (sandboxes.StopOptions, error) {
	mode := sandboxes.StopModeForce
	if request.GetMode() == agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_GRACEFUL {
		mode = sandboxes.StopModeGraceful
	} else if request.GetMode() != agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_UNSPECIFIED && request.GetMode() != agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_FORCE {
		return sandboxes.StopOptions{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported sandbox stop mode %s", request.GetMode()))
	}
	gracePeriod := time.Duration(0)
	if request.GetGracePeriod() != nil {
		if err := request.GetGracePeriod().CheckValid(); err != nil {
			return sandboxes.StopOptions{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid grace period: %w", err))
		}
		gracePeriod = request.GetGracePeriod().AsDuration()
		if gracePeriod <= 0 || gracePeriod > maxSandboxGracePeriod {
			return sandboxes.StopOptions{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("grace period must be greater than zero and at most %s", maxSandboxGracePeriod))
		}
	}
	if gracePeriod > 0 && mode != sandboxes.StopModeGraceful {
		return sandboxes.StopOptions{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("grace period requires graceful stop mode"))
	}
	return sandboxes.StopOptions{Mode: mode, GracePeriod: gracePeriod}, nil
}

func sandboxStopOutcomeToProto(outcome sandboxes.StopOutcome) agentcomposev2.SandboxStopOutcome {
	switch outcome.Preparation.Outcome {
	case sandboxes.StopPreparationGraceful:
		return agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_GRACEFUL
	case sandboxes.StopPreparationTimeout:
		return agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_FORCE_AFTER_GRACE_TIMEOUT
	case sandboxes.StopPreparationFailed:
		return agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_FORCE_AFTER_GRACE_ERROR
	default:
		return agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_FORCE
	}
}

func (h *SandboxHandler) ResumeSandbox(ctx context.Context, req *connect.Request[agentcomposev2.ResumeSandboxRequest]) (*connect.Response[agentcomposev2.ResumeSandboxResponse], error) {
	sandbox, err := h.loadSandbox(ctx, req.Msg.GetSandboxId())
	if err != nil {
		return nil, err
	}
	if sandbox.Summary.VMStatus == domain.VMStatusRunning {
		return connect.NewResponse(&agentcomposev2.ResumeSandboxResponse{Sandbox: h.sandboxToV2(ctx, sandbox)}), nil
	}
	if sandbox.Summary.VMStatus != domain.VMStatusStopped {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sandbox %s cannot be resumed from state %s", sandbox.Summary.ID, sandbox.Summary.VMStatus))
	}
	if domain.SandboxWorkspaceUnavailable(sandbox) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sandbox %s workspace was reclaimed and cannot be resumed", sandbox.Summary.ID))
	}
	resumed, err := h.delegate.ResumeSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev2.ResumeSandboxResponse{Sandbox: h.sandboxToV2(ctx, resumed)}), nil
}

func (h *SandboxHandler) loadSandbox(ctx context.Context, rawID string) (*domain.Sandbox, error) {
	sandboxID := strings.TrimSpace(rawID)
	if err := validateSandboxID(sandboxID); err != nil {
		return nil, err
	}
	sandbox, err := h.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if h.reconciler == nil {
		return sandbox, nil
	}
	reconciled, err := h.reconciler.ReconcileRuntimeState(ctx, sandbox)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return reconciled, nil
}

func sandboxToV2(sandbox *domain.Sandbox) *agentcomposev2.Sandbox {
	return sandboxToV2WithTarget(sandbox, runs.SandboxRunTarget{})
}

func sandboxToV2WithTarget(sandbox *domain.Sandbox, target runs.SandboxRunTarget) *agentcomposev2.Sandbox {
	if sandbox == nil {
		return nil
	}
	result := &agentcomposev2.Sandbox{
		SandboxId:            sandbox.Summary.ID,
		Status:               sandboxStatusToProto(sandbox.Summary.VMStatus),
		Driver:               sandbox.Summary.Driver,
		CreatedAt:            timestamppb.New(sandbox.Summary.CreatedAt),
		UpdatedAt:            timestamppb.New(sandbox.Summary.UpdatedAt),
		Image:                sandbox.Summary.GuestImage,
		WorkspacePath:        sandbox.Summary.WorkspacePath,
		WorkspaceDelivery:    sandboxWorkspaceDeliveryToProto(sandbox.Workspace),
		Title:                sandbox.Summary.Title,
		ProxyPath:            sandbox.Summary.ProxyPath,
		TriggerSource:        sandbox.Summary.TriggerSource,
		CellCount:            uint32(sandbox.Summary.CellCount),
		EventCount:           uint32(sandbox.Summary.EventCount),
		ProjectId:            target.ProjectID,
		AgentName:            target.AgentName,
		StoppedRuntimePolicy: sandboxes.EffectiveStoppedRuntimePolicy(sandbox),
		StoppedRuntimeState:  sandboxes.EffectiveStoppedRuntimeState(sandbox),
	}
	if stoppedRuntime := sandbox.StoppedRuntime; stoppedRuntime != nil {
		result.StoppedRuntimeLastError = stoppedRuntime.LastError
		result.StoppedRuntimeReleasedAt = sandboxHistoryTimestamp(stoppedRuntime.ReleasedAt)
	}
	for _, tag := range sandbox.Summary.Tags {
		result.Tags = append(result.Tags, &agentcomposev2.SandboxTag{Name: tag.Name, Value: tag.Value})
	}
	if reclamation := sandbox.WorkspaceReclamation; reclamation != nil {
		result.WorkspaceReclamationState = reclamationStateToProto(reclamation.State)
		result.WorkspaceReclamationStartedAt = sandboxHistoryTimestamp(reclamation.StartedAt)
		result.WorkspaceReclamationCompletedAt = sandboxHistoryTimestamp(reclamation.CompletedAt)
		result.WorkspaceReclamationLastError = reclamation.LastError
	}
	return result
}

func (h *SandboxHandler) sandboxToV2(ctx context.Context, sandbox *domain.Sandbox) *agentcomposev2.Sandbox {
	if h.runTargets == nil || sandbox == nil {
		return sandboxToV2(sandbox)
	}
	target, err := h.runTargets.Resolve(ctx, sandbox)
	if err != nil {
		slog.Warn("failed to resolve sandbox run target", "sandbox_id", sandbox.Summary.ID, "error", err)
		return sandboxToV2(sandbox)
	}
	return sandboxToV2WithTarget(sandbox, target)
}

func (h *SandboxHandler) resolveSandboxTargets(ctx context.Context, sandboxes []*domain.Sandbox) map[string]runs.SandboxRunTarget {
	if h.runTargets == nil || len(sandboxes) == 0 {
		return nil
	}
	targets, err := h.runTargets.ResolveBatch(ctx, sandboxes)
	if err != nil {
		slog.Warn("failed to resolve sandbox run targets", "sandbox_count", len(sandboxes), "error", err)
		return nil
	}
	return targets
}

func (h *SandboxHandler) RemoveSandbox(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
	sandboxID := strings.TrimSpace(req.Msg.GetSandboxId())
	if err := validateSandboxID(sandboxID); err != nil {
		return nil, err
	}
	if h.removal != nil {
		result, err := h.removal.Remove(ctx, sandboxID, req.Msg.GetForce())
		if err != nil {
			if errors.Is(err, sandboxes.ErrSandboxRunning) {
				return nil, connect.NewError(connect.CodeFailedPrecondition, err)
			}
			if errors.Is(err, sandboxes.ErrOwnershipUnknown) || errors.Is(err, sandboxes.ErrUnsafeResidue) {
				return nil, connect.NewError(connect.CodeFailedPrecondition, err)
			}
			return nil, ConnectErrorForDomain(err)
		}
		if h.dashboard != nil {
			h.dashboard.Notify("sandbox_removed")
		}
		return connect.NewResponse(&agentcomposev2.RemoveSandboxResponse{SandboxId: result.SandboxID, Stopped: result.Stopped, Removed: result.Removed}), nil
	}
	session, err := h.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if h.reconciler != nil {
		reconciled, recErr := h.reconciler.ReconcileRuntimeState(ctx, session)
		if recErr != nil {
			slog.Warn("failed to reconcile sandbox runtime state before remove", "sandbox_id", sandboxID, "error", recErr)
			return nil, ConnectErrorForDomain(recErr)
		}
		session = reconciled
	}
	stopped := false
	if session.Summary.VMStatus == domain.VMStatusRunning {
		if !req.Msg.GetForce() {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sandbox %s is running", sandboxID))
		}
		if _, err := h.delegate.StopSandbox(ctx, sandboxID); err != nil {
			return nil, err
		}
		stopped = true
	}
	if h.remover == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sandbox runtime remover is required"))
	}
	if err := h.remover.RemoveSandboxVM(ctx, session); err != nil {
		return nil, ConnectErrorForDomain(err)
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

func (h *SandboxHandler) PruneSandboxes(ctx context.Context, req *connect.Request[agentcomposev2.PruneSandboxesRequest]) (*connect.Response[agentcomposev2.PruneSandboxesResponse], error) {
	if h.removal == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("sandbox removal coordinator is unavailable"))
	}
	olderThan, err := RuntimeCacheOlderThanFromProto(req.Msg.GetOlderThanSeconds())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	statuses, err := sandboxStatusesFromProto(req.Msg.GetStatus())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	result, err := h.removal.Prune(ctx, sandboxes.PruneRequest{
		ProjectID: strings.TrimSpace(req.Msg.GetProjectId()), Statuses: statuses,
		AgentName: strings.TrimSpace(req.Msg.GetAgentName()), Driver: strings.TrimSpace(req.Msg.GetDriver()),
		OlderThan: olderThan, IncludeOrphans: req.Msg.GetIncludeOrphans(), Force: req.Msg.GetForce(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentcomposev2.PruneSandboxesResponse{
		DryRun: result.DryRun, Matched: sandboxPruneCandidatesToProto(result.Matched), Removed: result.Removed,
		Skipped: sandboxPruneCandidatesToProto(result.Skipped), Warnings: result.Warnings,
	}), nil
}

func sandboxPruneCandidatesToProto(items []sandboxes.PruneCandidate) []*agentcomposev2.SandboxPruneCandidate {
	out := make([]*agentcomposev2.SandboxPruneCandidate, 0, len(items))
	for _, item := range items {
		kind := agentcomposev2.SandboxPruneCandidateKind_SANDBOX_PRUNE_CANDIDATE_KIND_SANDBOX_RECORD
		if item.Kind == sandboxes.PruneCandidateRuntimeResidue {
			kind = agentcomposev2.SandboxPruneCandidateKind_SANDBOX_PRUNE_CANDIDATE_KIND_RUNTIME_RESIDUE
		}
		candidate := &agentcomposev2.SandboxPruneCandidate{
			Kind: kind, SandboxId: item.SandboxID, ProjectId: item.ProjectID, AgentName: item.AgentName,
			Driver: item.Driver, Status: sandboxStatusToProto(item.Status), RuntimeId: item.RuntimeID,
			Removable: item.Removable, BlockedReasons: append([]string(nil), item.BlockedReasons...),
		}
		if !item.UpdatedAt.IsZero() {
			candidate.UpdatedAt = timestamppb.New(item.UpdatedAt)
		}
		out = append(out, candidate)
	}
	return out
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
	if !identity.IsID(sandboxID) && !isLegacySandboxID(sandboxID) {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid sandbox id %q", sandboxID))
	}
	if sandboxID == "." || sandboxID == ".." || filepath.Base(sandboxID) != sandboxID {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid sandbox id %q", sandboxID))
	}
	return nil
}

func isLegacySandboxID(sandboxID string) bool {
	parsed, err := uuid.Parse(sandboxID)
	return err == nil && parsed.String() == strings.ToLower(sandboxID)
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

package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
	"agent-compose/pkg/sessions"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
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
	SubscribeSandbox(string) (<-chan sessions.WatchEvent, func())
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

type SandboxLifecycleDelegate interface {
	ResumeSandbox(context.Context, string) (*domain.Sandbox, error)
	StopSandbox(context.Context, string) (*domain.Sandbox, error)
	GetSandboxProxy(context.Context, string) (SandboxProxy, error)
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
	reconciler SessionRuntimeReconciler
	dashboard  SandboxDashboardNotifier
	stats      SandboxStatsRuntimeResolver
	runTargets SandboxRunTargetResolver
}

func (h *SandboxHandler) WithRunTargetResolver(resolver SandboxRunTargetResolver) *SandboxHandler {
	h.runTargets = resolver
	return h
}

func NewSandboxHandler(delegate SandboxLifecycleDelegate, store SandboxStore, remover SandboxRuntimeRemover, dashboard SandboxDashboardNotifier, stats ...SandboxStatsRuntimeResolver) *SandboxHandler {
	handler := &SandboxHandler{delegate: delegate, store: store, remover: remover, dashboard: dashboard}
	if reconciler, ok := delegate.(SessionRuntimeReconciler); ok {
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
	limit := int(req.Msg.GetLimit())
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("limit must be between 1 and 500"))
	}
	cursor, err := decodeSandboxCursor(req.Msg.GetCursor())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	store, ok := h.store.(SandboxListStore)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sandbox list store is required"))
	}
	result, err := store.ListSandboxes(ctx, domain.SandboxListOptions{Limit: limit, BeforeUpdatedAt: cursor.UpdatedAt, BeforeID: cursor.SandboxID})
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	response := &agentcomposev2.ListSandboxesResponse{Sandboxes: make([]*agentcomposev2.Sandbox, 0, len(result.Sandboxes))}
	targets := h.resolveSandboxTargets(ctx, result.Sandboxes)
	for _, sandbox := range result.Sandboxes {
		response.Sandboxes = append(response.Sandboxes, sandboxToV2WithTarget(sandbox, targets[sandbox.Summary.ID]))
	}
	if result.HasMore {
		last := result.Sandboxes[len(result.Sandboxes)-1]
		response.NextCursor = encodeSandboxCursor(last.Summary.UpdatedAt, last.Summary.ID)
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
	response := &agentcomposev2.ListSandboxHistoryResponse{LegacyHistory: true}
	for _, cell := range cells {
		response.Cells = append(response.Cells, &agentcomposev2.SandboxHistoryCell{
			Id: cell.ID, Type: cell.Type, Source: cell.Source, Stdout: cell.Stdout, Stderr: cell.Stderr,
			Output: cell.Output, ExitCode: int32(cell.ExitCode), Success: cell.Success, Running: cell.Running,
			CreatedAt: sandboxHistoryTimestamp(cell.CreatedAt), Agent: cell.Agent, AgentThreadId: cell.AgentThreadID, StopReason: cell.StopReason,
		})
	}
	for _, event := range events {
		response.Events = append(response.Events, &agentcomposev2.SandboxHistoryEvent{
			Id: event.ID, Type: event.Type, Level: event.Level, Message: event.Message, CreatedAt: sandboxHistoryTimestamp(event.CreatedAt),
		})
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

func sandboxWatchEventToV2(event sessions.WatchEvent) *agentcomposev2.WatchSandboxResponse {
	response := &agentcomposev2.WatchSandboxResponse{CellId: event.CellID, Chunk: event.Chunk, Stream: StdioStreamToProto(event.Stream)}
	switch event.EventType {
	case sessions.WatchEventTypeSandboxUpdated:
		response.EventType = agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_SANDBOX_UPDATED
		if event.Sandbox != nil {
			response.Sandbox = sandboxToV2(&domain.Sandbox{Summary: *event.Sandbox})
		}
	case sessions.WatchEventTypeCellStarted:
		response.EventType = agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_CELL_STARTED
		response.Cell = sandboxHistoryCellToV2(event.Cell)
	case sessions.WatchEventTypeCellOutput:
		response.EventType = agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_CELL_OUTPUT
	case sessions.WatchEventTypeCellCompleted:
		response.EventType = agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_CELL_COMPLETED
		response.Cell = sandboxHistoryCellToV2(event.Cell)
	case sessions.WatchEventTypeEventAdded:
		response.EventType = agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_EVENT_ADDED
		response.Event = sandboxHistoryEventToV2(event.Event)
	}
	return response
}

func (h *SandboxHandler) sandboxWatchEventToV2(ctx context.Context, event sessions.WatchEvent) *agentcomposev2.WatchSandboxResponse {
	response := sandboxWatchEventToV2(event)
	if event.EventType == sessions.WatchEventTypeSandboxUpdated && event.Sandbox != nil {
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

func sandboxHistoryCellToV2(cell *domain.NotebookCell) *agentcomposev2.SandboxHistoryCell {
	if cell == nil {
		return nil
	}
	return &agentcomposev2.SandboxHistoryCell{
		Id: cell.ID, Type: cell.Type, Source: cell.Source, Stdout: cell.Stdout, Stderr: cell.Stderr,
		Output: cell.Output, ExitCode: int32(cell.ExitCode), Success: cell.Success, Running: cell.Running,
		CreatedAt: sandboxHistoryTimestamp(cell.CreatedAt), Agent: cell.Agent, AgentThreadId: cell.AgentThreadID, StopReason: cell.StopReason,
	}
}

func sandboxHistoryEventToV2(event *domain.SandboxEvent) *agentcomposev2.SandboxHistoryEvent {
	if event == nil {
		return nil
	}
	return &agentcomposev2.SandboxHistoryEvent{Id: event.ID, Type: event.Type, Level: event.Level, Message: event.Message, CreatedAt: sandboxHistoryTimestamp(event.CreatedAt)}
}

func (h *SandboxHandler) StopSandbox(ctx context.Context, req *connect.Request[agentcomposev2.StopSandboxRequest]) (*connect.Response[agentcomposev2.StopSandboxResponse], error) {
	sandbox, err := h.loadSandbox(ctx, req.Msg.GetSandboxId())
	if err != nil {
		return nil, err
	}
	if sandbox.Summary.VMStatus == domain.VMStatusStopped {
		return connect.NewResponse(&agentcomposev2.StopSandboxResponse{Sandbox: h.sandboxToV2(ctx, sandbox)}), nil
	}
	if sandbox.Summary.VMStatus != domain.VMStatusRunning {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sandbox %s cannot be stopped from state %s", sandbox.Summary.ID, sandbox.Summary.VMStatus))
	}
	stopped, err := h.delegate.StopSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev2.StopSandboxResponse{Sandbox: h.sandboxToV2(ctx, stopped)}), nil
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
		SandboxId:     sandbox.Summary.ID,
		Status:        sandbox.Summary.VMStatus,
		Driver:        sandbox.Summary.Driver,
		CreatedAt:     timestamppb.New(sandbox.Summary.CreatedAt),
		UpdatedAt:     timestamppb.New(sandbox.Summary.UpdatedAt),
		Image:         sandbox.Summary.GuestImage,
		WorkspacePath: sandbox.Summary.WorkspacePath,
		Title:         sandbox.Summary.Title,
		ProxyPath:     sandbox.Summary.ProxyPath,
		TriggerSource: sandbox.Summary.TriggerSource,
		CellCount:     uint32(sandbox.Summary.CellCount),
		EventCount:    uint32(sandbox.Summary.EventCount),
		ProjectId:     target.ProjectID,
		AgentName:     target.AgentName,
		Network:       SandboxNetworkStateToProto(sandbox.NetworkState),
	}
	for _, tag := range sandbox.Summary.Tags {
		result.Tags = append(result.Tags, &agentcomposev2.SandboxTag{Name: tag.Name, Value: tag.Value})
	}
	return result
}

func SandboxNetworkStateToProto(state *domain.SandboxNetworkState) *agentcomposev2.SandboxNetworkState {
	if state == nil {
		return nil
	}
	result := &agentcomposev2.SandboxNetworkState{}
	for _, attachment := range state.Attachments {
		result.Attachments = append(result.Attachments, &agentcomposev2.SandboxNetworkEndpoint{
			Name:               attachment.Name,
			RuntimeNetworkName: attachment.RuntimeNetworkName,
		})
	}
	for _, binding := range state.Bindings {
		result.Bindings = append(result.Bindings, &agentcomposev2.SandboxPortBinding{
			Networks:   append([]string(nil), binding.Networks...),
			HostIp:     binding.HostIP,
			HostPort:   uint32(binding.HostPort),
			GuestPort:  uint32(binding.GuestPort),
			Protocol:   binding.Protocol,
			Visibility: binding.Visibility,
			Publisher:  binding.Publisher,
		})
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

type sandboxPageCursor struct {
	UpdatedAt time.Time `json:"updated_at"`
	SandboxID string    `json:"sandbox_id"`
}

func encodeSandboxCursor(updatedAt time.Time, sandboxID string) string {
	data, _ := json.Marshal(sandboxPageCursor{UpdatedAt: updatedAt.UTC(), SandboxID: sandboxID})
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeSandboxCursor(token string) (sandboxPageCursor, error) {
	if strings.TrimSpace(token) == "" {
		return sandboxPageCursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return sandboxPageCursor{}, fmt.Errorf("invalid cursor")
	}
	var cursor sandboxPageCursor
	if json.Unmarshal(decoded, &cursor) != nil || cursor.UpdatedAt.IsZero() || strings.TrimSpace(cursor.SandboxID) == "" {
		return sandboxPageCursor{}, fmt.Errorf("invalid cursor")
	}
	return cursor, nil
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

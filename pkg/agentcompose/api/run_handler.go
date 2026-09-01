package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type RunAgentDelegate interface {
	RunAgent(context.Context, *connect.Request[agentcomposev2.RunAgentRequest]) (*connect.Response[agentcomposev2.RunAgentResponse], error)
	StartAgentRun(context.Context, *connect.Request[agentcomposev2.StartAgentRunRequest]) (*connect.Response[agentcomposev2.StartAgentRunResponse], error)
	StreamAgentRun(context.Context, *connect.Request[agentcomposev2.RunAgentRequest], *connect.ServerStream[agentcomposev2.StreamAgentRunResponse]) error
	AttachAgentRun(context.Context, *connect.BidiStream[agentcomposev2.AttachAgentRunRequest, agentcomposev2.AttachAgentRunResponse]) error
}

type ActiveRunStopper interface {
	StopActiveRun(context.Context, string, string) (bool, error)
}

type RunStore interface {
	runs.Store
	ListProjectRunsByOptions(context.Context, domain.ProjectRunListOptions) ([]domain.ProjectRunRecord, error)
	ListProjectRunsForSandbox(context.Context, string) ([]domain.ProjectRunRecord, error)
}

type projectRunCountStore interface {
	CountProjectRuns(context.Context, domain.ProjectRunListOptions) (int, error)
}

type RunEventStore interface {
	HasProjectRunEvents(context.Context, string) (bool, error)
	ListProjectRunEventRunIDsForSandbox(context.Context, string) ([]string, error)
	ListProjectRunEvents(context.Context, string, uint64, int) ([]domain.ProjectRunEventRecord, error)
	ListProjectRunEventsForSandbox(context.Context, string, domain.ProjectRunEventSandboxCursor) ([]domain.ProjectRunEventRecord, error)
	ListProjectRunEventsPage(context.Context, string, int, int) ([]domain.ProjectRunEventRecord, error)
	ListProjectRunEventsForSandboxPage(context.Context, string, int, int) ([]domain.ProjectRunEventRecord, error)
	CountProjectRunEvents(context.Context, string) (int, error)
	CountProjectRunEventsForSandbox(context.Context, string) (int, error)
}

type RunHandler struct {
	agentcomposev2connect.UnimplementedRunServiceHandler
	delegate RunAgentDelegate
	stopper  ActiveRunStopper
	store    RunStore
	runLogs  *runs.RunLogHub
}

func NewRunHandler(delegate RunAgentDelegate, store RunStore, stoppers ...ActiveRunStopper) *RunHandler {
	handler := &RunHandler{delegate: delegate, store: store}
	if len(stoppers) > 0 {
		handler.stopper = stoppers[0]
	}
	return handler
}

func NewRunHandlerWithRunLogHub(delegate RunAgentDelegate, store RunStore, hub *runs.RunLogHub, stoppers ...ActiveRunStopper) *RunHandler {
	handler := NewRunHandler(delegate, store, stoppers...)
	handler.runLogs = hub
	return handler
}

func (h *RunHandler) RunAgent(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest]) (*connect.Response[agentcomposev2.RunAgentResponse], error) {
	return h.delegate.RunAgent(ctx, req)
}

func (h *RunHandler) StartAgentRun(ctx context.Context, req *connect.Request[agentcomposev2.StartAgentRunRequest]) (*connect.Response[agentcomposev2.StartAgentRunResponse], error) {
	if h.delegate == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("start run is not configured"))
	}
	return h.delegate.StartAgentRun(ctx, req)
}

func (h *RunHandler) StreamAgentRun(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.StreamAgentRunResponse]) error {
	PrepareStreamingHeaders(stream.ResponseHeader())
	return h.delegate.StreamAgentRun(ctx, req, stream)
}

func (h *RunHandler) AttachAgentRun(ctx context.Context, stream *connect.BidiStream[agentcomposev2.AttachAgentRunRequest, agentcomposev2.AttachAgentRunResponse]) error {
	if h.delegate == nil {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("run attach is not configured"))
	}
	if err := h.delegate.AttachAgentRun(ctx, stream); err != nil {
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			return connectErr
		}
		return ConnectErrorForDomain(err)
	}
	return nil
}

func (h *RunHandler) ListRunEvents(ctx context.Context, req *connect.Request[agentcomposev2.ListRunEventsRequest]) (*connect.Response[agentcomposev2.ListRunEventsResponse], error) {
	runID := strings.TrimSpace(req.Msg.GetRunId())
	if runID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("run id is required"))
	}
	_, err := h.store.GetProjectRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, domain.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	offset, limit, err := listPagination(req.Msg.GetOffset(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}
	store, ok := h.store.(RunEventStore)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("run event store is required"))
	}
	events, err := store.ListProjectRunEventsPage(ctx, runID, offset, limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	historyAvailable, err := store.HasProjectRunEvents(ctx, runID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	total, err := store.CountProjectRunEvents(ctx, runID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	response := &agentcomposev2.ListRunEventsResponse{HistoryAvailable: historyAvailable, Total: uint32(total)}
	for _, event := range events {
		response.Events = append(response.Events, runEventToProto(event))
	}
	return connect.NewResponse(response), nil
}

func (h *RunHandler) ListSandboxRunEvents(ctx context.Context, req *connect.Request[agentcomposev2.ListSandboxRunEventsRequest]) (*connect.Response[agentcomposev2.ListSandboxRunEventsResponse], error) {
	sandboxID := strings.TrimSpace(req.Msg.GetSandboxId())
	if err := validateSandboxID(sandboxID); err != nil {
		return nil, err
	}
	offset, limit, err := listPagination(req.Msg.GetOffset(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}
	store, ok := h.store.(RunEventStore)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("run event store is required"))
	}
	events, err := store.ListProjectRunEventsForSandboxPage(ctx, sandboxID, offset, limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	historyAvailableRunIDs, err := store.ListProjectRunEventRunIDsForSandbox(ctx, sandboxID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	total, err := store.CountProjectRunEventsForSandbox(ctx, sandboxID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	response := &agentcomposev2.ListSandboxRunEventsResponse{HistoryAvailableRunIds: historyAvailableRunIDs, Total: uint32(total)}
	for _, event := range events {
		response.Events = append(response.Events, runEventToProto(event))
	}
	return connect.NewResponse(response), nil
}

func runEventToProto(event domain.ProjectRunEventRecord) *agentcomposev2.RunEvent {
	kind := agentcomposev2.RunEventKind_RUN_EVENT_KIND_UNSPECIFIED
	switch event.Kind {
	case domain.ProjectRunEventKindUserMessage:
		kind = agentcomposev2.RunEventKind_RUN_EVENT_KIND_USER_MESSAGE
	case domain.ProjectRunEventKindAgentMessage:
		kind = agentcomposev2.RunEventKind_RUN_EVENT_KIND_AGENT_MESSAGE
	case domain.ProjectRunEventKindAgentActivity:
		kind = agentcomposev2.RunEventKind_RUN_EVENT_KIND_AGENT_ACTIVITY
	case domain.ProjectRunEventKindStatus:
		kind = agentcomposev2.RunEventKind_RUN_EVENT_KIND_STATUS
	}
	return &agentcomposev2.RunEvent{Id: event.ID, RunId: event.RunID, Seq: event.Sequence, Kind: kind, Text: event.Text, Agent: event.Agent, Name: event.Name, PayloadJson: event.PayloadJSON, Success: event.Success, ExitCode: int32(event.ExitCode), StopReason: event.StopReason, CreatedAt: timestamppb.New(event.CreatedAt)}
}

func (h *RunHandler) GetRun(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
	if h.store == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	runID := strings.TrimSpace(req.Msg.GetRunId())
	if runID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("run id is required"))
	}
	run, err := h.store.GetProjectRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if projectID := strings.TrimSpace(req.Msg.GetProjectId()); projectID != "" && run.ProjectID != projectID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project run %s not found in project %s", runID, projectID))
	}
	return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: ProjectRunDetailToProto(run)}), nil
}

func (h *RunHandler) ListRuns(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
	if h.store == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	offset, limit, err := listPagination(req.Msg.GetOffset(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}
	startedFrom, startedTo, err := listRunsStartedRange(req.Msg.GetStartedFrom(), req.Msg.GetStartedTo())
	if err != nil {
		return nil, err
	}
	options := domain.ProjectRunListOptions{
		ProjectID:      req.Msg.GetProjectId(),
		AgentName:      req.Msg.GetAgentName(),
		SandboxID:      req.Msg.GetSandboxId(),
		SchedulerID:    req.Msg.GetSchedulerId(),
		SchedulerRunID: req.Msg.GetSchedulerRunId(),
		Status:         ProjectRunStatusFromProto(req.Msg.GetStatus()),
		Source:         ProjectRunSourceFilterFromProto(req.Msg.GetSource()),
		StartedFrom:    startedFrom,
		StartedTo:      startedTo,
		Offset:         offset,
		Limit:          limit,
		Labels:         req.Msg.GetLabels(),
	}
	runs, err := h.store.ListProjectRunsByOptions(ctx, options)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	items := make([]*agentcomposev2.RunSummary, 0, len(runs))
	for _, run := range runs {
		items = append(items, ProjectRunSummaryToProto(run))
	}
	total := offset + len(runs)
	if countStore, ok := h.store.(projectRunCountStore); ok {
		total, err = countStore.CountProjectRuns(ctx, options)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: items, Total: uint32(total)}), nil
}

func (h *RunHandler) FollowRunLogs(ctx context.Context, req *connect.Request[agentcomposev2.FollowRunLogsRequest], stream *connect.ServerStream[agentcomposev2.RunLogChunk]) error {
	if h.store == nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	runID := strings.TrimSpace(req.Msg.GetRunId())
	if runID == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("run id is required"))
	}
	PrepareStreamingHeaders(stream.ResponseHeader())
	run, err := h.projectRunForLogRequest(ctx, req.Msg.GetProjectId(), runID)
	if err != nil {
		return err
	}
	offset, err := initialRunLogOffset(run.LogsPath, int(req.Msg.GetTailLines()), req.Msg.GetStartOffset(), req.Msg.GetTailSet() || req.Msg.GetTailLines() > 0)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if req.Msg.GetIncludeMetadata() {
		if err := stream.Send(&agentcomposev2.RunLogChunk{
			Offset: offset, RunStatus: ProjectRunStatusToProto(run.Status), CreatedAt: FormatProjectTime(time.Now().UTC()),
			Run: ProjectRunSummaryToProto(run), Prompt: run.Prompt,
		}); err != nil {
			return err
		}
	}
	target := runLogFollowTarget{Run: run, Offset: offset, Stream: stream}
	if req.Msg.GetFollow() && h.runLogs != nil {
		return h.followRunLogsWithHub(ctx, req.Msg.GetProjectId(), target)
	}
	return h.followRunLogsByPolling(ctx, req, target)
}

// runLogFollowTarget bundles the run, log offset, and output stream shared by
// followRunLogsByPolling and followRunLogsWithHub.
type runLogFollowTarget struct {
	Run    domain.ProjectRunRecord
	Offset uint64
	Stream *connect.ServerStream[agentcomposev2.RunLogChunk]
}

func (h *RunHandler) followRunLogsByPolling(ctx context.Context, req *connect.Request[agentcomposev2.FollowRunLogsRequest], target runLogFollowTarget) error {
	run, offset, stream := target.Run, target.Offset, target.Stream
	runID := run.RunID
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		var err error
		run, err = h.projectRunForLogRequest(ctx, req.Msg.GetProjectId(), runID)
		if err != nil {
			return err
		}
		if err := sendRunLogFileChunks(stream, run, &offset, time.Now().UTC()); err != nil {
			return err
		}
		if !req.Msg.GetFollow() || runs.StatusIsTerminal(run.Status) {
			return sendRunLogFinal(stream, run, offset)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (h *RunHandler) followRunLogsWithHub(ctx context.Context, projectID string, target runLogFollowTarget) error {
	run, offset, stream := target.Run, target.Offset, target.Stream
	sub := h.runLogs.Subscribe(run.RunID)
	if sub == nil {
		return h.followRunLogsByPolling(ctx, connect.NewRequest(&agentcomposev2.FollowRunLogsRequest{ProjectId: projectID, RunId: run.RunID, Follow: true}), target)
	}
	defer sub.Close()
	if err := sendRunLogFileChunks(stream, run, &offset, time.Now().UTC()); err != nil {
		return err
	}
	if runs.StatusIsTerminal(run.Status) {
		return sendRunLogFinal(stream, run, offset)
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-sub.C():
			if !ok {
				return nil
			}
			if event.Offset <= offset {
				continue
			}
			current, err := h.projectRunForLogRequest(ctx, projectID, run.RunID)
			if err != nil {
				return err
			}
			if err := sendRunLogFileChunks(stream, current, &offset, event.CreatedAt); err != nil {
				return err
			}
		case <-ticker.C:
			current, err := h.projectRunForLogRequest(ctx, projectID, run.RunID)
			if err != nil {
				return err
			}
			if err := sendRunLogFileChunks(stream, current, &offset, time.Now().UTC()); err != nil {
				return err
			}
			if runs.StatusIsTerminal(current.Status) {
				return sendRunLogFinal(stream, current, offset)
			}
		}
	}
}

func sendRunLogFileChunks(stream *connect.ServerStream[agentcomposev2.RunLogChunk], run domain.ProjectRunRecord, offset *uint64, createdAt time.Time) error {
	for {
		data, nextOffset, atEnd, err := readRunLogChunkFromOffset(run.LogsPath, *offset)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return connect.NewError(connect.CodeInternal, err)
		}
		*offset = nextOffset
		if data != "" {
			if createdAt.IsZero() {
				createdAt = time.Now().UTC()
			}
			if err := stream.Send(&agentcomposev2.RunLogChunk{
				Data:      data,
				Offset:    *offset,
				RunStatus: ProjectRunStatusToProto(run.Status),
				CreatedAt: FormatProjectTime(createdAt),
			}); err != nil {
				return connect.NewError(connect.CodeUnknown, err)
			}
		}
		if atEnd {
			return nil
		}
	}
}

func sendRunLogFinal(stream *connect.ServerStream[agentcomposev2.RunLogChunk], run domain.ProjectRunRecord, offset uint64) error {
	return stream.Send(&agentcomposev2.RunLogChunk{
		Offset:    offset,
		IsFinal:   true,
		RunStatus: ProjectRunStatusToProto(run.Status),
		CreatedAt: FormatProjectTime(time.Now().UTC()),
		Run:       ProjectRunSummaryToProto(run),
	})
}

func (h *RunHandler) projectRunForLogRequest(ctx context.Context, projectID, runID string) (domain.ProjectRunRecord, error) {
	run, err := h.store.GetProjectRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ProjectRunRecord{}, connect.NewError(connect.CodeNotFound, err)
		}
		return domain.ProjectRunRecord{}, connect.NewError(connect.CodeInternal, err)
	}
	if projectID := strings.TrimSpace(projectID); projectID != "" && run.ProjectID != projectID {
		return domain.ProjectRunRecord{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("project run %s not found in project %s", runID, projectID))
	}
	return run, nil
}

func initialRunLogOffset(path string, tailLines int, startOffset uint64, tailSet bool) (uint64, error) {
	if tailSet {
		return tailRunLogOffset(path, tailLines)
	}
	if startOffset > 0 {
		return startOffset, nil
	}
	return 0, nil
}

const runLogFileChunkBytes = 64 * 1024

func readRunLogChunkFromOffset(path string, offset uint64) (string, uint64, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", offset, false, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return "", offset, false, err
	}
	if offset > uint64(info.Size()) {
		offset = 0
	}
	if _, err := file.Seek(int64(offset), io.SeekStart); err != nil {
		return "", offset, false, err
	}
	data := make([]byte, runLogFileChunkBytes)
	n, err := io.ReadFull(file, data)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", offset, false, err
	}
	nextOffset := offset + uint64(n)
	atSnapshotEnd := nextOffset >= uint64(info.Size())
	if !utf8.Valid(data[:n]) {
		prefixLength, incomplete := incompleteUTF8SuffixStart(data[:n])
		if !incomplete {
			return "", offset, false, fmt.Errorf("run log contains invalid UTF-8 at or after byte offset %d", offset)
		}
		n = prefixLength
		nextOffset = offset + uint64(n)
		if atSnapshotEnd {
			return string(data[:n]), nextOffset, true, nil
		}
	}
	return string(data[:n]), nextOffset, nextOffset >= uint64(info.Size()), nil
}

func incompleteUTF8SuffixStart(data []byte) (int, bool) {
	if len(data) == 0 || utf8.Valid(data) {
		return len(data), false
	}
	start := len(data) - 1
	for start > 0 && !utf8.RuneStart(data[start]) {
		start--
	}
	if !utf8.RuneStart(data[start]) || utf8.FullRune(data[start:]) || !utf8.Valid(data[:start]) {
		return 0, false
	}
	return start, true
}

func tailRunLogOffset(path string, lines int) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	if lines <= 0 || size == 0 {
		return uint64(size), nil
	}
	seen := 0
	buffer := make([]byte, runLogFileChunkBytes)
	for end := size; end > 0; {
		start := max(int64(0), end-int64(len(buffer)))
		chunk := buffer[:end-start]
		if _, err := file.ReadAt(chunk, start); err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		for index := len(chunk) - 1; index >= 0; index-- {
			position := start + int64(index)
			if chunk[index] != '\n' || position == size-1 {
				continue
			}
			seen++
			if seen == lines {
				return uint64(position + 1), nil
			}
		}
		end = start
	}
	return 0, nil
}

func (h *RunHandler) StopRun(ctx context.Context, req *connect.Request[agentcomposev2.StopRunRequest]) (*connect.Response[agentcomposev2.StopRunResponse], error) {
	if h.store == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	runID := strings.TrimSpace(req.Msg.GetRunId())
	if runID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("run id is required"))
	}
	if h.stopper != nil {
		stopped, err := h.stopper.StopActiveRun(ctx, runID, req.Msg.GetReason())
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if stopped {
			run, err := h.store.GetProjectRun(ctx, runID)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, connect.NewError(connect.CodeNotFound, err)
				}
				return nil, connect.NewError(connect.CodeInternal, err)
			}
			return connect.NewResponse(&agentcomposev2.StopRunResponse{
				Run:           ProjectRunDetailToProto(run),
				StopRequested: true,
			}), nil
		}
	}
	current, err := h.store.GetProjectRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if runs.StatusIsTerminal(current.Status) {
		return connect.NewResponse(&agentcomposev2.StopRunResponse{
			Run:           ProjectRunDetailToProto(current),
			StopRequested: false,
		}), nil
	}
	return connect.NewResponse(&agentcomposev2.StopRunResponse{
		Run:           ProjectRunDetailToProto(current),
		StopRequested: false,
	}), nil
}

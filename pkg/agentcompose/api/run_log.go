package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
	"github.com/chaitin/agent-compose/pkg/runs"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// RunMessageStore pages the conversation messages of runs.
type RunMessageStore interface {
	ListProjectRunMessagesPage(ctx context.Context, runID, sandboxID string, offset, limit int) ([]domain.ProjectRunEventRecord, error)
	CountProjectRunMessages(ctx context.Context, runID, sandboxID string) (int, error)
}

func (h *RunHandler) ListRunMessages(ctx context.Context, req *connect.Request[agentcomposev2.ListRunMessagesRequest]) (*connect.Response[agentcomposev2.ListRunMessagesResponse], error) {
	runID := strings.TrimSpace(req.Msg.GetRunId())
	sandboxID := strings.TrimSpace(req.Msg.GetSandboxId())
	switch {
	case runID == "" && sandboxID == "":
		return nil, ErrorForCode(connect.CodeInvalidArgument, ResourceRun, fmt.Errorf("one of run_id and sandbox_id is required"))
	case runID != "" && sandboxID != "":
		return nil, ErrorForCode(connect.CodeInvalidArgument, ResourceRun, fmt.Errorf("run_id and sandbox_id are mutually exclusive"))
	}
	if runID != "" {
		if _, err := h.store.GetProjectRun(ctx, runID); err != nil {
			if errors.Is(err, sql.ErrNoRows) || errors.Is(err, domain.ErrNotFound) {
				return nil, ErrorForCode(connect.CodeNotFound, ResourceRun, err)
			}
			return nil, ConnectErrorForDomainKind(err, ResourceRun)
		}
	} else if err := validateSandboxID(sandboxID); err != nil {
		return nil, err
	}
	offset, limit, err := listPagination(req.Msg.GetOffset(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}
	store, ok := h.store.(RunMessageStore)
	if !ok {
		return nil, ErrorForCode(connect.CodeInternal, ResourceRun, fmt.Errorf("run message store is required"))
	}
	messages, err := store.ListProjectRunMessagesPage(ctx, runID, sandboxID, offset, limit)
	if err != nil {
		return nil, ConnectErrorForDomainKind(err, ResourceRun)
	}
	total, err := store.CountProjectRunMessages(ctx, runID, sandboxID)
	if err != nil {
		return nil, ConnectErrorForDomainKind(err, ResourceRun)
	}
	response := &agentcomposev2.ListRunMessagesResponse{Total: uint32(total)}
	for _, message := range messages {
		response.Messages = append(response.Messages, runMessageToProto(message))
	}
	return connect.NewResponse(response), nil
}

func runMessageToProto(event domain.ProjectRunEventRecord) *agentcomposev2.RunMessage {
	role := agentcomposev2.RunMessageRole_RUN_MESSAGE_ROLE_UNSPECIFIED
	switch event.Kind {
	case domain.ProjectRunEventKindUserMessage:
		role = agentcomposev2.RunMessageRole_RUN_MESSAGE_ROLE_USER
	case domain.ProjectRunEventKindAgentMessage:
		role = agentcomposev2.RunMessageRole_RUN_MESSAGE_ROLE_AGENT
	}
	return &agentcomposev2.RunMessage{
		Id: event.ID, RunId: event.RunID, Role: role, Text: event.Text, Agent: event.Agent,
		LogSeq: event.LogSeq, StopReason: event.StopReason, CreatedAt: timestamppb.New(event.CreatedAt),
	}
}

func runLogSummaryToProto(summaryJSON string) *agentcomposev2.RunLogSummary {
	if strings.TrimSpace(summaryJSON) == "" {
		return nil
	}
	var summary runlog.Summary
	if json.Unmarshal([]byte(summaryJSON), &summary) != nil {
		return nil
	}
	return &agentcomposev2.RunLogSummary{
		Turns: uint32(summary.Turns), ToolCalls: uint32(summary.ToolCalls), ToolFailures: uint32(summary.ToolFailures),
		InputTokens: summary.InputTokens, OutputTokens: summary.OutputTokens, ReasoningTokens: summary.ReasoningTokens,
		CachedTokens: summary.CachedTokens, CostUsd: summary.CostUSD, LastError: summary.LastError, Truncated: summary.Truncated,
	}
}

// followRunLogEntries serves a structured FollowRunLogs: the run's log
// entries after start_seq, then, with follow, new entries until the run ends.
// A run that predates run logs gets one chunk saying so.
func (h *RunHandler) followRunLogEntries(ctx context.Context, req *connect.Request[agentcomposev2.FollowRunLogsRequest], run domain.ProjectRunRecord, stream *connect.ServerStream[agentcomposev2.RunLogChunk]) error {
	if run.LogFormat != runlog.FormatJSONL {
		return stream.Send(&agentcomposev2.RunLogChunk{
			IsFinal: true, LegacyFormat: true, RunStatus: ProjectRunStatusToProto(run.Status),
			CreatedAt: FormatProjectTime(time.Now().UTC()), Run: ProjectRunSummaryToProto(run),
		})
	}
	seq := req.Msg.GetStartSeq()
	var sub *runs.RunLogSubscription
	if req.Msg.GetFollow() && h.runLogs != nil {
		sub = h.runLogs.Subscribe(run.RunID)
		if sub != nil {
			defer sub.Close()
		}
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := sendRunLogEntries(stream, run, &seq); err != nil {
			return err
		}
		if !req.Msg.GetFollow() || runs.StatusIsTerminal(run.Status) {
			return stream.Send(&agentcomposev2.RunLogChunk{
				IsFinal: true, RunStatus: ProjectRunStatusToProto(run.Status),
				CreatedAt: FormatProjectTime(time.Now().UTC()), Run: ProjectRunSummaryToProto(run),
			})
		}
		if err := waitForRunLog(ctx, sub, ticker, seq); err != nil {
			return err
		}
		current, err := h.projectRunForLogRequest(ctx, req.Msg.GetProjectId(), run.RunID)
		if err != nil {
			return err
		}
		run = current
	}
}

// waitForRunLog returns when an entry after seq is published or the poll
// interval passes; polling also notices the run reaching a terminal state.
func waitForRunLog(ctx context.Context, sub *runs.RunLogSubscription, ticker *time.Ticker, seq uint64) error {
	var events <-chan runs.RunLogEvent
	if sub != nil {
		events = sub.C()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			return nil
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event.Seq > seq {
				return nil
			}
		}
	}
}

// runLogEntriesPerChunk bounds one structured chunk; a run log holds at most
// a few thousand entries, so a reader catches up in a handful of frames.
const runLogEntriesPerChunk = 200

func sendRunLogEntries(stream *connect.ServerStream[agentcomposev2.RunLogChunk], run domain.ProjectRunRecord, seq *uint64) error {
	entries, err := runlog.ReadEntries(run.LogsPath, *seq)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return connect.NewError(connect.CodeInternal, err)
	}
	for len(entries) > 0 {
		n := min(len(entries), runLogEntriesPerChunk)
		chunk := &agentcomposev2.RunLogChunk{RunStatus: ProjectRunStatusToProto(run.Status), CreatedAt: FormatProjectTime(time.Now().UTC())}
		for _, entry := range entries[:n] {
			chunk.Entries = append(chunk.Entries, RunLogEntryToProto(entry))
		}
		if err := stream.Send(chunk); err != nil {
			return err
		}
		*seq = entries[n-1].Seq
		entries = entries[n:]
	}
	return nil
}

// RunLogEntryToProto maps one run log entry onto the wire.
func RunLogEntryToProto(entry runlog.Entry) *agentcomposev2.RunLogEntry {
	out := &agentcomposev2.RunLogEntry{Seq: entry.Seq, CreatedAt: timestamppb.New(entry.Time), Turn: entry.Turn, TextOffset: entry.TextOffset}
	switch entry.Type {
	case runlog.EntryUser:
		out.Entry = &agentcomposev2.RunLogEntry_UserInput{UserInput: &agentcomposev2.RunLogUserInput{Text: entry.Text, Initial: entry.Initial}}
	case runlog.EntryText:
		out.Entry = &agentcomposev2.RunLogEntry_Text{Text: &agentcomposev2.RunLogText{Text: entry.Text}}
	case runlog.EntryToolCall:
		if tool := entry.Tool; tool != nil {
			out.Entry = &agentcomposev2.RunLogEntry_ToolCall{ToolCall: &agentcomposev2.RunLogToolCall{
				Id: tool.ID, ParentId: tool.ParentID, Name: tool.Name, Kind: tool.Kind, Command: tool.Command, InputJson: string(tool.Input),
			}}
		}
	case runlog.EntryToolResult:
		if tool := entry.Tool; tool != nil {
			result := &agentcomposev2.RunLogToolResult{
				Id: tool.ID, ParentId: tool.ParentID, Name: tool.Name, Ok: tool.OK,
				Output: tool.Output, OutputBytes: uint64(tool.OutputBytes), Error: tool.Error,
			}
			if tool.ExitCode != nil {
				code := int32(*tool.ExitCode)
				result.ExitCode = &code
			}
			out.Entry = &agentcomposev2.RunLogEntry_ToolResult{ToolResult: result}
		}
	case runlog.EntryUsage:
		if usage := entry.Usage; usage != nil {
			out.Entry = &agentcomposev2.RunLogEntry_Usage{Usage: &agentcomposev2.RunLogUsage{
				Scope: usage.Scope, Model: usage.Model, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
				ReasoningTokens: usage.ReasoningTokens, CachedTokens: usage.CachedTokens, CacheWriteTokens: usage.CacheWriteTokens, CostUsd: usage.CostUSD,
			}}
		}
	case runlog.EntryError:
		out.Entry = &agentcomposev2.RunLogEntry_Error{Error: &agentcomposev2.RunLogError{Message: entry.Text, Severity: entry.Severity, Code: entry.Code}}
	case runlog.EntryOutput:
		out.Entry = &agentcomposev2.RunLogEntry_Output{Output: &agentcomposev2.RunLogOutput{Stream: entry.Stream, Data: entry.Data}}
	case runlog.EntryTurnEnd:
		out.Entry = &agentcomposev2.RunLogEntry_TurnEnd{TurnEnd: &agentcomposev2.RunLogTurnEnd{StopReason: entry.StopReason, FinalText: entry.FinalText}}
	case runlog.EntryTruncated:
		out.Entry = &agentcomposev2.RunLogEntry_Truncated{Truncated: &agentcomposev2.RunLogTruncated{DroppedBytes: entry.DroppedBytes}}
	}
	return out
}

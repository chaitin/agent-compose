package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
	"github.com/chaitin/agent-compose/pkg/runs"
)

// SandboxRunHistoryStore lets ListSandboxHistory show the Agent Runs a sandbox
// hosted that keep their history in a run log. Those runs store no notebook
// cell or agent event in the sandbox; their cell and conversation are rebuilt
// here from the run, its messages and its log, so the run log stays the one
// copy of what happened.
type SandboxRunHistoryStore interface {
	ListProjectRunsByOptions(context.Context, domain.ProjectRunListOptions) (domain.ProjectRunListResult, error)
	ListProjectRunMessagesPage(ctx context.Context, runID, sandboxID string, offset, limit int) ([]domain.ProjectRunEventRecord, error)
}

// WithRunHistory makes ListSandboxHistory include run-log Agent Runs.
func (h *SandboxHandler) WithRunHistory(store SandboxRunHistoryStore) *SandboxHandler {
	h.runHistory = store
	return h
}

// sandboxRunOutputBytes bounds the output a rebuilt cell carries. The tail is
// kept, since a cell is read for how the run ended; the whole log is one
// FollowRunLogs call away.
const sandboxRunOutputBytes = 1 << 20

const sandboxRunHistoryPage = 200

// sandboxRunHistory is what the sandbox's run-log runs contribute to its
// history, with each cell's output left to fill in once paging decides which
// cells are returned.
type sandboxRunHistory struct {
	cells  []domain.NotebookCell
	events []domain.SandboxEvent
	logs   map[string]domain.ProjectRunRecord // by cell id
}

func (h *SandboxHandler) loadSandboxRunHistory(ctx context.Context, sandboxID string, stored []domain.NotebookCell) (sandboxRunHistory, error) {
	history := sandboxRunHistory{logs: map[string]domain.ProjectRunRecord{}}
	if h.runHistory == nil {
		return history, nil
	}
	storedCells := make(map[string]struct{}, len(stored))
	for _, cell := range stored {
		storedCells[cell.ID] = struct{}{}
	}
	runsByID := map[string]domain.ProjectRunRecord{}
	for offset := 0; ; offset += sandboxRunHistoryPage {
		page, err := h.runHistory.ListProjectRunsByOptions(ctx, domain.ProjectRunListOptions{SandboxID: sandboxID, Offset: offset, Limit: sandboxRunHistoryPage})
		if err != nil {
			return history, fmt.Errorf("list sandbox runs: %w", err)
		}
		for _, run := range page.Runs {
			cellID := strings.TrimSpace(run.AgentCellID)
			// A run that never started its agent has no cell, live or here;
			// one whose cell is stored is a notebook run already listed.
			if run.LogFormat != runlog.FormatJSONL || cellID == "" {
				continue
			}
			if _, ok := storedCells[cellID]; ok {
				continue
			}
			runsByID[run.RunID] = run
		}
		if len(page.Runs) < sandboxRunHistoryPage {
			break
		}
	}
	if len(runsByID) == 0 {
		return history, nil
	}
	agents := map[string]string{}
	stopReasons := map[string]string{}
	for offset := 0; ; offset += maxListLimit {
		messages, err := h.runHistory.ListProjectRunMessagesPage(ctx, "", sandboxID, offset, maxListLimit)
		if err != nil {
			return history, fmt.Errorf("list sandbox run messages: %w", err)
		}
		for _, message := range messages {
			if _, ok := runsByID[message.RunID]; !ok {
				continue // Notebook runs keep their own sandbox events.
			}
			event := domain.SandboxEvent{ID: message.ID, Type: "agent.user", Level: "info", Message: message.Text, CreatedAt: message.CreatedAt}
			if message.Kind == domain.ProjectRunEventKindAgentMessage {
				event.Type = "agent.assistant"
				agents[message.RunID] = message.Agent
				stopReasons[message.RunID] = message.StopReason
			}
			history.events = append(history.events, event)
		}
		if len(messages) < maxListLimit {
			break
		}
	}
	for _, run := range runsByID {
		cell := sandboxRunCell(run, agents[run.RunID], stopReasons[run.RunID])
		history.cells = append(history.cells, cell)
		history.logs[cell.ID] = run
		if run.Status == domain.ProjectRunStatusFailed && strings.TrimSpace(run.Error) != "" {
			history.events = append(history.events, domain.SandboxEvent{
				ID: run.RunID + ":failed", Type: "agent.assistant.failed", Level: "error",
				Message: run.Error, CreatedAt: firstTime(run.CompletedAt, run.UpdatedAt),
			})
		}
	}
	return history, nil
}

func sandboxRunCell(run domain.ProjectRunRecord, agent, stopReason string) domain.NotebookCell {
	if strings.TrimSpace(agent) == "" {
		agent = run.AgentName
	}
	return domain.NotebookCell{
		ID:         strings.TrimSpace(run.AgentCellID),
		Type:       "agent",
		Source:     run.Prompt,
		ExitCode:   run.ExitCode,
		Success:    run.Status == domain.ProjectRunStatusSucceeded,
		Running:    !runs.StatusIsTerminal(run.Status),
		CreatedAt:  firstTime(run.StartedAt, run.CreatedAt),
		Agent:      agent,
		StopReason: stopReason,
	}
}

func firstTime(times ...time.Time) time.Time {
	for _, t := range times {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// fillSandboxRunOutput renders the output of the rebuilt cells that made it
// onto the page; the rest are never read.
func fillSandboxRunOutput(response *agentcomposev2.ListSandboxHistoryResponse, logs map[string]domain.ProjectRunRecord) {
	for _, cell := range response.GetCells() {
		run, ok := logs[cell.GetId()]
		if !ok {
			continue
		}
		cell.Output = sandboxRunOutput(run.LogsPath)
	}
}

func sandboxRunOutput(path string) string {
	text, err := openRunLogText(path)
	// Neither message names a host path: the cell output reaches API callers.
	if errors.Is(err, runlog.ErrArchived) {
		return "[" + runlog.ErrArchived.Error() + "; ListRunMessages keeps the conversation]\n"
	}
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "[run log unreadable]\n"
		}
		return ""
	}
	defer func() { _ = text.close() }()
	size := text.size()
	start := max(int64(0), size-sandboxRunOutputBytes)
	data := make([]byte, size-start)
	n, err := text.ReadAt(data, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return "[run log unreadable: " + err.Error() + "]\n"
	}
	output := strings.ToValidUTF8(string(data[:n]), "")
	if start > 0 {
		output = fmt.Sprintf("[first %d bytes omitted; FollowRunLogs returns the whole log]\n", start) + output
	}
	return output
}

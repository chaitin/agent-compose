package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

// SandboxStepHistoryStore lets ListSandboxHistory show the shell/exec steps
// scheduler runs ran in a sandbox. A step keeps its output in its step log
// and nothing in the sandbox; its cell is rebuilt here from the step's start
// and end events and the log.
type SandboxStepHistoryStore interface {
	ListSandboxSchedulerSteps(ctx context.Context, sandboxID string) ([]domain.SchedulerEvent, error)
}

type sandboxStepHistory struct {
	dataRoot string
	store    SandboxStepHistoryStore
}

// WithStepHistory makes ListSandboxHistory include scheduler steps, whose
// logs live under dataRoot.
func (h *SandboxHandler) WithStepHistory(dataRoot string, store SandboxStepHistoryStore) *SandboxHandler {
	h.steps = sandboxStepHistory{dataRoot: strings.TrimSpace(dataRoot), store: store}
	return h
}

func (h *SandboxHandler) loadSandboxStepHistory(ctx context.Context, sandboxID string, stored []domain.NotebookCell) (sandboxRunHistory, error) {
	history := sandboxRunHistory{logs: map[string]string{}}
	if h.steps.store == nil || h.steps.dataRoot == "" {
		return history, nil
	}
	events, err := h.steps.store.ListSandboxSchedulerSteps(ctx, sandboxID)
	if err != nil {
		return history, fmt.Errorf("list sandbox scheduler steps: %w", err)
	}
	storedCells := make(map[string]struct{}, len(stored))
	for _, cell := range stored {
		storedCells[cell.ID] = struct{}{}
	}
	steps := map[string]*domain.NotebookCell{}
	var order []string
	for _, event := range events {
		cellID := strings.TrimSpace(event.LinkedCellID)
		// A step from before step logs kept its notebook cell.
		if _, ok := storedCells[cellID]; ok {
			continue
		}
		cell := steps[cellID]
		if cell == nil {
			cell = &domain.NotebookCell{ID: cellID, Type: "shell", CreatedAt: event.CreatedAt, Running: true}
			steps[cellID] = cell
			order = append(order, cellID)
			path, err := runlog.SchedulerCellPath(h.steps.dataRoot, event.SchedulerID, event.RunID, cellID)
			if err != nil {
				return history, err
			}
			history.logs[cellID] = path
		}
		payload := stepEventPayload(event.PayloadJSON)
		switch event.Type {
		case "scheduler.command.started":
			cell.CreatedAt, cell.Source = event.CreatedAt, payload.Source
		default:
			cell.Running, cell.ExitCode, cell.Success = false, payload.ExitCode, payload.Success && event.Type == "scheduler.command.completed"
			if event.Type == "scheduler.command.failed" && cell.ExitCode == 0 {
				cell.ExitCode = 1
			}
		}
	}
	for _, cellID := range order {
		history.cells = append(history.cells, *steps[cellID])
	}
	return history, nil
}

type stepPayload struct {
	Source   string `json:"source"`
	ExitCode int    `json:"exitCode"`
	Success  bool   `json:"success"`
}

func stepEventPayload(raw string) stepPayload {
	var payload stepPayload
	_ = json.Unmarshal([]byte(raw), &payload) // A malformed payload leaves the defaults.
	return payload
}

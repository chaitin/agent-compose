package api

import (
	"sort"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type sandboxHistoryEntry struct {
	cell      *domain.NotebookCell
	event     *domain.SandboxEvent
	createdAt time.Time
	kind      string
	id        string
}

func paginateSandboxHistory(cells []domain.NotebookCell, events []domain.SandboxEvent, offset, limit uint32) (*agentcomposev2.ListSandboxHistoryResponse, error) {
	entries := make([]sandboxHistoryEntry, 0, len(cells)+len(events))
	for index := range cells {
		cell := &cells[index]
		entries = append(entries, sandboxHistoryEntry{cell: cell, createdAt: cell.CreatedAt, kind: "cell", id: strings.TrimSpace(cell.ID)})
	}
	for index := range events {
		event := &events[index]
		entries = append(entries, sandboxHistoryEntry{event: event, createdAt: event.CreatedAt, kind: "event", id: strings.TrimSpace(event.ID)})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].createdAt.Equal(entries[j].createdAt) {
			return entries[i].createdAt.After(entries[j].createdAt)
		}
		if entries[i].kind != entries[j].kind {
			return entries[i].kind < entries[j].kind
		}
		return entries[i].id > entries[j].id
	})
	page, total, err := paginateList(entries, offset, limit)
	if err != nil {
		return nil, err
	}
	response := &agentcomposev2.ListSandboxHistoryResponse{LegacyHistory: true, Total: total}
	for _, entry := range page {
		if entry.cell != nil {
			response.Cells = append(response.Cells, sandboxHistoryCellToV2(entry.cell))
			continue
		}
		response.Events = append(response.Events, sandboxHistoryEventToV2(entry.event))
	}
	return response, nil
}

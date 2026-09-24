package webhooks

import (
	"context"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// EventQueryStore provides the lightweight HTTP event-query projections.
type EventQueryStore interface {
	ListEventSummaries(context.Context, domain.TopicEventFilter) ([]domain.EventSummary, int, error)
	ListEventTopics(context.Context, string, int, int) ([]domain.EventTopicSummary, int, error)
	GetEventTrace(context.Context, string, int) (domain.EventTrace, error)
}

type eventTraceStore interface {
	GetEventTrace(context.Context, string, int) (domain.EventTrace, error)
}

// SandboxSummaryReader batches optional sandbox enrichment for event traces.
type SandboxSummaryReader interface {
	ListSandboxSummaries(context.Context, []string) (map[string]domain.SandboxSummary, error)
}

type eventTraceView struct {
	Trace               domain.EventTrace
	Sandboxes           map[string]domain.SandboxSummary
	SandboxSummaryError error
}

type traceService struct {
	store     eventTraceStore
	sandboxes SandboxSummaryReader
}

func newTraceService(store eventTraceStore, sandboxes SandboxSummaryReader) *traceService {
	return &traceService{store: store, sandboxes: sandboxes}
}

func (s *traceService) trace(ctx context.Context, eventID string) (eventTraceView, error) {
	trace, err := s.store.GetEventTrace(ctx, eventID, domain.MaxEventScopeEvents)
	if err != nil {
		return eventTraceView{}, err
	}
	view := eventTraceView{Trace: trace, Sandboxes: make(map[string]domain.SandboxSummary)}
	if s.sandboxes == nil {
		return view, nil
	}
	summaryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	summaries, summaryErr := s.sandboxes.ListSandboxSummaries(summaryCtx, eventTraceSandboxIDs(trace.SandboxLinks))
	if summaryErr != nil && ctx.Err() != nil {
		return eventTraceView{}, ctx.Err()
	}
	for id, summary := range summaries {
		view.Sandboxes[id] = summary
	}
	view.SandboxSummaryError = summaryErr
	return view, nil
}

func eventTraceSandboxIDs(links []domain.EventSandboxTraceItem) []string {
	seen := make(map[string]struct{}, len(links))
	ids := make([]string, 0, len(links))
	for _, link := range links {
		id := strings.TrimSpace(link.SandboxID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

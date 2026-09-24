package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// eventNotFoundRunStore embeds the shared store stub and reports unknown-event
// errors, so both the list call and the count call traverse the NotFound
// mapping in RunHandler.ListRuns.
type eventNotFoundRunStore struct {
	*apiProjectRunStore
}

func (s *eventNotFoundRunStore) ListProjectRunsByOptions(_ context.Context, options domain.ProjectRunListOptions) (domain.ProjectRunListResult, error) {
	if options.EventID == "" {
		return s.apiProjectRunStore.ListProjectRunsByOptions(context.Background(), options)
	}
	return domain.ProjectRunListResult{}, domain.ResourceError(domain.ErrNotFound, "event", options.EventID, "event "+options.EventID+" not found", nil)
}

func (s *eventNotFoundRunStore) CountProjectRuns(_ context.Context, options domain.ProjectRunListOptions) (int, bool, error) {
	if options.EventID == "" {
		return 0, false, nil
	}
	return 0, false, domain.ResourceError(domain.ErrNotFound, "event", options.EventID, "event "+options.EventID+" not found", nil)
}

func TestListRunsEventNotFoundMapsToCodeNotFound(t *testing.T) {
	handler := NewRunHandler(nil, &eventNotFoundRunStore{apiProjectRunStore: &apiProjectRunStore{}}, &sequenceRunStopper{})
	_, err := handler.ListRuns(context.Background(), connect.NewRequest(&agentcomposev2.ListRunsRequest{
		ProjectId: "project-1",
		EventId:   "evt-missing",
		Limit:     10,
	}))
	if err == nil {
		t.Fatalf("ListRuns with unknown event returned nil error")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("ListRuns unknown event code = %s, want not_found", connect.CodeOf(err))
	}
}

// eventTruncatedRunStore reports a truncated event scope so the handler
// surfaces the flag on the ListRunsResponse.
type eventTruncatedRunStore struct {
	*apiProjectRunStore
}

func (s *eventTruncatedRunStore) ListProjectRunsByOptions(_ context.Context, options domain.ProjectRunListOptions) (domain.ProjectRunListResult, error) {
	if options.EventID == "" {
		return s.apiProjectRunStore.ListProjectRunsByOptions(context.Background(), options)
	}
	return domain.ProjectRunListResult{EventScopeTruncated: true}, nil
}

func (s *eventTruncatedRunStore) CountProjectRuns(_ context.Context, options domain.ProjectRunListOptions) (int, bool, error) {
	if options.EventID == "" {
		return 0, false, nil
	}
	return 0, true, nil
}

func TestListRunsEventScopeTruncatedMappedToResponse(t *testing.T) {
	store := &eventTruncatedRunStore{apiProjectRunStore: &apiProjectRunStore{}}
	handler := NewRunHandler(nil, store, &sequenceRunStopper{})

	resp, err := handler.ListRuns(context.Background(), connect.NewRequest(&agentcomposev2.ListRunsRequest{
		ProjectId: "project-1",
		EventId:   "evt-big",
		Limit:     10,
	}))
	if err != nil {
		t.Fatalf("ListRuns with truncated scope: %v", err)
	}
	if !resp.Msg.GetEventScopeTruncated() {
		t.Fatalf("ListRuns event_scope_truncated = false, want true")
	}

	plain, err := handler.ListRuns(context.Background(), connect.NewRequest(&agentcomposev2.ListRunsRequest{
		ProjectId: "project-1",
		Limit:     10,
	}))
	if err != nil {
		t.Fatalf("ListRuns without event filter: %v", err)
	}
	if plain.Msg.GetEventScopeTruncated() {
		t.Fatalf("ListRuns without event filter set event_scope_truncated")
	}
}

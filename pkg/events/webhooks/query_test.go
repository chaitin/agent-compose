package webhooks

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestTraceServiceKeepsTraceWhenSandboxSummaryEnrichmentFails(t *testing.T) {
	summaryErr := errors.New("sandbox projection unavailable")
	reader := &sandboxSummaryReaderStub{
		summaries: map[string]domain.SandboxSummary{
			"sandbox-1": {ID: "sandbox-1", Title: "available"},
		},
		err: summaryErr,
	}
	service := newTraceService(eventTraceStoreStub{trace: domain.EventTrace{
		Event: domain.EventSummary{ID: "event-1"},
		SandboxLinks: []domain.EventSandboxTraceItem{
			{SandboxID: "sandbox-1"},
			{SandboxID: " sandbox-1 "},
			{SandboxID: "missing"},
			{},
		},
	}}, reader)

	view, err := service.trace(context.Background(), "event-1")
	if err != nil {
		t.Fatalf("trace returned error: %v", err)
	}
	if !errors.Is(view.SandboxSummaryError, summaryErr) || view.Sandboxes["sandbox-1"].Title != "available" {
		t.Fatalf("trace view = %#v", view)
	}
	if !slices.Equal(reader.ids, []string{"sandbox-1", "missing"}) {
		t.Fatalf("sandbox summary ids = %#v", reader.ids)
	}
}

func TestTraceServicePropagatesSandboxSummaryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := newTraceService(eventTraceStoreStub{trace: domain.EventTrace{
		Event:        domain.EventSummary{ID: "event-1"},
		SandboxLinks: []domain.EventSandboxTraceItem{{SandboxID: "sandbox-1"}},
	}}, &sandboxSummaryReaderStub{err: context.Canceled})

	if _, err := service.trace(ctx, "event-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("trace error = %v, want context canceled", err)
	}
}

type eventTraceStoreStub struct {
	trace domain.EventTrace
	err   error
}

func (s eventTraceStoreStub) GetEventTrace(context.Context, string, int) (domain.EventTrace, error) {
	return s.trace, s.err
}

type sandboxSummaryReaderStub struct {
	ids       []string
	summaries map[string]domain.SandboxSummary
	err       error
}

func (s *sandboxSummaryReaderStub) ListSandboxSummaries(_ context.Context, ids []string) (map[string]domain.SandboxSummary, error) {
	s.ids = append([]string(nil), ids...)
	return s.summaries, s.err
}

func TestTraceServiceBoundsOptionalSandboxSummaries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent := t.Context()
		reader := sandboxSummaryReaderFunc(func(ctx context.Context, _ []string) (map[string]domain.SandboxSummary, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("sandbox summaries have no deadline")
			}
			<-ctx.Done()
			return map[string]domain.SandboxSummary{"available": {ID: "available"}}, ctx.Err()
		})
		service := newTraceService(eventTraceStoreStub{trace: domain.EventTrace{
			Event:        domain.EventSummary{ID: "event-with-slow-summary"},
			SandboxLinks: []domain.EventSandboxTraceItem{{SandboxID: "available"}, {SandboxID: "slow"}},
		}}, reader)
		view, err := service.trace(parent, "event-with-slow-summary")
		if err != nil || parent.Err() != nil {
			t.Fatalf("trace error = %v, parent error = %v", err, parent.Err())
		}
		response := eventTraceResponseFor(view)
		if !response.SandboxSummariesIncomplete || len(response.Sandboxes) != 2 || response.Sandboxes[0].Sandbox == nil || response.Sandboxes[1].Sandbox != nil {
			t.Fatalf("partial trace response = %#v", response)
		}
		if response.Event.EventID != "event-with-slow-summary" || !errors.Is(view.SandboxSummaryError, context.DeadlineExceeded) {
			t.Fatalf("trace event/error = %#v/%v", response.Event, view.SandboxSummaryError)
		}
	})
}

type sandboxSummaryReaderFunc func(context.Context, []string) (map[string]domain.SandboxSummary, error)

func (f sandboxSummaryReaderFunc) ListSandboxSummaries(ctx context.Context, ids []string) (map[string]domain.SandboxSummary, error) {
	return f(ctx, ids)
}

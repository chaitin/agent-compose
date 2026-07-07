package loaders

import (
	"context"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

func TestTriggerLoopCollectDueAndDispatch(t *testing.T) {
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)
	store := &triggerLoopStoreFake{}
	loader := domain.Loader{Summary: domain.LoaderSummary{ID: "loader-1", Enabled: true}, Triggers: []domain.LoaderTrigger{{
		ID:         "interval-1",
		Kind:       domain.LoaderTriggerKindInterval,
		Enabled:    true,
		IntervalMs: 1000,
		NextFireAt: now.Add(-time.Second),
	}}}
	cached := map[string]domain.Loader{loader.Summary.ID: loader}
	var replaced map[string]domain.Loader
	runCalled := make(chan string, 1)
	loop := newTriggerLoop(TriggerLoopDependencies{
		RootCtx: context.Background(),
		Store:   store,
		Snapshot: func() map[string]domain.Loader {
			return cached
		},
		ReplaceCached: func(updated map[string]domain.Loader) {
			replaced = updated
			for id, item := range updated {
				cached[id] = item
			}
		},
		RunTimeout: func(time.Duration) time.Duration { return time.Second },
		Run: func(_ context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, _ string, source string, _ RunOptions, _ ...func(context.Context) error) (domain.LoaderRunSummary, error) {
			runCalled <- loader.Summary.ID + "/" + trigger.ID + "/" + source
			return domain.LoaderRunSummary{}, nil
		},
	})

	jobs := loop.CollectDue(now)
	if len(jobs) != 1 || jobs[0].Trigger.ID != "interval-1" || jobs[0].Source != "interval:1000" {
		t.Fatalf("jobs = %#v", jobs)
	}
	if len(replaced) != 1 || len(store.fired) != 1 {
		t.Fatalf("replaced/fired = %#v/%#v", replaced, store.fired)
	}
	next, ok := loop.NextFireAt()
	if !ok || !next.After(now) {
		t.Fatalf("next fire = %s/%v", next, ok)
	}

	loop.Dispatch(jobs)
	select {
	case got := <-runCalled:
		if got != "loader-1/interval-1/interval:1000" {
			t.Fatalf("run call = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for scheduled run")
	}
}

type triggerLoopStoreFake struct {
	fired []string
}

func (s *triggerLoopStoreFake) MarkLoaderTriggerFired(_ context.Context, loaderID, triggerID string, lastFiredAt, nextFireAt time.Time) error {
	s.fired = append(s.fired, loaderID+"/"+triggerID+"/"+lastFiredAt.Format(time.RFC3339)+"/"+nextFireAt.Format(time.RFC3339))
	return nil
}

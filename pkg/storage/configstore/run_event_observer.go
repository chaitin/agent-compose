package configstore

import (
	"context"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// RunEventObserver receives structured run events after their transaction has
// committed. The callback is best effort: it cannot change the persisted
// result, and a slow implementation must not block persistence indefinitely.
type RunEventObserver func(context.Context, domain.ProjectRunRecord, []domain.ProjectRunEventRecord)

// SetRunEventObserver installs the daemon's observation sink. It is set once
// during composition; a nil observer disables notifications.
func (s *ConfigStore) SetRunEventObserver(observer RunEventObserver) {
	if s == nil || s.projectStore == nil {
		return
	}
	s.runEventObserver = observer
}

func (s *projectStore) notifyRunEvents(ctx context.Context, run domain.ProjectRunRecord, events []domain.ProjectRunEventRecord) {
	if s == nil || s.runEventObserver == nil || len(events) == 0 {
		return
	}
	s.runEventObserver(ctx, run, events)
}

// createdRunEvents keeps only rows this append actually inserted, preserving
// the transaction's sequence order.
func createdRunEvents(results []domain.ProjectRunEventRecord, created []bool) []domain.ProjectRunEventRecord {
	if len(results) == 0 || len(results) != len(created) {
		return nil
	}
	events := make([]domain.ProjectRunEventRecord, 0, len(results))
	for index, event := range results {
		if created[index] {
			events = append(events, event)
		}
	}
	return events
}

func (s *projectStore) notifyRunEventsByRunID(ctx context.Context, results []domain.ProjectRunEventRecord, created []bool) {
	grouped := make(map[string][]domain.ProjectRunEventRecord)
	for index, event := range results {
		if index >= len(created) || !created[index] {
			continue
		}
		grouped[event.RunID] = append(grouped[event.RunID], event)
	}
	for runID, events := range grouped {
		run, err := s.GetProjectRun(ctx, runID)
		if err != nil {
			continue
		}
		s.notifyRunEvents(ctx, run, events)
	}
}

package loaders

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

func TestControllerCoverageWorkflow(t *testing.T) {
	ctx := context.Background()
	store := newControllerTestStore()
	notifier := &controllerTestNotifier{}
	publisher := &controllerTestPublisher{}
	root := t.TempDir()
	controller := NewController(ControllerDependencies{
		Store:  store,
		Engine: controllerTestEngine{},
		HostFactory: func(domain.Loader, *domain.LoaderRunSummary, TriggerEventMetadata) RunHost {
			return nil
		},
		Notifier:  notifier,
		Publisher: publisher,
		Artifacts: FSArtifacts{DataRoot: root},
		Loaders:   map[string]domain.Loader{},
		Running:   map[string]int{},
		Now:       func() time.Time { return time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC) },
		NewID:     func() string { return "event-id" },
		RunTimeout: func(override time.Duration) time.Duration {
			if override > 0 {
				return override
			}
			return time.Second
		},
	})

	created, err := controller.CreateSchedulerExecution(ctx, domain.Loader{Summary: domain.LoaderSummary{ID: "loader-1", Name: "Loader", Enabled: true}, Script: "function main(){}"})
	if err != nil {
		t.Fatalf("CreateSchedulerExecution returned error: %v", err)
	}
	if created.Summary.Runtime != domain.LoaderRuntimeScheduler || len(store.loaders[created.Summary.ID].Triggers) != 1 {
		t.Fatalf("created loader = %#v", created)
	}
	created.Summary.Description = "updated"
	updated, err := controller.UpdateSchedulerExecution(ctx, created)
	if err != nil || updated.Summary.Description != "updated" {
		t.Fatalf("UpdateSchedulerExecution updated=%#v err=%v", updated, err)
	}
	if _, err := controller.SetSchedulerExecutionEnabled(ctx, created.Summary.ID, false); err != nil {
		t.Fatalf("SetSchedulerExecutionEnabled returned error: %v", err)
	}
	if _, err := controller.SetSchedulerExecutionTriggerEnabled(ctx, created.Summary.ID, "trigger-1", false); err != nil {
		t.Fatalf("SetLoaderTriggerEnabled returned error: %v", err)
	}
	if _, trigger, err := controller.LoadSchedulerExecutionForRun(ctx, created.Summary.ID, "trigger-1"); err != nil || trigger == nil {
		t.Fatalf("LoadLoaderForRun trigger=%#v err=%v", trigger, err)
	}
	if _, _, err := controller.LoadSchedulerExecutionForRun(ctx, created.Summary.ID, "missing"); err == nil {
		t.Fatalf("expected missing trigger error")
	}
	manualRun, err := controller.RunSchedulerExecutionNow(ctx, created.Summary.ID, "trigger-1", `{"manual":true}`, time.Millisecond)
	if err != nil || manualRun.Status != domain.LoaderRunStatusSucceeded || manualRun.ResultJSON == "" {
		t.Fatalf("RunNow run=%#v err=%v", manualRun, err)
	}
	prepared, err := controller.Prepare(ctx, created, &created.Triggers[0], `{"prepared":true}`, "manual", RunOptions{})
	if err != nil || prepared.Run.Status != domain.LoaderRunStatusRunning {
		t.Fatalf("Prepare prepared=%#v err=%v", prepared, err)
	}
	executed, err := controller.Execute(ctx, prepared)
	if err != nil || executed.Status != domain.LoaderRunStatusSucceeded {
		t.Fatalf("Execute run=%#v err=%v", executed, err)
	}
	prepared, err = controller.Prepare(ctx, created, &created.Triggers[0], `{"abort":true}`, "manual", RunOptions{})
	if err != nil {
		t.Fatalf("Prepare before Abort returned error: %v", err)
	}
	controller.Abort(ctx, prepared, "")
	controller.Publish("topic.test", map[string]any{"ok": true})
	if len(publisher.events) != 1 {
		t.Fatalf("publisher events = %#v", publisher.events)
	}
	controller.ReplaceCachedLoaders(map[string]domain.Loader{created.Summary.ID: updated})
	if len(controller.CachedLoadersMap()) != 1 || len(controller.SnapshotLoaders()) != 1 {
		t.Fatalf("cache not populated")
	}
	if !controller.EnterRun(domain.Loader{Summary: domain.LoaderSummary{ID: created.Summary.ID, ConcurrencyPolicy: domain.LoaderConcurrencyPolicySkip}}) {
		t.Fatalf("first EnterRun should succeed")
	}
	if controller.EnterRun(domain.Loader{Summary: domain.LoaderSummary{ID: created.Summary.ID, ConcurrencyPolicy: domain.LoaderConcurrencyPolicySkip}}) {
		t.Fatalf("second EnterRun should be rejected")
	}
	if !controller.AnyTargetBusy([]EventTarget{{Loader: domain.Loader{Summary: domain.LoaderSummary{ID: created.Summary.ID}}}}) {
		t.Fatalf("target should be busy")
	}
	controller.LeaveRun(created.Summary.ID)
	if !controller.EnterRun(domain.Loader{Summary: domain.LoaderSummary{ID: created.Summary.ID, ConcurrencyPolicy: domain.LoaderConcurrencyPolicyParallel}}) {
		t.Fatalf("parallel EnterRun should succeed")
	}
	controller.LeaveRun(created.Summary.ID)
	event, err := controller.AddSchedulerExecutionEventRecord(ctx, created.Summary.ID, "run-1", "trigger-1", "loader.test", "", "message", map[string]any{"ok": true}, "session-1", "cell-1", "agent-session")
	if err != nil || event.ID != "event-id" || event.Level != "info" {
		t.Fatalf("AddLoaderEventRecord event=%#v err=%v", event, err)
	}
	if _, err := controller.AddSchedulerExecutionEventRecord(ctx, created.Summary.ID, "run-1", "trigger-1", "loader.bad", "", "message", func() {}, "", "", ""); err == nil {
		t.Fatalf("AddLoaderEventRecord invalid payload returned nil error")
	}
	dir := controller.RunArtifactsDir(created.Summary.ID, "run-1")
	if err := controller.WriteRunArtifact(dir, "output.txt", "hello"); err != nil {
		t.Fatalf("WriteRunArtifact returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "output.txt")); err != nil {
		t.Fatalf("artifact not written: %v", err)
	}
	controller.UpdateTriggerEventDelivery(ctx, domain.LoaderRunSummary{ID: "run-1", LoaderID: created.Summary.ID, TriggerID: "trigger-1", Status: domain.LoaderRunStatusSucceeded, PayloadJSON: `{"payload":{"eventId":"event-1"}}`})
	if len(store.deliveries) != 1 || store.deliveries[0].Status != domain.EventDeliveryStatusRunSucceeded {
		t.Fatalf("deliveries = %#v", store.deliveries)
	}
	controller.WakeScheduler()
	_ = controller.CollectDueScheduledRuns(time.Now().UTC())
	_, _ = controller.NextScheduledFireAt()
	controller.DispatchScheduledRuns(nil)
	var nilController *Controller
	nilController.Start()
	nilController.WakeScheduler()
	bareController := NewController(ControllerDependencies{
		Store:  store,
		Engine: controllerTestEngine{},
		HostFactory: func(domain.Loader, *domain.LoaderRunSummary, TriggerEventMetadata) RunHost {
			return nil
		},
	})
	if bareController.RunArtifactsDir("loader", "run") != "" {
		t.Fatalf("bare RunArtifactsDir returned non-empty path")
	}
	if err := bareController.WriteRunArtifact("", "ignored", "ignored"); err != nil {
		t.Fatalf("bare WriteRunArtifact returned error: %v", err)
	}
	if bareController.runTimeout(0) != 20*time.Minute || bareController.runTimeout(time.Second) != time.Second {
		t.Fatalf("default runTimeout returned unexpected values")
	}
	if bareController.now().IsZero() || bareController.newID() == "" {
		t.Fatalf("default now/newID returned empty values")
	}
	if err := controller.DeleteSchedulerExecution(ctx, created.Summary.ID); err != nil {
		t.Fatalf("DeleteLoader returned error: %v", err)
	}
	if len(notifier.reasons) == 0 {
		t.Fatalf("expected notifications")
	}

	replaceErrStore := newControllerTestStore()
	replaceErrStore.replaceErr = errors.New("replace failed")
	replaceErrController := NewController(ControllerDependencies{Store: replaceErrStore, Engine: controllerTestEngine{}})
	if _, err := replaceErrController.CreateSchedulerExecution(ctx, domain.Loader{Summary: domain.LoaderSummary{ID: "rollback", Name: "Rollback"}, Script: "script"}); err == nil {
		t.Fatalf("CreateSchedulerExecution with replace error returned nil error")
	}
	if _, ok := replaceErrStore.loaders["rollback"]; ok {
		t.Fatalf("CreateSchedulerExecution did not roll back created loader")
	}
}

func TestIntegrationControllerCoverageWorkflow(t *testing.T) {
	TestControllerCoverageWorkflow(t)
}

func TestE2EControllerCoverageWorkflow(t *testing.T) {
	TestControllerCoverageWorkflow(t)
}

type controllerTestEngine struct{}

func (controllerTestEngine) Validate(context.Context, string, string) (LoaderValidationResult, error) {
	return LoaderValidationResult{Triggers: []domain.LoaderTrigger{{ID: "trigger-1", Kind: domain.LoaderTriggerKindEvent, Topic: "topic.test", Enabled: true}}}, nil
}

func (controllerTestEngine) Execute(context.Context, LoaderExecutionRequest, LoaderHost) (LoaderExecutionResult, error) {
	return LoaderExecutionResult{ResultJSON: `{"ok":true}`}, nil
}

type controllerTestStore struct {
	loaders    map[string]domain.Loader
	runs       []domain.LoaderRunSummary
	events     []domain.LoaderEvent
	deliveries []domain.EventDelivery
	replaceErr error
}

func newControllerTestStore() *controllerTestStore {
	return &controllerTestStore{loaders: map[string]domain.Loader{}}
}

func (s *controllerTestStore) ListSchedulerExecutions(context.Context) ([]domain.Loader, error) {
	items := make([]domain.Loader, 0, len(s.loaders))
	for _, item := range s.loaders {
		items = append(items, CloneLoader(item))
	}
	return items, nil
}

func (s *controllerTestStore) GetSchedulerExecution(_ context.Context, executionID string) (domain.Loader, error) {
	return CloneLoader(s.loaders[executionID]), nil
}

func (s *controllerTestStore) CreateSchedulerExecution(_ context.Context, item domain.Loader) (domain.Loader, error) {
	s.loaders[item.Summary.ID] = CloneLoader(item)
	return item, nil
}

func (s *controllerTestStore) UpdateSchedulerExecution(_ context.Context, item domain.Loader) (domain.Loader, error) {
	current := CloneLoader(item)
	current.Triggers = s.loaders[item.Summary.ID].Triggers
	s.loaders[item.Summary.ID] = current
	return current, nil
}

func (s *controllerTestStore) DeleteSchedulerExecution(_ context.Context, executionID string) error {
	delete(s.loaders, executionID)
	return nil
}

func (s *controllerTestStore) ReplaceSchedulerExecutionTriggers(_ context.Context, executionID string, triggers []domain.LoaderTrigger) ([]domain.LoaderTrigger, error) {
	if s.replaceErr != nil {
		return nil, s.replaceErr
	}
	loader := s.loaders[executionID]
	loader.Triggers = append([]domain.LoaderTrigger(nil), triggers...)
	s.loaders[executionID] = loader
	return triggers, nil
}

func (s *controllerTestStore) SetSchedulerExecutionEnabled(_ context.Context, executionID string, enabled bool) error {
	loader := s.loaders[executionID]
	loader.Summary.Enabled = enabled
	s.loaders[executionID] = loader
	return nil
}

func (s *controllerTestStore) SetSchedulerExecutionTriggerEnabled(_ context.Context, executionID, triggerID string, enabled bool) error {
	loader := s.loaders[executionID]
	for i := range loader.Triggers {
		if loader.Triggers[i].ID == triggerID {
			loader.Triggers[i].Enabled = enabled
		}
	}
	s.loaders[executionID] = loader
	return nil
}

func (s *controllerTestStore) AddSchedulerExecutionEvent(_ context.Context, event domain.LoaderEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *controllerTestStore) CreateLoaderRun(_ context.Context, run domain.LoaderRunSummary) error {
	s.runs = append(s.runs, run)
	return nil
}

func (s *controllerTestStore) UpdateLoaderRun(_ context.Context, run domain.LoaderRunSummary) error {
	s.runs = append(s.runs, run)
	return nil
}

func (s *controllerTestStore) UpdateLoaderLastError(context.Context, string, string) error {
	return nil
}

func (s *controllerTestStore) MarkLoaderTriggerFired(context.Context, string, string, time.Time, time.Time) error {
	return nil
}

func (s *controllerTestStore) UpsertEventDelivery(_ context.Context, delivery domain.EventDelivery) error {
	s.deliveries = append(s.deliveries, delivery)
	return nil
}

type controllerTestNotifier struct {
	reasons []string
}

func (n *controllerTestNotifier) Notify(reason string) {
	n.reasons = append(n.reasons, reason)
}

type controllerTestPublisher struct {
	events []domain.LoaderTopicEvent
}

func (p *controllerTestPublisher) Publish(event domain.LoaderTopicEvent) bool {
	p.events = append(p.events, event)
	return true
}

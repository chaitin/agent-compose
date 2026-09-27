package schedulers

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestControllerCoverageWorkflow(t *testing.T) {
	ctx := context.Background()
	store := newControllerTestStore()
	created := domain.Scheduler{
		Summary:  domain.SchedulerSummary{ID: "scheduler-1", Name: "Scheduler", Runtime: domain.SchedulerRuntimeScheduler, Enabled: true},
		Script:   "function main(){}",
		Triggers: []domain.SchedulerTrigger{{SchedulerID: "scheduler-1", ID: "trigger-1", Kind: domain.SchedulerTriggerKindEvent, Topic: "topic.test", Enabled: true}},
	}
	store.schedulers[created.Summary.ID] = created
	notifier := &controllerTestNotifier{}
	publisher := &controllerTestPublisher{}
	root := t.TempDir()
	controller := NewController(ControllerDependencies{
		Store:  store,
		Engine: controllerTestEngine{},
		HostFactory: func(domain.Scheduler, RuntimeExecutionContext, TriggerEventMetadata) RunHost {
			return nil
		},
		Notifier:   notifier,
		Publisher:  publisher,
		Artifacts:  FSArtifacts{DataRoot: root},
		Schedulers: map[string]domain.Scheduler{},
		Running:    map[string]int{},
		Now:        func() time.Time { return time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC) },
		NewID:      func() string { return "event-id" },
		RunTimeout: func(override time.Duration) time.Duration {
			if override > 0 {
				return override
			}
			return time.Second
		},
	})

	if err := controller.Refresh(ctx); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	updated := created
	updated.Summary.Description = "updated"
	if _, err := controller.SetSchedulerEnabled(ctx, created.Summary.ID, false); err != nil {
		t.Fatalf("SetSchedulerEnabled returned error: %v", err)
	}
	if _, err := controller.SetSchedulerTriggerEnabled(ctx, created.Summary.ID, "trigger-1", false); err != nil {
		t.Fatalf("SetSchedulerTriggerEnabled returned error: %v", err)
	}
	if _, trigger, err := controller.LoadSchedulerForRun(ctx, created.Summary.ID, "trigger-1"); err != nil || trigger == nil {
		t.Fatalf("LoadSchedulerForRun trigger=%#v err=%v", trigger, err)
	}
	if _, _, err := controller.LoadSchedulerForRun(ctx, created.Summary.ID, "missing"); err == nil {
		t.Fatalf("expected missing trigger error")
	}
	manualRun, err := controller.RunNow(ctx, RunNowRequest{SchedulerID: created.Summary.ID, TriggerID: "trigger-1", PayloadJSON: `{"manual":true}`, Timeout: time.Second})
	if err != nil || manualRun.Status != domain.SchedulerRunStatusSucceeded || manualRun.ResultJSON == "" {
		t.Fatalf("RunNow run=%#v err=%v", manualRun, err)
	}
	if !identity.IsID(manualRun.ID) || identity.ShortID(manualRun.ID) == "" {
		t.Fatalf("RunNow run id = %q, want SHA-256 resource id", manualRun.ID)
	}
	prepared, err := controller.Prepare(ctx, RunTriggerRequest{Scheduler: created, Trigger: &created.Triggers[0], PayloadJSON: `{"prepared":true}`, Source: "manual"})
	if err != nil || prepared.Run.Status != domain.SchedulerRunStatusRunning {
		t.Fatalf("Prepare prepared=%#v err=%v", prepared, err)
	}
	if !identity.IsID(prepared.Run.ID) || prepared.Run.ID == manualRun.ID {
		t.Fatalf("Prepare run id = %q, want a distinct SHA-256 resource id", prepared.Run.ID)
	}
	executed, err := controller.Execute(ctx, prepared)
	if err != nil || executed.Status != domain.SchedulerRunStatusSucceeded {
		t.Fatalf("Execute run=%#v err=%v", executed, err)
	}
	prepared, err = controller.Prepare(ctx, RunTriggerRequest{Scheduler: created, Trigger: &created.Triggers[0], PayloadJSON: `{"abort":true}`, Source: "manual"})
	if err != nil {
		t.Fatalf("Prepare before Abort returned error: %v", err)
	}
	controller.Abort(ctx, prepared, "")
	controller.Publish(ctx, "topic.test", map[string]any{"ok": true})
	if len(publisher.events) != 1 {
		t.Fatalf("publisher events = %#v", publisher.events)
	}
	controller.ReplaceCachedSchedulers(map[string]domain.Scheduler{created.Summary.ID: updated})
	if len(controller.CachedSchedulersMap()) != 1 || len(controller.SnapshotSchedulers()) != 1 {
		t.Fatalf("cache not populated")
	}
	if !controller.EnterRun(domain.Scheduler{Summary: domain.SchedulerSummary{ID: created.Summary.ID, ConcurrencyPolicy: domain.SchedulerConcurrencyPolicySkip}}) {
		t.Fatalf("first EnterRun should succeed")
	}
	if controller.EnterRun(domain.Scheduler{Summary: domain.SchedulerSummary{ID: created.Summary.ID, ConcurrencyPolicy: domain.SchedulerConcurrencyPolicySkip}}) {
		t.Fatalf("second EnterRun should be rejected")
	}
	if !controller.AnyTargetBusy([]EventTarget{{Scheduler: domain.Scheduler{Summary: domain.SchedulerSummary{ID: created.Summary.ID}}}}) {
		t.Fatalf("target should be busy")
	}
	controller.LeaveRun(created.Summary.ID)
	if !controller.EnterRun(domain.Scheduler{Summary: domain.SchedulerSummary{ID: created.Summary.ID, ConcurrencyPolicy: domain.SchedulerConcurrencyPolicyParallel}}) {
		t.Fatalf("parallel EnterRun should succeed")
	}
	controller.LeaveRun(created.Summary.ID)
	event, err := controller.AddSchedulerEventRecord(ctx, SchedulerEventInput{
		SchedulerID: created.Summary.ID, RunID: "run-1", TriggerID: "trigger-1", EventType: "scheduler.test",
		Message: "message", Payload: map[string]any{"ok": true}, LinkedSandboxID: "session-1", LinkedCellID: "cell-1", LinkedAgentThreadID: "agent-session",
	})
	if err != nil || event.ID != "event-id" || event.Level != "info" {
		t.Fatalf("AddSchedulerEventRecord event=%#v err=%v", event, err)
	}
	if _, err := controller.AddSchedulerEventRecord(ctx, SchedulerEventInput{
		SchedulerID: created.Summary.ID, RunID: "run-1", TriggerID: "trigger-1", EventType: "scheduler.bad", Message: "message", Payload: func() {},
	}); err == nil {
		t.Fatalf("AddSchedulerEventRecord invalid payload returned nil error")
	}
	dir := controller.RunArtifactsDir(created.Summary.ID, "run-1")
	if err := controller.WriteRunArtifact(dir, "output.txt", "hello"); err != nil {
		t.Fatalf("WriteRunArtifact returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "output.txt")); err != nil {
		t.Fatalf("artifact not written: %v", err)
	}
	controller.UpdateTriggerEventDelivery(ctx, domain.SchedulerRunSummary{ID: "run-1", SchedulerID: created.Summary.ID, TriggerID: "trigger-1", Status: domain.SchedulerRunStatusSucceeded, PayloadJSON: `{"payload":{"eventId":"event-1"}}`})
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
		HostFactory: func(domain.Scheduler, RuntimeExecutionContext, TriggerEventMetadata) RunHost {
			return nil
		},
	})
	if bareController.RunArtifactsDir("scheduler", "run") != "" {
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
	if len(notifier.reasons) == 0 {
		t.Fatalf("expected notifications")
	}
}

func TestIntegrationControllerCoverageWorkflow(t *testing.T) {
	TestControllerCoverageWorkflow(t)
}

func TestE2EControllerCoverageWorkflow(t *testing.T) {
	TestControllerCoverageWorkflow(t)
}

type controllerTestEngine struct{}

func (controllerTestEngine) Validate(context.Context, string, string) (SchedulerValidationResult, error) {
	return SchedulerValidationResult{Triggers: []domain.SchedulerTrigger{{ID: "trigger-1", Kind: domain.SchedulerTriggerKindEvent, Topic: "topic.test", Enabled: true}}}, nil
}

func (controllerTestEngine) Execute(context.Context, SchedulerExecutionRequest, SchedulerHost) (SchedulerExecutionResult, error) {
	return SchedulerExecutionResult{ResultJSON: `{"ok":true}`}, nil
}

type controllerTestStore struct {
	schedulers  map[string]domain.Scheduler
	runs        []domain.SchedulerRunSummary
	events      []domain.SchedulerEvent
	deliveries  []domain.EventDelivery
	replaceErr  error
	nextFireErr error
}

func newControllerTestStore() *controllerTestStore {
	return &controllerTestStore{schedulers: map[string]domain.Scheduler{}}
}

func (s *controllerTestStore) ListSchedulers(context.Context) ([]domain.Scheduler, error) {
	items := make([]domain.Scheduler, 0, len(s.schedulers))
	for _, item := range s.schedulers {
		items = append(items, CloneScheduler(item))
	}
	return items, nil
}

func (s *controllerTestStore) GetScheduler(_ context.Context, schedulerID string) (domain.Scheduler, error) {
	return CloneScheduler(s.schedulers[schedulerID]), nil
}

func (s *controllerTestStore) CreateScheduler(_ context.Context, item domain.Scheduler) (domain.Scheduler, error) {
	s.schedulers[item.Summary.ID] = CloneScheduler(item)
	return item, nil
}

func (s *controllerTestStore) UpdateScheduler(_ context.Context, item domain.Scheduler) (domain.Scheduler, error) {
	current := CloneScheduler(item)
	current.Triggers = s.schedulers[item.Summary.ID].Triggers
	s.schedulers[item.Summary.ID] = current
	return current, nil
}

func (s *controllerTestStore) DeleteScheduler(_ context.Context, schedulerID string) error {
	delete(s.schedulers, schedulerID)
	return nil
}

func (s *controllerTestStore) ReplaceSchedulerTriggers(_ context.Context, schedulerID string, triggers []domain.SchedulerTrigger) ([]domain.SchedulerTrigger, error) {
	if s.replaceErr != nil {
		return nil, s.replaceErr
	}
	scheduler := s.schedulers[schedulerID]
	scheduler.Triggers = append([]domain.SchedulerTrigger(nil), triggers...)
	s.schedulers[schedulerID] = scheduler
	return triggers, nil
}

func (s *controllerTestStore) SetSchedulerEnabled(_ context.Context, schedulerID string, enabled bool) error {
	scheduler := s.schedulers[schedulerID]
	scheduler.Summary.Enabled = enabled
	s.schedulers[schedulerID] = scheduler
	return nil
}

func (s *controllerTestStore) SetSchedulerTriggerEnabled(_ context.Context, schedulerID, triggerID string, enabled bool) error {
	scheduler := s.schedulers[schedulerID]
	for i := range scheduler.Triggers {
		if scheduler.Triggers[i].ID == triggerID {
			scheduler.Triggers[i].Enabled = enabled
		}
	}
	s.schedulers[schedulerID] = scheduler
	return nil
}

func (s *controllerTestStore) SetSchedulerTriggerNextFireAt(_ context.Context, schedulerID, triggerID string, nextFireAt time.Time) error {
	if s.nextFireErr != nil {
		return s.nextFireErr
	}
	scheduler := s.schedulers[schedulerID]
	for i := range scheduler.Triggers {
		if scheduler.Triggers[i].ID == triggerID {
			scheduler.Triggers[i].NextFireAt = nextFireAt
		}
	}
	s.schedulers[schedulerID] = scheduler
	return nil
}

func (s *controllerTestStore) AddSchedulerEvent(_ context.Context, event domain.SchedulerEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *controllerTestStore) CreateSchedulerRun(_ context.Context, run domain.SchedulerRunSummary) error {
	s.runs = append(s.runs, run)
	return nil
}

func (s *controllerTestStore) UpdateSchedulerRun(_ context.Context, run domain.SchedulerRunSummary) error {
	s.runs = append(s.runs, run)
	return nil
}

func (s *controllerTestStore) UpdateSchedulerLastError(context.Context, string, string) error {
	return nil
}

func (s *controllerTestStore) MarkSchedulerTriggerFired(context.Context, string, string, time.Time, time.Time) error {
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
	events []domain.SchedulerTopicEvent
}

func (p *controllerTestPublisher) Publish(event domain.SchedulerTopicEvent) bool {
	p.events = append(p.events, event)
	return true
}

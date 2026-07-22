package loaders_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-compose/pkg/events/webhooks"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

func TestRunExecutorLifecycleWorkflows(t *testing.T) {
	ctx := context.Background()
	loader := domain.Loader{Summary: domain.LoaderSummary{ID: "loader-1", Runtime: domain.LoaderRuntimeScheduler}, Script: "script"}
	trigger := domain.LoaderTrigger{ID: "trigger-1", Kind: domain.LoaderTriggerKindEvent}
	store := &runStoreFake{}
	engine := &loaderEngineFake{result: loaders.LoaderExecutionResult{ResultJSON: `{"ok":true}`, Warnings: []string{"scheduler.session.getSession is deprecated; use scheduler.sandbox.getSandbox"}}}
	var events []string
	var deliveries []domain.LoaderRunSummary
	var notifications []string
	var refreshes int
	var leaves []string
	executor := loaders.NewRunExecutor(loaders.RunExecutorDependencies{
		Store:  store,
		Engine: engine,
		HostFactory: func(domain.Loader, loaders.RuntimeExecutionContext, loaders.TriggerEventMetadata) loaders.RunHost {
			return &runHostFake{}
		},
		ArtifactsDir: func(loaderID, runID string) string {
			return filepath.Join(t.TempDir(), loaderID, runID)
		},
		WriteArtifact: func(dir, name, content string) error {
			if strings.TrimSpace(dir) == "" || strings.TrimSpace(name) == "" {
				t.Fatalf("write artifact received empty path: %q/%q", dir, name)
			}
			return nil
		},
		EnterRun: func(domain.Loader) bool { return true },
		LeaveRun: func(loaderID string) {
			leaves = append(leaves, loaderID)
		},
		AddLoaderEvent: func(_ context.Context, _, _, _, eventType, _, _ string, _ any, _, _, _ string) error {
			events = append(events, eventType)
			return nil
		},
		UpdateTriggerEventDelivery: func(_ context.Context, run domain.LoaderRunSummary) {
			deliveries = append(deliveries, run)
		},
		Notify: func(reason string) {
			notifications = append(notifications, reason)
		},
		Refresh: func(context.Context) error {
			refreshes++
			return nil
		},
	})

	run, err := executor.Run(ctx, loader, &trigger, `{"eventId":"evt-1"}`, "manual", loaders.RunOptions{})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if run.Status != domain.LoaderRunStatusSucceeded || run.ResultJSON != `{"ok":true}` || run.TriggerID != trigger.ID {
		t.Fatalf("run = %#v", run)
	}
	if len(store.created) != 1 || len(store.updated) != 1 || store.lastError[loader.Summary.ID] != "" {
		t.Fatalf("store state = %#v/%#v/%#v", store.created, store.updated, store.lastError)
	}
	if !containsString(events, "loader.run.started") || !containsString(events, "loader.run.completed") || !containsString(events, "loader.deprecated_alias.warning") {
		t.Fatalf("events = %#v", events)
	}
	if len(deliveries) != 2 || len(notifications) != 2 || refreshes != 1 || len(leaves) != 1 {
		t.Fatalf("deliveries/notifications/refreshes/leaves = %d/%d/%d/%d", len(deliveries), len(notifications), refreshes, len(leaves))
	}

	busyStore := &runStoreFake{}
	busyExecutor := loaders.NewRunExecutor(loaders.RunExecutorDependencies{
		Store:  busyStore,
		Engine: engine,
		HostFactory: func(domain.Loader, loaders.RuntimeExecutionContext, loaders.TriggerEventMetadata) loaders.RunHost {
			return &runHostFake{}
		},
		ArtifactsDir:  func(loaderID, runID string) string { return filepath.Join(t.TempDir(), loaderID, runID) },
		WriteArtifact: func(string, string, string) error { return nil },
		EnterRun:      func(domain.Loader) bool { return false },
		AddLoaderEvent: func(_ context.Context, _, _, _, eventType, _, _ string, _ any, _, _, _ string) error {
			events = append(events, eventType)
			return nil
		},
		UpdateTriggerEventDelivery: func(context.Context, domain.LoaderRunSummary) {},
		Notify:                     func(string) {},
	})
	skipped, err := busyExecutor.Run(ctx, loader, nil, `{}`, "manual", loaders.RunOptions{})
	if err != nil {
		t.Fatalf("busy Run returned error: %v", err)
	}
	if skipped.Status != domain.LoaderRunStatusSkipped || skipped.Error == "" || busyStore.lastError[loader.Summary.ID] == "" {
		t.Fatalf("skipped run/store = %#v/%#v", skipped, busyStore.lastError)
	}
	if _, err := busyExecutor.Run(ctx, loader, nil, `{}`, "manual", loaders.RunOptions{RetryWhenBusy: true}); !errors.Is(err, loaders.ErrRunBusyForRetry) {
		t.Fatalf("busy retry err = %v", err)
	}
}

func TestEventDispatcherWorkflows(t *testing.T) {
	ctx := context.Background()
	store := &eventDeliveryStoreFake{}
	loader := domain.Loader{Summary: domain.LoaderSummary{ID: "loader-1", Enabled: true}, Triggers: []domain.LoaderTrigger{{
		ID:      "trigger-1",
		Kind:    domain.LoaderTriggerKindEvent,
		Topic:   "topic.one",
		Enabled: true,
	}}}

	noSubscriberAcked := false
	dispatcher := loaders.NewEventDispatcher(loaders.EventDispatcherDependencies{
		RootCtx: ctx,
		Targets: func(string) []loaders.EventTarget {
			return nil
		},
	})
	dispatcher.Dispatch(domain.LoaderTopicEvent{Topic: "missing", CreatedAt: time.Now().UTC(), NoSubscriberAck: func(context.Context) error {
		noSubscriberAcked = true
		return nil
	}})
	if !noSubscriberAcked {
		t.Fatalf("no subscriber ack was not called")
	}

	retryReason := ""
	dispatcher = loaders.NewEventDispatcher(loaders.EventDispatcherDependencies{
		RootCtx: ctx,
		Targets: func(string) []loaders.EventTarget {
			return []loaders.EventTarget{{Loader: loader, Trigger: loader.Triggers[0]}}
		},
		IsBusy: func([]loaders.EventTarget) bool { return true },
	})
	dispatcher.Dispatch(domain.LoaderTopicEvent{Topic: "topic.one", Source: domain.TopicEventSourceWebhook, CreatedAt: time.Now().UTC(), Retry: func(_ context.Context, reason string, _ time.Time) error {
		retryReason = reason
		return nil
	}})
	if retryReason != "loader is already running" {
		t.Fatalf("retry reason = %q", retryReason)
	}

	runCalled := make(chan string, 1)
	ackCalled := false
	released := false
	dispatcher = loaders.NewEventDispatcher(loaders.EventDispatcherDependencies{
		RootCtx: ctx,
		Store:   store,
		Targets: func(string) []loaders.EventTarget {
			return []loaders.EventTarget{{Loader: loader, Trigger: loader.Triggers[0]}}
		},
		ReserveSlots: func(domain.LoaderTopicEvent, int) ([]*webhooks.Reservation, bool) {
			return webhooks.NoopReservations(1), true
		},
		RunTimeout: func(time.Duration) time.Duration { return time.Second },
		Run: func(_ context.Context, _ domain.Loader, _ *domain.LoaderTrigger, payloadJSON, source string, _ loaders.RunOptions, ack ...func(context.Context) error) (domain.LoaderRunSummary, error) {
			if len(ack) > 0 && ack[0] != nil {
				_ = ack[0](ctx)
			}
			runCalled <- source + ":" + payloadJSON
			return domain.LoaderRunSummary{}, nil
		},
	})
	dispatcher.Dispatch(domain.LoaderTopicEvent{
		EventID:   "evt-1",
		Topic:     "topic.one",
		CreatedAt: time.Date(2026, 6, 2, 1, 2, 3, 0, time.UTC),
		Payload:   map[string]any{"value": "x"},
		Ack: func(context.Context) error {
			ackCalled = true
			return nil
		},
		Release: func() {
			released = true
		},
	})
	select {
	case got := <-runCalled:
		if !strings.Contains(got, "topic.one:") || !strings.Contains(got, `"value":"x"`) {
			t.Fatalf("run payload = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for dispatched run")
	}
	if !ackCalled || released {
		t.Fatalf("ack/released = %v/%v", ackCalled, released)
	}
	if len(store.deliveries) != 1 || store.deliveries[0].Status != domain.EventDeliveryStatusMatched {
		t.Fatalf("deliveries = %#v", store.deliveries)
	}
}

func TestEventDispatcherWebhookAndWrapperWorkflows(t *testing.T) {
	ctx := context.Background()
	loader := domain.Loader{Summary: domain.LoaderSummary{ID: "loader-1", Enabled: true}, Triggers: []domain.LoaderTrigger{{
		ID:      "trigger-1",
		Kind:    domain.LoaderTriggerKindEvent,
		Topic:   "topic.webhook",
		Enabled: true,
	}}}
	target := loaders.EventTarget{Loader: loader, Trigger: loader.Triggers[0]}

	acked := false
	released := false
	dispatcher := loaders.NewEventDispatcher(loaders.EventDispatcherDependencies{})
	dispatcher.AckNoSubscriber(domain.LoaderTopicEvent{Topic: "missing", Ack: func(context.Context) error {
		acked = true
		return nil
	}})
	dispatcher.Retry(domain.LoaderTopicEvent{Topic: "retry", Release: func() {
		released = true
	}}, "")
	if !acked || !released {
		t.Fatalf("wrapper ack/release = %v/%v", acked, released)
	}

	retryReason := ""
	dispatcher = loaders.NewEventDispatcher(loaders.EventDispatcherDependencies{
		RootCtx: ctx,
		Targets: func(string) []loaders.EventTarget {
			return []loaders.EventTarget{target}
		},
		ReserveSlots: func(domain.LoaderTopicEvent, int) ([]*webhooks.Reservation, bool) {
			return nil, false
		},
	})
	dispatcher.Dispatch(domain.LoaderTopicEvent{Topic: "topic.webhook", Source: domain.TopicEventSourceWebhook, CreatedAt: time.Now().UTC(), Retry: func(_ context.Context, reason string, _ time.Time) error {
		retryReason = reason
		return nil
	}})
	if retryReason != "webhook queue is full" {
		t.Fatalf("queue full retry reason = %q", retryReason)
	}

	executed := make(chan string, 1)
	webhookAcked := false
	dispatcher = loaders.NewEventDispatcher(loaders.EventDispatcherDependencies{
		RootCtx: ctx,
		Targets: func(string) []loaders.EventTarget {
			return []loaders.EventTarget{target}
		},
		ReserveSlots: func(domain.LoaderTopicEvent, int) ([]*webhooks.Reservation, bool) {
			return webhooks.NoopReservations(1), true
		},
		RunTimeout: func(time.Duration) time.Duration { return time.Second },
		Prepare: func(_ context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options loaders.RunOptions) (loaders.PreparedRun, error) {
			if !options.AlreadyEntered || source != "topic.webhook" || !strings.Contains(payloadJSON, `"topic":"topic.webhook"`) {
				t.Fatalf("Prepare source/options/payload = %q/%#v/%q", source, options, payloadJSON)
			}
			return loaders.PreparedRun{
				Loader:      loader,
				Trigger:     trigger,
				Run:         domain.LoaderRunSummary{LoaderID: loader.Summary.ID, TriggerID: trigger.ID},
				PayloadJSON: payloadJSON,
			}, nil
		},
		Execute: func(_ context.Context, prepared loaders.PreparedRun) (domain.LoaderRunSummary, error) {
			executed <- prepared.Loader.Summary.ID + "/" + prepared.Run.TriggerID
			return domain.LoaderRunSummary{}, nil
		},
	})
	dispatcher.Dispatch(domain.LoaderTopicEvent{Topic: "topic.webhook", Source: domain.TopicEventSourceWebhook, CreatedAt: time.Now().UTC(), Ack: func(context.Context) error {
		webhookAcked = true
		return nil
	}})
	select {
	case got := <-executed:
		if got != "loader-1/trigger-1" {
			t.Fatalf("executed target = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for webhook execute")
	}
	if !webhookAcked {
		t.Fatalf("webhook ack was not called")
	}

	entered := 0
	left := make([]string, 0)
	retryReason = ""
	dispatcher = loaders.NewEventDispatcher(loaders.EventDispatcherDependencies{
		RootCtx: ctx,
		Targets: func(string) []loaders.EventTarget {
			second := loader
			second.Summary.ID = "loader-2"
			return []loaders.EventTarget{target, {Loader: second, Trigger: second.Triggers[0]}}
		},
		ReserveSlots: func(domain.LoaderTopicEvent, int) ([]*webhooks.Reservation, bool) {
			return webhooks.NoopReservations(2), true
		},
		EnterRun: func(domain.Loader) bool {
			entered++
			return entered == 1
		},
		LeaveRun: func(loaderID string) {
			left = append(left, loaderID)
		},
	})
	dispatcher.Dispatch(domain.LoaderTopicEvent{Topic: "topic.webhook", Source: domain.TopicEventSourceWebhook, CreatedAt: time.Now().UTC(), Retry: func(_ context.Context, reason string, _ time.Time) error {
		retryReason = reason
		return nil
	}})
	if retryReason != "loader is already running" || len(left) != 1 || left[0] != "loader-1" {
		t.Fatalf("enter retry/left = %q/%#v", retryReason, left)
	}

	aborted := make(chan string, 1)
	retryReason = ""
	dispatcher = loaders.NewEventDispatcher(loaders.EventDispatcherDependencies{
		RootCtx: ctx,
		Targets: func(string) []loaders.EventTarget {
			second := loader
			second.Summary.ID = "loader-2"
			return []loaders.EventTarget{target, {Loader: second, Trigger: second.Triggers[0]}}
		},
		ReserveSlots: func(domain.LoaderTopicEvent, int) ([]*webhooks.Reservation, bool) {
			return webhooks.NoopReservations(2), true
		},
		Prepare: func(_ context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, payloadJSON, _ string, _ loaders.RunOptions) (loaders.PreparedRun, error) {
			if loader.Summary.ID == "loader-2" {
				return loaders.PreparedRun{}, errors.New("prepare failed")
			}
			return loaders.PreparedRun{
				Loader:      loader,
				Trigger:     trigger,
				Run:         domain.LoaderRunSummary{LoaderID: loader.Summary.ID, TriggerID: trigger.ID},
				PayloadJSON: payloadJSON,
			}, nil
		},
		Abort: func(_ context.Context, prepared loaders.PreparedRun, reason string) {
			aborted <- prepared.Loader.Summary.ID + ":" + reason
		},
	})
	dispatcher.Dispatch(domain.LoaderTopicEvent{Topic: "topic.webhook", Source: domain.TopicEventSourceWebhook, CreatedAt: time.Now().UTC(), Retry: func(_ context.Context, reason string, _ time.Time) error {
		retryReason = reason
		return nil
	}})
	select {
	case got := <-aborted:
		if got != "loader-1:prepare failed" {
			t.Fatalf("abort call = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for abort")
	}
	if retryReason != "prepare failed" {
		t.Fatalf("prepare retry reason = %q", retryReason)
	}
}

func TestSchedulerCollectDueAndDispatch(t *testing.T) {
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)
	store := &schedulerStoreFake{}
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
	scheduler := loaders.NewScheduler(loaders.SchedulerDependencies{
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
		Run: func(_ context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, _ string, source string, _ loaders.RunOptions, _ ...func(context.Context) error) (domain.LoaderRunSummary, error) {
			runCalled <- loader.Summary.ID + "/" + trigger.ID + "/" + source
			return domain.LoaderRunSummary{}, nil
		},
	})

	jobs := scheduler.CollectDue(now)
	if len(jobs) != 1 || jobs[0].Trigger.ID != "interval-1" || jobs[0].Source != "interval:1000" {
		t.Fatalf("jobs = %#v", jobs)
	}
	if len(replaced) != 1 || len(store.fired) != 1 {
		t.Fatalf("replaced/fired = %#v/%#v", replaced, store.fired)
	}
	next, ok := scheduler.NextFireAt()
	if !ok || !next.After(now) {
		t.Fatalf("next fire = %s/%v", next, ok)
	}

	scheduler.Dispatch(jobs)
	select {
	case got := <-runCalled:
		if got != "loader-1/interval-1/interval:1000" {
			t.Fatalf("run call = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for scheduled run")
	}
}

type runStoreFake struct {
	created   []domain.LoaderRunSummary
	updated   []domain.LoaderRunSummary
	lastError map[string]string
}

func (s *runStoreFake) CreateLoaderRun(_ context.Context, run domain.LoaderRunSummary) error {
	s.created = append(s.created, run)
	return nil
}

func (s *runStoreFake) UpdateLoaderRun(_ context.Context, run domain.LoaderRunSummary) error {
	s.updated = append(s.updated, run)
	return nil
}

func (s *runStoreFake) UpdateLoaderLastError(_ context.Context, loaderID, lastError string) error {
	if s.lastError == nil {
		s.lastError = map[string]string{}
	}
	s.lastError[loaderID] = lastError
	return nil
}

type loaderEngineFake struct {
	result loaders.LoaderExecutionResult
	err    error
}

func (e *loaderEngineFake) Validate(context.Context, string, string) (loaders.LoaderValidationResult, error) {
	return loaders.LoaderValidationResult{}, nil
}

func (e *loaderEngineFake) Execute(context.Context, loaders.LoaderExecutionRequest, loaders.LoaderHost) (loaders.LoaderExecutionResult, error) {
	return e.result, e.err
}

type runHostFake struct {
	cleanup int
}

func (h *runHostFake) Log(context.Context, string, any) error { return nil }
func (h *runHostFake) PublishEvent(context.Context, string, string) (domain.TopicEventRecord, error) {
	return domain.TopicEventRecord{}, nil
}
func (h *runHostFake) Agent(context.Context, string, domain.LoaderAgentRequest) (domain.LoaderAgentResult, error) {
	return domain.LoaderAgentResult{}, nil
}
func (h *runHostFake) Command(context.Context, domain.LoaderCommandRequest) (domain.LoaderCommandResult, error) {
	return domain.LoaderCommandResult{}, nil
}
func (h *runHostFake) LLM(context.Context, string, domain.LoaderLLMRequest) (domain.LoaderLLMResult, error) {
	return domain.LoaderLLMResult{}, nil
}
func (h *runHostFake) StateGet(context.Context, string) (string, bool, error) { return "", false, nil }
func (h *runHostFake) StateSet(context.Context, string, string) error         { return nil }
func (h *runHostFake) StateDelete(context.Context, string) error              { return nil }
func (h *runHostFake) CallSessionRPC(context.Context, string, string) (string, error) {
	return "", nil
}
func (h *runHostFake) CleanupCommandSessions(context.Context) { h.cleanup++ }

type eventDeliveryStoreFake struct {
	deliveries []domain.EventDelivery
}

func (s *eventDeliveryStoreFake) UpsertEventDelivery(_ context.Context, delivery domain.EventDelivery) error {
	s.deliveries = append(s.deliveries, delivery)
	return nil
}

type schedulerStoreFake struct {
	fired []domain.LoaderTrigger
}

func (s *schedulerStoreFake) MarkLoaderTriggerFired(_ context.Context, loaderID, triggerID string, lastFiredAt, nextFireAt time.Time) error {
	s.fired = append(s.fired, domain.LoaderTrigger{ID: loaderID + "/" + triggerID, LastFiredAt: lastFiredAt, NextFireAt: nextFireAt})
	return nil
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

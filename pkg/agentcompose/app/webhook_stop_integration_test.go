package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"

	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/events/webhooks"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	"github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

func TestIntegrationWebhookStopCancelsDispatchedSchedulerRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:    root,
		SandboxRoot: filepath.Join(root, "sandboxes"),
		DbAddr:      filepath.Join(root, "data.db"),
		DbTimeout:   time.Second,
	}
	database, err := sqlite.Open(config.DbAddr, config.DbTimeout)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	store := configstore.FromDB(database.DB())
	sandboxStore, err := sandboxstore.NewWithDatabase(config, database.DB())
	if err != nil {
		t.Fatalf("open sandbox store: %v", err)
	}
	t.Cleanup(func() {
		if err := sandboxStore.Close(); err != nil {
			t.Errorf("close sandbox store: %v", err)
		}
	})

	scheduler := seedWebhookStopScheduler(t, ctx, store)
	engine := &blockingWebhookSchedulerEngine{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
	t.Cleanup(func() {
		select {
		case <-engine.release:
		default:
			close(engine.release)
		}
	})
	controller := schedulers.NewController(schedulers.ControllerDependencies{
		RootCtx: ctx,
		Store:   store,
		Engine:  engine,
		HostFactory: func(scheduler domain.Scheduler, execution schedulers.RuntimeExecutionContext, triggerEvent schedulers.TriggerEventMetadata) schedulers.RunHost {
			return schedulers.NewRuntimeHost(schedulers.RunHostDependencies{}, scheduler, execution, triggerEvent)
		},
		Artifacts:  schedulers.FSArtifacts{DataRoot: root},
		Schedulers: map[string]domain.Scheduler{scheduler.Summary.ID: scheduler},
		RunTimeout: func(time.Duration) time.Duration { return time.Minute },
	})

	const token = "stop-token"
	if _, err := store.UpsertWebhookSource(ctx, domain.WebhookSource{
		ID: "stop-source", Name: "Stop source", Enabled: true, Provider: "generic",
		TopicPrefix: "webhook.stop.", TokenHash: webhooks.TokenHash(token),
	}); err != nil {
		t.Fatalf("create webhook source: %v", err)
	}
	event, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID: "stop-event", Topic: "webhook.stop.run", Source: domain.TopicEventSourceWebhook,
		PayloadJSON: `{"webhookSourceId":"stop-source"}`, DispatchStatus: domain.TopicEventDispatchPublishedToBus,
	})
	if err != nil {
		t.Fatalf("create webhook event: %v", err)
	}

	app := echo.New()
	di := do.New()
	do.ProvideValue(di, app)
	do.ProvideValue(di, config)
	do.ProvideValue(di, store)
	do.ProvideValue(di, sandboxStore)
	do.ProvideValue(di, controller)
	registerWebhookRoutes(app, di)

	controller.DispatchEvent(domain.SchedulerTopicEvent{
		EventID: event.ID, Topic: event.Topic, Source: domain.TopicEventSourceWebhook,
		Payload: map[string]any{"eventId": event.ID}, CreatedAt: time.Now().UTC(),
	})
	select {
	case <-engine.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scheduler run to start")
	}

	deliveries, err := store.ListEventDeliveries(ctx, []string{event.ID})
	if err != nil {
		t.Fatalf("list event deliveries: %v", err)
	}
	if len(deliveries) != 1 || deliveries[0].SchedulerID != scheduler.Summary.ID || strings.TrimSpace(deliveries[0].RunID) == "" {
		t.Fatalf("event deliveries = %#v", deliveries)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/webhooks/events/"+event.ID+"/stop", strings.NewReader(`{"reason":"integration stop"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	responseDone := make(chan struct{})
	go func() {
		app.ServeHTTP(recorder, request)
		close(responseDone)
	}()
	select {
	case <-responseDone:
	case <-time.After(time.Second):
		close(engine.release)
		<-responseDone
		t.Fatal("stop request waited for scheduler execution to reach a terminal state")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("stop status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		StopRequested bool `json:"stop_requested"`
		RequestedRuns int  `json:"requested_runs"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode stop response: %v", err)
	}
	if !response.StopRequested || response.RequestedRuns != 1 {
		t.Fatalf("stop response = %#v", response)
	}
	select {
	case <-engine.canceled:
	case <-time.After(time.Second):
		t.Fatal("scheduler execution did not receive cancellation")
	}
	close(engine.release)

	stopCtx, cancelStop := context.WithTimeout(ctx, time.Second)
	defer cancelStop()
	run, _, err := controller.SchedulerRuns().StopSchedulerRun(stopCtx, scheduler.Summary.ID, deliveries[0].RunID, "integration stop")
	if err != nil {
		t.Fatalf("load stopped scheduler run: %v", err)
	}
	if run.Status != domain.SchedulerRunStatusCanceled || run.Error != "integration stop" {
		t.Fatalf("stopped scheduler run = %#v", run)
	}
}

func seedWebhookStopScheduler(t *testing.T, ctx context.Context, store *configstore.ConfigStore) domain.Scheduler {
	t.Helper()
	return seedWebhookStopSchedulerNamed(t, ctx, store, "webhook-stop-project", "webhook-stop-agent", "webhook-stop-scheduler", "webhook.stop.run")
}

// seedWebhookStopSchedulerNamed seeds one project/agent/scheduler that fires
// on the given topic. Tests that need more than one independently-triggered
// scheduler (e.g. to reproduce two events published by different webhook
// sources) call this with distinct IDs and topics.
func seedWebhookStopSchedulerNamed(t *testing.T, ctx context.Context, store *configstore.ConfigStore, projectID, agentID, schedulerID, topic string) domain.Scheduler {
	t.Helper()
	const agentName = "worker"
	specJSON, err := (&compose.NormalizedProjectSpec{
		Name: projectID,
		Agents: []compose.NormalizedAgentSpec{{
			Name: agentName, Enabled: true,
			Scheduler: &compose.NormalizedSchedulerSpec{
				Enabled: true, SandboxPolicy: domain.SchedulerSandboxPolicyNew,
				ConcurrencyPolicy: domain.SchedulerConcurrencyPolicyParallel,
				Script:            fmt.Sprintf("scheduler.on(%q, async function () {});", topic),
			},
		}},
	}).MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("marshal project spec: %v", err)
	}
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: projectID, Name: projectID})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	revision, _, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{
		ProjectID: project.ID, SpecHash: schedulerID + "-spec", SpecJSON: string(specJSON),
	})
	if err != nil {
		t.Fatalf("create project revision: %v", err)
	}
	if _, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{
		ID: agentID, ProjectID: project.ID, AgentName: agentName, Revision: revision.Revision,
		SchedulerEnabled: true, SpecJSON: "{}",
	}); err != nil {
		t.Fatalf("create project agent: %v", err)
	}
	if _, err := store.UpsertProjectScheduler(ctx, domain.ProjectSchedulerRecord{
		ID: schedulerID, ProjectID: project.ID, SchedulerID: schedulerID, AgentName: agentName,
		Revision: revision.Revision, Enabled: true, TriggerCount: 1, SpecJSON: "{}",
	}); err != nil {
		t.Fatalf("create project scheduler: %v", err)
	}
	if _, err := store.ReplaceSchedulerTriggers(ctx, schedulerID, []domain.SchedulerTrigger{{
		ID: schedulerID + "-trigger", Kind: domain.SchedulerTriggerKindEvent,
		Topic: topic, Enabled: true,
	}}); err != nil {
		t.Fatalf("create scheduler trigger: %v", err)
	}
	scheduler, err := store.GetScheduler(ctx, schedulerID)
	if err != nil {
		t.Fatalf("load scheduler: %v", err)
	}
	return scheduler
}

type blockingWebhookSchedulerEngine struct {
	started      chan struct{}
	canceled     chan struct{}
	release      chan struct{}
	startedOnce  sync.Once
	canceledOnce sync.Once
}

func (*blockingWebhookSchedulerEngine) Validate(context.Context, string, string) (schedulers.SchedulerValidationResult, error) {
	return schedulers.SchedulerValidationResult{}, nil
}

func (e *blockingWebhookSchedulerEngine) Execute(ctx context.Context, _ schedulers.SchedulerExecutionRequest, _ schedulers.SchedulerHost) (schedulers.SchedulerExecutionResult, error) {
	e.startedOnce.Do(func() { close(e.started) })
	<-ctx.Done()
	e.canceledOnce.Do(func() { close(e.canceled) })
	<-e.release
	return schedulers.SchedulerExecutionResult{}, ctx.Err()
}

// multiBlockingWebhookSchedulerEngine is blockingWebhookSchedulerEngine
// generalized to more than one concurrently-running scheduler: it keys a
// started/canceled/release lane by the firing trigger's topic, so two
// schedulers dispatched at once (e.g. by two events sharing a
// correlation_id but published through different webhook sources) each get
// their own independent signal instead of racing on one shared channel.
type multiBlockingWebhookSchedulerEngine struct {
	mu    sync.Mutex
	lanes map[string]*webhookSchedulerLane
}

type webhookSchedulerLane struct {
	started      chan struct{}
	canceled     chan struct{}
	release      chan struct{}
	startedOnce  sync.Once
	canceledOnce sync.Once
}

func newMultiBlockingWebhookSchedulerEngine() *multiBlockingWebhookSchedulerEngine {
	return &multiBlockingWebhookSchedulerEngine{lanes: map[string]*webhookSchedulerLane{}}
}

func (e *multiBlockingWebhookSchedulerEngine) lane(key string) *webhookSchedulerLane {
	e.mu.Lock()
	defer e.mu.Unlock()
	lane, ok := e.lanes[key]
	if !ok {
		lane = &webhookSchedulerLane{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
		e.lanes[key] = lane
	}
	return lane
}

func (*multiBlockingWebhookSchedulerEngine) Validate(context.Context, string, string) (schedulers.SchedulerValidationResult, error) {
	return schedulers.SchedulerValidationResult{}, nil
}

func (e *multiBlockingWebhookSchedulerEngine) Execute(ctx context.Context, request schedulers.SchedulerExecutionRequest, _ schedulers.SchedulerHost) (schedulers.SchedulerExecutionResult, error) {
	key := ""
	if request.Trigger != nil {
		key = request.Trigger.Topic
	}
	lane := e.lane(key)
	lane.startedOnce.Do(func() { close(lane.started) })
	<-ctx.Done()
	lane.canceledOnce.Do(func() { close(lane.canceled) })
	<-lane.release
	return schedulers.SchedulerExecutionResult{}, ctx.Err()
}

func (e *multiBlockingWebhookSchedulerEngine) waitStarted(t *testing.T, topic string) {
	t.Helper()
	select {
	case <-e.lane(topic).started:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for scheduler run on topic %q to start", topic)
	}
}

func (e *multiBlockingWebhookSchedulerEngine) waitCanceled(t *testing.T, topic string) {
	t.Helper()
	select {
	case <-e.lane(topic).canceled:
	case <-time.After(time.Second):
		t.Fatalf("scheduler run on topic %q did not receive cancellation", topic)
	}
}

func (e *multiBlockingWebhookSchedulerEngine) releaseAll() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, lane := range e.lanes {
		select {
		case <-lane.release:
		default:
			close(lane.release)
		}
	}
}

// TestIntegrationWebhookStopCascadesToCorrelatedEventAcrossSources reproduces
// the real production shape reported in the field: a "router" webhook event
// (source A) finishes and, in a separate HTTP call, publishes a follow-up
// webhook event (source B) that never sets parent_event_id on itself — only
// a shared correlation_id links the two. Calling /stop on the router event
// with only source A's token must still reach source B's dispatched run.
func TestIntegrationWebhookStopCascadesToCorrelatedEventAcrossSources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:    root,
		SandboxRoot: filepath.Join(root, "sandboxes"),
		DbAddr:      filepath.Join(root, "data.db"),
		DbTimeout:   time.Second,
	}
	database, err := sqlite.Open(config.DbAddr, config.DbTimeout)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	store := configstore.FromDB(database.DB())
	sandboxStore, err := sandboxstore.NewWithDatabase(config, database.DB())
	if err != nil {
		t.Fatalf("open sandbox store: %v", err)
	}
	t.Cleanup(func() {
		if err := sandboxStore.Close(); err != nil {
			t.Errorf("close sandbox store: %v", err)
		}
	})

	const routerTopic = "webhook.router.task"
	const workerTopic = "webhook.worker.task"
	routerScheduler := seedWebhookStopSchedulerNamed(t, ctx, store, "router-project", "router-agent", "router-scheduler", routerTopic)
	workerScheduler := seedWebhookStopSchedulerNamed(t, ctx, store, "worker-project", "worker-agent", "worker-scheduler", workerTopic)

	engine := newMultiBlockingWebhookSchedulerEngine()
	t.Cleanup(engine.releaseAll)
	controller := schedulers.NewController(schedulers.ControllerDependencies{
		RootCtx: ctx,
		Store:   store,
		Engine:  engine,
		HostFactory: func(scheduler domain.Scheduler, execution schedulers.RuntimeExecutionContext, triggerEvent schedulers.TriggerEventMetadata) schedulers.RunHost {
			return schedulers.NewRuntimeHost(schedulers.RunHostDependencies{}, scheduler, execution, triggerEvent)
		},
		Artifacts: schedulers.FSArtifacts{DataRoot: root},
		Schedulers: map[string]domain.Scheduler{
			routerScheduler.Summary.ID: routerScheduler,
			workerScheduler.Summary.ID: workerScheduler,
		},
		RunTimeout: func(time.Duration) time.Duration { return time.Minute },
	})

	const routerToken = "router-token"
	const workerToken = "worker-token"
	if _, err := store.UpsertWebhookSource(ctx, domain.WebhookSource{
		ID: "router-source", Name: "Router source", Enabled: true, Provider: "generic",
		TopicPrefix: "webhook.router.", TokenHash: webhooks.TokenHash(routerToken),
	}); err != nil {
		t.Fatalf("create router webhook source: %v", err)
	}
	if _, err := store.UpsertWebhookSource(ctx, domain.WebhookSource{
		ID: "worker-source", Name: "Worker source", Enabled: true, Provider: "generic",
		TopicPrefix: "webhook.worker.", TokenHash: webhooks.TokenHash(workerToken),
	}); err != nil {
		t.Fatalf("create worker webhook source: %v", err)
	}

	const sharedCorrelationID = "devboard-task-integration-test"
	routerEvent, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID: "router-event", Topic: routerTopic, Source: domain.TopicEventSourceWebhook,
		CorrelationID: sharedCorrelationID, PayloadJSON: `{"webhookSourceId":"router-source"}`,
		DispatchStatus: domain.TopicEventDispatchPublishedToBus,
	})
	if err != nil {
		t.Fatalf("create router event: %v", err)
	}
	// No ParentEventID: this is the "webhook forwarder" pattern — a follow-up
	// HTTP call into a different topic/source, not an internally-published
	// child event. Only the shared correlation_id ties it to routerEvent.
	workerEvent, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID: "worker-event", Topic: workerTopic, Source: domain.TopicEventSourceWebhook,
		CorrelationID: sharedCorrelationID, PayloadJSON: `{"webhookSourceId":"worker-source"}`,
		DispatchStatus: domain.TopicEventDispatchPublishedToBus,
	})
	if err != nil {
		t.Fatalf("create worker event: %v", err)
	}

	app := echo.New()
	di := do.New()
	do.ProvideValue(di, app)
	do.ProvideValue(di, config)
	do.ProvideValue(di, store)
	do.ProvideValue(di, sandboxStore)
	do.ProvideValue(di, controller)
	registerWebhookRoutes(app, di)

	controller.DispatchEvent(domain.SchedulerTopicEvent{
		EventID: routerEvent.ID, Topic: routerEvent.Topic, Source: domain.TopicEventSourceWebhook,
		Payload: map[string]any{"eventId": routerEvent.ID}, CreatedAt: time.Now().UTC(),
	})
	controller.DispatchEvent(domain.SchedulerTopicEvent{
		EventID: workerEvent.ID, Topic: workerEvent.Topic, Source: domain.TopicEventSourceWebhook,
		Payload: map[string]any{"eventId": workerEvent.ID}, CreatedAt: time.Now().UTC(),
	})
	engine.waitStarted(t, routerTopic)
	engine.waitStarted(t, workerTopic)

	routerDeliveries, err := store.ListEventDeliveries(ctx, []string{routerEvent.ID})
	if err != nil {
		t.Fatalf("list router event deliveries: %v", err)
	}
	if len(routerDeliveries) != 1 || strings.TrimSpace(routerDeliveries[0].RunID) == "" {
		t.Fatalf("router event deliveries = %#v", routerDeliveries)
	}
	workerDeliveries, err := store.ListEventDeliveries(ctx, []string{workerEvent.ID})
	if err != nil {
		t.Fatalf("list worker event deliveries: %v", err)
	}
	if len(workerDeliveries) != 1 || strings.TrimSpace(workerDeliveries[0].RunID) == "" {
		t.Fatalf("worker event deliveries = %#v", workerDeliveries)
	}

	// Only the router source's token is presented. Before this fix,
	// ListDescendantEventIDs never found workerEvent (no parent_event_id
	// link), so its run would be left running.
	request := httptest.NewRequest(http.MethodPost, "/api/webhooks/events/"+routerEvent.ID+"/stop", strings.NewReader(`{"reason":"integration stop"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+routerToken)
	recorder := httptest.NewRecorder()
	responseDone := make(chan struct{})
	go func() {
		app.ServeHTTP(recorder, request)
		close(responseDone)
	}()
	select {
	case <-responseDone:
	case <-time.After(time.Second):
		engine.releaseAll()
		<-responseDone
		t.Fatal("stop request waited for scheduler execution to reach a terminal state")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("stop status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		StopRequested bool `json:"stop_requested"`
		RequestedRuns int  `json:"requested_runs"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode stop response: %v", err)
	}
	if !response.StopRequested || response.RequestedRuns != 2 {
		t.Fatalf("stop response = %#v, want both the router and the correlated worker run requested", response)
	}

	engine.waitCanceled(t, routerTopic)
	engine.waitCanceled(t, workerTopic)
	engine.releaseAll()

	stopCtx, cancelStop := context.WithTimeout(ctx, time.Second)
	defer cancelStop()
	routerRun, _, err := controller.SchedulerRuns().StopSchedulerRun(stopCtx, routerScheduler.Summary.ID, routerDeliveries[0].RunID, "integration stop")
	if err != nil {
		t.Fatalf("load stopped router scheduler run: %v", err)
	}
	if routerRun.Status != domain.SchedulerRunStatusCanceled || routerRun.Error != "integration stop" {
		t.Fatalf("stopped router scheduler run = %#v", routerRun)
	}
	workerRun, _, err := controller.SchedulerRuns().StopSchedulerRun(stopCtx, workerScheduler.Summary.ID, workerDeliveries[0].RunID, "integration stop")
	if err != nil {
		t.Fatalf("load stopped worker scheduler run: %v", err)
	}
	if workerRun.Status != domain.SchedulerRunStatusCanceled || workerRun.Error != "integration stop" {
		t.Fatalf("stopped worker scheduler run = %#v, want it canceled by the correlated stop cascade", workerRun)
	}
}

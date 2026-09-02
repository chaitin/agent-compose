package app

import (
	"context"
	"encoding/json"
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
	const (
		projectID   = "webhook-stop-project"
		agentName   = "worker"
		agentID     = "webhook-stop-agent"
		schedulerID = "webhook-stop-scheduler"
	)
	specJSON, err := (&compose.NormalizedProjectSpec{
		Name: "webhook-stop-project",
		Agents: []compose.NormalizedAgentSpec{{
			Name: agentName, Enabled: true,
			Scheduler: &compose.NormalizedSchedulerSpec{
				Enabled: true, SandboxPolicy: domain.SchedulerSandboxPolicyNew,
				ConcurrencyPolicy: domain.SchedulerConcurrencyPolicyParallel,
				Script:            "scheduler.on('webhook.stop.run', async function () {});",
			},
		}},
	}).MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("marshal project spec: %v", err)
	}
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: projectID, Name: "webhook-stop-project"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	revision, _, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{
		ProjectID: project.ID, SpecHash: "webhook-stop-spec", SpecJSON: string(specJSON),
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
		ID: "webhook-stop-trigger", Kind: domain.SchedulerTriggerKindEvent,
		Topic: "webhook.stop.run", Enabled: true,
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

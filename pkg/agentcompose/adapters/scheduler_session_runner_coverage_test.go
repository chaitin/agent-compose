package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/volumes"
)

func TestSchedulerSandboxRunnerLoadResumeAndShutdownCoverage(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	bridge.config.RuntimeBaseURL = "http://agent-compose.test:7410"
	bridge.config.LLMAPIEndpoint = "https://llm.example.test/v1"
	bridge.config.LLMAPIKey = "provider-key"
	bridge.config.LLMModel = "gpt-scheduler-retry"
	bridge.config.LLMAPIProtocol = "responses"
	projectTestDaemonLLMConfig(t, ctx, bridge)
	publisher := &schedulerSessionPublisherFake{}
	runner := NewSchedulerSandboxRunner(SchedulerSandboxRunnerDeps{
		Config:           bridge.config,
		Store:            bridge.store,
		ConfigDB:         bridge.configDB,
		WorkspaceEnsurer: bridge.workspaceEnsurer,
		Driver:           driver,
		Cap:              nil,
		VolumeResolver:   nil,
		Streams:          bridge.streams,
		Publisher:        publisher,
		CapTokens:        nil,
		AgentExecutor:    bridge.agentExecutor,
	})

	running, err := bridge.store.CreateSandbox(ctx, "running", "", driverpkg.RuntimeDriverBoxlite, "", "", "scheduler", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession running returned error: %v", err)
	}
	running.Summary.VMStatus = domain.VMStatusRunning
	if err := bridge.store.UpdateSandbox(ctx, running); err != nil {
		t.Fatalf("UpdateSession running returned error: %v", err)
	}
	loaded, err := runner.Load(ctx, running.Summary.ID)
	if err != nil || loaded.Summary.ID != running.Summary.ID {
		t.Fatalf("Load loaded=%#v err=%v", loaded, err)
	}
	resumed, eventType, err := runner.LoadOrResume(ctx, running.Summary.ID)
	if err != nil || resumed.Summary.ID != running.Summary.ID || eventType != "" || len(driver.startCalls) != 0 {
		t.Fatalf("LoadOrResume running resumed=%#v event=%q err=%v starts=%#v", resumed, eventType, err, driver.startCalls)
	}

	stopped, err := bridge.store.CreateSandbox(ctx, "stopped", "", driverpkg.RuntimeDriverBoxlite, "", "", "scheduler", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession stopped returned error: %v", err)
	}
	stopped.Summary.VMStatus = domain.VMStatusStopped
	stopped.Summary.Tags = []domain.SandboxTag{{Name: "capset", Value: "dev"}}
	if err := bridge.store.UpdateSandbox(ctx, stopped); err != nil {
		t.Fatalf("UpdateSession stopped returned error: %v", err)
	}
	resumed, eventType, err = runner.LoadOrResume(ctx, stopped.Summary.ID)
	if err != nil || resumed.Summary.VMStatus != domain.VMStatusRunning || eventType != "scheduler.sandbox.resumed" || len(driver.startCalls) != 1 {
		t.Fatalf("LoadOrResume stopped resumed=%#v event=%q err=%v starts=%#v", resumed, eventType, err, driver.startCalls)
	}
	if token := domain.SandboxEnvMap(driver.startSessions[0].RuntimeEnvItems)["AGENT_COMPOSE_SANDBOX_TOKEN"]; token == "" {
		t.Fatal("fresh scheduler retry started without an agent token")
	}
	if len(publisher.events) != 1 || publisher.events[0].Topic != "agent-compose.session.resumed" {
		t.Fatalf("resume publisher events=%#v", publisher.events)
	}

	if err := runner.Shutdown(ctx, ""); err != nil {
		t.Fatalf("Shutdown empty returned error: %v", err)
	}
	if err := runner.Shutdown(ctx, resumed.Summary.ID); err != nil {
		t.Fatalf("Shutdown running returned error: %v", err)
	}
	shutdownLoaded, err := bridge.store.GetSandbox(ctx, resumed.Summary.ID)
	if err != nil {
		t.Fatalf("GetSession shutdown returned error: %v", err)
	}
	if shutdownLoaded.Summary.VMStatus != domain.VMStatusStopped || len(driver.stopCalls) != 1 {
		t.Fatalf("shutdown session=%#v stopCalls=%#v", shutdownLoaded.Summary, driver.stopCalls)
	}
	if err := runner.Shutdown(ctx, resumed.Summary.ID); err != nil {
		t.Fatalf("Shutdown stopped returned error: %v", err)
	}
	if len(driver.stopCalls) != 1 {
		t.Fatalf("Shutdown stopped should not call driver again: %#v", driver.stopCalls)
	}

	if snapshot := toSandboxWorkspaceSnapshot(domain.WorkspaceConfig{ID: "workspace-1", Name: "Workspace", Type: "file", ConfigJSON: "{}"}); snapshot.ID != "workspace-1" || snapshot.Name != "Workspace" {
		t.Fatalf("toSandboxWorkspaceSnapshot = %#v", snapshot)
	}
	if workspace, err := runner.workspaceSnapshot(ctx, ""); err != nil || workspace != nil {
		t.Fatalf("workspaceSnapshot empty workspace=%#v err=%v", workspace, err)
	}
	if driverName, err := runner.driver(domain.SchedulerAgentRequest{Driver: driverpkg.RuntimeDriverDocker}, domain.Scheduler{}, nil); err != nil || driverName != driverpkg.RuntimeDriverDocker {
		t.Fatalf("driver override=%q err=%v", driverName, err)
	}
	if image := runner.guestImage(domain.SchedulerAgentRequest{GuestImage: "request:latest"}, domain.Scheduler{Summary: domain.SchedulerSummary{GuestImage: "scheduler:latest"}}, &domain.AgentDefinition{GuestImage: "agent:latest"}, driverpkg.RuntimeDriverDocker); image != "request:latest" {
		t.Fatalf("guestImage = %q", image)
	}
}

func TestSchedulerSandboxRunnerReleasedRuntimeResumePreparesAgentEnvironment(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	bridge.config.RuntimeBaseURL = "http://agent-compose.test:7410"
	bridge.config.LLMAPIEndpoint = "https://llm.example.test/v1"
	bridge.config.LLMAPIKey = "provider-key"
	bridge.config.LLMModel = "gpt-scheduler-retry"
	bridge.config.LLMAPIProtocol = "responses"
	projectTestDaemonLLMConfig(t, ctx, bridge)
	runner := NewSchedulerSandboxRunner(SchedulerSandboxRunnerDeps{
		Config:           bridge.config,
		Store:            bridge.store,
		ConfigDB:         bridge.configDB,
		WorkspaceEnsurer: bridge.workspaceEnsurer,
		Driver:           driver,
		Cap:              nil,
		VolumeResolver:   nil,
		Streams:          bridge.streams,
		Publisher:        nil,
		CapTokens:        nil,
		AgentExecutor:    bridge.agentExecutor,
	})

	released, err := bridge.store.CreateSandbox(ctx, "released", "", driverpkg.RuntimeDriverBoxlite, "", "", "scheduler", nil, nil, []domain.SandboxTag{
		{Name: domain.AgentSandboxTagProvider, Value: "codex"},
	})
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	released.Summary.VMStatus = domain.VMStatusStopped
	released.StoppedRuntime = &domain.StoppedRuntime{State: domain.StoppedRuntimeStateReleased, ReleasedAt: time.Now().UTC()}
	if err := bridge.store.UpdateSandbox(ctx, released); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}
	if err := bridge.store.SaveVMState(released.Summary.ID, domain.VMState{
		Driver:    driverpkg.RuntimeDriverBoxlite,
		StartedAt: time.Now().Add(-time.Minute).UTC(),
	}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}

	resumed, eventType, err := runner.LoadOrResume(ctx, released.Summary.ID)
	if err != nil {
		t.Fatalf("LoadOrResume returned error: %v", err)
	}
	if resumed.Summary.VMStatus != domain.VMStatusRunning || eventType != "scheduler.sandbox.resumed" || len(driver.startSessions) != 1 {
		t.Fatalf("resumed=%#v event=%q starts=%d", resumed, eventType, len(driver.startSessions))
	}
	if token := domain.SandboxEnvMap(driver.startSessions[0].RuntimeEnvItems)["AGENT_COMPOSE_SANDBOX_TOKEN"]; token == "" {
		t.Fatal("released runtime recreation started without a replacement agent token")
	}
}

func TestSchedulerSandboxRunnerRuntimeReleaseFailureFinalizesConfirmedStop(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	releaseErr := errors.New("runtime release failed")
	driver.releaseErr = releaseErr
	publisher := &schedulerSessionPublisherFake{}
	resolver := NewCapabilitySandboxResolver(bridge.store)
	resolver.initialized = true
	runner := NewSchedulerSandboxRunner(SchedulerSandboxRunnerDeps{
		Config:           bridge.config,
		Store:            bridge.store,
		ConfigDB:         bridge.configDB,
		WorkspaceEnsurer: bridge.workspaceEnsurer,
		Driver:           driver,
		Cap:              nil,
		VolumeResolver:   nil,
		Streams:          bridge.streams,
		Publisher:        publisher,
		CapTokens:        resolver,
		AgentExecutor:    bridge.agentExecutor,
	})

	const capabilityToken = "capability-token"
	sandbox, err := bridge.store.CreateSandbox(ctx, "release failure", "", driverpkg.RuntimeDriverBoxlite, "", "", "scheduler", nil,
		[]domain.SandboxEnvVar{{Name: capabilities.SandboxTokenEnvName, Value: capabilityToken, Secret: true}},
		[]domain.SandboxTag{{Name: capabilities.CapsetTagName, Value: "dev"}},
	)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	sandbox.Summary.VMStatus = domain.VMStatusRunning
	sandbox.StoppedRuntimePolicy = domain.StoppedRuntimePolicyRemove
	if err := bridge.store.UpdateSandbox(ctx, sandbox); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}
	resolver.IndexSandbox(sandbox, nil)
	if _, ok := resolver.tokens[capabilityToken]; !ok {
		t.Fatal("capability token was not indexed before shutdown")
	}

	if err := runner.Shutdown(ctx, sandbox.Summary.ID); !errors.Is(err, releaseErr) {
		t.Fatalf("Shutdown error = %v, want %v", err, releaseErr)
	}
	loaded, err := bridge.store.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	if loaded.Summary.VMStatus != domain.VMStatusStopped || sandboxes.EffectiveStoppedRuntimeState(loaded) != domain.StoppedRuntimeStateReleasePending {
		t.Fatalf("stopped sandbox = %#v release=%#v", loaded.Summary, loaded.StoppedRuntime)
	}
	if _, ok := resolver.tokens[capabilityToken]; ok {
		t.Fatal("capability token remains indexed after confirmed stop")
	}
	if len(publisher.events) != 1 || publisher.events[0].Topic != "agent-compose.session.stopped" {
		t.Fatalf("publisher events = %#v, want one stopped event", publisher.events)
	}
	events, err := bridge.store.ListEvents(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	var releaseFailed, stopped bool
	for _, event := range events {
		switch event.Type {
		case "sandbox.runtime_release_failed":
			releaseFailed = true
		case "sandbox.stopped":
			stopped = event.Message == "sandbox stopped; runtime release pending"
		}
	}
	if !releaseFailed || !stopped {
		t.Fatalf("events = %#v, want release failure and pending stopped event", events)
	}
}

func TestSchedulerSandboxRunnerRejectsUnsupportedStickyResumeBeforeSideEffects(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	runner := NewSchedulerSandboxRunner(SchedulerSandboxRunnerDeps{
		Config:           bridge.config,
		Store:            bridge.store,
		ConfigDB:         bridge.configDB,
		WorkspaceEnsurer: bridge.workspaceEnsurer,
		Driver:           driver,
		Cap:              nil,
		VolumeResolver:   nil,
		Streams:          bridge.streams,
		Publisher:        nil,
		CapTokens:        nil,
		AgentExecutor:    bridge.agentExecutor,
	})
	session, err := bridge.store.CreateSandbox(ctx, "historical sticky", "", driverpkg.RuntimeDriverMicrosandbox, "", "", "scheduler", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusStopped
	session.Summary.RuntimeRef = "original-runtime-ref"
	if err := bridge.store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}
	driver.validateErr = domain.ClassifyError(domain.ErrUnsupported, "", driverpkg.ErrRuntimeDriverNotCompiled)

	_, _, err = runner.LoadOrResume(ctx, session.Summary.ID)
	if !errors.Is(err, domain.ErrUnsupported) || !errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) {
		t.Fatalf("LoadOrResume error = %v, want unsupported runtime", err)
	}
	loaded, err := bridge.store.GetSandbox(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	if loaded.Summary.VMStatus != domain.VMStatusStopped || loaded.Summary.Driver != driverpkg.RuntimeDriverMicrosandbox || loaded.Summary.RuntimeRef != "original-runtime-ref" {
		t.Fatalf("unsupported sticky resume changed summary: %#v", loaded.Summary)
	}
	if len(driver.startCalls) != 0 {
		t.Fatalf("unsupported sticky resume called StartSandboxVM: %#v", driver.startCalls)
	}
	events, err := bridge.store.ListEvents(ctx, session.Summary.ID)
	if err != nil || len(events) != 0 {
		t.Fatalf("events after unsupported sticky resume = %#v, %v", events, err)
	}
}

func TestSchedulerSandboxRunnerRejectsUncompiledDriverBeforePersistence(t *testing.T) {
	for _, runtimeDriver := range []string{driverpkg.RuntimeDriverBoxlite, driverpkg.RuntimeDriverMicrosandbox} {
		t.Run(runtimeDriver, func(t *testing.T) {
			rawErr := driverpkg.ValidateCompiledRuntimeDriver(runtimeDriver)
			if rawErr == nil {
				t.Skipf("runtime driver %s is compiled in this build", runtimeDriver)
			}
			ctx := context.Background()
			bridge, sandboxDriver := newTestSandboxRPCBridge(t)
			publisher := &schedulerSessionPublisherFake{}
			runner := NewSchedulerSandboxRunner(SchedulerSandboxRunnerDeps{
				Config:           bridge.config,
				Store:            bridge.store,
				ConfigDB:         bridge.configDB,
				WorkspaceEnsurer: bridge.workspaceEnsurer,
				Driver:           sandboxDriver,
				Cap:              nil,
				VolumeResolver:   nil,
				Streams:          bridge.streams,
				Publisher:        publisher,
				CapTokens:        nil,
				AgentExecutor:    bridge.agentExecutor,
			})
			scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{
				ID:            "scheduler-uncompiled-" + runtimeDriver,
				Name:          "Uncompiled " + runtimeDriver,
				Driver:        runtimeDriver,
				SandboxPolicy: domain.SchedulerSandboxPolicySticky,
			}}
			scheduler = createNativeTestScheduler(t, ctx, bridge.configDB, scheduler)
			triggerID := "trigger-uncompiled"
			originalBinding := domain.SchedulerBinding{SchedulerID: scheduler.Summary.ID, TriggerID: triggerID, SandboxID: "missing-original-sandbox"}
			if err := bridge.configDB.UpsertSchedulerBinding(ctx, originalBinding); err != nil {
				t.Fatalf("UpsertSchedulerBinding returned error: %v", err)
			}
			originalBinding, found, err := bridge.configDB.GetSchedulerBinding(ctx, scheduler.Summary.ID, triggerID)
			if err != nil || !found {
				t.Fatalf("GetSchedulerBinding before Ensure returned binding=%#v found=%v err=%v", originalBinding, found, err)
			}
			beforeSandboxes, err := bridge.store.ListSandboxes(ctx, domain.SandboxListOptions{})
			if err != nil {
				t.Fatalf("ListSandboxes before Ensure returned error: %v", err)
			}
			beforeEntries := sandboxRootEntryNames(t, bridge.config.SandboxRoot)
			_, _, err = runner.Ensure(ctx, scheduler, domain.SchedulerAgentRequest{BindingTriggerID: triggerID}, false)
			if !errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) || !errors.Is(err, domain.ErrUnsupported) {
				t.Fatalf("Ensure error = %v, want typed unsupported error", err)
			}
			var notCompiled *driverpkg.RuntimeDriverNotCompiledError
			if !errors.As(err, &notCompiled) || notCompiled.Driver != runtimeDriver {
				t.Fatalf("Ensure typed error = %#v, want driver %q", notCompiled, runtimeDriver)
			}
			afterSandboxes, err := bridge.store.ListSandboxes(ctx, domain.SandboxListOptions{})
			if err != nil || len(afterSandboxes.Sandboxes) != len(beforeSandboxes.Sandboxes) {
				t.Fatalf("sandboxes changed: before=%d after=%d err=%v", len(beforeSandboxes.Sandboxes), len(afterSandboxes.Sandboxes), err)
			}
			binding, found, err := bridge.configDB.GetSchedulerBinding(ctx, scheduler.Summary.ID, triggerID)
			if err != nil || !found || binding != originalBinding {
				t.Fatalf("binding changed: got=%#v found=%v err=%v, want %#v", binding, found, err, originalBinding)
			}
			events, err := bridge.configDB.ListSchedulerEvents(ctx, scheduler.Summary.ID, 100)
			if err != nil || len(events) != 0 || len(publisher.events) != 0 {
				t.Fatalf("events changed: persisted=%#v published=%#v err=%v", events, publisher.events, err)
			}
			if afterEntries := sandboxRootEntryNames(t, bridge.config.SandboxRoot); !reflect.DeepEqual(afterEntries, beforeEntries) {
				t.Fatalf("sandbox artifacts changed: before=%#v after=%#v", beforeEntries, afterEntries)
			}
			if len(sandboxDriver.startCalls) != 0 {
				t.Fatalf("unsupported Ensure called StartSandboxVM: %#v", sandboxDriver.startCalls)
			}
		})
	}
}

func sandboxRootEntryNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir(%s) returned error: %v", root, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestSchedulerSandboxRunnerResolvesVolumeMounts(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	hostPath := t.TempDir()
	resolver := &schedulerVolumeResolverFake{
		mounts: []domain.SandboxVolumeMount{{
			ID:       "mount-cache",
			Type:     domain.VolumeMountTypeVolume,
			Source:   "request-cache",
			Target:   "/cache",
			VolumeID: "vol-cache",
			Driver:   domain.VolumeDriverLocal,
			HostPath: hostPath,
		}},
		warnings: []string{"volume target /cache overlaps test path"},
	}
	runner := NewSchedulerSandboxRunner(SchedulerSandboxRunnerDeps{
		Config:           bridge.config,
		Store:            bridge.store,
		ConfigDB:         bridge.configDB,
		WorkspaceEnsurer: bridge.workspaceEnsurer,
		Driver:           driver,
		Cap:              nil,
		VolumeResolver:   resolver,
		Streams:          bridge.streams,
		Publisher:        nil,
		CapTokens:        nil,
		AgentExecutor:    bridge.agentExecutor,
	})
	projectRoot := t.TempDir()
	projectPath := filepath.Join(projectRoot, "agent-compose.yml")
	if _, err := bridge.configDB.UpsertProject(ctx, domain.ProjectRecord{ID: "project-1", Name: "project", SourcePath: projectPath}); err != nil {
		t.Fatalf("UpsertProject returned error: %v", err)
	}
	projectVolume, err := bridge.configDB.CreateVolume(ctx, domain.VolumeRecord{ID: "vol-request-cache", Name: "project_request-cache", Driver: domain.VolumeDriverLocal, Path: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateVolume returned error: %v", err)
	}
	if err := bridge.configDB.UpsertProjectVolume(ctx, "project-1", "request-cache", domain.ProjectVolumeLink{VolumeID: projectVolume.ID}); err != nil {
		t.Fatalf("UpsertProjectVolume returned error: %v", err)
	}
	scheduler := domain.Scheduler{
		Summary: domain.SchedulerSummary{ID: "scheduler-1", Name: "Scheduler", Driver: driverpkg.RuntimeDriverDocker, ProjectID: "project-1", AgentName: "reviewer", ProjectSchedulerID: "scheduler-1"},
		Volumes: []domain.VolumeMountSpec{{
			Type:   domain.VolumeMountTypeVolume,
			Source: "scheduler-cache",
			Target: "/cache",
		}},
	}
	request := domain.SchedulerAgentRequest{
		SandboxPolicy: domain.SchedulerSandboxPolicyNew,
		Volumes: []domain.VolumeMountSpec{{
			Type:   domain.VolumeMountTypeVolume,
			Source: "request-cache",
			Target: "/cache",
		}},
	}
	session, eventType, err := runner.Ensure(ctx, scheduler, request, false)
	if err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	if eventType != "scheduler.sandbox.created" || len(driver.startCalls) != 1 {
		t.Fatalf("eventType=%q startCalls=%#v", eventType, driver.startCalls)
	}
	if len(resolver.specs) != 1 || resolver.specs[0].Source != "request-cache" {
		t.Fatalf("resolver specs = %#v", resolver.specs)
	}
	if resolver.options.ProjectVolumes["request-cache"].ID != projectVolume.ID {
		t.Fatalf("resolver project volumes = %#v", resolver.options.ProjectVolumes)
	}
	if resolver.options.ProjectRoot != projectRoot {
		t.Fatalf("resolver project root = %q, want %q", resolver.options.ProjectRoot, projectRoot)
	}
	if len(session.VolumeMounts) != 1 || session.VolumeMounts[0].HostPath != hostPath {
		t.Fatalf("session volume mounts = %#v", session.VolumeMounts)
	}
	tags := make(map[string]string)
	for _, tag := range session.Summary.Tags {
		tags[tag.Name] = tag.Value
	}
	if tags["origin"] != "scheduler" || tags["project"] != "project-1" || tags["project_id"] != "project-1" || tags["agent"] != "reviewer" || tags["scheduler_id"] != "scheduler-1" || tags["scheduler_name"] != "Scheduler" || tags["loader_id"] != "" || tags["loader_name"] != "" {
		t.Fatalf("managed scheduler sandbox tags = %#v", tags)
	}
	events, err := bridge.store.ListEvents(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	var foundWarning bool
	for _, event := range events {
		if event.Type == "sandbox.volume.warning" {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("expected sandbox.volume.warning event, got %#v", events)
	}
}

func TestIntegrationSchedulerSandboxRunnerLoadResumeAndShutdownCoverage(t *testing.T) {
	TestSchedulerSandboxRunnerLoadResumeAndShutdownCoverage(t)
	TestSchedulerSandboxRunnerRejectsUncompiledDriverBeforePersistence(t)
	TestSchedulerSandboxRunnerResolvesVolumeMounts(t)
}

func TestE2ESchedulerSandboxRunnerLoadResumeAndShutdownCoverage(t *testing.T) {
	TestSchedulerSandboxRunnerLoadResumeAndShutdownCoverage(t)
	TestSchedulerSandboxRunnerRejectsUncompiledDriverBeforePersistence(t)
	TestSchedulerSandboxRunnerResolvesVolumeMounts(t)
}

type schedulerSessionPublisherFake struct {
	mu     sync.Mutex
	events []domain.SchedulerTopicEvent
}

func (p *schedulerSessionPublisherFake) Publish(event domain.SchedulerTopicEvent) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return true
}

var _ schedulers.ControllerPublisher = (*schedulerSessionPublisherFake)(nil)

type schedulerVolumeResolverFake struct {
	specs    []domain.VolumeMountSpec
	options  volumes.ResolveOptions
	mounts   []domain.SandboxVolumeMount
	warnings []string
	err      error
}

func (r *schedulerVolumeResolverFake) ResolveMounts(_ context.Context, specs []domain.VolumeMountSpec, options volumes.ResolveOptions) ([]domain.SandboxVolumeMount, []string, error) {
	r.specs = append([]domain.VolumeMountSpec(nil), specs...)
	r.options = options
	if r.err != nil {
		return nil, nil, r.err
	}
	return append([]domain.SandboxVolumeMount(nil), r.mounts...), append([]string(nil), r.warnings...), nil
}

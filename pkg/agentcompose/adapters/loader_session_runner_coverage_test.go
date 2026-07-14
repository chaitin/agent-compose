package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/volumes"
)

func TestLoaderSandboxRunnerLoadResumeAndShutdownCoverage(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	publisher := &loaderSessionPublisherFake{}
	runner := NewLoaderSandboxRunner(bridge.config, bridge.store, bridge.configDB, bridge.workspaceEnsurer, driver, nil, nil, bridge.streams, publisher, nil)

	running, err := bridge.store.CreateSandbox(ctx, "running", "", driverpkg.RuntimeDriverBoxlite, "", "", "loader", nil, nil, nil)
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

	stopped, err := bridge.store.CreateSandbox(ctx, "stopped", "", driverpkg.RuntimeDriverBoxlite, "", "", "loader", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession stopped returned error: %v", err)
	}
	stopped.Summary.VMStatus = domain.VMStatusStopped
	stopped.Summary.Tags = []domain.SandboxTag{{Name: "capset", Value: "dev"}}
	if err := bridge.store.UpdateSandbox(ctx, stopped); err != nil {
		t.Fatalf("UpdateSession stopped returned error: %v", err)
	}
	resumed, eventType, err = runner.LoadOrResume(ctx, stopped.Summary.ID)
	if err != nil || resumed.Summary.VMStatus != domain.VMStatusRunning || eventType != "loader.sandbox.resumed" || len(driver.startCalls) != 1 {
		t.Fatalf("LoadOrResume stopped resumed=%#v event=%q err=%v starts=%#v", resumed, eventType, err, driver.startCalls)
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
	if driverName, err := runner.driver(domain.LoaderAgentRequest{Driver: driverpkg.RuntimeDriverDocker}, domain.Loader{}, nil); err != nil || driverName != driverpkg.RuntimeDriverDocker {
		t.Fatalf("driver override=%q err=%v", driverName, err)
	}
	if image := runner.guestImage(domain.LoaderAgentRequest{GuestImage: "request:latest"}, domain.Loader{Summary: domain.LoaderSummary{GuestImage: "loader:latest"}}, &domain.AgentDefinition{GuestImage: "agent:latest"}, driverpkg.RuntimeDriverDocker); image != "request:latest" {
		t.Fatalf("guestImage = %q", image)
	}
}

func TestLoaderSandboxRunnerRejectsUnsupportedStickyResumeBeforeSideEffects(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	runner := NewLoaderSandboxRunner(bridge.config, bridge.store, bridge.configDB, bridge.workspaceEnsurer, driver, nil, nil, bridge.streams, nil, nil)
	session, err := bridge.store.CreateSandbox(ctx, "historical sticky", "", driverpkg.RuntimeDriverMicrosandbox, "", "", "loader", nil, nil, nil)
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

func TestLoaderSandboxRunnerRejectsUncompiledDriverBeforePersistence(t *testing.T) {
	for _, runtimeDriver := range []string{driverpkg.RuntimeDriverBoxlite, driverpkg.RuntimeDriverMicrosandbox} {
		t.Run(runtimeDriver, func(t *testing.T) {
			rawErr := driverpkg.ValidateCompiledRuntimeDriver(runtimeDriver)
			if rawErr == nil {
				t.Skipf("runtime driver %s is compiled in this build", runtimeDriver)
			}
			ctx := context.Background()
			bridge, sandboxDriver := newTestSandboxRPCBridge(t)
			publisher := &loaderSessionPublisherFake{}
			runner := NewLoaderSandboxRunner(bridge.config, bridge.store, bridge.configDB, bridge.workspaceEnsurer, sandboxDriver, nil, nil, bridge.streams, publisher, nil)
			loader := domain.Loader{Summary: domain.LoaderSummary{
				ID:            "loader-uncompiled-" + runtimeDriver,
				Name:          "Uncompiled " + runtimeDriver,
				Driver:        runtimeDriver,
				SandboxPolicy: domain.LoaderSandboxPolicySticky,
			}}
			triggerID := "trigger-uncompiled"
			originalBinding := domain.LoaderBinding{LoaderID: loader.Summary.ID, TriggerID: triggerID, SandboxID: "missing-original-sandbox"}
			if err := bridge.configDB.UpsertLoaderBinding(ctx, originalBinding); err != nil {
				t.Fatalf("UpsertLoaderBinding returned error: %v", err)
			}
			originalBinding, found, err := bridge.configDB.GetLoaderBinding(ctx, loader.Summary.ID, triggerID)
			if err != nil || !found {
				t.Fatalf("GetLoaderBinding before Ensure returned binding=%#v found=%v err=%v", originalBinding, found, err)
			}
			beforeSandboxes, err := bridge.store.ListSandboxes(ctx, domain.SandboxListOptions{})
			if err != nil {
				t.Fatalf("ListSandboxes before Ensure returned error: %v", err)
			}
			beforeEntries := sandboxRootEntryNames(t, bridge.config.SandboxRoot)
			_, _, err = runner.Ensure(ctx, loader, domain.LoaderAgentRequest{BindingTriggerID: triggerID}, false)
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
			binding, found, err := bridge.configDB.GetLoaderBinding(ctx, loader.Summary.ID, triggerID)
			if err != nil || !found || binding != originalBinding {
				t.Fatalf("binding changed: got=%#v found=%v err=%v, want %#v", binding, found, err, originalBinding)
			}
			events, err := bridge.configDB.ListLoaderEvents(ctx, loader.Summary.ID, 100)
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

func TestLoaderSandboxRunnerResolvesVolumeMounts(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	hostPath := t.TempDir()
	resolver := &loaderVolumeResolverFake{
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
	runner := NewLoaderSandboxRunner(bridge.config, bridge.store, bridge.configDB, bridge.workspaceEnsurer, driver, nil, resolver, bridge.streams, nil, nil)
	projectRoot := t.TempDir()
	projectPath := filepath.Join(projectRoot, "agent-compose.yml")
	if _, err := bridge.configDB.UpsertProject(ctx, domain.ProjectRecord{ID: "project-1", Name: "Project", SourcePath: projectPath}); err != nil {
		t.Fatalf("UpsertProject returned error: %v", err)
	}
	projectVolume, err := bridge.configDB.CreateVolume(ctx, domain.VolumeRecord{ID: "vol-request-cache", Name: "project_request-cache", Driver: domain.VolumeDriverLocal, Path: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateVolume returned error: %v", err)
	}
	if err := bridge.configDB.UpsertProjectVolume(ctx, "project-1", "request-cache", projectVolume.ID, false); err != nil {
		t.Fatalf("UpsertProjectVolume returned error: %v", err)
	}
	loader := domain.Loader{
		Summary: domain.LoaderSummary{ID: "loader-1", Name: "Loader", Driver: driverpkg.RuntimeDriverDocker, ManagedProjectID: "project-1"},
		Volumes: []domain.VolumeMountSpec{{
			Type:   domain.VolumeMountTypeVolume,
			Source: "loader-cache",
			Target: "/cache",
		}},
	}
	request := domain.LoaderAgentRequest{
		SandboxPolicy: domain.LoaderSandboxPolicyNew,
		Volumes: []domain.VolumeMountSpec{{
			Type:   domain.VolumeMountTypeVolume,
			Source: "request-cache",
			Target: "/cache",
		}},
	}
	session, eventType, err := runner.Ensure(ctx, loader, request, false)
	if err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	if eventType != "loader.sandbox.created" || len(driver.startCalls) != 1 {
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

func TestIntegrationLoaderSandboxRunnerLoadResumeAndShutdownCoverage(t *testing.T) {
	TestLoaderSandboxRunnerLoadResumeAndShutdownCoverage(t)
	TestLoaderSandboxRunnerRejectsUncompiledDriverBeforePersistence(t)
	TestLoaderSandboxRunnerResolvesVolumeMounts(t)
}

func TestE2ELoaderSandboxRunnerLoadResumeAndShutdownCoverage(t *testing.T) {
	TestLoaderSandboxRunnerLoadResumeAndShutdownCoverage(t)
	TestLoaderSandboxRunnerRejectsUncompiledDriverBeforePersistence(t)
	TestLoaderSandboxRunnerResolvesVolumeMounts(t)
}

type loaderSessionPublisherFake struct {
	events []domain.LoaderTopicEvent
}

func (p *loaderSessionPublisherFake) Publish(event domain.LoaderTopicEvent) bool {
	p.events = append(p.events, event)
	return true
}

var _ loaders.ControllerPublisher = (*loaderSessionPublisherFake)(nil)

type loaderVolumeResolverFake struct {
	specs    []domain.VolumeMountSpec
	options  volumes.ResolveOptions
	mounts   []domain.SandboxVolumeMount
	warnings []string
	err      error
}

func (r *loaderVolumeResolverFake) ResolveMounts(_ context.Context, specs []domain.VolumeMountSpec, options volumes.ResolveOptions) ([]domain.SandboxVolumeMount, []string, error) {
	r.specs = append([]domain.VolumeMountSpec(nil), specs...)
	r.options = options
	if r.err != nil {
		return nil, nil, r.err
	}
	return append([]domain.SandboxVolumeMount(nil), r.mounts...), append([]string(nil), r.warnings...), nil
}

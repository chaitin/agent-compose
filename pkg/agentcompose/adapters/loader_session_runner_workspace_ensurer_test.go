package adapters

import (
	"context"
	"errors"
	"strings"
	"testing"

	"agent-compose/pkg/capabilities"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/workspaces"
)

type recordingLoaderWorkspaceEnsurer struct {
	calls             []*domain.Sandbox
	initialIDs        []string
	initialWorkspaces []*domain.SandboxWorkspace
	initialStatuses   []string
	err               error
	ensure            func(context.Context, *domain.Sandbox) error
}

var _ workspaces.WorkspaceEnsurer = (*recordingLoaderWorkspaceEnsurer)(nil)

func (e *recordingLoaderWorkspaceEnsurer) Ensure(ctx context.Context, sandbox *domain.Sandbox) error {
	e.calls = append(e.calls, sandbox)
	e.initialIDs = append(e.initialIDs, sandbox.Summary.ID)
	if sandbox.Workspace == nil {
		e.initialWorkspaces = append(e.initialWorkspaces, nil)
	} else {
		workspace := *sandbox.Workspace
		e.initialWorkspaces = append(e.initialWorkspaces, &workspace)
	}
	if sandbox.WorkspaceProvisioning == nil {
		e.initialStatuses = append(e.initialStatuses, "")
	} else {
		e.initialStatuses = append(e.initialStatuses, sandbox.WorkspaceProvisioning.Status)
	}
	if e.ensure != nil {
		if err := e.ensure(ctx, sandbox); err != nil {
			return err
		}
	}
	return e.err
}

func TestLoaderSandboxRunnerEnsureUsesWorkspaceEnsurerBeforeGuideAndDriver(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	workspace, err := bridge.configDB.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{
		ID:         "loader-create-workspace",
		Name:       "Loader Create Workspace",
		Type:       "file",
		ConfigJSON: `{"root":"source-v1"}`,
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}

	order := []string{}
	ensurer := &recordingLoaderWorkspaceEnsurer{ensure: func(ctx context.Context, sandbox *domain.Sandbox) error {
		order = append(order, "ensure")
		if len(driver.startCalls) != 0 {
			t.Fatalf("driver starts before workspace ready = %#v", driver.startCalls)
		}
		if err := domain.TransitionSandboxWorkspaceProvisioning(sandbox, domain.SandboxWorkspaceProvisioningStatusReady); err != nil {
			return err
		}
		return bridge.store.UpdateSandbox(ctx, sandbox)
	}}
	bridge.cap = testCapabilityProvider{guide: func(_ context.Context, _ string) ([]byte, error) {
		order = append(order, "guide")
		if len(ensurer.calls) != 1 || ensurer.calls[0].WorkspaceProvisioning == nil || ensurer.calls[0].WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
			t.Fatalf("capability guide ran before workspace ready: %#v", ensurer.calls)
		}
		return []byte("# loader capability guide"), nil
	}}
	driver.onStart = func(sandbox *domain.Sandbox) {
		order = append(order, "driver.start")
		if sandbox.WorkspaceProvisioning == nil || sandbox.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
			t.Fatalf("driver started with provisioning = %#v, want ready", sandbox.WorkspaceProvisioning)
		}
	}
	publisher := &loaderSessionPublisherFake{}
	runner := NewLoaderSandboxRunner(bridge.config, bridge.store, bridge.configDB, ensurer, driver, bridge.cap, nil, bridge.streams, publisher, nil, bridge.agentExecutor)
	loader := domain.Loader{Summary: domain.LoaderSummary{
		ID:            "loader-create",
		Name:          "Loader Create",
		WorkspaceID:   workspace.ID,
		Driver:        driverpkg.RuntimeDriverDocker,
		SandboxPolicy: domain.LoaderSandboxPolicySticky,
		CapsetIDs:     []string{"dev"},
	}}

	sandbox, eventType, err := runner.Ensure(ctx, loader, domain.LoaderAgentRequest{BindingTriggerID: "trigger-create"}, false)
	if err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	if eventType != "loader.sandbox.created" {
		t.Fatalf("Ensure event type = %q, want loader.sandbox.created", eventType)
	}
	if len(ensurer.calls) != 1 || ensurer.initialIDs[0] != sandbox.Summary.ID {
		t.Fatalf("workspace Ensure calls = %#v ids=%#v, want sandbox %q once", ensurer.calls, ensurer.initialIDs, sandbox.Summary.ID)
	}
	if ensurer.initialStatuses[0] != domain.SandboxWorkspaceProvisioningStatusPending {
		t.Fatalf("workspace Ensure initial status = %q, want pending", ensurer.initialStatuses[0])
	}
	initialWorkspace := ensurer.initialWorkspaces[0]
	if initialWorkspace == nil || initialWorkspace.ID != workspace.ID || initialWorkspace.ConfigJSON != workspace.ConfigJSON {
		t.Fatalf("workspace Ensure snapshot = %#v, want identity of %#v", initialWorkspace, workspace)
	}
	if len(driver.startCalls) != 1 || len(driver.startSessions) != 1 || driver.startSessions[0] != ensurer.calls[0] {
		t.Fatalf("driver starts = %#v sessions=%#v, want same ensured sandbox once", driver.startCalls, driver.startSessions)
	}
	if got := strings.Join(order, ","); got != "ensure,guide,driver.start" {
		t.Fatalf("loader create order = %q, want ensure,guide,driver.start", got)
	}
	if sandbox.Summary.VMStatus != domain.VMStatusRunning || sandbox.WorkspaceProvisioning == nil || sandbox.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("created sandbox state = vm:%q provisioning:%#v", sandbox.Summary.VMStatus, sandbox.WorkspaceProvisioning)
	}
	if execution.SessionTagValue(sandbox.Summary.Tags, domain.AgentSandboxTagProvider) != domain.DefaultAgentProvider {
		t.Fatalf("sandbox provider tag = %#v", sandbox.Summary.Tags)
	}
	binding, ok, err := bridge.configDB.GetLoaderBinding(ctx, loader.Summary.ID, "trigger-create")
	if err != nil || !ok || binding.SandboxID != sandbox.Summary.ID {
		t.Fatalf("loader binding = %#v ok=%v err=%v, want sandbox %q", binding, ok, err, sandbox.Summary.ID)
	}
	assertLoaderLifecycleEvidence(t, bridge, publisher, sandbox.Summary.ID, "sandbox.created", "agent-compose.session.created")
}

func TestLoaderSandboxRunnerEnsureWorkspaceEnsurerErrorShortCircuitsDriver(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	ensureErr := errors.New("loader workspace provisioning failed")
	ensurer := &recordingLoaderWorkspaceEnsurer{err: ensureErr}
	guideCalls := 0
	capabilityProvider := testCapabilityProvider{guide: func(context.Context, string) ([]byte, error) {
		guideCalls++
		return []byte("unexpected guide"), nil
	}}
	publisher := &loaderSessionPublisherFake{}
	runner := NewLoaderSandboxRunner(bridge.config, bridge.store, bridge.configDB, ensurer, driver, capabilityProvider, nil, bridge.streams, publisher, nil, bridge.agentExecutor)
	loader := domain.Loader{Summary: domain.LoaderSummary{
		ID:            "loader-ensure-error",
		Name:          "Loader Ensure Error",
		Driver:        driverpkg.RuntimeDriverDocker,
		SandboxPolicy: domain.LoaderSandboxPolicyNew,
		CapsetIDs:     []string{"dev"},
	}}

	_, _, err := runner.Ensure(ctx, loader, domain.LoaderAgentRequest{}, false)
	if !errors.Is(err, ensureErr) {
		t.Fatalf("Ensure error = %v, want %v", err, ensureErr)
	}
	if len(ensurer.calls) != 1 {
		t.Fatalf("workspace Ensure call count = %d, want 1", len(ensurer.calls))
	}
	if len(driver.startCalls) != 0 || guideCalls != 0 {
		t.Fatalf("driver/guide calls after workspace error = %d/%d, want 0/0", len(driver.startCalls), guideCalls)
	}
	persisted, loadErr := bridge.store.GetSandbox(ctx, ensurer.initialIDs[0])
	if loadErr != nil {
		t.Fatalf("GetSandbox after workspace error returned error: %v", loadErr)
	}
	if persisted.Summary.VMStatus != domain.VMStatusFailed {
		t.Fatalf("persisted VM status = %q, want failed", persisted.Summary.VMStatus)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("publisher events after workspace error = %#v, want none", publisher.events)
	}
	events, listErr := bridge.store.ListEvents(ctx, persisted.Summary.ID)
	if listErr != nil || len(events) != 0 {
		t.Fatalf("sandbox events after workspace error = %#v err=%v, want none", events, listErr)
	}
}

func TestLoaderSandboxRunnerEnsureRuntimeFailurePreservesReadyProvisioning(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	workspace, err := bridge.configDB.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{
		ID:         "loader-runtime-failure-workspace",
		Name:       "Loader Runtime Failure Workspace",
		Type:       "file",
		ConfigJSON: `{"root":"unused-by-fake"}`,
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}
	ensurer := &recordingLoaderWorkspaceEnsurer{ensure: func(ctx context.Context, sandbox *domain.Sandbox) error {
		if err := domain.TransitionSandboxWorkspaceProvisioning(sandbox, domain.SandboxWorkspaceProvisioningStatusReady); err != nil {
			return err
		}
		return bridge.store.UpdateSandbox(ctx, sandbox)
	}}
	startErr := errors.New("loader runtime start failed")
	driver.startErr = startErr
	runner := NewLoaderSandboxRunner(bridge.config, bridge.store, bridge.configDB, ensurer, driver, nil, nil, bridge.streams, nil, nil, bridge.agentExecutor)
	loader := domain.Loader{Summary: domain.LoaderSummary{
		ID:            "loader-runtime-error",
		Name:          "Loader Runtime Error",
		WorkspaceID:   workspace.ID,
		Driver:        driverpkg.RuntimeDriverDocker,
		SandboxPolicy: domain.LoaderSandboxPolicyNew,
	}}

	_, _, err = runner.Ensure(ctx, loader, domain.LoaderAgentRequest{}, false)
	if !errors.Is(err, startErr) {
		t.Fatalf("Ensure error = %v, want %v", err, startErr)
	}
	if len(ensurer.calls) != 1 || len(driver.startCalls) != 1 {
		t.Fatalf("workspace Ensure/driver calls = %d/%d, want 1/1", len(ensurer.calls), len(driver.startCalls))
	}
	persisted, loadErr := bridge.store.GetSandbox(ctx, ensurer.initialIDs[0])
	if loadErr != nil {
		t.Fatalf("GetSandbox after runtime error returned error: %v", loadErr)
	}
	if persisted.Summary.VMStatus != domain.VMStatusFailed {
		t.Fatalf("persisted VM status = %q, want failed", persisted.Summary.VMStatus)
	}
	if persisted.WorkspaceProvisioning == nil || persisted.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("persisted workspace provisioning = %#v, want ready", persisted.WorkspaceProvisioning)
	}
}

func TestLoaderSandboxRunnerLoadOrResumeUsesWorkspaceEnsurer(t *testing.T) {
	t.Run("resume orders workspace guide driver and publishes lifecycle", func(t *testing.T) {
		ctx := context.Background()
		bridge, driver := newTestSandboxRPCBridge(t)
		workspaceConfig, err := bridge.configDB.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{
			ID:         "loader-resume-workspace",
			Name:       "Loader Resume Workspace",
			Type:       "file",
			ConfigJSON: `{"root":"source-v1"}`,
		})
		if err != nil {
			t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
		}
		workspace := &domain.SandboxWorkspace{ID: workspaceConfig.ID, Name: workspaceConfig.Name, Type: workspaceConfig.Type, ConfigJSON: workspaceConfig.ConfigJSON}
		stopped, err := bridge.store.CreateSandbox(ctx, "stopped loader", "", driverpkg.RuntimeDriverBoxlite, "", workspace.ID, "loader", workspace, nil, []domain.SandboxTag{{Name: "capset", Value: "dev"}})
		if err != nil {
			t.Fatalf("CreateSandbox returned error: %v", err)
		}
		if err := domain.TransitionSandboxWorkspaceProvisioning(stopped, domain.SandboxWorkspaceProvisioningStatusReady); err != nil {
			t.Fatalf("transition workspace ready: %v", err)
		}
		readyUpdatedAt := stopped.WorkspaceProvisioning.UpdatedAt
		stopped.Summary.VMStatus = domain.VMStatusStopped
		if err := bridge.store.UpdateSandbox(ctx, stopped); err != nil {
			t.Fatalf("UpdateSandbox returned error: %v", err)
		}
		workspaceConfig.ConfigJSON = `{"root":"source-v2"}`
		if _, err := bridge.configDB.UpdateWorkspaceConfig(ctx, workspaceConfig); err != nil {
			t.Fatalf("UpdateWorkspaceConfig returned error: %v", err)
		}
		order := []string{}
		ensurer := &recordingLoaderWorkspaceEnsurer{ensure: func(_ context.Context, sandbox *domain.Sandbox) error {
			order = append(order, "ensure")
			if len(driver.startCalls) != 0 {
				t.Fatalf("driver starts before workspace ready = %#v", driver.startCalls)
			}
			if sandbox.WorkspaceProvisioning == nil || sandbox.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
				t.Fatalf("workspace Ensure input provisioning = %#v, want persisted ready", sandbox.WorkspaceProvisioning)
			}
			if sandbox.Workspace == nil || sandbox.Workspace.ConfigJSON != workspace.ConfigJSON {
				t.Fatalf("workspace Ensure input snapshot = %#v, want original %#v", sandbox.Workspace, workspace)
			}
			return nil
		}}
		capabilityProvider := testCapabilityProvider{guide: func(context.Context, string) ([]byte, error) {
			order = append(order, "guide")
			return []byte("# resume guide"), nil
		}}
		driver.onStart = func(sandbox *domain.Sandbox) {
			order = append(order, "driver.start")
			if sandbox.WorkspaceProvisioning == nil || sandbox.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
				t.Fatalf("driver started with provisioning = %#v, want ready", sandbox.WorkspaceProvisioning)
			}
		}
		publisher := &loaderSessionPublisherFake{}
		runner := NewLoaderSandboxRunner(bridge.config, bridge.store, bridge.configDB, ensurer, driver, capabilityProvider, nil, bridge.streams, publisher, nil, bridge.agentExecutor)

		resumed, eventType, err := runner.LoadOrResume(ctx, stopped.Summary.ID)
		if err != nil {
			t.Fatalf("LoadOrResume returned error: %v", err)
		}
		if eventType != "loader.sandbox.resumed" || resumed.Summary.VMStatus != domain.VMStatusRunning {
			t.Fatalf("resumed event/status = %q/%q", eventType, resumed.Summary.VMStatus)
		}
		if len(ensurer.calls) != 1 || ensurer.initialIDs[0] != stopped.Summary.ID || ensurer.calls[0] != driver.startSessions[0] {
			t.Fatalf("workspace Ensure calls=%#v ids=%#v driver sessions=%#v", ensurer.calls, ensurer.initialIDs, driver.startSessions)
		}
		if ensurer.initialStatuses[0] != domain.SandboxWorkspaceProvisioningStatusReady || ensurer.initialWorkspaces[0] == nil || ensurer.initialWorkspaces[0].ConfigJSON != workspace.ConfigJSON {
			t.Fatalf("workspace Ensure initial status/snapshot = %q/%#v, want ready/%#v", ensurer.initialStatuses[0], ensurer.initialWorkspaces[0], workspace)
		}
		if got := strings.Join(order, ","); got != "ensure,guide,driver.start" {
			t.Fatalf("loader resume order = %q, want ensure,guide,driver.start", got)
		}
		if resumed.Workspace == nil || resumed.Workspace.ConfigJSON != workspace.ConfigJSON || resumed.Workspace.ConfigJSON == workspaceConfig.ConfigJSON {
			t.Fatalf("resumed workspace snapshot = %#v, want original %#v and not updated source %q", resumed.Workspace, workspace, workspaceConfig.ConfigJSON)
		}
		if resumed.WorkspaceProvisioning == nil || resumed.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady || !resumed.WorkspaceProvisioning.UpdatedAt.Equal(readyUpdatedAt) {
			t.Fatalf("resumed provisioning = %#v, want ready with UpdatedAt %s", resumed.WorkspaceProvisioning, readyUpdatedAt)
		}
		persisted, loadErr := bridge.store.GetSandbox(ctx, stopped.Summary.ID)
		if loadErr != nil {
			t.Fatalf("GetSandbox after resume returned error: %v", loadErr)
		}
		if persisted.Workspace == nil || persisted.Workspace.ConfigJSON != workspace.ConfigJSON || persisted.WorkspaceProvisioning == nil || !persisted.WorkspaceProvisioning.UpdatedAt.Equal(readyUpdatedAt) {
			t.Fatalf("persisted resume workspace/provisioning = %#v/%#v, want original snapshot and unchanged ready timestamp", persisted.Workspace, persisted.WorkspaceProvisioning)
		}
		assertLoaderLifecycleEvidence(t, bridge, publisher, stopped.Summary.ID, "sandbox.resumed", "agent-compose.session.resumed")
	})

	t.Run("workspace error preserves direct return and skips guide driver", func(t *testing.T) {
		ctx := context.Background()
		bridge, driver := newTestSandboxRPCBridge(t)
		stopped, err := bridge.store.CreateSandbox(ctx, "stopped error", "", driverpkg.RuntimeDriverBoxlite, "", "", "loader", nil, nil, []domain.SandboxTag{{Name: "capset", Value: "dev"}})
		if err != nil {
			t.Fatalf("CreateSandbox returned error: %v", err)
		}
		stopped.Summary.VMStatus = domain.VMStatusStopped
		if err := bridge.store.UpdateSandbox(ctx, stopped); err != nil {
			t.Fatalf("UpdateSandbox returned error: %v", err)
		}
		ensureErr := errors.New("resume workspace provisioning failed")
		ensurer := &recordingLoaderWorkspaceEnsurer{err: ensureErr}
		guideCalls := 0
		capabilityProvider := testCapabilityProvider{guide: func(context.Context, string) ([]byte, error) {
			guideCalls++
			return nil, nil
		}}
		publisher := &loaderSessionPublisherFake{}
		runner := NewLoaderSandboxRunner(bridge.config, bridge.store, bridge.configDB, ensurer, driver, capabilityProvider, nil, bridge.streams, publisher, nil, bridge.agentExecutor)

		_, _, err = runner.LoadOrResume(ctx, stopped.Summary.ID)
		if err != ensureErr {
			t.Fatalf("LoadOrResume error = %v, want direct %v", err, ensureErr)
		}
		if len(ensurer.calls) != 1 || len(driver.startCalls) != 0 || guideCalls != 0 {
			t.Fatalf("workspace/driver/guide calls = %d/%d/%d, want 1/0/0", len(ensurer.calls), len(driver.startCalls), guideCalls)
		}
		persisted, loadErr := bridge.store.GetSandbox(ctx, stopped.Summary.ID)
		if loadErr != nil || persisted.Summary.VMStatus != domain.VMStatusStopped {
			t.Fatalf("persisted sandbox after workspace error = %#v err=%v, want stopped", persisted, loadErr)
		}
		if len(publisher.events) != 0 {
			t.Fatalf("publisher events after workspace error = %#v, want none", publisher.events)
		}
	})

	t.Run("runtime error keeps workspace ready", func(t *testing.T) {
		ctx := context.Background()
		bridge, driver := newTestSandboxRPCBridge(t)
		workspace := &domain.SandboxWorkspace{ID: "loader-resume-runtime-workspace", Name: "Resume Runtime Workspace", Type: "file", ConfigJSON: `{}`}
		stopped, err := bridge.store.CreateSandbox(ctx, "stopped runtime error", "", driverpkg.RuntimeDriverBoxlite, "", workspace.ID, "loader", workspace, nil, nil)
		if err != nil {
			t.Fatalf("CreateSandbox returned error: %v", err)
		}
		stopped.Summary.VMStatus = domain.VMStatusStopped
		if err := bridge.store.UpdateSandbox(ctx, stopped); err != nil {
			t.Fatalf("UpdateSandbox returned error: %v", err)
		}
		ensurer := &recordingLoaderWorkspaceEnsurer{ensure: func(ctx context.Context, sandbox *domain.Sandbox) error {
			if err := domain.TransitionSandboxWorkspaceProvisioning(sandbox, domain.SandboxWorkspaceProvisioningStatusReady); err != nil {
				return err
			}
			return bridge.store.UpdateSandbox(ctx, sandbox)
		}}
		startErr := errors.New("resume runtime start failed")
		driver.startErr = startErr
		runner := NewLoaderSandboxRunner(bridge.config, bridge.store, bridge.configDB, ensurer, driver, nil, nil, bridge.streams, nil, nil, bridge.agentExecutor)

		_, _, err = runner.LoadOrResume(ctx, stopped.Summary.ID)
		if err != startErr {
			t.Fatalf("LoadOrResume error = %v, want direct %v", err, startErr)
		}
		persisted, loadErr := bridge.store.GetSandbox(ctx, stopped.Summary.ID)
		if loadErr != nil {
			t.Fatalf("GetSandbox after runtime error returned error: %v", loadErr)
		}
		if persisted.Summary.VMStatus != domain.VMStatusStopped {
			t.Fatalf("persisted VM status = %q, want existing stopped", persisted.Summary.VMStatus)
		}
		if persisted.WorkspaceProvisioning == nil || persisted.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
			t.Fatalf("persisted provisioning after runtime error = %#v, want ready", persisted.WorkspaceProvisioning)
		}
	})
}

func TestLoaderSandboxRunnerStickyRunningMatchingConfigSkipsEnsurerAndPreservesWorkspaceSnapshot(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	workspaceConfig, err := bridge.configDB.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{
		ID:         "loader-sticky-workspace",
		Name:       "Loader Sticky Workspace",
		Type:       "file",
		ConfigJSON: `{"root":"source-v1"}`,
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}
	originalSnapshot := &domain.SandboxWorkspace{ID: workspaceConfig.ID, Name: workspaceConfig.Name, Type: workspaceConfig.Type, ConfigJSON: workspaceConfig.ConfigJSON}
	running, err := bridge.store.CreateSandbox(ctx, "sticky running", "", driverpkg.RuntimeDriverDocker, "", workspaceConfig.ID, "loader", originalSnapshot, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	if err := domain.TransitionSandboxWorkspaceProvisioning(running, domain.SandboxWorkspaceProvisioningStatusReady); err != nil {
		t.Fatalf("transition workspace ready: %v", err)
	}
	running.Summary.VMStatus = domain.VMStatusRunning
	if err := bridge.store.UpdateSandbox(ctx, running); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}
	loader := domain.Loader{Summary: domain.LoaderSummary{
		ID:            "loader-sticky",
		Name:          "Loader Sticky",
		WorkspaceID:   workspaceConfig.ID,
		Driver:        driverpkg.RuntimeDriverDocker,
		SandboxPolicy: domain.LoaderSandboxPolicySticky,
	}}
	request := domain.LoaderAgentRequest{Agent: "codex", BindingTriggerID: "sticky-trigger"}
	ensurer := &recordingLoaderWorkspaceEnsurer{err: errors.New("running sticky path must not ensure workspace")}
	publisher := &loaderSessionPublisherFake{}
	runner := NewLoaderSandboxRunner(bridge.config, bridge.store, bridge.configDB, ensurer, driver, nil, nil, bridge.streams, publisher, nil, bridge.agentExecutor)
	baseConfigHash, err := loaderSandboxConfigHash(loader)
	if err != nil {
		t.Fatalf("loaderSandboxConfigHash returned error: %v", err)
	}
	guestImage := runner.guestImage(request, loader, nil, driverpkg.RuntimeDriverDocker)
	configHash, err := loaderRequestSandboxConfigHash(baseConfigHash, request, nil, nil, nil, originalSnapshot, driverpkg.RuntimeDriverDocker, guestImage, nil)
	if err != nil {
		t.Fatalf("loaderRequestSandboxConfigHash returned error: %v", err)
	}
	if err := bridge.configDB.UpsertLoaderBinding(ctx, domain.LoaderBinding{LoaderID: loader.Summary.ID, TriggerID: "sticky-trigger", SandboxID: running.Summary.ID, SandboxConfigHash: configHash}); err != nil {
		t.Fatalf("UpsertLoaderBinding returned error: %v", err)
	}
	reused, eventType, err := runner.Ensure(ctx, loader, request, false)
	if err != nil {
		t.Fatalf("Ensure sticky reuse returned error: %v", err)
	}
	if reused.Summary.ID != running.Summary.ID || eventType != "" {
		t.Fatalf("sticky reuse sandbox/event = %q/%q, want %q/empty", reused.Summary.ID, eventType, running.Summary.ID)
	}
	if len(ensurer.calls) != 0 || len(driver.startCalls) != 0 {
		t.Fatalf("workspace Ensure/driver calls on running sticky reuse = %d/%d, want 0/0", len(ensurer.calls), len(driver.startCalls))
	}
	if reused.Workspace == nil || reused.Workspace.ConfigJSON != originalSnapshot.ConfigJSON {
		t.Fatalf("sticky workspace snapshot = %#v, want original %#v", reused.Workspace, originalSnapshot)
	}
	if reused.WorkspaceProvisioning == nil || reused.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("sticky provisioning = %#v, want ready", reused.WorkspaceProvisioning)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("publisher events for running sticky fast path = %#v, want none", publisher.events)
	}
}

func TestLoaderSandboxRunnerAdoptsLegacyStickyBindingWithoutStoppingSandbox(t *testing.T) {
	tests := []struct {
		name  string
		reuse func(context.Context, *LoaderSandboxRunner, domain.Loader, string, string) (*domain.Sandbox, string, bool, error)
	}{
		{
			name: "initial reuse",
			reuse: func(ctx context.Context, runner *LoaderSandboxRunner, loader domain.Loader, triggerID, configHash string) (*domain.Sandbox, string, bool, error) {
				sandbox, eventType, reused, _, err := runner.reuseCompatibleLoaderBinding(ctx, loader, triggerID, configHash)
				return sandbox, eventType, reused, err
			},
		},
		{
			name: "concurrent winner",
			reuse: func(ctx context.Context, runner *LoaderSandboxRunner, loader domain.Loader, triggerID, configHash string) (*domain.Sandbox, string, bool, error) {
				return runner.reuseWinningLoaderBinding(ctx, loader.Summary.ID, triggerID, configHash)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			bridge, driver := newTestSandboxRPCBridge(t)
			runner := NewLoaderSandboxRunner(bridge.config, bridge.store, bridge.configDB, &recordingLoaderWorkspaceEnsurer{}, driver, nil, nil, bridge.streams, nil, nil, bridge.agentExecutor)
			loader := domain.Loader{Summary: domain.LoaderSummary{ID: "legacy-loader", SandboxPolicy: domain.LoaderSandboxPolicySticky}}
			const triggerID = "legacy-trigger"
			const configHash = "sha256:current"
			running, err := bridge.store.CreateSandbox(ctx, "legacy sticky", "", driverpkg.RuntimeDriverDocker, "", "", domain.SandboxTypeScript+":"+loader.Summary.ID, nil, nil, nil)
			if err != nil {
				t.Fatalf("CreateSandbox returned error: %v", err)
			}
			running.Summary.VMStatus = domain.VMStatusRunning
			if err := bridge.store.UpdateSandbox(ctx, running); err != nil {
				t.Fatalf("UpdateSandbox returned error: %v", err)
			}
			if err := bridge.configDB.UpsertLoaderBinding(ctx, domain.LoaderBinding{LoaderID: loader.Summary.ID, TriggerID: triggerID, SandboxID: running.Summary.ID}); err != nil {
				t.Fatalf("UpsertLoaderBinding returned error: %v", err)
			}

			reusedSandbox, eventType, reused, err := test.reuse(ctx, runner, loader, triggerID, configHash)
			if err != nil {
				t.Fatalf("reuse legacy binding returned error: %v", err)
			}
			if !reused || reusedSandbox == nil || reusedSandbox.Summary.ID != running.Summary.ID || eventType != "" {
				t.Fatalf("reuse result = %#v/%q/%v, want sandbox %q/empty/true", reusedSandbox, eventType, reused, running.Summary.ID)
			}
			if len(driver.stopCalls) != 0 || len(driver.startCalls) != 0 {
				t.Fatalf("driver stop/start calls = %#v/%#v, want none", driver.stopCalls, driver.startCalls)
			}
			binding, found, err := bridge.configDB.GetLoaderBinding(ctx, loader.Summary.ID, triggerID)
			if err != nil || !found || binding.SandboxID != running.Summary.ID || binding.SandboxConfigHash != configHash {
				t.Fatalf("adopted binding = %#v found=%v err=%v, want sandbox %q hash %q", binding, found, err, running.Summary.ID, configHash)
			}
		})
	}
}

func TestLoaderSandboxRunnerConcurrentStickyClaimReusesWinner(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	runner := NewLoaderSandboxRunner(bridge.config, bridge.store, bridge.configDB, &recordingLoaderWorkspaceEnsurer{}, driver, nil, nil, bridge.streams, nil, nil, bridge.agentExecutor)
	loader := domain.Loader{Summary: domain.LoaderSummary{
		ID:                "loader-concurrent-sticky",
		Name:              "Concurrent Sticky",
		Driver:            driverpkg.RuntimeDriverDocker,
		SandboxPolicy:     domain.LoaderSandboxPolicySticky,
		ConcurrencyPolicy: domain.LoaderConcurrencyPolicyParallel,
	}}
	request := domain.LoaderAgentRequest{Agent: "codex", BindingTriggerID: "trigger-1"}
	guestImage := runner.guestImage(request, loader, nil, driverpkg.RuntimeDriverDocker)
	baseConfigHash, err := loaderSandboxConfigHash(loader)
	if err != nil {
		t.Fatalf("loaderSandboxConfigHash returned error: %v", err)
	}
	configHash, err := loaderRequestSandboxConfigHash(baseConfigHash, request, nil, nil, nil, nil, driverpkg.RuntimeDriverDocker, guestImage, nil)
	if err != nil {
		t.Fatalf("loaderRequestSandboxConfigHash returned error: %v", err)
	}
	winner, err := bridge.store.CreateSandbox(ctx, "winner", "", driverpkg.RuntimeDriverDocker, guestImage, "", domain.SandboxTypeScript+":"+loader.Summary.ID, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox winner returned error: %v", err)
	}
	winner.Summary.VMStatus = domain.VMStatusRunning
	if err := bridge.store.UpdateSandbox(ctx, winner); err != nil {
		t.Fatalf("UpdateSandbox winner returned error: %v", err)
	}
	var bindErr error
	driver.onStart = func(*domain.Sandbox) {
		if bindErr != nil {
			return
		}
		bindErr = bridge.configDB.UpsertLoaderBinding(ctx, domain.LoaderBinding{
			LoaderID:          loader.Summary.ID,
			TriggerID:         request.BindingTriggerID,
			SandboxID:         winner.Summary.ID,
			SandboxConfigHash: configHash,
		})
	}

	reused, eventType, err := runner.Ensure(ctx, loader, request, false)
	if bindErr != nil {
		t.Fatalf("seed winning binding returned error: %v", bindErr)
	}
	if err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	if reused.Summary.ID != winner.Summary.ID || eventType != "" {
		t.Fatalf("Ensure result = %q/%q, want winning sandbox %q with no resume event", reused.Summary.ID, eventType, winner.Summary.ID)
	}
	if len(driver.startCalls) != 1 {
		t.Fatalf("driver starts = %#v, want one losing sandbox start", driver.startCalls)
	}
	loser, err := bridge.store.GetSandbox(ctx, driver.startCalls[0])
	if err != nil {
		t.Fatalf("GetSandbox loser returned error: %v", err)
	}
	if loser.Summary.VMStatus != domain.VMStatusStopped {
		t.Fatalf("losing sandbox status = %q, want stopped", loser.Summary.VMStatus)
	}
}

func TestLoaderSandboxRunnerStickyWorkspaceConfigChangeCreatesReplacement(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	workspace, err := bridge.configDB.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{
		ID:         "loader-sticky-workspace-update",
		Name:       "Loader Sticky Workspace Update",
		Type:       "file",
		ConfigJSON: `{"root":"source-v1"}`,
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}
	runner := NewLoaderSandboxRunner(bridge.config, bridge.store, bridge.configDB, &recordingLoaderWorkspaceEnsurer{}, driver, nil, nil, bridge.streams, &loaderSessionPublisherFake{}, nil, bridge.agentExecutor)
	loader := domain.Loader{Summary: domain.LoaderSummary{
		ID:            "loader-sticky-workspace-update",
		Name:          "Loader Sticky Workspace Update",
		WorkspaceID:   workspace.ID,
		Driver:        driverpkg.RuntimeDriverDocker,
		SandboxPolicy: domain.LoaderSandboxPolicySticky,
	}}
	request := domain.LoaderAgentRequest{BindingTriggerID: "sticky-trigger"}
	first, _, err := runner.Ensure(ctx, loader, request, false)
	if err != nil {
		t.Fatalf("first Ensure returned error: %v", err)
	}
	workspace.ConfigJSON = `{"root":"source-v2"}`
	if _, err := bridge.configDB.UpdateWorkspaceConfig(ctx, workspace); err != nil {
		t.Fatalf("UpdateWorkspaceConfig returned error: %v", err)
	}
	replacement, eventType, err := runner.Ensure(ctx, loader, request, false)
	if err != nil {
		t.Fatalf("Ensure after workspace update returned error: %v", err)
	}
	if replacement.Summary.ID == first.Summary.ID || eventType != "loader.sandbox.created" {
		t.Fatalf("replacement sandbox/event = %q/%q, want a new sandbox/loader.sandbox.created", replacement.Summary.ID, eventType)
	}
	if replacement.Workspace == nil || replacement.Workspace.ConfigJSON != workspace.ConfigJSON {
		t.Fatalf("replacement workspace = %#v, want updated %#v", replacement.Workspace, workspace)
	}
}

func TestLoaderSandboxRunnerStickyRunningConfigChangeCreatesReplacement(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	capabilityProvider := testCapabilityProvider{
		target: "http://capability-gateway.test",
		guide: func(_ context.Context, capsetID string) ([]byte, error) {
			return []byte("# " + capsetID), nil
		},
	}
	capTokens := NewCapabilitySandboxResolver(bridge.store)
	runner := NewLoaderSandboxRunner(
		bridge.config,
		bridge.store,
		bridge.configDB,
		&recordingLoaderWorkspaceEnsurer{},
		driver,
		capabilityProvider,
		nil,
		bridge.streams,
		&loaderSessionPublisherFake{},
		capTokens,
		bridge.agentExecutor,
	)
	loader := domain.Loader{
		Summary: domain.LoaderSummary{
			ID:            "loader-sticky-update",
			Name:          "Loader Sticky Update",
			Driver:        driverpkg.RuntimeDriverDocker,
			SandboxPolicy: domain.LoaderSandboxPolicySticky,
			CapsetIDs:     []string{"A"},
		},
		EnvItems: []domain.SandboxEnvVar{{Name: "BUG_VALUE", Value: "A"}},
		Script:   "function main() {}",
	}
	loader, err := bridge.configDB.CreateLoader(ctx, loader)
	if err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}
	request := domain.LoaderAgentRequest{BindingTriggerID: "sticky-trigger"}
	first, _, err := runner.Ensure(ctx, loader, request, false)
	if err != nil {
		t.Fatalf("first Ensure returned error: %v", err)
	}
	oldToken := capabilities.SandboxToken(first)
	if oldToken == "" {
		t.Fatal("first sticky sandbox has no CAP_TOKEN")
	}
	firstBinding, found, err := bridge.configDB.GetLoaderBinding(ctx, loader.Summary.ID, request.BindingTriggerID)
	if err != nil || !found {
		t.Fatalf("first binding = %#v found=%v err=%v", firstBinding, found, err)
	}

	loader.EnvItems[0].Value = "B"
	loader.Summary.CapsetIDs = []string{"B"}
	loader, err = bridge.configDB.UpdateLoader(ctx, loader)
	if err != nil {
		t.Fatalf("UpdateLoader returned error: %v", err)
	}
	replacement, eventType, err := runner.Ensure(ctx, loader, request, false)
	if err != nil {
		t.Fatalf("Ensure after Loader update returned error: %v", err)
	}
	if replacement.Summary.ID == first.Summary.ID || eventType != "loader.sandbox.created" {
		t.Fatalf("replacement sandbox/event = %q/%q, want a new sandbox/loader.sandbox.created", replacement.Summary.ID, eventType)
	}
	if got := domain.SandboxEnvMap(replacement.EnvItems)["BUG_VALUE"]; got != "B" {
		t.Fatalf("replacement BUG_VALUE = %q, want B", got)
	}
	retired, err := bridge.store.GetSandbox(ctx, first.Summary.ID)
	if err != nil {
		t.Fatalf("load retired sandbox: %v", err)
	}
	if retired.Summary.VMStatus != domain.VMStatusStopped {
		t.Fatalf("retired sandbox status = %q, want stopped", retired.Summary.VMStatus)
	}
	if len(driver.stopCalls) != 1 || driver.stopCalls[0] != first.Summary.ID {
		t.Fatalf("driver stop calls = %#v, want [%q]", driver.stopCalls, first.Summary.ID)
	}
	binding, found, err := bridge.configDB.GetLoaderBinding(ctx, loader.Summary.ID, request.BindingTriggerID)
	if err != nil || !found || binding.SandboxID != replacement.Summary.ID {
		t.Fatalf("replacement binding = %#v found=%v err=%v", binding, found, err)
	}
	if binding.SandboxConfigHash == "" || binding.SandboxConfigHash == firstBinding.SandboxConfigHash {
		t.Fatalf("replacement binding hash = %q, want a non-empty hash different from %q", binding.SandboxConfigHash, firstBinding.SandboxConfigHash)
	}
	if _, err := capTokens.ResolveCapabilitySandbox(ctx, oldToken); err == nil {
		t.Fatal("old CAP_TOKEN still resolves after stale sandbox retirement")
	}
	newToken := capabilities.SandboxToken(replacement)
	resolved, err := capTokens.ResolveCapabilitySandbox(ctx, newToken)
	if err != nil {
		t.Fatalf("new CAP_TOKEN did not resolve: %v", err)
	}
	if len(resolved.CapsetIDs) != 1 || resolved.CapsetIDs[0] != "B" {
		t.Fatalf("new CAP_TOKEN capsets = %#v, want [B]", resolved.CapsetIDs)
	}
}

func TestLoaderSandboxRunnerStickyStoppedConfigChangeDoesNotResume(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	runner := NewLoaderSandboxRunner(bridge.config, bridge.store, bridge.configDB, &recordingLoaderWorkspaceEnsurer{}, driver, nil, nil, bridge.streams, &loaderSessionPublisherFake{}, nil, bridge.agentExecutor)
	loader := domain.Loader{
		Summary: domain.LoaderSummary{
			ID:            "loader-sticky-stopped-update",
			Name:          "Loader Sticky Stopped Update",
			Driver:        driverpkg.RuntimeDriverDocker,
			SandboxPolicy: domain.LoaderSandboxPolicySticky,
		},
		EnvItems: []domain.SandboxEnvVar{{Name: "BUG_VALUE", Value: "A"}},
	}
	request := domain.LoaderAgentRequest{BindingTriggerID: "sticky-trigger"}
	first, _, err := runner.Ensure(ctx, loader, request, false)
	if err != nil {
		t.Fatalf("first Ensure returned error: %v", err)
	}
	if err := runner.Shutdown(ctx, first.Summary.ID); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	loader.EnvItems[0].Value = "B"
	replacement, eventType, err := runner.Ensure(ctx, loader, request, false)
	if err != nil {
		t.Fatalf("Ensure after Loader update returned error: %v", err)
	}
	if replacement.Summary.ID == first.Summary.ID || eventType != "loader.sandbox.created" {
		t.Fatalf("replacement sandbox/event = %q/%q, want a new sandbox/loader.sandbox.created", replacement.Summary.ID, eventType)
	}
	if len(driver.startCalls) != 2 || driver.startCalls[0] != first.Summary.ID || driver.startCalls[1] != replacement.Summary.ID {
		t.Fatalf("driver start calls = %#v, want create/create without old resume", driver.startCalls)
	}
}

func assertLoaderLifecycleEvidence(t *testing.T, bridge *SandboxRPCBridge, publisher *loaderSessionPublisherFake, sandboxID, eventType, topic string) {
	t.Helper()
	events, err := bridge.store.ListEvents(context.Background(), sandboxID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type == eventType {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("sandbox events = %#v, want %q", events, eventType)
	}
	if len(publisher.events) != 1 || publisher.events[0].Topic != topic {
		t.Fatalf("publisher events = %#v, want one %q", publisher.events, topic)
	}
}

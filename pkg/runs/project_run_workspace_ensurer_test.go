package runs

import (
	"context"
	"errors"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type recordingProjectRunWorkspaceEnsurer struct {
	err             error
	markReady       bool
	beforeEnsure    func(*domain.Sandbox)
	sandboxIDs      []string
	workspaceCopies []*domain.SandboxWorkspace
}

func (e *recordingProjectRunWorkspaceEnsurer) Ensure(_ context.Context, sandbox *domain.Sandbox) error {
	if e.beforeEnsure != nil {
		e.beforeEnsure(sandbox)
	}
	e.sandboxIDs = append(e.sandboxIDs, sandbox.Summary.ID)
	if sandbox.Workspace == nil {
		e.workspaceCopies = append(e.workspaceCopies, nil)
	} else {
		workspace := *sandbox.Workspace
		e.workspaceCopies = append(e.workspaceCopies, &workspace)
	}
	if e.err != nil {
		return e.err
	}
	if e.markReady && sandbox.WorkspaceProvisioning != nil {
		provisioning := *sandbox.WorkspaceProvisioning
		provisioning.Status = domain.SandboxWorkspaceProvisioningStatusReady
		provisioning.UpdatedAt = time.Now().UTC()
		sandbox.WorkspaceProvisioning = &provisioning
	}
	return nil
}

func TestRunsControllerProjectRunWorkspaceEnsurerPaths(t *testing.T) {
	t.Run("new sandbox", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		ensurer := projectRunEnsurerBeforeDriver(t, fixture)
		fixture.controller.workspaceEnsurer = ensurer
		prepared := Preparation{Workspace: projectRunWorkspaceSnapshot("prepared")}

		result, err := fixture.controller.ensureProjectRunSandbox(
			fixture.ctx,
			projectRunEnsurerRecord("run-new"),
			prepared,
			RunAgentRequest{},
		)
		if err != nil {
			t.Fatalf("ensureProjectRunSandbox returned error: %v", err)
		}
		if !result.Created || result.Sandbox == nil {
			t.Fatalf("sandbox result = %#v, want newly created sandbox", result)
		}
		assertProjectRunEnsurerCall(t, ensurer, result.Sandbox.Summary.ID, prepared.Workspace)
	})

	t.Run("explicit sandbox preserves workspace snapshot", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		original := projectRunWorkspaceSnapshot("existing")
		sandbox := createProjectRunWorkspaceSandbox(t, fixture, original)
		markProjectRunWorkspaceReady(t, fixture, sandbox)
		readyAt := sandbox.WorkspaceProvisioning.UpdatedAt
		ensurer := projectRunEnsurerBeforeDriver(t, fixture)
		ensurer.markReady = false
		fixture.controller.workspaceEnsurer = ensurer

		result, err := fixture.controller.ensureProjectRunSandbox(
			fixture.ctx,
			projectRunEnsurerRecord("run-explicit"),
			Preparation{Workspace: projectRunWorkspaceSnapshot("prepared-conflict")},
			RunAgentRequest{SandboxID: sandbox.Summary.ID},
		)
		if err != nil {
			t.Fatalf("ensureProjectRunSandbox returned error: %v", err)
		}
		if result.Created || result.Sandbox == nil || result.Sandbox.Summary.ID != sandbox.Summary.ID {
			t.Fatalf("sandbox result = %#v, want reused sandbox %q", result, sandbox.Summary.ID)
		}
		assertProjectRunEnsurerCall(t, ensurer, sandbox.Summary.ID, original)
		assertProjectRunWorkspaceSnapshot(t, "returned sandbox", result.Sandbox.Workspace, original)
		persisted, err := fixture.store.GetSandbox(fixture.ctx, sandbox.Summary.ID)
		if err != nil {
			t.Fatalf("GetSandbox returned error: %v", err)
		}
		assertProjectRunWorkspaceSnapshot(t, "persisted sandbox", persisted.Workspace, original)
		assertProjectRunReadyUnchanged(t, "persisted sandbox", persisted, readyAt)
	})

	t.Run("sticky binding preserves workspace snapshot", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		original := projectRunWorkspaceSnapshot("sticky-existing")
		sandbox := createProjectRunWorkspaceSandbox(t, fixture, original)
		markProjectRunWorkspaceReady(t, fixture, sandbox)
		readyAt := sandbox.WorkspaceProvisioning.UpdatedAt
		fixture.configDB.bindings = map[string]domain.LoaderBinding{
			"loader-1/trigger-1": {LoaderID: "loader-1", TriggerID: "trigger-1", SandboxID: sandbox.Summary.ID},
		}
		ensurer := projectRunEnsurerBeforeDriver(t, fixture)
		ensurer.markReady = false
		fixture.controller.workspaceEnsurer = ensurer

		result, err := fixture.controller.ensureProjectRunSandbox(
			fixture.ctx,
			projectRunEnsurerRecord("run-sticky"),
			Preparation{Workspace: projectRunWorkspaceSnapshot("prepared-conflict")},
			RunAgentRequest{StickyBindingLoaderID: "loader-1", StickyBindingTriggerID: "trigger-1"},
		)
		if err != nil {
			t.Fatalf("ensureProjectRunSandbox returned error: %v", err)
		}
		if result.Created || result.Sandbox == nil || result.Sandbox.Summary.ID != sandbox.Summary.ID {
			t.Fatalf("sandbox result = %#v, want sticky sandbox %q", result, sandbox.Summary.ID)
		}
		assertProjectRunEnsurerCall(t, ensurer, sandbox.Summary.ID, original)
		assertProjectRunWorkspaceSnapshot(t, "sticky sandbox", result.Sandbox.Workspace, original)
		assertProjectRunReadyUnchanged(t, "sticky sandbox", result.Sandbox, readyAt)
		if binding := fixture.configDB.bindings["loader-1/trigger-1"]; binding.SandboxID != sandbox.Summary.ID {
			t.Fatalf("sticky binding = %#v, want sandbox %q", binding, sandbox.Summary.ID)
		}
	})

	t.Run("running sandbox is ensured without another driver start", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		original := projectRunWorkspaceSnapshot("running")
		sandbox := createProjectRunWorkspaceSandbox(t, fixture, original)
		markProjectRunWorkspaceReady(t, fixture, sandbox)
		readyAt := sandbox.WorkspaceProvisioning.UpdatedAt
		sandbox.Summary.VMStatus = domain.VMStatusRunning
		if err := fixture.store.UpdateSandbox(fixture.ctx, sandbox); err != nil {
			t.Fatalf("UpdateSandbox returned error: %v", err)
		}
		ensurer := projectRunEnsurerBeforeDriver(t, fixture)
		ensurer.markReady = false
		fixture.controller.workspaceEnsurer = ensurer

		result, err := fixture.controller.ensureProjectRunSandbox(
			fixture.ctx,
			projectRunEnsurerRecord("run-running"),
			Preparation{Workspace: projectRunWorkspaceSnapshot("prepared-conflict")},
			RunAgentRequest{SandboxID: sandbox.Summary.ID},
		)
		if err != nil {
			t.Fatalf("ensureProjectRunSandbox returned error: %v", err)
		}
		if fixture.driver.started {
			t.Fatal("running sandbox started its driver again")
		}
		assertProjectRunEnsurerCall(t, ensurer, sandbox.Summary.ID, original)
		assertProjectRunWorkspaceSnapshot(t, "running sandbox", result.Sandbox.Workspace, original)
		assertProjectRunReadyUnchanged(t, "running sandbox", result.Sandbox, readyAt)
	})
}

func TestRunsControllerProjectRunWorkspaceEnsurerFailureShortCircuitsAndCleansUp(t *testing.T) {
	t.Run("created sandbox", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		ensureErr := errors.New("workspace ensure failed")
		ensurer := &recordingProjectRunWorkspaceEnsurer{
			err: ensureErr,
			beforeEnsure: func(*domain.Sandbox) {
				if fixture.driver.started {
					t.Fatal("driver started before workspace Ensurer")
				}
			},
		}
		fixture.controller.workspaceEnsurer = ensurer

		run, execErr, err := fixture.controller.RunProjectAgent(fixture.ctx, RunAgentRequest{
			ProjectID:       "project-1",
			AgentName:       "worker",
			Prompt:          "do work",
			Source:          domain.ProjectRunSourceAPI,
			ClientRequestID: "ensurer-created-failure",
			CleanupPolicy:   agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION,
		}, nil)
		if err != nil || !errors.Is(execErr, ensureErr) {
			t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
		}
		if run.Status != domain.ProjectRunStatusFailed || run.SandboxID == "" || !errors.Is(execErr, ensureErr) {
			t.Fatalf("failed run = %#v, execErr=%v", run, execErr)
		}
		if fixture.driver.started {
			t.Fatal("driver started after workspace Ensurer failure")
		}
		if len(ensurer.sandboxIDs) != 1 || ensurer.sandboxIDs[0] != run.SandboxID {
			t.Fatalf("Ensurer sandbox ids = %#v, want [%q]", ensurer.sandboxIDs, run.SandboxID)
		}
		if _, loadErr := fixture.store.GetSandbox(fixture.ctx, run.SandboxID); loadErr == nil {
			t.Fatalf("created sandbox %q was not removed after start failure", run.SandboxID)
		}
		if !fixture.driver.removed {
			t.Fatal("created sandbox runtime was not removed after Ensurer failure")
		}
	})

	t.Run("reused sandbox", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		original := projectRunWorkspaceSnapshot("existing-failure")
		sandbox := createProjectRunWorkspaceSandbox(t, fixture, original)
		ensureErr := errors.New("workspace ensure failed")
		ensurer := &recordingProjectRunWorkspaceEnsurer{err: ensureErr}
		fixture.controller.workspaceEnsurer = ensurer

		run, execErr, err := fixture.controller.RunProjectAgent(fixture.ctx, RunAgentRequest{
			ProjectID:       "project-1",
			AgentName:       "worker",
			Prompt:          "do work",
			SandboxID:       sandbox.Summary.ID,
			Source:          domain.ProjectRunSourceAPI,
			ClientRequestID: "ensurer-existing-failure",
			CleanupPolicy:   agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION,
		}, nil)
		if err != nil || !errors.Is(execErr, ensureErr) {
			t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
		}
		if run.Status != domain.ProjectRunStatusFailed || run.SandboxID != sandbox.Summary.ID {
			t.Fatalf("failed run = %#v, want reused sandbox %q", run, sandbox.Summary.ID)
		}
		if fixture.driver.started || fixture.driver.removed {
			t.Fatalf("driver started=%v removed=%v after reused sandbox Ensurer failure", fixture.driver.started, fixture.driver.removed)
		}
		assertProjectRunEnsurerCall(t, ensurer, sandbox.Summary.ID, original)
		persisted, loadErr := fixture.store.GetSandbox(fixture.ctx, sandbox.Summary.ID)
		if loadErr != nil {
			t.Fatalf("reused sandbox was removed: %v", loadErr)
		}
		if persisted.Summary.VMStatus != domain.VMStatusFailed {
			t.Fatalf("reused sandbox VM status = %q, want failed", persisted.Summary.VMStatus)
		}
		assertProjectRunWorkspaceSnapshot(t, "reused failed sandbox", persisted.Workspace, original)
	})
}

func TestRunsControllerProjectRunRuntimeFailurePreservesReadyWorkspace(t *testing.T) {
	fixture := newControllerRunFixture(t)
	fixture.driver.startErr = errors.New("runtime start failed")
	ensurer := projectRunEnsurerBeforeDriver(t, fixture)
	fixture.controller.workspaceEnsurer = ensurer
	prepared := Preparation{Workspace: projectRunWorkspaceSnapshot("runtime-failure")}

	result, err := fixture.controller.ensureProjectRunSandbox(
		fixture.ctx,
		projectRunEnsurerRecord("run-runtime-failure"),
		prepared,
		RunAgentRequest{},
	)
	if !errors.Is(err, fixture.driver.startErr) || result.Sandbox == nil || !result.Created {
		t.Fatalf("ensureProjectRunSandbox result=%#v err=%v", result, err)
	}
	assertProjectRunEnsurerCall(t, ensurer, result.Sandbox.Summary.ID, prepared.Workspace)
	persisted, loadErr := fixture.store.GetSandbox(fixture.ctx, result.Sandbox.Summary.ID)
	if loadErr != nil {
		t.Fatalf("GetSandbox returned error: %v", loadErr)
	}
	if persisted.Summary.VMStatus != domain.VMStatusFailed {
		t.Fatalf("VM status = %q, want failed", persisted.Summary.VMStatus)
	}
	if persisted.WorkspaceProvisioning == nil || persisted.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("workspace provisioning = %#v, want ready", persisted.WorkspaceProvisioning)
	}
}

func projectRunEnsurerBeforeDriver(t *testing.T, fixture *controllerRunFixture) *recordingProjectRunWorkspaceEnsurer {
	t.Helper()
	return &recordingProjectRunWorkspaceEnsurer{
		markReady: true,
		beforeEnsure: func(*domain.Sandbox) {
			if fixture.driver.started {
				t.Fatal("driver started before workspace Ensurer")
			}
		},
	}
}

func projectRunEnsurerRecord(runID string) domain.ProjectRunRecord {
	return domain.ProjectRunRecord{
		RunID:       runID,
		ProjectID:   "project-1",
		ProjectName: "Project",
		AgentName:   "worker",
		Source:      domain.ProjectRunSourceAPI,
		Driver:      "docker",
		ImageRef:    "guest:latest",
	}
}

func projectRunWorkspaceSnapshot(id string) *domain.SandboxWorkspace {
	return &domain.SandboxWorkspace{
		ID:         id,
		Name:       id + " name",
		Type:       "git",
		ConfigJSON: `{"url":"https://example.test/` + id + `.git"}`,
	}
}

func createProjectRunWorkspaceSandbox(t *testing.T, fixture *controllerRunFixture, workspace *domain.SandboxWorkspace) *domain.Sandbox {
	t.Helper()
	sandbox, err := fixture.store.CreateSandbox(
		fixture.ctx,
		"existing",
		"",
		"docker",
		"guest:latest",
		workspace.ID,
		domain.SandboxTypeManual,
		workspace,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	return sandbox
}

func markProjectRunWorkspaceReady(t *testing.T, fixture *controllerRunFixture, sandbox *domain.Sandbox) {
	t.Helper()
	if sandbox.WorkspaceProvisioning == nil {
		t.Fatal("workspace sandbox has no provisioning metadata")
	}
	provisioning := *sandbox.WorkspaceProvisioning
	provisioning.Status = domain.SandboxWorkspaceProvisioningStatusReady
	provisioning.UpdatedAt = time.Now().UTC()
	sandbox.WorkspaceProvisioning = &provisioning
	if err := fixture.store.UpdateSandbox(fixture.ctx, sandbox); err != nil {
		t.Fatalf("UpdateSandbox ready workspace returned error: %v", err)
	}
}

func assertProjectRunEnsurerCall(t *testing.T, ensurer *recordingProjectRunWorkspaceEnsurer, sandboxID string, workspace *domain.SandboxWorkspace) {
	t.Helper()
	if len(ensurer.sandboxIDs) != 1 || ensurer.sandboxIDs[0] != sandboxID {
		t.Fatalf("Ensurer sandbox ids = %#v, want [%q]", ensurer.sandboxIDs, sandboxID)
	}
	if len(ensurer.workspaceCopies) != 1 {
		t.Fatalf("Ensurer workspace calls = %d, want 1", len(ensurer.workspaceCopies))
	}
	assertProjectRunWorkspaceSnapshot(t, "Ensurer sandbox", ensurer.workspaceCopies[0], workspace)
}

func assertProjectRunWorkspaceSnapshot(t *testing.T, label string, got, want *domain.SandboxWorkspace) {
	t.Helper()
	if got == nil || want == nil {
		if got != want {
			t.Fatalf("%s workspace = %#v, want %#v", label, got, want)
		}
		return
	}
	if *got != *want {
		t.Fatalf("%s workspace = %#v, want %#v", label, got, want)
	}
}

func assertProjectRunReadyUnchanged(t *testing.T, label string, sandbox *domain.Sandbox, readyAt time.Time) {
	t.Helper()
	if sandbox.WorkspaceProvisioning == nil || sandbox.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("%s workspace provisioning = %#v, want ready", label, sandbox.WorkspaceProvisioning)
	}
	if !sandbox.WorkspaceProvisioning.UpdatedAt.Equal(readyAt) {
		t.Fatalf("%s workspace ready timestamp = %v, want unchanged %v", label, sandbox.WorkspaceProvisioning.UpdatedAt, readyAt)
	}
}

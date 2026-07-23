package runs

import (
	"fmt"
	"testing"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/sessionstore"
)

func TestStickyProjectRunConfigHashTracksEffectiveSandboxSpec(t *testing.T) {
	run := domain.ProjectRunRecord{
		ProjectID:       "project-1",
		ProjectRevision: 1,
		AgentName:       "worker",
		ManagedAgentID:  "agent-1",
		Driver:          "docker",
		ImageRef:        "guest:v1",
	}
	prepared := Preparation{
		EnvItems:  []domain.SandboxEnvVar{{Name: "BUG_VALUE", Value: "A"}},
		CapsetIDs: []string{"a", "b"},
		Workspace: &domain.SandboxWorkspace{ID: "workspace-1", ConfigJSON: `{"root":"v1"}`},
	}
	baseHash := "sha256:loader"
	volumeMounts := []domain.SandboxVolumeMount{
		{ID: "volume-v2", Type: "volume", Source: "data", Target: "/workspace/data", HostPath: "/host/v2"},
		{ID: "volume-cache", Type: "bind", Source: "./cache", Target: "/workspace/cache", HostPath: "/host/cache"},
	}
	first, err := stickyProjectRunConfigHash(baseHash, run, prepared, "docker", "guest:v1", volumeMounts, sessionstore.CreateSandboxOptions{})
	if err != nil {
		t.Fatalf("stickyProjectRunConfigHash returned error: %v", err)
	}

	reordered := prepared
	reordered.CapsetIDs = []string{"b", "a", "a"}
	same, err := stickyProjectRunConfigHash(baseHash, run, reordered, "docker", "guest:v1", volumeMounts, sessionstore.CreateSandboxOptions{})
	if err != nil {
		t.Fatalf("stickyProjectRunConfigHash reordered returned error: %v", err)
	}
	if same != first {
		t.Fatalf("capset ordering changed effective hash: got %q want %q", same, first)
	}
	jupyterFirst, err := stickyProjectRunConfigHash(baseHash, run, prepared, "docker", "guest:v1", volumeMounts, sessionstore.CreateSandboxOptions{VolumeMounts: volumeMounts})
	if err != nil {
		t.Fatalf("stickyProjectRunConfigHash with Jupyter mounts returned error: %v", err)
	}
	reorderedMounts := []domain.SandboxVolumeMount{volumeMounts[1], volumeMounts[0]}
	same, err = stickyProjectRunConfigHash(baseHash, run, prepared, "docker", "guest:v1", reorderedMounts, sessionstore.CreateSandboxOptions{VolumeMounts: reorderedMounts})
	if err != nil {
		t.Fatalf("stickyProjectRunConfigHash reordered mounts returned error: %v", err)
	}
	if same != jupyterFirst {
		t.Fatalf("volume mount ordering changed effective hash: got %q want %q", same, jupyterFirst)
	}

	changed := prepared
	changed.EnvItems = []domain.SandboxEnvVar{{Name: "BUG_VALUE", Value: "B"}}
	second, err := stickyProjectRunConfigHash(baseHash, run, changed, "docker", "guest:v1", volumeMounts, sessionstore.CreateSandboxOptions{})
	if err != nil {
		t.Fatalf("stickyProjectRunConfigHash changed returned error: %v", err)
	}
	if second == first {
		t.Fatal("effective environment change did not change sticky project sandbox hash")
	}

	for name, args := range map[string]struct {
		driver       string
		guestImage   string
		volumeMounts []domain.SandboxVolumeMount
	}{
		"driver":        {driver: "boxlite", guestImage: "guest:v1", volumeMounts: volumeMounts},
		"guest image":   {driver: "docker", guestImage: "guest:v2", volumeMounts: volumeMounts},
		"volume source": {driver: "docker", guestImage: "guest:v1", volumeMounts: []domain.SandboxVolumeMount{{ID: "volume-v2", Type: "volume", Target: "/workspace/data", HostPath: "/host/remapped"}}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := stickyProjectRunConfigHash(baseHash, run, prepared, args.driver, args.guestImage, args.volumeMounts, sessionstore.CreateSandboxOptions{})
			if err != nil {
				t.Fatalf("stickyProjectRunConfigHash returned error: %v", err)
			}
			if got == first {
				t.Fatalf("effective %s change did not change sticky project sandbox hash", name)
			}
		})
	}
}

func TestResolveStickyLoaderBindingInvalidatesBeforeRuntimeStop(t *testing.T) {
	fixture := newControllerRunFixture(t)
	sandbox, err := fixture.store.CreateSandbox(fixture.ctx, "sticky", "", "docker", "guest:v1", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	sandbox.Summary.VMStatus = domain.VMStatusRunning
	if err := fixture.store.UpdateSandbox(fixture.ctx, sandbox); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}
	binding := domain.LoaderBinding{LoaderID: "loader-1", TriggerID: "trigger-1", SandboxID: sandbox.Summary.ID, SandboxConfigHash: "sha256:old"}
	fixture.configDB.bindings = map[string]domain.LoaderBinding{"loader-1/trigger-1": binding}
	fixture.driver.onStop = func(*domain.Sandbox) error {
		current := fixture.configDB.bindings["loader-1/trigger-1"]
		desiredHash, retiring := loaders.RetiringLoaderBindingConfigHash(current)
		if !retiring || desiredHash != "sha256:new" {
			return fmt.Errorf("binding at runtime stop = %#v, want retirement for sha256:new", current)
		}
		return nil
	}

	gotSandboxID, previous, _, err := fixture.controller.resolveStickyLoaderBinding(fixture.ctx, fixture.configDB, "loader-1", "trigger-1", "sha256:new")
	if err != nil {
		t.Fatalf("resolveStickyLoaderBinding returned error: %v", err)
	}
	if gotSandboxID != "" || previous == nil {
		t.Fatalf("resolveStickyLoaderBinding result = %q/%#v, want replacement binding", gotSandboxID, previous)
	}
	if desiredHash, retiring := loaders.RetiringLoaderBindingConfigHash(*previous); !retiring || desiredHash != "sha256:new" {
		t.Fatalf("previous binding = %#v, want retirement for sha256:new", previous)
	}
}

func TestResolveStickyLoaderBindingAdoptsLegacyConfigHashWithoutStoppingSandbox(t *testing.T) {
	fixture := newControllerRunFixture(t)
	sandbox, err := fixture.store.CreateSandbox(fixture.ctx, "sticky", "", "docker", "guest:v1", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	sandbox.Summary.VMStatus = domain.VMStatusRunning
	if err := fixture.store.UpdateSandbox(fixture.ctx, sandbox); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}
	fixture.configDB.bindings = map[string]domain.LoaderBinding{
		"loader-1/trigger-1": {LoaderID: "loader-1", TriggerID: "trigger-1", SandboxID: sandbox.Summary.ID},
	}

	gotSandboxID, binding, warnings, err := fixture.controller.resolveStickyLoaderBinding(fixture.ctx, fixture.configDB, "loader-1", "trigger-1", "sha256:current")
	if err != nil {
		t.Fatalf("resolveStickyLoaderBinding returned error: %v", err)
	}
	if gotSandboxID != sandbox.Summary.ID || binding == nil || binding.SandboxConfigHash != "sha256:current" {
		t.Fatalf("resolveStickyLoaderBinding result = %q/%#v, want adopted binding for %q", gotSandboxID, binding, sandbox.Summary.ID)
	}
	if len(warnings) != 0 || fixture.driver.stopped {
		t.Fatalf("legacy reuse warnings/stopped = %#v/%v, want none/false", warnings, fixture.driver.stopped)
	}
	stored := fixture.configDB.bindings["loader-1/trigger-1"]
	if stored.SandboxID != sandbox.Summary.ID || stored.SandboxConfigHash != "sha256:current" {
		t.Fatalf("stored binding = %#v, want adopted legacy binding", stored)
	}
}

func TestResolveStickyLoaderBindingDoesNotReuseRetiringLegacyBinding(t *testing.T) {
	fixture := newControllerRunFixture(t)
	sandbox, err := fixture.store.CreateSandbox(fixture.ctx, "sticky", "", "docker", "guest:v1", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	sandbox.Summary.VMStatus = domain.VMStatusRunning
	if err := fixture.store.UpdateSandbox(fixture.ctx, sandbox); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}
	retiring := loaders.RetiringLoaderBinding(domain.LoaderBinding{
		LoaderID: "loader-1", TriggerID: "trigger-1", SandboxID: sandbox.Summary.ID, SandboxConfigHash: "sha256:old",
	}, "sha256:new")
	fixture.configDB.bindings = map[string]domain.LoaderBinding{"loader-1/trigger-1": retiring}

	gotSandboxID, previous, _, err := fixture.controller.resolveStickyLoaderBinding(fixture.ctx, fixture.configDB, "loader-1", "trigger-1", "")
	if err != nil {
		t.Fatalf("resolveStickyLoaderBinding returned error: %v", err)
	}
	if gotSandboxID != "" || previous == nil {
		t.Fatalf("resolveStickyLoaderBinding result = %q/%#v, want replacement binding", gotSandboxID, previous)
	}
	if desiredHash, ok := loaders.RetiringLoaderBindingConfigHash(*previous); !ok || desiredHash != "" {
		t.Fatalf("previous binding = %#v, want legacy retirement claim", previous)
	}
}

func TestEnsureProjectRunSandboxConcurrentStickyClaimReusesWinner(t *testing.T) {
	fixture := newControllerRunFixture(t)
	run := domain.ProjectRunRecord{RunID: "run-1", ProjectID: "project-1", ProjectRevision: 1, ProjectName: "Project", AgentName: "worker", ManagedAgentID: "agent-1"}
	prepared := Preparation{}
	baseHash := "sha256:loader"
	configHash, err := stickyProjectRunConfigHash(baseHash, run, prepared, "docker", "guest:latest", nil, sessionstore.CreateSandboxOptions{})
	if err != nil {
		t.Fatalf("stickyProjectRunConfigHash returned error: %v", err)
	}
	winner, err := fixture.store.CreateSandbox(fixture.ctx, "winner", "", "docker", "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox winner returned error: %v", err)
	}
	winner.Summary.VMStatus = domain.VMStatusRunning
	if err := fixture.store.UpdateSandbox(fixture.ctx, winner); err != nil {
		t.Fatalf("UpdateSandbox winner returned error: %v", err)
	}
	fixture.driver.onStart = func(*domain.Sandbox) error {
		fixture.configDB.bindings = map[string]domain.LoaderBinding{
			"loader-1/trigger-1": {LoaderID: "loader-1", TriggerID: "trigger-1", SandboxID: winner.Summary.ID, SandboxConfigHash: configHash},
		}
		return nil
	}

	result, err := fixture.controller.ensureProjectRunSandbox(fixture.ctx, run, prepared, RunAgentRequest{
		StickyBindingLoaderID:   "loader-1",
		StickyBindingTriggerID:  "trigger-1",
		StickyBindingConfigHash: baseHash,
	})
	if err != nil {
		t.Fatalf("ensureProjectRunSandbox returned error: %v", err)
	}
	if result.Sandbox == nil || result.Sandbox.Summary.ID != winner.Summary.ID || result.Created {
		t.Fatalf("ensureProjectRunSandbox result = %#v, want reused winner %q", result, winner.Summary.ID)
	}
}

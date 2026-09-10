package adapters

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/compose"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

func newWorkspaceMountSchedulerRunner(bridge *SandboxRPCBridge, driver *fakeRPCSandboxDriver) *SchedulerSandboxRunner {
	return NewSchedulerSandboxRunner(SchedulerSandboxRunnerDeps{
		Config: bridge.config, Store: bridge.store, ConfigDB: bridge.configDB,
		WorkspaceEnsurer: workspaces.NewProvisioner(bridge.config, bridge.configDB, bridge.store),
		Driver:           driver, Streams: bridge.streams, AgentExecutor: bridge.agentExecutor,
	})
}

func TestIntegrationSchedulerWorkspaceMountPersistsAcrossStickyResume(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "read-write", true: "read-only"}[readOnly], func(t *testing.T) {
			ctx := context.Background()
			bridge, driver := newTestSandboxRPCBridge(t)
			projectRoot := t.TempDir()
			source := filepath.Join(projectRoot, "source")
			if err := os.MkdirAll(source, 0o755); err != nil {
				t.Fatal(err)
			}
			writeIntegrationWorkspaceFile(t, filepath.Join(source, "input.txt"), "shared source", 0o640)
			scheduler := createNativeTestSchedulerWithWorkspace(t, ctx, bridge.configDB, domain.Scheduler{Summary: domain.SchedulerSummary{
				ID: "scheduler-mount", Name: "Scheduler Mount", Driver: "docker", SandboxPolicy: domain.SchedulerSandboxPolicySticky,
			}}, projectRoot, &compose.WorkspaceSpec{Provider: "file", Path: "source", Target: "inputs", Mode: "mount", ReadOnly: readOnly})
			request := domain.SchedulerAgentRequest{BindingTriggerID: "mount-trigger"}
			runner := newWorkspaceMountSchedulerRunner(bridge, driver)
			created, _, err := runner.Ensure(ctx, scheduler, request, false)
			if err != nil {
				t.Fatal(err)
			}
			assertIntegrationWorkspaceReady(t, created, "after mount creation")
			if created.Workspace == nil {
				t.Fatal("mount snapshot missing")
			}
			var mount workspaces.FileWorkspaceMountConfig
			if err := json.Unmarshal([]byte(created.Workspace.ConfigJSON), &mount); err != nil {
				t.Fatal(err)
			}
			canonicalSource, err := filepath.EvalSymlinks(source)
			if err != nil {
				t.Fatal(err)
			}
			if mount.Mode != "mount" || mount.SourcePath != canonicalSource || mount.Target != "inputs" || mount.ReadOnly != readOnly {
				t.Fatalf("scheduler lost delivery fields: %#v", mount)
			}
			if err := runner.Shutdown(ctx, created.Summary.ID); err != nil {
				t.Fatal(err)
			}
			writeIntegrationWorkspaceFile(t, filepath.Join(source, "input.txt"), "source updated", 0o640)
			resumed, _, err := newWorkspaceMountSchedulerRunner(bridge, driver).Ensure(ctx, scheduler, request, false)
			if err != nil {
				t.Fatal(err)
			}
			if resumed.Summary.ID != created.Summary.ID || !reflect.DeepEqual(resumed.Workspace, created.Workspace) {
				t.Fatalf("sticky resume replaced stored mount: created=%#v resumed=%#v", created.Workspace, resumed.Workspace)
			}
			if !resumed.WorkspaceProvisioning.UpdatedAt.Equal(created.WorkspaceProvisioning.UpdatedAt) {
				t.Fatal("sticky resume reprovisioned ready mount")
			}
			manifest, err := testutil.WorkspaceManifest(resumed.Summary.WorkspacePath)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range manifest {
				if entry.Type != testutil.WorkspaceManifestEntryTypeDirectory {
					t.Fatalf("scheduler copied mount content: %#v", manifest)
				}
			}
			if _, err := os.Stat(filepath.Join(bridge.config.DataRoot, "workspaces")); !os.IsNotExist(err) {
				t.Fatalf("scheduler created intermediate workspace snapshots: %v", err)
			}
			if len(driver.startCalls) != 2 || driver.startCalls[0] != driver.startCalls[1] {
				t.Fatalf("expected sticky runtime recreation: %#v", driver.startCalls)
			}
			if content, err := os.ReadFile(filepath.Join(source, "input.txt")); err != nil || string(content) != "source updated" {
				t.Fatalf("source changed during lifecycle: %q, %v", content, err)
			}
		})
	}
}

func TestIntegrationSchedulerWorkspaceMountHonorsDockerDriverOverride(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	scheduler := createNativeTestSchedulerWithWorkspace(t, ctx, bridge.configDB, domain.Scheduler{Summary: domain.SchedulerSummary{
		ID: "mount-driver-override", Name: "Mount Driver Override", Driver: "boxlite",
	}}, t.TempDir(), &compose.WorkspaceSpec{Provider: "file", Path: ".", Mode: "mount"})
	sandbox, _, err := newWorkspaceMountSchedulerRunner(bridge, driver).Ensure(ctx, scheduler, domain.SchedulerAgentRequest{Driver: "docker"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if sandbox.Summary.Driver != "docker" || sandbox.Workspace == nil || len(driver.startCalls) != 1 {
		t.Fatalf("docker override did not create mounted sandbox: %#v", sandbox)
	}
	if _, err := os.Stat(filepath.Join(bridge.config.DataRoot, "workspaces")); !os.IsNotExist(err) {
		t.Fatalf("driver override created intermediate snapshot: %v", err)
	}
}

func TestIntegrationSchedulerWorkspaceMountRejectsEffectiveNonDockerDriverBeforeCreate(t *testing.T) {
	for _, test := range []struct {
		name          string
		driver        string
		requestDriver string
		defaultDriver string
		path          string
	}{
		{name: "boxlite-agent", driver: "boxlite"},
		{name: "microsandbox-agent", driver: "microsandbox"},
		{name: "k8s-agent", driver: "k8s"},
		{name: "request-override", driver: "docker", requestDriver: "boxlite"},
		{name: "daemon-default", defaultDriver: "microsandbox"},
		{name: "driver-before-missing-source", driver: "boxlite", path: "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			bridge, driver := newTestSandboxRPCBridge(t)
			if test.defaultDriver != "" {
				bridge.config.RuntimeDriver = test.defaultDriver
			}
			projectRoot := t.TempDir()
			path := test.path
			if path == "" {
				path = "."
			}
			scheduler := createNativeTestSchedulerWithWorkspace(t, ctx, bridge.configDB, domain.Scheduler{Summary: domain.SchedulerSummary{
				ID: "invalid-mount", Name: "Invalid Mount", Driver: test.driver,
			}}, projectRoot, &compose.WorkspaceSpec{Provider: "file", Path: path, Mode: "mount"})
			_, _, err := newWorkspaceMountSchedulerRunner(bridge, driver).Ensure(ctx, scheduler, domain.SchedulerAgentRequest{Driver: test.requestDriver}, false)
			if err == nil || !strings.Contains(err.Error(), "docker") {
				t.Fatalf("effective non-docker mount error = %v", err)
			}
			sandboxes, err := bridge.store.ListSandboxes(ctx, domain.SandboxListOptions{})
			if err != nil || sandboxes.TotalCount != 0 || len(driver.startCalls) != 0 {
				t.Fatalf("invalid mount persisted or started runtime: sandboxes=%#v starts=%v err=%v", sandboxes, driver.startCalls, err)
			}
			if _, err := os.Stat(filepath.Join(bridge.config.DataRoot, "workspaces")); !os.IsNotExist(err) {
				t.Fatalf("invalid mount created intermediate snapshot: %v", err)
			}
		})
	}
}

func TestIntegrationSchedulerWorkspaceRejectsInvalidDeliveryBeforeSourceResolution(t *testing.T) {
	for _, spec := range []compose.WorkspaceSpec{
		{Provider: "file", Path: ".", Mode: "map"},
		{Provider: "git", URL: "https://example.invalid/source", Mode: "mount"},
		{Provider: "file", Path: ".", ReadOnly: true},
	} {
		t.Run(spec.Provider+"-"+spec.Mode, func(t *testing.T) {
			bridge, driver := newTestSandboxRPCBridge(t)
			runner := newWorkspaceMountSchedulerRunner(bridge, driver)
			_, _, err := runner.inlineWorkspaceSnapshot(context.Background(), &domain.AgentDefinition{ID: "invalid-agent"}, &spec, "docker")
			if err == nil || strings.Contains(err.Error(), "project-managed") {
				t.Fatalf("delivery must fail before source resolution: %v", err)
			}
		})
	}
}

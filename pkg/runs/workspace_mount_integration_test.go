package runs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chaitin/agent-compose/internal/projects"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/workspaces"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestIntegrationProjectWorkspaceMountPersistsWithoutCopiesAcrossResume(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	source := filepath.Join(projectRoot, "source")
	writeProjectWorkspaceFile(t, filepath.Join(source, "input.txt"), "source v1", 0o644)
	config := &appconfig.Config{
		DataRoot: filepath.Join(root, "data"), DbAddr: filepath.Join(root, "data", "data.db"),
		SandboxRoot: filepath.Join(root, "sandboxes"), RuntimeDriver: "docker", DefaultImage: "guest:latest",
	}
	configDB, sandboxStore, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatal(err)
	}
	project, err := configDB.UpsertProject(ctx, domain.ProjectRecord{ID: "mounted-project", Name: "mounted-project", SourcePath: projectRoot})
	if err != nil {
		t.Fatal(err)
	}
	const spec = `{"name":"mounted-project","workspaces":[{"key":"shared","provider":"file","path":"source","mode":"mount","read_only":true,"target":"inputs"}],"agents":[{"name":"worker","provider":"codex","workspace":{"name":"shared"}}]}`
	revision, _, err := configDB.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{ProjectID: project.ID, SpecHash: "mount-v1", SpecJSON: spec})
	if err != nil {
		t.Fatal(err)
	}
	agentID, err := projects.StableProjectAgentID(project.ID, "worker")
	if err != nil {
		t.Fatal(err)
	}
	upsertProjectWorkspaceAgent(t, projectWorkspaceStoreHarness{Ctx: ctx, Store: configDB}, projectWorkspaceAgentSpec{Project: project, AgentID: agentID, Revision: revision.Revision})
	driver := &projectWorkspaceManifestDriver{store: sandboxStore}
	newController := func() *runs.Controller {
		return runs.NewController(runs.ControllerDependencies{
			Config: config, Store: sandboxStore, ConfigDB: configDB,
			WorkspaceEnsurer: workspaces.NewProvisioner(config, configDB, sandboxStore),
			Driver:           driver, Executor: projectWorkspaceExecutor{}, Images: projectWorkspaceImages{},
		})
	}
	var firstWorkspace *domain.SandboxWorkspace
	sandboxID := ""
	for attempt := range 2 {
		writeProjectWorkspaceFile(t, filepath.Join(source, "input.txt"), fmt.Sprintf("source v%d", attempt+1), 0o644)
		// A new controller/provisioner must use the stored mount on resume.
		run, execErr, err := newController().RunProjectAgent(ctx, runs.RunAgentRequest{
			ProjectID: project.ID, AgentName: "worker", Prompt: "inspect input", SandboxID: sandboxID,
			Source: domain.ProjectRunSourceAPI, ClientRequestID: fmt.Sprintf("mount-attempt-%d", attempt),
			CleanupPolicy: agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_STOP_ON_COMPLETION,
		}, nil)
		if err != nil || execErr != nil || run.Status != domain.ProjectRunStatusSucceeded {
			t.Fatalf("attempt %d run=%#v err=%v execErr=%v", attempt, run, err, execErr)
		}
		if sandboxID != "" && run.SandboxID != sandboxID {
			t.Fatalf("resume changed sandbox: %q → %q", sandboxID, run.SandboxID)
		}
		sandboxID = run.SandboxID
		sandbox, err := sandboxStore.GetSandbox(ctx, sandboxID)
		if err != nil {
			t.Fatal(err)
		}
		if sandbox.Workspace == nil || sandbox.Workspace.Type != "file" || sandbox.WorkspaceProvisioning == nil || sandbox.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
			t.Fatalf("stored workspace is not ready: %#v", sandbox)
		}
		var mount struct {
			Mode     string `json:"mode"`
			Target   string `json:"target"`
			ReadOnly bool   `json:"read_only"`
		}
		if err := json.Unmarshal([]byte(sandbox.Workspace.ConfigJSON), &mount); err != nil {
			t.Fatal(err)
		}
		if mount.Mode != "mount" || mount.Target != "inputs" || !mount.ReadOnly {
			t.Fatalf("revision lost mount fields: %s", sandbox.Workspace.ConfigJSON)
		}
		if firstWorkspace == nil {
			firstWorkspace = sandbox.Workspace
		} else if !reflect.DeepEqual(firstWorkspace, sandbox.Workspace) {
			t.Fatalf("resume replaced saved mount snapshot: first=%#v resumed=%#v", firstWorkspace, sandbox.Workspace)
		}
		if sandbox.Summary.WorkspacePath != filepath.Join(sandboxStore.SandboxDir(sandboxID), "workspace") {
			t.Fatalf("owned workspace path changed to shared source: %q", sandbox.Summary.WorkspacePath)
		}
		manifest := mustProjectWorkspaceManifest(t, sandbox.Summary.WorkspacePath)
		for _, entry := range manifest {
			if entry.Type != testutil.WorkspaceManifestEntryTypeDirectory {
				t.Fatalf("mount copied source content into owned workspace: %#v", manifest)
			}
		}
		if _, err := os.Stat(filepath.Join(config.DataRoot, "workspaces")); !os.IsNotExist(err) {
			t.Fatalf("mount created intermediate workspace snapshots: %v", err)
		}
	}
	if len(driver.starts) != 2 || driver.starts[0].sandboxID != driver.starts[1].sandboxID {
		t.Fatalf("expected the stored sandbox to restart: %#v", driver.starts)
	}
	if content, err := os.ReadFile(filepath.Join(source, "input.txt")); err != nil || string(content) != "source v2" {
		t.Fatalf("source changed during stop/resume: %q, %v", content, err)
	}
}

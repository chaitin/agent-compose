package runs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

func TestIntegrationJSONLAgentRunPersistsCellBeforeDetachAndResumes(t *testing.T) {
	ctx := context.Background()
	resumer := &fakeAgentResumer{cell: domain.NotebookCell{ID: "cell-9", Type: execution.CellTypeAgent, Output: "done", Success: true}}
	controller, run := newAgentReattachTestController(t, resumer)
	run.AgentName = "worker"
	controller.configDB.(*fakeControllerStore).project = domain.ProjectRecord{ID: run.ProjectID, Name: "project"}
	controller.configDB.(*fakeControllerStore).revision = domain.ProjectRevisionRecord{SpecJSON: `{"name":"project","agents":[{"name":"worker","provider":"codex"}]}`}
	run.AgentCellID = ""
	run.Detach = false
	run.ExecutionID = ""
	run.LogFormat = runlog.FormatJSONL
	logsPath, err := runlog.RunPath(controller.config.DataRoot, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	run.LogsPath = logsPath
	if _, err := controller.configDB.UpdateProjectRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	sandbox, err := controller.store.GetSandbox(ctx, run.SandboxID)
	if err != nil {
		t.Fatal(err)
	}
	stream := projectRunAgentExecutionStream(ctx, agentExecutionStreamRequest{
		Coordinator: NewCoordinator(controller.configDB, nil), Run: run, Sandbox: sandbox,
	})
	if err := stream.OnStart(domain.NotebookCell{ID: "cell-9"}); err != nil {
		t.Fatal(err)
	}
	// The detach callback holds the earlier run snapshot. It must preserve
	// the cell identity the executor persisted before launching the task.
	if err := controller.persistDetachedRunFields(ctx, run, "exec-1", domain.DetachKindAgent, run.Deadline); err != nil {
		t.Fatal(err)
	}
	persisted, err := controller.configDB.GetProjectRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.AgentCellID != "cell-9" || persisted.LogsPath != run.LogsPath || persisted.ExecutionID != "exec-1" {
		t.Fatalf("persisted execution=%+v", persisted)
	}
	got, err := controller.ReattachDetachedRun(ctx, persisted)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ProjectRunStatusSucceeded || resumer.resumed.CellID != "cell-9" {
		t.Fatalf("status=%q error=%q resume=%+v", got.Status, got.Error, resumer.resumed)
	}
	if got.LogsPath != run.LogsPath || filepath.Base(got.LogsPath) != runlog.FileName {
		t.Fatalf("logs path changed: %s", got.LogsPath)
	}
}

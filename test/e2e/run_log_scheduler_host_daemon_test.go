package e2e

import (
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

const runLogE2ESchedulerScript = `
function main() {
  scheduler.log("runlog-scheduler-begin");
  const shell = scheduler.shell("printf scheduler-shell-output", { sandboxPolicy: "sticky" });
  if (!shell.success) throw new Error("shell failed");
  const exec = scheduler.exec({ command: "printf", args: ["scheduler-exec-output"], sandboxPolicy: "sticky" });
  if (!exec.success) throw new Error("exec failed");
  const agent = scheduler.agent("runlog:scheduled:immediate", { sandboxPolicy: "sticky" });
  scheduler.log("runlog-scheduler-end");
  return { shell: shell.output, exec: exec.output, answer: agent.text };
}
scheduler.cron("manual", "0 0 1 1 *", main);
`

func (f *runLogE2E) exerciseScheduler(t *testing.T) {
	t.Helper()
	project := &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: f.projectID}}
	response, err := f.projects.RunScheduler(f.ctx, connect.NewRequest(&agentcomposev2.RunSchedulerRequest{Project: project, AgentName: "worker", TriggerId: "manual"}))
	if err != nil {
		t.Fatal(err)
	}
	run := response.Msg.GetRun()
	if run.GetStatus() != agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED {
		t.Fatalf("scheduler failed: %s", run)
	}
	events, err := f.projects.ListProjectSchedulerEvents(f.ctx, connect.NewRequest(&agentcomposev2.ListProjectSchedulerEventsRequest{Project: project, AgentName: "worker", Limit: 100}))
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[string]bool{"scheduler-shell-output": false, "scheduler-exec-output": false}
	steps := 0
	for _, event := range events.Msg.GetEvents() {
		if event.GetRunId() != run.GetRunId() || event.GetType() != "scheduler.command.completed" {
			continue
		}
		steps++
		if event.GetLinkedCellId() == "" || event.GetLinkedSandboxId() == "" {
			t.Fatalf("scheduler command lost artifact links: %s", event)
		}
		entries, _ := f.logs(t, &agentcomposev2.FollowRunLogsRequest{SchedulerId: run.GetSchedulerId(), SchedulerRunId: run.GetRunId(), CellId: event.GetLinkedCellId(), Structured: true})
		_, text := f.logs(t, &agentcomposev2.FollowRunLogsRequest{SchedulerId: run.GetSchedulerId(), SchedulerRunId: run.GetRunId(), CellId: event.GetLinkedCellId()})
		if _, ok := outputs[text]; !ok || outputs[text] {
			t.Fatalf("missing or duplicate scheduler step output %q", text)
		}
		outputs[text] = true
		if len(entries) != 1 || entries[0].GetOutput().GetData() != text {
			t.Fatalf("step's structured and text logs differ: entries=%v text=%q", entries, text)
		}
	}
	if steps != 2 || !outputs["scheduler-shell-output"] || !outputs["scheduler-exec-output"] {
		t.Fatalf("scheduler step coverage: steps=%d outputs=%v", steps, outputs)
	}
	children, err := f.runs.ListRuns(f.ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{ProjectId: f.projectID, SchedulerRunId: run.GetRunId(), Limit: 100}))
	if err != nil {
		t.Fatal(err)
	}
	if len(children.Msg.GetRuns()) != 1 {
		t.Fatalf("scheduler agent created missing or duplicate child runs: %s", children.Msg)
	}
	child := children.Msg.GetRuns()[0]
	if child.GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED || child.GetLogIncomplete() {
		t.Fatalf("scheduler child failed: %s", child)
	}
	f.assertMessages(t, child.GetRunId(), []string{runLogE2EAnswer("scheduled")}, false)
	f.assertLog(t, child.GetRunId(), []string{runLogE2EAnswer("scheduled")})
	if !strings.Contains(run.GetResultJson(), "scheduler-shell-output") || !strings.Contains(run.GetResultJson(), "scheduler-exec-output") {
		t.Fatalf("scheduler result lost command outputs: %s", run)
	}
	t.Logf("verified shell/exec step logs and agent child history: driver=%s scheduler_run=%s", f.driver, run.GetRunId())
}

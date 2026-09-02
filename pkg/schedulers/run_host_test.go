package schedulers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

func TestRuntimeHostAgentCommandLLMAndSessionRPC(t *testing.T) {
	ctx := context.Background()
	scheduler := domain.Scheduler{
		Summary: domain.SchedulerSummary{ID: "scheduler-host", Name: "Scheduler Host", Runtime: domain.SchedulerRuntimeScheduler, DefaultAgent: "gemini"},
	}
	run := &domain.SchedulerRunSummary{ID: "run-host", SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-host"}
	store := &hostStoreFake{}
	events := &hostEventsFake{}
	sandboxes := &hostSessionsFake{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-host", VMStatus: domain.VMStatusRunning}},
	}
	agentExecutor := &hostAgentExecutorFake{cell: domain.NotebookCell{
		ID:            "cell-agent",
		Output:        "agent text",
		Agent:         "gemini",
		AgentThreadID: "agent-session",
		StopReason:    "complete",
		Success:       true,
	}}
	commandExecutor := &hostCommandExecutorFake{result: domain.SchedulerCommandResult{
		Output:    "command output",
		Success:   true,
		SandboxID: "session-host",
		CellID:    "cell-command",
	}}
	llm := &hostLLMFake{result: domain.SchedulerLLMResult{Text: "llm text", Model: "model-a", ResponseID: "resp-1", FinishReason: "stop"}}
	rpc := &hostRPCFake{response: `{"sessionId":"sandbox-rpc"}`}
	publisher := &hostPublisherFake{}
	host := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		Store:            store,
		Events:           events,
		Sessions:         sandboxes,
		AgentDefinitions: hostAgentDefinitionsFake{},
		AgentExecutor:    agentExecutor,
		CommandExecutor:  commandExecutor,
		LLM:              llm,
		SandboxRPC:       rpc,
		Publisher:        publisher,
		CommandRequiresCleanup: func(domain.Scheduler, domain.SchedulerCommandRequest) bool {
			return true
		},
		LinkedSandboxIDFromJSON: func(_, _, responseJSON string) string {
			if strings.Contains(responseJSON, "sandbox-rpc") {
				return "sandbox-rpc"
			}
			return ""
		},
	}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{EventID: "topic-event"})

	agentResult, err := host.Agent(ctx, "summarize", domain.SchedulerAgentRequest{})
	if err != nil {
		t.Fatalf("Agent returned error: %v", err)
	}
	if agentResult.Text != "agent text" || agentExecutor.request.Provider != "gemini" || len(sandboxes.shutdowns) != 1 {
		t.Fatalf("agent result/request/shutdowns = %#v/%#v/%#v", agentResult, agentExecutor.request, sandboxes.shutdowns)
	}
	if len(publisher.events) != 1 || publisher.events[0].topic != "agent-compose.agent.completed" {
		t.Fatalf("publisher events = %#v", publisher.events)
	}
	if !events.contains("scheduler.sandbox.created") || !events.contains("scheduler.agent.completed") || !events.contains("scheduler.sandbox.stopped") {
		t.Fatalf("agent events = %#v", events.types())
	}

	_, err = host.Command(ctx, domain.SchedulerCommandRequest{Mode: "shell", Command: "echo one"})
	if err != nil {
		t.Fatalf("Command first returned error: %v", err)
	}
	_, err = host.Command(ctx, domain.SchedulerCommandRequest{Mode: "shell", Command: "echo two"})
	if err != nil {
		t.Fatalf("Command second returned error: %v", err)
	}
	if sandboxes.ensureCalls != 2 || sandboxes.loadCalls != 1 || commandExecutor.calls != 2 {
		t.Fatalf("command ensure/load/exec calls = %d/%d/%d", sandboxes.ensureCalls, sandboxes.loadCalls, commandExecutor.calls)
	}
	host.CleanupCommandSessions(ctx)
	if len(sandboxes.shutdowns) != 2 || sandboxes.shutdowns[1] != "session-host" {
		t.Fatalf("shutdowns after cleanup = %#v", sandboxes.shutdowns)
	}
	if !events.contains("scheduler.command.started") || !events.contains("scheduler.command.completed") {
		t.Fatalf("command events = %#v", events.types())
	}

	llmResult, err := host.LLM(ctx, "prompt", domain.SchedulerLLMRequest{Model: "model-a"})
	if err != nil {
		t.Fatalf("LLM returned error: %v", err)
	}
	if llmResult.Text != "llm text" || llm.prompt != "prompt" {
		t.Fatalf("llm result/prompt = %#v/%q", llmResult, llm.prompt)
	}
	if !events.contains("scheduler.llm.completed") {
		t.Fatalf("llm events = %#v", events.types())
	}

	responseJSON, err := host.CallSandboxRPC(ctx, "GetSandbox", `{"sandboxId":"sandbox-rpc"}`)
	if err != nil {
		t.Fatalf("CallSandboxRPC returned error: %v", err)
	}
	if responseJSON != rpc.response || rpc.source != domain.SandboxTypeScript+":"+scheduler.Summary.ID {
		t.Fatalf("rpc response/source = %q/%q", responseJSON, rpc.source)
	}
	if rpc.creation.Provider != "gemini" || rpc.creation.AgentDefinitionID != "" {
		t.Fatalf("rpc sandbox creation context = %#v", rpc.creation)
	}
	if !store.containsLink("sandbox-rpc", "sandbox_rpc_completed") {
		t.Fatalf("sandbox links = %#v", store.links)
	}
}

func TestRuntimeHostAgentPrefersAssistantMessageOverTranscript(t *testing.T) {
	ctx := context.Background()
	scheduler := domain.Scheduler{
		Summary: domain.SchedulerSummary{ID: "scheduler-final-text", Runtime: domain.SchedulerRuntimeScheduler, DefaultAgent: "codex"},
	}
	run := &domain.SchedulerRunSummary{ID: "run-final-text", SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-final-text"}
	newHost := func(agentExecutor *hostAgentExecutorFake) *schedulers.RuntimeHost {
		return schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
			Store:            &hostStoreFake{},
			Events:           &hostEventsFake{},
			Sessions:         &hostSessionsFake{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-final-text", VMStatus: domain.VMStatusRunning}}},
			AgentDefinitions: hostAgentDefinitionsFake{},
			AgentExecutor:    agentExecutor,
			Publisher:        &hostPublisherFake{},
		}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{EventID: "topic-event"})
	}

	// A provider that emits a distinct final assistant message: Text/FinalText
	// must carry that message, not the raw transcript in cell.Output.
	withMessage := &hostAgentExecutorFake{
		cell:          domain.NotebookCell{ID: "cell-1", Output: "thinking...\ncat SKILL.md\n{...}", Success: true},
		assistantText: "here is the answer",
	}
	result, err := newHost(withMessage).Agent(ctx, "prompt", domain.SchedulerAgentRequest{})
	if err != nil {
		t.Fatalf("Agent returned error: %v", err)
	}
	if result.FinalText != "here is the answer" || result.Text != "here is the answer" {
		t.Fatalf("FinalText/Text = %q/%q, want the assistant message", result.FinalText, result.Text)
	}
	if result.Output != withMessage.cell.Output {
		t.Fatalf("Output = %q, want the untouched transcript %q", result.Output, withMessage.cell.Output)
	}
	if result.FinalText == result.Output {
		t.Fatalf("FinalText must not equal the transcript Output when a distinct assistant message exists")
	}

	// No distinct assistant message (e.g. transcript-fallback source): FinalText
	// keeps falling back to the transcript, same as before this fix.
	withoutMessage := &hostAgentExecutorFake{
		cell: domain.NotebookCell{ID: "cell-2", Output: "plain transcript", Success: true},
	}
	result, err = newHost(withoutMessage).Agent(ctx, "prompt", domain.SchedulerAgentRequest{})
	if err != nil {
		t.Fatalf("Agent returned error: %v", err)
	}
	if result.FinalText != "plain transcript" || result.Output != "plain transcript" {
		t.Fatalf("FinalText/Output = %q/%q, want both to fall back to the transcript", result.FinalText, result.Output)
	}

	// On a failed run, agentAssistantMessage synthesizes a placeholder like
	// "<agent> failed without output" instead of returning "" — that must
	// not displace the real transcript, which carries the actual failure
	// diagnostics.
	failed := &hostAgentExecutorFake{
		cell:          domain.NotebookCell{ID: "cell-3", Output: "tool call failed: ENOENT /workspace/build.sh\nstack: ...", Success: false},
		assistantText: "codex failed without output",
	}
	result, err = newHost(failed).Agent(ctx, "prompt", domain.SchedulerAgentRequest{})
	if err != nil {
		t.Fatalf("Agent returned error: %v", err)
	}
	if result.FinalText != failed.cell.Output || result.Text != failed.cell.Output {
		t.Fatalf("FinalText/Text = %q/%q, want the real transcript %q, not the synthesized failure summary", result.FinalText, result.Text, failed.cell.Output)
	}
}

func TestRuntimeHostLLMModelPriorityAndEnvironmentPropagation(t *testing.T) {
	for _, test := range []struct {
		name           string
		requestModel   string
		envModel       string
		schedulerModel string
		agentModel     string
		want           string
	}{
		{name: "request", requestModel: "request-model", envModel: "env-model", schedulerModel: "scheduler-model", agentModel: "agent-model", want: "request-model"},
		{name: "environment", envModel: "env-model", schedulerModel: "scheduler-model", agentModel: "agent-model", want: "env-model"},
		{name: "scheduler", schedulerModel: "scheduler-model", agentModel: "agent-model", want: "scheduler-model"},
		{name: "agent", agentModel: "agent-model", want: "agent-model"},
		{name: "daemon default", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &hostLLMFake{}
			env := []domain.SandboxEnvVar{{Name: "LLM_API_KEY", Value: "secret", Secret: true}}
			if test.envModel != "" {
				env = append(env, domain.SandboxEnvVar{Name: "LLM_MODEL", Value: test.envModel})
			}
			scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler-priority"}, Model: test.schedulerModel, AgentModel: test.agentModel, EnvItems: env}
			host := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{LLM: runner}, scheduler, schedulers.RuntimeExecutionContext{}, schedulers.TriggerEventMetadata{})
			if _, err := host.LLM(context.Background(), "prompt", domain.SchedulerLLMRequest{Model: test.requestModel}); err != nil {
				t.Fatal(err)
			}
			if runner.request.Model != test.want || runner.request.SchedulerID != scheduler.Summary.ID || len(runner.request.EnvItems) != len(env) {
				t.Fatalf("request = %#v, want model %q", runner.request, test.want)
			}
		})
	}
}

func TestRuntimeHostProjectAgentPath(t *testing.T) {
	ctx := context.Background()
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{
		ID:                 "scheduler-project",
		ProjectID:          "project-1",
		AgentName:          "reviewer",
		ProjectSchedulerID: "scheduler-1",
	}}
	run := &domain.SchedulerRunSummary{ID: "run-project", SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-1"}
	events := &hostEventsFake{}
	projectRunner := &hostProjectAgentRunnerFake{run: domain.ProjectRunRecord{
		RunID:      "project-run",
		ProjectID:  "project-1",
		AgentName:  "reviewer",
		Status:     domain.ProjectRunStatusSucceeded,
		SandboxID:  "session-project",
		Output:     "project output",
		ResultJSON: `{"ok":true}`,
	}}
	publisher := &hostPublisherFake{}
	host := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		Store:              &hostStoreFake{},
		Events:             events,
		ProjectAgentRunner: projectRunner,
		Publisher:          publisher,
	}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{EventID: "topic-event"})

	expectedConfigHash, err := schedulers.SchedulerSandboxConfigHash(scheduler)
	if err != nil {
		t.Fatalf("SchedulerSandboxConfigHash returned error: %v", err)
	}
	result, err := host.Agent(ctx, "review", domain.SchedulerAgentRequest{})
	if err != nil {
		t.Fatalf("Project Agent returned error: %v", err)
	}
	if result.Text != "project output" || projectRunner.request.ProjectID != "project-1" || projectRunner.request.ClientRequestID != run.ID+":agent:1" || projectRunner.request.TriggerID != run.TriggerID || projectRunner.request.SandboxConfigHash != expectedConfigHash {
		t.Fatalf("project result/request = %#v/%#v", result, projectRunner.request)
	}
	if !events.contains("scheduler.agent.completed") || len(publisher.events) != 1 || publisher.events[0].payload["projectRunId"] != "project-run" {
		t.Fatalf("events/publisher = %#v/%#v", events.types(), publisher.events)
	}
	if publisher.events[0].payload["schedulerRunId"] != run.ID {
		t.Fatalf("trigger execution publisher payload = %#v", publisher.events[0].payload)
	}
	invocationPublisher := &hostPublisherFake{}
	invocationHost := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		ProjectAgentRunner: projectRunner,
		Publisher:          invocationPublisher,
	}, scheduler, schedulers.RuntimeExecutionContext{ID: "invocation-correlation", Kind: schedulers.ExecutionKindInvocation}, schedulers.TriggerEventMetadata{})
	if _, err := invocationHost.Agent(ctx, "review", domain.SchedulerAgentRequest{}); err != nil {
		t.Fatalf("Invocation Project Agent returned error: %v", err)
	}
	if len(invocationPublisher.events) != 1 || invocationPublisher.events[0].payload["projectRunId"] != "project-run" {
		t.Fatalf("invocation publisher = %#v", invocationPublisher.events)
	}
	if _, ok := invocationPublisher.events[0].payload["schedulerRunId"]; ok {
		t.Fatalf("invocation payload exposed correlation id as scheduler run: %#v", invocationPublisher.events[0].payload)
	}
}

func TestRuntimeHostProjectAgentUsesUniqueRequestIDs(t *testing.T) {
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{
		ID:        "scheduler-project",
		ProjectID: "project-1",
		AgentName: "reviewer",
	}}
	run := &domain.SchedulerRunSummary{ID: "run-project", SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-1"}
	projectRunner := &hostProjectAgentRunnerFake{run: domain.ProjectRunRecord{
		RunID:     "project-run",
		ProjectID: "project-1",
		AgentName: "reviewer",
		Status:    domain.ProjectRunStatusSucceeded,
	}}
	host := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		ProjectAgentRunner: projectRunner,
	}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{})

	for range 2 {
		if _, err := host.Agent(context.Background(), "review", domain.SchedulerAgentRequest{}); err != nil {
			t.Fatalf("Project Agent returned error: %v", err)
		}
	}

	if len(projectRunner.requests) != 2 || projectRunner.requests[0].ClientRequestID != run.ID+":agent:1" || projectRunner.requests[1].ClientRequestID != run.ID+":agent:2" {
		t.Fatalf("project request IDs = %#v", projectRunner.requests)
	}
}

func TestRuntimeHostErrorBranches(t *testing.T) {
	ctx := context.Background()
	scheduler := domain.Scheduler{
		Summary: domain.SchedulerSummary{ID: "scheduler-errors", Name: "Scheduler Errors", Runtime: domain.SchedulerRuntimeScheduler, DefaultAgent: "codex"},
	}
	run := &domain.SchedulerRunSummary{ID: "run-errors", SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-errors"}

	events := &hostEventsFake{}
	host := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		Events: events,
		Sessions: &hostSessionsFake{
			session:     &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-agent", VMStatus: domain.VMStatusRunning}},
			shutdownErr: errors.New("stop failed"),
		},
		AgentDefinitions: hostAgentDefinitionsFake{},
		AgentExecutor: &hostAgentExecutorFake{
			cell: domain.NotebookCell{ID: "cell-agent", Stderr: "agent stderr", Success: false, ExitCode: 2},
			err:  errors.New("agent failed"),
		},
		Publisher: &hostPublisherFake{},
	}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{EventID: "topic-event"})
	agentResult, err := host.Agent(ctx, "prompt", domain.SchedulerAgentRequest{})
	if err == nil || agentResult.Text != "agent stderr" || !events.contains("scheduler.agent.failed") || !events.contains("scheduler.sandbox.stop_failed") {
		t.Fatalf("agent error result=%#v err=%v events=%#v", agentResult, err, events.types())
	}

	projectEvents := &hostEventsFake{}
	projectScheduler := domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler-project-errors", ProjectID: "project-1", AgentName: "reviewer"}}
	projectHost := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		Events: projectEvents,
		ProjectAgentRunner: &hostProjectAgentRunnerFake{run: domain.ProjectRunRecord{
			RunID:     "project-run",
			ProjectID: "project-1",
			AgentName: "reviewer",
			Status:    domain.ProjectRunStatusFailed,
			Error:     "project failed",
		}},
		Publisher: &hostPublisherFake{},
	}, projectScheduler, triggerExecution(run), schedulers.TriggerEventMetadata{})
	projectResult, err := projectHost.Agent(ctx, "prompt", domain.SchedulerAgentRequest{})
	if err != nil || projectResult.Text != "project failed" || !projectEvents.contains("scheduler.agent.failed") {
		t.Fatalf("project failed result=%#v err=%v events=%#v", projectResult, err, projectEvents.types())
	}
	projectHost = schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		ProjectAgentRunner: &hostProjectAgentRunnerFake{err: errors.New("project unavailable")},
	}, projectScheduler, triggerExecution(run), schedulers.TriggerEventMetadata{})
	if _, err := projectHost.Agent(ctx, "prompt", domain.SchedulerAgentRequest{}); err == nil {
		t.Fatalf("project runner error returned nil")
	}

	commandEvents := &hostEventsFake{}
	commandHost := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		Events: commandEvents,
		Sessions: &hostSessionsFake{
			session:   &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-command", VMStatus: domain.VMStatusRunning}},
			ensureErr: errors.New("ensure failed"),
		},
	}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{})
	if _, err := commandHost.Command(ctx, domain.SchedulerCommandRequest{Mode: "shell", Command: "echo"}); err == nil || !commandEvents.contains("scheduler.command.failed") {
		t.Fatalf("command ensure err=%v events=%#v", err, commandEvents.types())
	}
	commandEvents = &hostEventsFake{}
	commandHost = schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		Events:          commandEvents,
		Sessions:        &hostSessionsFake{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-command", VMStatus: domain.VMStatusRunning}}},
		CommandExecutor: &hostCommandExecutorFake{err: errors.New("command failed"), result: domain.SchedulerCommandResult{SandboxID: "session-command", CellID: "cell-command", Output: "partial"}},
	}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{})
	if result, err := commandHost.Command(ctx, domain.SchedulerCommandRequest{Mode: "shell", Command: "false"}); err == nil || result.Output != "partial" || !commandEvents.contains("scheduler.command.failed") {
		t.Fatalf("command executor result=%#v err=%v events=%#v", result, err, commandEvents.types())
	}
	commandEvents = &hostEventsFake{}
	commandHost = schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		Events:          commandEvents,
		Sessions:        &hostSessionsFake{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-command", VMStatus: domain.VMStatusRunning}}},
		CommandExecutor: &hostCommandExecutorFake{result: domain.SchedulerCommandResult{Output: "bad", Success: false, SandboxID: "session-command"}},
	}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{})
	if result, err := commandHost.Command(ctx, domain.SchedulerCommandRequest{Mode: "shell", Command: "false"}); err != nil || result.Success || !commandEvents.contains("scheduler.command.completed") {
		t.Fatalf("command nonzero result=%#v err=%v events=%#v", result, err, commandEvents.types())
	}

	if _, err := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{}).LLM(ctx, "prompt", domain.SchedulerLLMRequest{}); err == nil {
		t.Fatalf("nil LLM returned nil error")
	}
	llmEvents := &hostEventsFake{}
	llmHost := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		Events: llmEvents,
		LLM:    &hostLLMFake{err: errors.New("llm failed")},
	}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{})
	if _, err := llmHost.LLM(ctx, "prompt", domain.SchedulerLLMRequest{Model: "model-a"}); err == nil || !llmEvents.contains("scheduler.llm.failed") {
		t.Fatalf("llm err=%v events=%#v", err, llmEvents.types())
	}

	if _, err := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{}).CallSandboxRPC(ctx, "GetSandbox", `{}`); err == nil {
		t.Fatalf("nil session RPC returned nil error")
	}
	rpcEvents := &hostEventsFake{}
	rpcStore := &hostStoreFake{}
	rpcHost := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		Store:      rpcStore,
		Events:     rpcEvents,
		SandboxRPC: &hostRPCFake{response: `{"sandboxId":"sandbox-rpc"}`, err: errors.New("rpc failed")},
		LinkedSandboxIDFromJSON: func(_, _, responseJSON string) string {
			if strings.Contains(responseJSON, "sandbox-rpc") {
				return "sandbox-rpc"
			}
			return ""
		},
	}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{EventID: "topic-event"})
	if _, err := rpcHost.CallSandboxRPC(ctx, "GetSandbox", `{"sandboxId":"sandbox-rpc"}`); err == nil || !rpcEvents.contains("scheduler.sandbox.rpc.failed") || !rpcStore.containsLink("sandbox-rpc", "sandbox_rpc_failed") {
		t.Fatalf("rpc err=%v events=%#v links=%#v", err, rpcEvents.types(), rpcStore.links)
	}
}

func TestRuntimeHostCommandPersistsRunSandboxLinkBeforeExecution(t *testing.T) {
	ctx := context.Background()
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler-link"}}
	run := &domain.SchedulerRunSummary{ID: "run-link", SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-link"}
	events := &hostEventsFake{}
	executor := &hostCommandExecutorFake{}
	executor.onExecute = func() {
		if len(events.items) == 0 {
			t.Fatal("command executed before its sandbox link was persisted")
		}
		linked := events.items[len(events.items)-1]
		if linked.Type != "scheduler.command.started" || linked.RunID != run.ID || linked.LinkedSandboxID != "sandbox-link" {
			t.Fatalf("last pre-execution event = %#v, want linked scheduler.command.started", linked)
		}
	}
	host := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		Events:          events,
		Sessions:        &hostSessionsFake{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-link", VMStatus: domain.VMStatusRunning}}},
		CommandExecutor: executor,
	}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{})

	if _, err := host.Command(ctx, domain.SchedulerCommandRequest{Mode: "shell", Command: "echo linked"}); err != nil {
		t.Fatalf("Command returned error: %v", err)
	}
	if executor.calls != 1 {
		t.Fatalf("command executor calls = %d, want 1", executor.calls)
	}
}

func TestRuntimeHostCommandCompletedMessageAlwaysEmpty(t *testing.T) {
	ctx := context.Background()
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler-empty-message"}}
	run := &domain.SchedulerRunSummary{ID: "run-empty-message", SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-empty-message"}

	largeOutput := strings.Repeat("x", 200_000)
	cases := []struct {
		name   string
		result domain.SchedulerCommandResult
	}{
		{"small output", domain.SchedulerCommandResult{Output: "ok", Success: true, SandboxID: "sandbox-small", CellID: "cell-small"}},
		{"large output", domain.SchedulerCommandResult{Output: largeOutput, Success: true, SandboxID: "sandbox-large", CellID: "cell-large"}},
		{"empty output", domain.SchedulerCommandResult{Success: true, SandboxID: "sandbox-none", CellID: "cell-none"}},
		{"failed exit code", domain.SchedulerCommandResult{Output: "boom", Success: false, SandboxID: "sandbox-fail", CellID: "cell-fail"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := &hostEventsFake{}
			host := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
				Events:          events,
				Sessions:        &hostSessionsFake{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: tc.result.SandboxID, VMStatus: domain.VMStatusRunning}}},
				CommandExecutor: &hostCommandExecutorFake{result: tc.result},
			}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{})

			if _, err := host.Command(ctx, domain.SchedulerCommandRequest{Mode: "shell", Command: "echo"}); err != nil {
				t.Fatalf("Command returned error: %v", err)
			}
			event, ok := events.find("scheduler.command.completed")
			if !ok {
				t.Fatalf("scheduler.command.completed event not recorded, events = %#v", events.types())
			}
			if event.Message != "" {
				t.Fatalf("message = %q, want empty regardless of output size (§4)", event.Message)
			}

			var payload map[string]any
			if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
				t.Fatalf("unmarshal payload_json: %v", err)
			}
			if _, ok := payload["messageTruncated"]; ok {
				t.Fatalf("payload_json contains messageTruncated, which was never introduced: %#v", payload)
			}
			gotBytes, ok := payload["outputBytes"].(float64)
			if !ok {
				t.Fatalf("payload_json missing numeric outputBytes: %#v", payload)
			}
			if int(gotBytes) != len(tc.result.Output) {
				t.Fatalf("outputBytes = %v, want %d", gotBytes, len(tc.result.Output))
			}
		})
	}
}

func TestRuntimeHostCommandDoesNotExecuteWhenRunSandboxLinkFails(t *testing.T) {
	ctx := context.Background()
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler-link-failure"}}
	run := &domain.SchedulerRunSummary{ID: "run-link-failure", SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-link-failure"}
	persistErr := errors.New("event persistence failed")
	events := &hostEventsFake{failType: "scheduler.command.started", addRecordErr: persistErr}
	executor := &hostCommandExecutorFake{}
	host := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		Events:          events,
		Sessions:        &hostSessionsFake{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-link-failure", VMStatus: domain.VMStatusRunning}}},
		CommandExecutor: executor,
	}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{})

	if _, err := host.Command(ctx, domain.SchedulerCommandRequest{Mode: "shell", Command: "must not run"}); !errors.Is(err, persistErr) {
		t.Fatalf("Command error = %v, want wrapped persistence error", err)
	}
	if executor.calls != 0 {
		t.Fatalf("command executor calls = %d, want 0", executor.calls)
	}
	if !events.contains("scheduler.command.failed") {
		t.Fatalf("events after sandbox link failure = %#v, want scheduler.command.failed", events.types())
	}
}

func TestRuntimeHostTriggerCommandWithoutSchedulerEventRecorderStillExecutes(t *testing.T) {
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler-link-missing-recorder"}}
	run := &domain.SchedulerRunSummary{ID: "run-link-missing-recorder", SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-link-missing-recorder"}
	executor := &hostCommandExecutorFake{}
	store := &hostStoreFake{}
	host := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		Store:           store,
		Sessions:        &hostSessionsFake{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-link-missing-recorder", VMStatus: domain.VMStatusRunning}}},
		CommandExecutor: executor,
	}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{EventID: "trigger-event-link-missing-recorder"})

	if _, err := host.Command(context.Background(), domain.SchedulerCommandRequest{Mode: "shell", Command: "run without events"}); err != nil {
		t.Fatalf("Command returned error: %v", err)
	}
	if executor.calls != 1 {
		t.Fatalf("command executor calls = %d, want 1", executor.calls)
	}
	if len(store.links) != 0 {
		t.Fatalf("event sandbox links = %#v, want none when event recorder is unavailable", store.links)
	}
}

func TestRuntimeHostInvocationCommandDoesNotRequireSchedulerRunLink(t *testing.T) {
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler-invocation"}}
	executor := &hostCommandExecutorFake{}
	host := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		Sessions:        &hostSessionsFake{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-invocation", VMStatus: domain.VMStatusRunning}}},
		CommandExecutor: executor,
	}, scheduler, schedulers.RuntimeExecutionContext{ID: "invocation", Kind: schedulers.ExecutionKindInvocation}, schedulers.TriggerEventMetadata{})

	if _, err := host.Command(context.Background(), domain.SchedulerCommandRequest{Mode: "shell", Command: "echo invocation"}); err != nil {
		t.Fatalf("invocation Command returned error: %v", err)
	}
	if executor.calls != 1 {
		t.Fatalf("command executor calls = %d, want 1", executor.calls)
	}
}

func TestRuntimeHostLogPublishEventAndState(t *testing.T) {
	ctx := context.Background()
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler-state", Name: "State Scheduler"}}
	run := &domain.SchedulerRunSummary{ID: "run-state", SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-state"}
	store := &hostStoreFake{}
	events := &hostEventsFake{}
	host := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		Store:  store,
		Events: events,
	}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{
		EventID:       "trigger-event",
		CorrelationID: "correlation-1",
	})

	if err := host.Log(ctx, "hello", map[string]any{"ok": true}); err != nil {
		t.Fatalf("Log returned error: %v", err)
	}
	if !events.contains("scheduler.log") {
		t.Fatalf("events after Log = %#v", events.types())
	}

	created, err := host.PublishEvent(ctx, "runtime.demo", `{"value":1}`)
	if err != nil {
		t.Fatalf("PublishEvent returned error: %v", err)
	}
	if created.Topic != "runtime.demo" || created.Sequence != 7 || created.PayloadJSON == `{"value":1}` {
		t.Fatalf("created event = %#v", created)
	}
	if created.PublisherRunID != run.ID {
		t.Fatalf("trigger publisher run ID = %q, want %q", created.PublisherRunID, run.ID)
	}
	if !events.contains("scheduler.event.published") {
		t.Fatalf("events after PublishEvent = %#v", events.types())
	}
	invocationStore := &hostStoreFake{}
	invocationHost := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{Store: invocationStore}, scheduler, schedulers.RuntimeExecutionContext{
		ID: "invocation-correlation", Kind: schedulers.ExecutionKindInvocation,
	}, schedulers.TriggerEventMetadata{})
	invocationEvent, err := invocationHost.PublishEvent(ctx, "runtime.demo", `{"value":2}`)
	if err != nil {
		t.Fatalf("Invocation PublishEvent returned error: %v", err)
	}
	if invocationEvent.PublisherRunID != "" {
		t.Fatalf("invocation correlation ID exposed as publisher run ID: %#v", invocationEvent)
	}

	if err := host.StateSet(ctx, "cursor", `{"offset":2}`); err != nil {
		t.Fatalf("StateSet returned error: %v", err)
	}
	value, ok, err := host.StateGet(ctx, "cursor")
	if err != nil || !ok || value != `{"offset":2}` {
		t.Fatalf("StateGet value=%q ok=%v err=%v", value, ok, err)
	}
	if err := host.StateDelete(ctx, "cursor"); err != nil {
		t.Fatalf("StateDelete returned error: %v", err)
	}
	if _, ok, err := host.StateGet(ctx, "cursor"); err != nil || ok {
		t.Fatalf("StateGet after delete ok=%v err=%v", ok, err)
	}

	missingStoreHost := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{})
	if _, err := missingStoreHost.PublishEvent(ctx, "runtime.demo", `{}`); err == nil || !strings.Contains(err.Error(), "event store is unavailable") {
		t.Fatalf("PublishEvent missing store error = %v", err)
	}
}

func triggerExecution(run *domain.SchedulerRunSummary) schedulers.RuntimeExecutionContext {
	return schedulers.RuntimeExecutionContext{ID: run.ID, TriggerID: run.TriggerID, Kind: schedulers.ExecutionKindTrigger}
}

type hostStoreFake struct {
	events []domain.TopicEventRecord
	state  map[string]string
	links  []domain.EventSandboxLink
}

func (s *hostStoreFake) CreateEvent(_ context.Context, event domain.TopicEventRecord) (domain.TopicEventRecord, error) {
	event.ID = firstNonEmptyTest(event.ID, "event-created")
	event.Sequence = 7
	s.events = append(s.events, event)
	return event, nil
}

func (s *hostStoreFake) UpdateEventPayload(_ context.Context, eventID, payloadJSON string) error {
	for index := range s.events {
		if s.events[index].ID == eventID {
			s.events[index].PayloadJSON = payloadJSON
		}
	}
	return nil
}

func (s *hostStoreFake) GetSchedulerState(_ context.Context, _, key string) (string, bool, error) {
	value, ok := s.state[key]
	return value, ok, nil
}

func (s *hostStoreFake) SetSchedulerState(_ context.Context, _, key, valueJSON string) error {
	if s.state == nil {
		s.state = map[string]string{}
	}
	s.state[key] = valueJSON
	return nil
}

func (s *hostStoreFake) DeleteSchedulerState(_ context.Context, _, key string) error {
	delete(s.state, key)
	return nil
}

func (s *hostStoreFake) AddEventSandboxLink(_ context.Context, link domain.EventSandboxLink) error {
	s.links = append(s.links, link)
	return nil
}

func (s *hostStoreFake) containsLink(sessionID, relation string) bool {
	for _, link := range s.links {
		if link.SandboxID == sessionID && link.Relation == relation {
			return true
		}
	}
	return false
}

type hostEventsFake struct {
	items        []domain.SchedulerEvent
	failType     string
	addRecordErr error
}

func (e *hostEventsFake) Add(ctx context.Context, event schedulers.SchedulerEventInput) error {
	_, err := e.AddRecord(ctx, event)
	return err
}

func (e *hostEventsFake) AddRecord(_ context.Context, input schedulers.SchedulerEventInput) (domain.SchedulerEvent, error) {
	if e.addRecordErr != nil && (e.failType == "" || e.failType == input.EventType) {
		return domain.SchedulerEvent{}, e.addRecordErr
	}
	payloadJSON := ""
	if input.Payload != nil {
		if data, err := json.Marshal(input.Payload); err == nil {
			payloadJSON = string(data)
		}
	}
	event := domain.SchedulerEvent{
		ID:                  fmt.Sprintf("event-%d", len(e.items)+1),
		SchedulerID:         input.SchedulerID,
		RunID:               input.RunID,
		TriggerID:           input.TriggerID,
		Type:                input.EventType,
		Level:               input.Level,
		Message:             input.Message,
		PayloadJSON:         payloadJSON,
		LinkedSandboxID:     input.LinkedSandboxID,
		LinkedCellID:        input.LinkedCellID,
		LinkedAgentThreadID: input.LinkedAgentThreadID,
		CreatedAt:           time.Now().UTC(),
	}
	e.items = append(e.items, event)
	return event, nil
}

func (e *hostEventsFake) contains(eventType string) bool {
	for _, item := range e.items {
		if item.Type == eventType {
			return true
		}
	}
	return false
}

func (e *hostEventsFake) find(eventType string) (domain.SchedulerEvent, bool) {
	for _, item := range e.items {
		if item.Type == eventType {
			return item, true
		}
	}
	return domain.SchedulerEvent{}, false
}

func (e *hostEventsFake) types() []string {
	result := make([]string, 0, len(e.items))
	for _, item := range e.items {
		result = append(result, item.Type)
	}
	return result
}

type hostSessionsFake struct {
	session     *domain.Sandbox
	ensureErr   error
	loadErr     error
	shutdownErr error
	ensureCalls int
	loadCalls   int
	shutdowns   []string
}

func (s *hostSessionsFake) Ensure(context.Context, domain.Scheduler, domain.SchedulerAgentRequest, bool) (*domain.Sandbox, string, error) {
	s.ensureCalls++
	if s.ensureErr != nil {
		return nil, "", s.ensureErr
	}
	return s.session, "scheduler.sandbox.created", nil
}

func (s *hostSessionsFake) Load(context.Context, string) (*domain.Sandbox, error) {
	s.loadCalls++
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.session, nil
}

func (s *hostSessionsFake) Shutdown(_ context.Context, sessionID string) error {
	s.shutdowns = append(s.shutdowns, sessionID)
	if s.shutdownErr != nil {
		return s.shutdownErr
	}
	return nil
}

type hostAgentDefinitionsFake struct{}

func (hostAgentDefinitionsFake) ResolveSchedulerAgentDefinition(context.Context, domain.Scheduler) (*domain.AgentDefinition, error) {
	return nil, nil
}

type hostAgentExecutorFake struct {
	request       schedulers.HostAgentExecutionRequest
	cell          domain.NotebookCell
	assistantText string
	err           error
}

func (e *hostAgentExecutorFake) ExecuteAgent(_ context.Context, _ *domain.Sandbox, request schedulers.HostAgentExecutionRequest) (domain.NotebookCell, string, error) {
	e.request = request
	return e.cell, e.assistantText, e.err
}

type hostCommandExecutorFake struct {
	calls     int
	result    domain.SchedulerCommandResult
	err       error
	onExecute func()
}

func (e *hostCommandExecutorFake) ExecuteSchedulerCommand(context.Context, *domain.Sandbox, domain.SchedulerCommandRequest) (domain.SchedulerCommandResult, error) {
	e.calls++
	if e.onExecute != nil {
		e.onExecute()
	}
	return e.result, e.err
}

type hostProjectAgentRunnerFake struct {
	request  schedulers.HostProjectAgentRequest
	requests []schedulers.HostProjectAgentRequest
	run      domain.ProjectRunRecord
	execErr  error
	err      error
}

func (r *hostProjectAgentRunnerFake) RunProjectAgent(_ context.Context, request schedulers.HostProjectAgentRequest) (domain.ProjectRunRecord, error, error) {
	r.request = request
	r.requests = append(r.requests, request)
	return r.run, r.execErr, r.err
}

type hostLLMFake struct {
	prompt  string
	request schedulers.HostLLMGenerateRequest
	result  domain.SchedulerLLMResult
	err     error
}

func (l *hostLLMFake) Generate(_ context.Context, request schedulers.HostLLMGenerateRequest) (domain.SchedulerLLMResult, error) {
	l.prompt = request.Prompt
	l.request = request
	return l.result, l.err
}

type hostRPCFake struct {
	response string
	source   string
	creation schedulers.SandboxCreationContext
	err      error
}

func (r *hostRPCFake) CallJSONWithSource(ctx context.Context, _, _, source string) (string, error) {
	r.source = source
	r.creation = schedulers.SandboxCreationContextFromContext(ctx)
	return r.response, r.err
}

type hostPublisherFake struct {
	events []publishedEvent
}

type publishedEvent struct {
	topic   string
	payload map[string]any
}

func (p *hostPublisherFake) Publish(topic string, payload map[string]any) {
	p.events = append(p.events, publishedEvent{topic: topic, payload: payload})
}

func firstNonEmptyTest(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

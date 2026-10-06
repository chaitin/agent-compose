package schedulers_test

import (
	"context"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

var hostIdentityHeaders = []domain.TrustedHeader{{Name: "x-mpi-username", Value: "bob@example.com"}}

// An execution a user started must never land in the scheduler's sticky
// sandbox: that sandbox outlives the run and is shared with unattended runs.
func TestRuntimeHostProjectAgentUsesOwnSandboxForUserIdentity(t *testing.T) {
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{
		ID:            "scheduler-project",
		ProjectID:     "project-1",
		AgentName:     "reviewer",
		SandboxPolicy: domain.SchedulerSandboxPolicySticky,
	}}
	run := &domain.SchedulerRunSummary{ID: "run-project", SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-1"}
	for name, tc := range map[string]struct {
		ctx  context.Context
		want string
	}{
		"unattended keeps sticky": {ctx: context.Background(), want: domain.SchedulerSandboxPolicySticky},
		"user gets a new sandbox": {ctx: domain.NewContextWithTrustedHeaders(context.Background(), hostIdentityHeaders), want: domain.SchedulerSandboxPolicyNew},
	} {
		t.Run(name, func(t *testing.T) {
			projectRunner := &hostProjectAgentRunnerFake{run: domain.ProjectRunRecord{RunID: "project-run", Status: domain.ProjectRunStatusSucceeded}}
			host := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{ProjectAgentRunner: projectRunner}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{})

			if _, err := host.Agent(tc.ctx, "review", domain.SchedulerAgentRequest{}); err != nil {
				t.Fatalf("Agent returned error: %v", err)
			}
			if projectRunner.request.SandboxPolicy != tc.want {
				t.Fatalf("sandbox policy = %q, want %q", projectRunner.request.SandboxPolicy, tc.want)
			}
			if got, want := len(domain.TrustedHeadersFromContext(projectRunner.ctx)), len(domain.TrustedHeadersFromContext(tc.ctx)); got != want {
				t.Fatalf("project agent run saw %d trusted headers, want %d", got, want)
			}
		})
	}
}

// Command and non-project agent calls reach the sandbox through the scheduler
// sandbox runner. They follow the same rule and see the same identity as a
// project agent call in the same execution.
func TestRuntimeHostCommandAndAgentUseOwnSandboxForUserIdentity(t *testing.T) {
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler-host", DefaultAgent: "codex", SandboxPolicy: domain.SchedulerSandboxPolicySticky}}
	run := &domain.SchedulerRunSummary{ID: "run-host", SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-1"}
	userCtx := domain.NewContextWithTrustedHeaders(context.Background(), hostIdentityHeaders)
	calls := map[string]func(*schedulers.RuntimeHost, context.Context) error{
		"command": func(host *schedulers.RuntimeHost, ctx context.Context) error {
			_, err := host.Command(ctx, domain.SchedulerCommandRequest{Mode: "shell", Command: "true"})
			return err
		},
		"agent": func(host *schedulers.RuntimeHost, ctx context.Context) error {
			_, err := host.Agent(ctx, "work", domain.SchedulerAgentRequest{Agent: "codex"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				ctx  context.Context
				want string
			}{
				{ctx: context.Background(), want: ""},
				{ctx: userCtx, want: domain.SchedulerSandboxPolicyNew},
			} {
				sessions := &hostSessionsFake{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}}}
				host := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
					Events:           &hostEventsFake{},
					Sessions:         sessions,
					AgentDefinitions: hostAgentDefinitionsFake{},
					AgentExecutor:    &hostAgentExecutorFake{cell: domain.NotebookCell{Success: true}},
					CommandExecutor:  &hostCommandExecutorFake{},
				}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{})

				if err := call(host, tc.ctx); err != nil {
					t.Fatalf("call returned error: %v", err)
				}
				if sessions.ensureReq.SandboxPolicy != tc.want {
					t.Fatalf("sandbox policy = %q, want %q", sessions.ensureReq.SandboxPolicy, tc.want)
				}
				if got, want := len(domain.TrustedHeadersFromContext(sessions.ensureCtx)), len(domain.TrustedHeadersFromContext(tc.ctx)); got != want {
					t.Fatalf("sandbox runner saw %d trusted headers, want %d", got, want)
				}
			}
		})
	}
}

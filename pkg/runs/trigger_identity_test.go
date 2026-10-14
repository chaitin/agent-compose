package runs

import (
	"context"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// A manual run of a sticky trigger joins the trigger's sticky sandbox only when
// it carries no user identity. A run started by a user gets a sandbox of its
// own, so the sticky sandbox its unattended runs share never holds that user's
// capability binding or data.
func TestRunsControllerManualTriggerWithUserIdentitySkipsStickySandbox(t *testing.T) {
	for name, tc := range map[string]struct {
		headers    []domain.TrustedHeader
		wantSticky bool
	}{
		"no identity joins the sticky sandbox": {wantSticky: true},
		"user identity gets its own sandbox":   {headers: []domain.TrustedHeader{{Name: "x-mpi-username", Value: "bob@example.com"}}},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newControllerRunFixture(t)
			trigger := domain.SchedulerTrigger{
				SchedulerID: "scheduler-1",
				ID:          "trigger-1",
				Kind:        domain.SchedulerTriggerKindInterval,
				IntervalMs:  1000,
				Enabled:     true,
				SpecJSON:    `{"kind":"interval","intervalMs":1000}`,
			}
			fixture.configDB.schedulers = []domain.ProjectSchedulerRecord{{
				ProjectID: "project-1", SchedulerID: "scheduler-1", AgentName: "worker", ID: "scheduler-1", Enabled: true, TriggerCount: 1,
			}}
			fixture.configDB.schedulerDefinitions = map[string]domain.Scheduler{
				"scheduler-1": {
					Summary: domain.SchedulerSummary{
						ID:                 "scheduler-1",
						Enabled:            true,
						Runtime:            domain.SchedulerRuntimeScheduler,
						SandboxPolicy:      domain.SchedulerSandboxPolicySticky,
						ProjectID:          "project-1",
						AgentName:          "worker",
						ProjectSchedulerID: "scheduler-1",
					},
					Script:   `scheduler.interval("trigger-1", async function() { return scheduler.agent("resolved prompt"); }, 1000);`,
					Triggers: []domain.SchedulerTrigger{trigger},
				},
			}
			ctx := context.Context(fixture.ctx)
			if len(tc.headers) > 0 {
				ctx = domain.NewContextWithTrustedHeaders(ctx, tc.headers)
			}

			resolved, err := fixture.controller.resolveTriggerForManualRun(ctx, RunAgentRequest{
				ProjectID: "project-1",
				AgentName: "worker",
				Source:    domain.ProjectRunSourceManual,
				TriggerID: "trigger-1",
			})
			if err != nil {
				t.Fatalf("resolveTriggerForManualRun returned error: %v", err)
			}
			if got := resolved.Request.StickyBindingSchedulerID != ""; got != tc.wantSticky {
				t.Fatalf("sticky binding requested = %v, want %v (request %#v)", got, tc.wantSticky, resolved.Request)
			}
			if !tc.wantSticky && (resolved.Request.StickyBindingTriggerID != "" || resolved.Request.StickyBindingConfigHash != "") {
				t.Fatalf("sticky fields set for a user-identified run: %#v", resolved.Request)
			}
		})
	}
}

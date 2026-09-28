package adapters

import (
	"context"
	"testing"
	"time"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/events"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

func TestSandboxLifecyclePublisherProject(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSandboxRPCBridge(t)
	owner := createNativeTestScheduler(t, ctx, bridge.configDB, domain.Scheduler{Summary: domain.SchedulerSummary{
		ID: "scheduler-owner", Name: "Owner", Driver: driverpkg.RuntimeDriverDocker,
	}})
	forgedTag := []domain.SandboxTag{{Name: "project", Value: "project-b"}, {Name: "agent", Value: "worker"}}
	sandbox := func(triggerSource string, tags []domain.SandboxTag) *domain.Sandbox {
		return &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox", TriggerSource: triggerSource, Tags: tags}}
	}
	tests := []struct {
		name    string
		ctx     context.Context
		sandbox *domain.Sandbox
		want    string
	}{
		{
			name:    "an acting scheduler run speaks for its own project",
			ctx:     events.WithPublisherProject(ctx, "project-a"),
			sandbox: sandbox(domain.SandboxTypeScript+":"+owner.Summary.ID, forgedTag),
			want:    "project-a",
		},
		{
			name:    "an operator action goes to the script sandbox's scheduler project, not its tags",
			ctx:     ctx,
			sandbox: sandbox(domain.SandboxTypeScript+":"+owner.Summary.ID, forgedTag),
			want:    owner.Summary.ProjectID,
		},
		{
			name:    "a script sandbox whose scheduler is gone belongs to no project",
			ctx:     ctx,
			sandbox: sandbox(domain.SandboxTypeScript+":scheduler-removed", forgedTag),
			want:    "",
		},
		{
			name:    "an operator action goes to the project a project-run sandbox is tagged with",
			ctx:     ctx,
			sandbox: sandbox(domain.SandboxTypeManual, []domain.SandboxTag{{Name: "project", Value: "project-run-owner"}}),
			want:    "project-run-owner",
		},
		{
			name:    "an untagged manual sandbox belongs to no project",
			ctx:     ctx,
			sandbox: sandbox(domain.SandboxTypeManual, nil),
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bridge.sandboxLifecyclePublisherProject(tt.ctx, tt.sandbox); got != tt.want {
				t.Fatalf("publisher project = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSandboxRPCBridgeOperatorStopAndResumeReachOwningProject covers the
// Sandbox API path: stopping or resuming a scheduler's sandbox by hand carries
// no Project in its context, yet the lifecycle topics must still reach the
// owning Project's triggers under the default project scope.
func TestSandboxRPCBridgeOperatorStopAndResumeReachOwningProject(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSandboxRPCBridge(t)
	bus := schedulers.NewBusWithBuffer(8)
	bridge.bus = bus
	owner := createNativeTestScheduler(t, ctx, bridge.configDB, domain.Scheduler{Summary: domain.SchedulerSummary{
		ID: "scheduler-operator", Name: "Operator", Driver: driverpkg.RuntimeDriverDocker,
	}})
	created, err := bridge.store.CreateSandbox(ctx, "scheduler sandbox", "", driverpkg.RuntimeDriverDocker, "guest:latest", "",
		domain.SandboxTypeScript+":"+owner.Summary.ID, nil, nil, []domain.SandboxTag{{Name: "project", Value: "project-b"}})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	created.Summary.VMStatus = domain.VMStatusRunning
	if err := bridge.store.UpdateSandbox(ctx, created); err != nil {
		t.Fatalf("UpdateSandbox: %v", err)
	}

	if _, err := bridge.StopSandbox(ctx, created.Summary.ID); err != nil {
		t.Fatalf("StopSandbox: %v", err)
	}
	assertLifecycleTopic(t, bus, "agent-compose.session.stopped", owner.Summary.ProjectID)
	if _, err := bridge.ResumeSandbox(ctx, created.Summary.ID); err != nil {
		t.Fatalf("ResumeSandbox: %v", err)
	}
	assertLifecycleTopic(t, bus, "agent-compose.session.resumed", owner.Summary.ProjectID)
}

func assertLifecycleTopic(t *testing.T, bus *schedulers.Bus, topic, projectID string) {
	t.Helper()
	select {
	case event := <-bus.Events():
		if event.Topic != topic || event.PublisherProjectID != projectID {
			t.Fatalf("published %s from %q, want %s from %q", event.Topic, event.PublisherProjectID, topic, projectID)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s published", topic)
	}
}

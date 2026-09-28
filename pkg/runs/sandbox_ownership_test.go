package runs

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestValidateProjectRunSandboxOwnership(t *testing.T) {
	t.Parallel()
	requested := domain.ProjectRunRecord{ProjectID: "project-1", AgentName: "worker", AgentID: "agent-worker"}
	tests := []struct {
		name      string
		tags      []domain.SandboxTag
		wantError string
	}{
		{
			name: "matching owner",
			tags: []domain.SandboxTag{
				{Name: "project", Value: "project-1"},
				{Name: "agent", Value: "worker"},
				{Name: domain.AgentSandboxTagID, Value: "agent-worker"},
				{Name: domain.AgentSandboxTagID, Value: "agent-worker"},
			},
		},
		{
			name: "matching legacy project tag",
			tags: []domain.SandboxTag{
				{Name: "project_id", Value: "project-1"},
				{Name: "agent", Value: "worker"},
			},
		},
		{name: "unowned manual sandbox"},
		{
			name: "different agent",
			tags: []domain.SandboxTag{
				{Name: "project", Value: "project-1"},
				{Name: "agent", Value: "reviewer"},
				{Name: domain.AgentSandboxTagID, Value: "agent-reviewer"},
			},
			wantError: `belongs to project "project-1" agent "reviewer"`,
		},
		{
			name: "different project",
			tags: []domain.SandboxTag{
				{Name: "project", Value: "project-2"},
				{Name: "agent", Value: "worker"},
			},
			wantError: `belongs to project "project-2" agent "worker"`,
		},
		{
			name: "different stable agent id",
			tags: []domain.SandboxTag{
				{Name: "project", Value: "project-1"},
				{Name: "agent", Value: "worker"},
				{Name: domain.AgentSandboxTagID, Value: "stale-agent-worker"},
			},
			wantError: `agent ID "stale-agent-worker"`,
		},
		{
			name: "conflicting agent owners",
			tags: []domain.SandboxTag{
				{Name: "project", Value: "project-1"},
				{Name: "agent", Value: "reviewer"},
				{Name: "agent", Value: "worker"},
			},
			wantError: "conflicting ownership metadata",
		},
		{
			name: "conflicting project aliases",
			tags: []domain.SandboxTag{
				{Name: "project", Value: "project-1"},
				{Name: "project_id", Value: "project-2"},
				{Name: "agent", Value: "worker"},
			},
			wantError: "conflicting ownership metadata",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", Tags: tt.tags}}
			err := validateProjectRunSandboxOwnership(sandbox, requested)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("validateProjectRunSandboxOwnership returned error: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v, want ErrInvalidRequest containing %q", err, tt.wantError)
			}
		})
	}
}

func TestRunsControllerRejectsCrossAgentSandboxReuseWithoutMutatingOwnerSandbox(t *testing.T) {
	fixture := newControllerRunFixture(t)
	ownerRun := domain.ProjectRunRecord{
		RunID: "owner-run", ProjectID: "project-1", AgentName: "reviewer", AgentID: "agent-reviewer",
	}
	originalWorkspace := &domain.SandboxWorkspace{
		ID: "reviewer-workspace", Name: "Reviewer workspace", Type: "file", ConfigJSON: `{"path":"/reviewer"}`,
	}
	originalEnv := []domain.SandboxEnvVar{{Name: "OWNER_SECRET", Value: "reviewer-secret", Secret: true}}
	sandbox, err := fixture.store.CreateSandbox(
		fixture.ctx,
		"reviewer sandbox",
		"",
		driverpkg.RuntimeDriverDocker,
		"guest:latest",
		"",
		domain.SandboxTypeManual,
		originalWorkspace,
		originalEnv,
		SandboxTags(ownerRun),
	)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	sandbox.Summary.VMStatus = domain.VMStatusRunning
	if err := fixture.store.UpdateSandbox(fixture.ctx, sandbox); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}
	originalTags := append([]domain.SandboxTag(nil), sandbox.Summary.Tags...)

	run, execErr, err := fixture.controller.RunProjectAgent(fixture.ctx, RunAgentRequest{
		ProjectID:       "project-1",
		AgentName:       "worker",
		Command:         "printf should-not-run",
		SandboxID:       sandbox.Summary.ID,
		Source:          domain.ProjectRunSourceAPI,
		ClientRequestID: "cross-agent-sandbox-reuse",
	}, nil)
	if err != nil {
		t.Fatalf("RunProjectAgent returned controller error: %v", err)
	}
	if !errors.Is(execErr, ErrInvalidRequest) || !strings.Contains(execErr.Error(), `agent "reviewer"`) || !strings.Contains(execErr.Error(), `agent "worker"`) {
		t.Fatalf("execution error = %v, want cross-agent ErrInvalidRequest", execErr)
	}
	if run.Status != domain.ProjectRunStatusFailed || run.SandboxID != "" {
		t.Fatalf("rejected run = %#v, want failed without sandbox association", run)
	}
	if fixture.driver.started || fixture.driver.stopped || fixture.driver.removed {
		t.Fatalf("sandbox driver was invoked: %#v", fixture.driver)
	}
	if fixture.executor.prepareCalls != 0 || fixture.executor.prepareFromTagsCalls != 0 {
		t.Fatalf("agent executor was invoked: %#v", fixture.executor)
	}

	stored, err := fixture.store.GetSandbox(fixture.ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	if stored.Summary.VMStatus != domain.VMStatusRunning {
		t.Fatalf("owner sandbox status = %q, want %q", stored.Summary.VMStatus, domain.VMStatusRunning)
	}
	if !reflect.DeepEqual(stored.Summary.Tags, originalTags) {
		t.Fatalf("owner sandbox tags changed: got %#v want %#v", stored.Summary.Tags, originalTags)
	}
	if !reflect.DeepEqual(stored.EnvItems, originalEnv) {
		t.Fatalf("owner sandbox env changed: got %#v want %#v", stored.EnvItems, originalEnv)
	}
	if !reflect.DeepEqual(stored.Workspace, originalWorkspace) {
		t.Fatalf("owner sandbox workspace changed: got %#v want %#v", stored.Workspace, originalWorkspace)
	}
}

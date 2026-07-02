package agentcompose

import (
	"context"
	"fmt"
	"time"

	rundomain "agent-compose/internal/agentcompose/run"
	"agent-compose/pkg/agentcompose/domain"
)

type ProjectRunStartRequest = rundomain.StartRequest
type ProjectRunTransitionRequest = rundomain.TransitionRequest

type RunCoordinator struct {
	store *ConfigStore
	now   func() time.Time
}

func NewRunCoordinator(store *ConfigStore) *RunCoordinator {
	return &RunCoordinator{
		store: store,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (c *RunCoordinator) BeginRun(ctx context.Context, req ProjectRunStartRequest) (ProjectRunRecord, error) {
	return c.coordinator().BeginRun(ctx, req)
}

func (c *RunCoordinator) MarkRunning(ctx context.Context, runID, sessionID string) (ProjectRunRecord, error) {
	return c.coordinator().MarkRunning(ctx, runID, sessionID)
}

func (c *RunCoordinator) MarkSucceeded(ctx context.Context, req ProjectRunTransitionRequest) (ProjectRunRecord, error) {
	return c.coordinator().MarkSucceeded(ctx, req)
}

func (c *RunCoordinator) MarkFailed(ctx context.Context, req ProjectRunTransitionRequest) (ProjectRunRecord, error) {
	return c.coordinator().MarkFailed(ctx, req)
}

func (c *RunCoordinator) MarkCanceled(ctx context.Context, req ProjectRunTransitionRequest) (ProjectRunRecord, error) {
	return c.coordinator().MarkCanceled(ctx, req)
}

func (c *RunCoordinator) TransitionRun(ctx context.Context, req ProjectRunTransitionRequest) (ProjectRunRecord, error) {
	return c.coordinator().TransitionRun(ctx, req)
}

func (c *RunCoordinator) coordinator() *rundomain.Coordinator {
	var store *ConfigStore
	var now func() time.Time
	if c != nil {
		store = c.store
		now = c.now
	}
	coordinator := rundomain.NewCoordinator(projectRunStoreAdapter{store: store})
	if now != nil {
		coordinator.SetNow(now)
	}
	return coordinator
}

type projectRunStoreAdapter struct {
	store *ConfigStore
}

func (a projectRunStoreAdapter) GetProject(ctx context.Context, projectID string) (ProjectRecord, error) {
	if a.store == nil {
		return ProjectRecord{}, nilConfigStoreError()
	}
	return a.store.GetProject(ctx, projectID)
}

func (a projectRunStoreAdapter) GetProjectAgent(ctx context.Context, projectID, agentName string) (ProjectAgentRecord, error) {
	if a.store == nil {
		return ProjectAgentRecord{}, nilConfigStoreError()
	}
	return a.store.GetProjectAgent(ctx, projectID, agentName)
}

func (a projectRunStoreAdapter) GetManagedAgentDefinition(ctx context.Context, agentID string) (rundomain.ManagedAgentDefinition, error) {
	if a.store == nil {
		return rundomain.ManagedAgentDefinition{}, nilConfigStoreError()
	}
	agent, err := a.store.GetAgentDefinition(ctx, agentID)
	if err != nil {
		return rundomain.ManagedAgentDefinition{}, err
	}
	return rundomain.ManagedAgentDefinition{
		ID:               agent.ID,
		Enabled:          agent.Enabled,
		DeletedAt:        agent.DeletedAt,
		Driver:           agent.Driver,
		GuestImage:       agent.GuestImage,
		ManagedProjectID: agent.ManagedProjectID,
		ManagedAgentName: agent.ManagedAgentName,
	}, nil
}

func (a projectRunStoreAdapter) CreateProjectRun(ctx context.Context, run ProjectRunRecord) (ProjectRunRecord, error) {
	if a.store == nil {
		return ProjectRunRecord{}, nilConfigStoreError()
	}
	return a.store.CreateProjectRun(ctx, run)
}

func (a projectRunStoreAdapter) GetProjectRun(ctx context.Context, runID string) (ProjectRunRecord, error) {
	if a.store == nil {
		return ProjectRunRecord{}, nilConfigStoreError()
	}
	return a.store.GetProjectRun(ctx, runID)
}

func (a projectRunStoreAdapter) UpdateProjectRun(ctx context.Context, run ProjectRunRecord) (ProjectRunRecord, error) {
	if a.store == nil {
		return ProjectRunRecord{}, nilConfigStoreError()
	}
	return a.store.UpdateProjectRun(ctx, run)
}

func nilConfigStoreError() error {
	return fmt.Errorf("config store is required")
}

func applyProjectRunTransitionFields(run *ProjectRunRecord, req ProjectRunTransitionRequest) {
	rundomain.ApplyTransitionFields(run, req)
}

func validateProjectRunTransition(from, to string) error {
	return domain.ValidateProjectRunTransition(from, to)
}

func projectRunStatusIsTerminal(status string) bool {
	return domain.ProjectRunStatusIsTerminal(status)
}

func normalizeProjectRunSource(source string) string {
	return domain.NormalizeProjectRunSource(source)
}

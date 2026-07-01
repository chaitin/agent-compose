package agentcompose

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"agent-compose/pkg/model"

	"github.com/google/uuid"
)

type ProjectRunStartRequest = model.ProjectRunStartRequest
type ProjectRunTransitionRequest = model.ProjectRunTransitionRequest

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
	if c == nil || c.store == nil {
		return ProjectRunRecord{}, fmt.Errorf("config store is required")
	}
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.AgentName = strings.TrimSpace(req.AgentName)
	req.Source = normalizeProjectRunSource(req.Source)
	req.SchedulerID = strings.TrimSpace(req.SchedulerID)
	req.TriggerID = strings.TrimSpace(req.TriggerID)
	req.ClientRequestID = strings.TrimSpace(req.ClientRequestID)
	if req.ProjectID == "" || req.AgentName == "" {
		return ProjectRunRecord{}, fmt.Errorf("project id and agent name are required")
	}
	if req.ClientRequestID == "" {
		req.ClientRequestID = uuid.NewString()
	}
	project, err := c.store.GetProject(ctx, req.ProjectID)
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("resolve project %s: %w", req.ProjectID, err)
	}
	projectAgent, err := c.store.GetProjectAgent(ctx, project.ID, req.AgentName)
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("resolve project agent %s/%s: %w", project.ID, req.AgentName, err)
	}
	agent, err := c.store.GetAgentDefinition(ctx, projectAgent.ManagedAgentID)
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("resolve managed agent definition %s: %w", projectAgent.ManagedAgentID, err)
	}
	if !agent.Enabled || !agent.DeletedAt.IsZero() {
		return ProjectRunRecord{}, fmt.Errorf("managed agent definition %s is disabled", agent.ID)
	}
	if agent.ManagedProjectID != project.ID || agent.ManagedAgentName != projectAgent.AgentName {
		return ProjectRunRecord{}, fmt.Errorf("managed agent definition %s does not belong to project agent %s/%s", agent.ID, project.ID, projectAgent.AgentName)
	}
	runID, err := StableProjectRunID(project.ID, projectAgent.AgentName, req.Source, req.ClientRequestID)
	if err != nil {
		return ProjectRunRecord{}, err
	}
	run := ProjectRunRecord{
		RunID:           runID,
		ProjectID:       project.ID,
		ProjectName:     project.Name,
		ProjectRevision: project.CurrentRevision,
		AgentName:       projectAgent.AgentName,
		ManagedAgentID:  agent.ID,
		Source:          req.Source,
		SchedulerID:     req.SchedulerID,
		TriggerID:       req.TriggerID,
		Status:          ProjectRunStatusPending,
		Prompt:          req.Prompt,
		Driver:          firstNonEmpty(agent.Driver, projectAgent.Driver),
		ImageRef:        firstNonEmpty(agent.GuestImage, projectAgent.Image),
		ResultJSON:      "{}",
	}
	created, err := c.store.CreateProjectRun(ctx, run)
	if err == nil {
		return created, nil
	}
	if existing, loadErr := c.store.GetProjectRun(ctx, runID); loadErr == nil {
		return existing, nil
	}
	return ProjectRunRecord{}, err
}

func (c *RunCoordinator) MarkRunning(ctx context.Context, runID, sessionID string) (ProjectRunRecord, error) {
	return c.TransitionRun(ctx, ProjectRunTransitionRequest{
		RunID:     runID,
		Status:    ProjectRunStatusRunning,
		SessionID: sessionID,
	})
}

func (c *RunCoordinator) MarkSucceeded(ctx context.Context, req ProjectRunTransitionRequest) (ProjectRunRecord, error) {
	req.Status = ProjectRunStatusSucceeded
	return c.TransitionRun(ctx, req)
}

func (c *RunCoordinator) MarkFailed(ctx context.Context, req ProjectRunTransitionRequest) (ProjectRunRecord, error) {
	req.Status = ProjectRunStatusFailed
	return c.TransitionRun(ctx, req)
}

func (c *RunCoordinator) MarkCanceled(ctx context.Context, req ProjectRunTransitionRequest) (ProjectRunRecord, error) {
	req.Status = ProjectRunStatusCanceled
	return c.TransitionRun(ctx, req)
}

func (c *RunCoordinator) TransitionRun(ctx context.Context, req ProjectRunTransitionRequest) (ProjectRunRecord, error) {
	if c == nil || c.store == nil {
		return ProjectRunRecord{}, fmt.Errorf("config store is required")
	}
	req.RunID = strings.TrimSpace(req.RunID)
	req.Status = normalizeProjectRunStatus(req.Status)
	if req.RunID == "" {
		return ProjectRunRecord{}, fmt.Errorf("run id is required")
	}
	current, err := c.store.GetProjectRun(ctx, req.RunID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectRunRecord{}, err
		}
		return ProjectRunRecord{}, err
	}
	if err := validateProjectRunTransition(current.Status, req.Status); err != nil {
		return ProjectRunRecord{}, err
	}
	now := c.nowUTC()
	next := current
	next.Status = req.Status
	applyProjectRunTransitionFields(&next, req)
	switch req.Status {
	case ProjectRunStatusRunning:
		if next.StartedAt.IsZero() {
			next.StartedAt = now
		}
	case ProjectRunStatusSucceeded, ProjectRunStatusFailed, ProjectRunStatusCanceled:
		if next.StartedAt.IsZero() {
			next.StartedAt = now
		}
		if next.CompletedAt.IsZero() {
			next.CompletedAt = now
		}
		next.DurationMs = max(0, next.CompletedAt.Sub(next.StartedAt).Milliseconds())
	}
	return c.store.UpdateProjectRun(ctx, next)
}

func (c *RunCoordinator) nowUTC() time.Time {
	if c != nil && c.now != nil {
		return c.now().UTC()
	}
	return time.Now().UTC()
}

func applyProjectRunTransitionFields(run *ProjectRunRecord, req ProjectRunTransitionRequest) {
	if value := strings.TrimSpace(req.SessionID); value != "" {
		run.SessionID = value
	}
	if req.ExitCode != 0 {
		run.ExitCode = req.ExitCode
	}
	if value := strings.TrimSpace(req.Error); value != "" {
		run.Error = value
	}
	if req.Output != "" {
		run.Output = req.Output
	}
	if value := strings.TrimSpace(req.ResultJSON); value != "" {
		run.ResultJSON = value
	}
	if value := strings.TrimSpace(req.LogsPath); value != "" {
		run.LogsPath = value
	}
	if value := strings.TrimSpace(req.ArtifactsDir); value != "" {
		run.ArtifactsDir = value
	}
	if value := strings.TrimSpace(req.CleanupError); value != "" {
		run.CleanupError = value
	}
}

func validateProjectRunTransition(from, to string) error {
	from = normalizeProjectRunStatus(from)
	to = normalizeProjectRunStatus(to)
	if from == to {
		return nil
	}
	if projectRunStatusIsTerminal(from) {
		return fmt.Errorf("project run transition %s -> %s is not allowed: run is already terminal", from, to)
	}
	switch from {
	case ProjectRunStatusPending:
		switch to {
		case ProjectRunStatusRunning, ProjectRunStatusFailed, ProjectRunStatusCanceled:
			return nil
		}
	case ProjectRunStatusRunning:
		switch to {
		case ProjectRunStatusSucceeded, ProjectRunStatusFailed, ProjectRunStatusCanceled:
			return nil
		}
	}
	return fmt.Errorf("project run transition %s -> %s is not allowed", from, to)
}

func projectRunStatusIsTerminal(status string) bool {
	switch normalizeProjectRunStatus(status) {
	case ProjectRunStatusSucceeded, ProjectRunStatusFailed, ProjectRunStatusCanceled:
		return true
	default:
		return false
	}
}

func normalizeProjectRunSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case ProjectRunSourceScheduler:
		return ProjectRunSourceScheduler
	case ProjectRunSourceAPI:
		return ProjectRunSourceAPI
	case ProjectRunSourceManual:
		return ProjectRunSourceManual
	default:
		return ProjectRunSourceManual
	}
}

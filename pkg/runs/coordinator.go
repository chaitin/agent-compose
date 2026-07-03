package runs

import (
	domain "agent-compose/pkg/model"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type StartRequest struct {
	ProjectID       string
	AgentName       string
	Source          string
	SchedulerID     string
	TriggerID       string
	Prompt          string
	ClientRequestID string
}

type TransitionRequest struct {
	RunID        string
	Status       string
	SessionID    string
	ExitCode     int
	Error        string
	Output       string
	ResultJSON   string
	LogsPath     string
	ArtifactsDir string
	CleanupError string
}

type ManagedAgentDefinition struct {
	ID               string
	Enabled          bool
	DeletedAt        time.Time
	Driver           string
	GuestImage       string
	ManagedProjectID string
	ManagedAgentName string
}

type Store interface {
	GetProject(context.Context, string) (domain.ProjectRecord, error)
	GetProjectAgent(context.Context, string, string) (domain.ProjectAgentRecord, error)
	GetManagedAgentDefinition(context.Context, string) (ManagedAgentDefinition, error)
	CreateProjectRun(context.Context, domain.ProjectRunRecord) (domain.ProjectRunRecord, error)
	GetProjectRun(context.Context, string) (domain.ProjectRunRecord, error)
	UpdateProjectRun(context.Context, domain.ProjectRunRecord) (domain.ProjectRunRecord, error)
}

type StableRunIDFunc func(projectID, agentName, source, idempotencyKey string) (string, error)

type Coordinator struct {
	store       Store
	stableRunID StableRunIDFunc
	now         func() time.Time
}

func NewCoordinator(store Store, stableRunID StableRunIDFunc) *Coordinator {
	return &Coordinator{
		store:       store,
		stableRunID: stableRunID,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (c *Coordinator) SetNow(now func() time.Time) {
	if c == nil {
		return
	}
	c.now = now
}

func (c *Coordinator) BeginRun(ctx context.Context, req StartRequest) (domain.ProjectRunRecord, error) {
	if c == nil || c.store == nil {
		return domain.ProjectRunRecord{}, fmt.Errorf("config store is required")
	}
	if c.stableRunID == nil {
		return domain.ProjectRunRecord{}, fmt.Errorf("stable run id function is required")
	}
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.AgentName = strings.TrimSpace(req.AgentName)
	req.Source = NormalizeSource(req.Source)
	req.SchedulerID = strings.TrimSpace(req.SchedulerID)
	req.TriggerID = strings.TrimSpace(req.TriggerID)
	req.ClientRequestID = strings.TrimSpace(req.ClientRequestID)
	if req.ProjectID == "" || req.AgentName == "" {
		return domain.ProjectRunRecord{}, fmt.Errorf("project id and agent name are required")
	}
	if req.ClientRequestID == "" {
		req.ClientRequestID = uuid.NewString()
	}
	project, err := c.store.GetProject(ctx, req.ProjectID)
	if err != nil {
		return domain.ProjectRunRecord{}, fmt.Errorf("resolve project %s: %w", req.ProjectID, err)
	}
	projectAgent, err := c.store.GetProjectAgent(ctx, project.ID, req.AgentName)
	if err != nil {
		return domain.ProjectRunRecord{}, fmt.Errorf("resolve project agent %s/%s: %w", project.ID, req.AgentName, err)
	}
	agent, err := c.store.GetManagedAgentDefinition(ctx, projectAgent.ManagedAgentID)
	if err != nil {
		return domain.ProjectRunRecord{}, fmt.Errorf("resolve managed agent definition %s: %w", projectAgent.ManagedAgentID, err)
	}
	if !agent.Enabled || !agent.DeletedAt.IsZero() {
		return domain.ProjectRunRecord{}, fmt.Errorf("managed agent definition %s is disabled", agent.ID)
	}
	if agent.ManagedProjectID != project.ID || agent.ManagedAgentName != projectAgent.AgentName {
		return domain.ProjectRunRecord{}, fmt.Errorf("managed agent definition %s does not belong to project agent %s/%s", agent.ID, project.ID, projectAgent.AgentName)
	}
	runID, err := c.stableRunID(project.ID, projectAgent.AgentName, req.Source, req.ClientRequestID)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	run := domain.ProjectRunRecord{
		RunID:           runID,
		ProjectID:       project.ID,
		ProjectName:     project.Name,
		ProjectRevision: project.CurrentRevision,
		AgentName:       projectAgent.AgentName,
		ManagedAgentID:  agent.ID,
		Source:          req.Source,
		SchedulerID:     req.SchedulerID,
		TriggerID:       req.TriggerID,
		Status:          domain.ProjectRunStatusPending,
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
	return domain.ProjectRunRecord{}, err
}

func (c *Coordinator) MarkRunning(ctx context.Context, runID, sessionID string) (domain.ProjectRunRecord, error) {
	return c.TransitionRun(ctx, TransitionRequest{
		RunID:     runID,
		Status:    domain.ProjectRunStatusRunning,
		SessionID: sessionID,
	})
}

func (c *Coordinator) MarkSucceeded(ctx context.Context, req TransitionRequest) (domain.ProjectRunRecord, error) {
	req.Status = domain.ProjectRunStatusSucceeded
	return c.TransitionRun(ctx, req)
}

func (c *Coordinator) MarkFailed(ctx context.Context, req TransitionRequest) (domain.ProjectRunRecord, error) {
	req.Status = domain.ProjectRunStatusFailed
	return c.TransitionRun(ctx, req)
}

func (c *Coordinator) MarkCanceled(ctx context.Context, req TransitionRequest) (domain.ProjectRunRecord, error) {
	req.Status = domain.ProjectRunStatusCanceled
	return c.TransitionRun(ctx, req)
}

func (c *Coordinator) TransitionRun(ctx context.Context, req TransitionRequest) (domain.ProjectRunRecord, error) {
	if c == nil || c.store == nil {
		return domain.ProjectRunRecord{}, fmt.Errorf("config store is required")
	}
	req.RunID = strings.TrimSpace(req.RunID)
	req.Status = NormalizeStatus(req.Status)
	if req.RunID == "" {
		return domain.ProjectRunRecord{}, fmt.Errorf("run id is required")
	}
	current, err := c.store.GetProjectRun(ctx, req.RunID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ProjectRunRecord{}, err
		}
		return domain.ProjectRunRecord{}, err
	}
	if err := validateProjectRunTransition(current.Status, req.Status); err != nil {
		return domain.ProjectRunRecord{}, err
	}
	now := c.nowUTC()
	next := current
	next.Status = req.Status
	applyProjectRunTransitionFields(&next, req)
	switch req.Status {
	case domain.ProjectRunStatusRunning:
		if next.StartedAt.IsZero() {
			next.StartedAt = now
		}
	case domain.ProjectRunStatusSucceeded, domain.ProjectRunStatusFailed, domain.ProjectRunStatusCanceled:
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

func (c *Coordinator) nowUTC() time.Time {
	if c != nil && c.now != nil {
		return c.now().UTC()
	}
	return time.Now().UTC()
}

func applyProjectRunTransitionFields(run *domain.ProjectRunRecord, req TransitionRequest) {
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
	from = NormalizeStatus(from)
	to = NormalizeStatus(to)
	if from == to {
		return nil
	}
	if StatusIsTerminal(from) {
		return fmt.Errorf("project run transition %s -> %s is not allowed: run is already terminal", from, to)
	}
	switch from {
	case domain.ProjectRunStatusPending:
		switch to {
		case domain.ProjectRunStatusRunning, domain.ProjectRunStatusFailed, domain.ProjectRunStatusCanceled:
			return nil
		}
	case domain.ProjectRunStatusRunning:
		switch to {
		case domain.ProjectRunStatusSucceeded, domain.ProjectRunStatusFailed, domain.ProjectRunStatusCanceled:
			return nil
		}
	}
	return fmt.Errorf("project run transition %s -> %s is not allowed", from, to)
}

func StatusIsTerminal(status string) bool {
	switch NormalizeStatus(status) {
	case domain.ProjectRunStatusSucceeded, domain.ProjectRunStatusFailed, domain.ProjectRunStatusCanceled:
		return true
	default:
		return false
	}
}

func NormalizeSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case domain.ProjectRunSourceScheduler:
		return domain.ProjectRunSourceScheduler
	case domain.ProjectRunSourceAPI:
		return domain.ProjectRunSourceAPI
	case domain.ProjectRunSourceManual:
		return domain.ProjectRunSourceManual
	default:
		return domain.ProjectRunSourceManual
	}
}

func NormalizeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case domain.ProjectRunStatusRunning:
		return domain.ProjectRunStatusRunning
	case domain.ProjectRunStatusSucceeded:
		return domain.ProjectRunStatusSucceeded
	case domain.ProjectRunStatusFailed:
		return domain.ProjectRunStatusFailed
	case domain.ProjectRunStatusCanceled:
		return domain.ProjectRunStatusCanceled
	default:
		return domain.ProjectRunStatusPending
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

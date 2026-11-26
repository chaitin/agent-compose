package runs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/chaitin/agent-compose/internal/projects"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
	"strings"
	"time"

	"github.com/google/uuid"
)

type StartRequest struct {
	ProjectID       string
	AgentName       string
	Source          string
	SchedulerID     string
	SchedulerRunID  string
	TriggerID       string
	Prompt          string
	Driver          string
	CleanupPolicy   string
	ClientRequestID string
	Labels          map[string]string
	// RunLogRoot is the daemon data root. When set, the run records its
	// history in a run log under it rather than a legacy text transcript.
	RunLogRoot string
}

type TransitionRequest struct {
	LogIncomplete  bool
	RunID          string
	Status         string
	SandboxID      string
	ExitCode       int
	Error          string
	ErrorStack     string
	Output         string
	ResultJSON     string
	LogsPath       string
	ArtifactsDir   string
	AgentCellID    string
	CleanupError   string
	TerminalEvents []domain.ProjectRunEventRecord
}

type Store interface {
	GetProject(context.Context, string) (domain.ProjectRecord, error)
	GetProjectRevision(context.Context, string, int64) (domain.ProjectRevisionRecord, error)
	GetProjectAgent(context.Context, string, string) (domain.ProjectAgentRecord, error)
	CreateProjectRun(context.Context, domain.ProjectRunRecord) (domain.ProjectRunRecord, error)
	CreateProjectRunWithEvents(context.Context, domain.ProjectRunRecord, []domain.ProjectRunEventRecord) (domain.ProjectRunRecord, error)
	GetProjectRun(context.Context, string) (domain.ProjectRunRecord, error)
	UpdateProjectRun(context.Context, domain.ProjectRunRecord) (domain.ProjectRunRecord, error)
	UpdateProjectRunWithEvents(context.Context, domain.ProjectRunRecord, []domain.ProjectRunEventRecord) (domain.ProjectRunRecord, error)
}

type structuredEventStore interface {
	AppendProjectRunEvent(context.Context, domain.ProjectRunEventRecord) (domain.ProjectRunEventRecord, bool, error)
	AppendProjectRunEvents(context.Context, []domain.ProjectRunEventRecord) ([]domain.ProjectRunEventRecord, []bool, error)
}

type StableRunIDFunc func(projectID, agentName, source, idempotencyKey string) (string, error)

type Coordinator struct {
	store       Store
	stableRunID StableRunIDFunc
	now         func() time.Time
	observer    RunObserver
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

func (c *Coordinator) WithObserver(observer RunObserver) *Coordinator {
	c.observer = observer
	return c
}

func (c *Coordinator) SetNow(now func() time.Time) {
	if c == nil {
		return
	}
	c.now = now
}

// runIdentity is the part of a start request the stable run ID depends on.
// BeginRun and ExistingRun resolve it through one path, so an idempotent retry
// looks up exactly the run the first attempt created.
type runIdentity struct {
	project      domain.ProjectRecord
	projectAgent domain.ProjectAgentRecord
	runID        string
}

// normalizeStartRequest trims the fields both run-start paths share and
// normalizes the source the stable run ID uses.
func normalizeStartRequest(req StartRequest) StartRequest {
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.AgentName = strings.TrimSpace(req.AgentName)
	req.Source = NormalizeSource(req.Source)
	req.SchedulerID = strings.TrimSpace(req.SchedulerID)
	req.SchedulerRunID = strings.TrimSpace(req.SchedulerRunID)
	req.TriggerID = strings.TrimSpace(req.TriggerID)
	req.Driver = strings.TrimSpace(req.Driver)
	req.ClientRequestID = strings.TrimSpace(req.ClientRequestID)
	return req
}

// resolveIdentity resolves the project, agent, and stable run ID. Callers
// normalize the request first and only call it with a project, agent, and
// idempotency key present.
func (c *Coordinator) resolveIdentity(ctx context.Context, req StartRequest) (runIdentity, error) {
	project, err := c.store.GetProject(ctx, req.ProjectID)
	if err != nil {
		return runIdentity{}, fmt.Errorf("resolve project %s: %w", req.ProjectID, err)
	}
	projectAgent, err := c.store.GetProjectAgent(ctx, project.ID, req.AgentName)
	if err != nil {
		return runIdentity{}, fmt.Errorf("resolve project agent %s/%s: %w", project.ID, req.AgentName, err)
	}
	runID, err := c.stableRunID(project.ID, projectAgent.AgentName, req.Source, req.ClientRequestID)
	if err != nil {
		return runIdentity{}, err
	}
	return runIdentity{project: project, projectAgent: projectAgent, runID: runID}, nil
}

func (c *Coordinator) BeginRun(ctx context.Context, req StartRequest) (domain.ProjectRunRecord, error) {
	if c == nil || c.store == nil {
		return domain.ProjectRunRecord{}, fmt.Errorf("config store is required")
	}
	if c.stableRunID == nil {
		return domain.ProjectRunRecord{}, fmt.Errorf("stable run id function is required")
	}
	req = normalizeStartRequest(req)
	if req.ProjectID == "" || req.AgentName == "" {
		return domain.ProjectRunRecord{}, fmt.Errorf("project id and agent name are required")
	}
	if req.ClientRequestID == "" {
		req.ClientRequestID = uuid.NewString()
	}
	identity, err := c.resolveIdentity(ctx, req)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	project, projectAgent, runID := identity.project, identity.projectAgent, identity.runID
	revision, err := c.store.GetProjectRevision(ctx, project.ID, project.CurrentRevision)
	if err != nil {
		return domain.ProjectRunRecord{}, fmt.Errorf("resolve project revision %s/%d: %w", project.ID, project.CurrentRevision, err)
	}
	agent, err := projects.AgentDefinitionFromRevision(project, revision, projectAgent.AgentName)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	if !agent.Enabled {
		return domain.ProjectRunRecord{}, fmt.Errorf("project agent %s is disabled", agent.ID)
	}
	driver := firstNonEmpty(req.Driver, agent.Driver, projectAgent.Driver)
	if driver != "" {
		driver, err = driverpkg.ResolveSandboxRuntimeDriver(driver, "")
		if err != nil {
			return domain.ProjectRunRecord{}, err
		}
	}
	run := domain.ProjectRunRecord{
		RunID:           runID,
		ProjectID:       project.ID,
		ProjectName:     project.Name,
		ProjectRevision: project.CurrentRevision,
		AgentName:       projectAgent.AgentName,
		AgentID:         agent.ID,
		Source:          req.Source,
		SchedulerID:     req.SchedulerID,
		SchedulerRunID:  req.SchedulerRunID,
		TriggerID:       req.TriggerID,
		Status:          domain.ProjectRunStatusPending,
		Prompt:          req.Prompt,
		Driver:          driver,
		ImageRef:        firstNonEmpty(agent.GuestImage, projectAgent.Image),
		CleanupPolicy:   NormalizeCleanupPolicy(req.CleanupPolicy),
		ResultJSON:      "{}",
		Labels:          req.Labels,
	}
	var promptLogSeq uint64
	if root := strings.TrimSpace(req.RunLogRoot); root != "" {
		logsPath, pathErr := runlog.RunPath(root, run.RunID)
		if pathErr != nil {
			return domain.ProjectRunRecord{}, pathErr
		}
		run.LogFormat = runlog.FormatJSONL
		run.LogsPath = logsPath
		promptLogSeq = initialPromptLogSeq
	}
	var initialEvents []domain.ProjectRunEventRecord
	if strings.TrimSpace(run.Prompt) != "" {
		initialEvents = append(initialEvents, domain.ProjectRunEventRecord{ID: initialPromptEventID(run.RunID), RunID: run.RunID, Kind: domain.ProjectRunEventKindUserMessage, Text: run.Prompt, Agent: run.AgentName, LogSeq: promptLogSeq})
	}
	created, err := c.store.CreateProjectRunWithEvents(ctx, run, initialEvents)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	if c.observer != nil {
		c.observer.RunStatusChanged(ctx, domain.ProjectRunRecord{}, created)
	}
	return created, nil
}

// ExistingRun returns the run a request's idempotency key already created. It
// resolves only what the run ID depends on, so a retry can be recognized
// without resolving the request input again.
func (c *Coordinator) ExistingRun(ctx context.Context, req StartRequest) (domain.ProjectRunRecord, bool, error) {
	if c == nil || c.store == nil {
		return domain.ProjectRunRecord{}, false, fmt.Errorf("config store is required")
	}
	if c.stableRunID == nil {
		return domain.ProjectRunRecord{}, false, fmt.Errorf("stable run id function is required")
	}
	req = normalizeStartRequest(req)
	if req.ProjectID == "" || req.AgentName == "" || req.ClientRequestID == "" {
		return domain.ProjectRunRecord{}, false, nil
	}
	identity, err := c.resolveIdentity(ctx, req)
	if err != nil {
		return domain.ProjectRunRecord{}, false, err
	}
	run, err := c.store.GetProjectRun(ctx, identity.runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, domain.ErrNotFound) {
			return domain.ProjectRunRecord{}, false, nil
		}
		return domain.ProjectRunRecord{}, false, err
	}
	return run, true, nil
}

// BindSandbox attaches a sandbox to a run and records whether this request
// created it.
func (c *Coordinator) BindSandbox(ctx context.Context, runID, sandboxID string, created bool) (domain.ProjectRunRecord, error) {
	current, err := c.store.GetProjectRun(ctx, strings.TrimSpace(runID))
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	if StatusIsTerminal(current.Status) {
		return domain.ProjectRunRecord{}, fmt.Errorf("project run %s is already terminal", current.RunID)
	}
	current.SandboxID = strings.TrimSpace(sandboxID)
	current.SandboxCreated = created
	return c.store.UpdateProjectRun(ctx, current)
}

func (c *Coordinator) MarkRunning(ctx context.Context, runID, sessionID string) (domain.ProjectRunRecord, error) {
	return c.TransitionRun(ctx, TransitionRequest{
		RunID:     runID,
		Status:    domain.ProjectRunStatusRunning,
		SandboxID: sessionID,
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
	batch := make([]domain.ProjectRunEventRecord, 0, len(req.TerminalEvents)+1)
	if StatusIsTerminal(next.Status) {
		// The summary is best effort: a run log that cannot be read must not
		// keep the run from completing.
		if err := finalizeRunLog(ctx, &next); err != nil {
			next.LogIncomplete = true
		}
		batch = append(batch, req.TerminalEvents...)
		batch = append(batch, domain.ProjectRunEventRecord{ID: terminalStatusEventID(next.RunID), RunID: next.RunID, Kind: domain.ProjectRunEventKindStatus, PayloadJSON: next.ResultJSON, Success: next.Status == domain.ProjectRunStatusSucceeded, ExitCode: next.ExitCode, StopReason: next.Error})
	}
	updated, err := c.store.UpdateProjectRunWithEvents(ctx, next, batch)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	if c.observer != nil && current.Status != updated.Status {
		c.observer.RunStatusChanged(ctx, current, updated)
	}
	return updated, nil
}

func (c *Coordinator) nowUTC() time.Time {
	if c != nil && c.now != nil {
		return c.now().UTC()
	}
	return time.Now().UTC()
}

func applyProjectRunTransitionFields(run *domain.ProjectRunRecord, req TransitionRequest) {
	run.LogIncomplete = run.LogIncomplete || req.LogIncomplete
	if sandboxID := strings.TrimSpace(req.SandboxID); sandboxID != "" {
		run.SandboxID = sandboxID
	}
	if req.ExitCode != 0 {
		run.ExitCode = req.ExitCode
	}
	if value := strings.TrimSpace(req.Error); value != "" {
		run.Error = value
	}
	if value := strings.TrimSpace(req.ErrorStack); value != "" {
		run.ErrorStack = value
	}
	if req.Output != "" {
		run.Output = req.Output
	}
	if value := strings.TrimSpace(req.ResultJSON); value != "" {
		run.ResultJSON = value
	}
	// A run log's location is fixed when the run is created; execution paths
	// that still report their legacy transcript path must not move it.
	if value := strings.TrimSpace(req.LogsPath); value != "" && run.LogFormat != runlog.FormatJSONL {
		run.LogsPath = value
	}
	if value := strings.TrimSpace(req.ArtifactsDir); value != "" {
		run.ArtifactsDir = value
	}
	if value := strings.TrimSpace(req.AgentCellID); value != "" {
		run.AgentCellID = value
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

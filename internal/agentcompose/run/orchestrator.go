package run

import (
	"context"
	"fmt"

	projectdomain "agent-compose/internal/agentcompose/project"
)

type ProjectRunCoordinator interface {
	BeginRun(context.Context, StartRequest) (projectdomain.ProjectRunRecord, error)
	MarkRunning(context.Context, string, string) (projectdomain.ProjectRunRecord, error)
	MarkSucceeded(context.Context, TransitionRequest) (projectdomain.ProjectRunRecord, error)
	MarkFailed(context.Context, TransitionRequest) (projectdomain.ProjectRunRecord, error)
}

type AgentRunRequest struct {
	ProjectID       string
	AgentName       string
	Source          string
	SchedulerID     string
	TriggerID       string
	Prompt          string
	ClientRequestID string
}

type SessionRef struct {
	ID      string
	HostDir string
	Value   any
}

type AgentOrchestrator struct {
	Coordinator ProjectRunCoordinator
	Prepare     func(context.Context, projectdomain.ProjectRunRecord) (any, error)
	Ensure      func(context.Context, projectdomain.ProjectRunRecord, any) (SessionRef, error)
	BeforeExec  func(context.Context, projectdomain.ProjectRunRecord, SessionRef) error
	Execute     func(context.Context, projectdomain.ProjectRunRecord, SessionRef) (AgentCell, error)
	Cleanup     func(context.Context, ProjectRunCoordinator, projectdomain.ProjectRunRecord, SessionRef) projectdomain.ProjectRunRecord
}

type StageError struct {
	Stage string
	Err   error
}

func (e *StageError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *StageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (o AgentOrchestrator) Run(ctx, transitionCtx context.Context, req AgentRunRequest) (projectdomain.ProjectRunRecord, error, error) {
	if transitionCtx == nil {
		transitionCtx = ctx
	}
	run, err := o.Coordinator.BeginRun(ctx, StartRequest{
		ProjectID:       req.ProjectID,
		AgentName:       req.AgentName,
		Source:          req.Source,
		SchedulerID:     req.SchedulerID,
		TriggerID:       req.TriggerID,
		Prompt:          req.Prompt,
		ClientRequestID: req.ClientRequestID,
	})
	if err != nil {
		return projectdomain.ProjectRunRecord{}, nil, &StageError{Stage: "begin", Err: err}
	}
	prepared, err := o.Prepare(ctx, run)
	if err != nil {
		run, markErr := o.Coordinator.MarkFailed(transitionCtx, TransitionRequest{
			RunID: run.RunID,
			Error: fmt.Sprintf("workspace preparation failed: %v", err),
		})
		if markErr != nil {
			return projectdomain.ProjectRunRecord{}, nil, &StageError{Stage: "transition", Err: markErr}
		}
		return run, err, nil
	}
	session, err := o.Ensure(ctx, run, prepared)
	if err != nil {
		transition := TransitionRequest{
			RunID: run.RunID,
			Error: fmt.Sprintf("session start failed: %v", err),
		}
		if session.ID != "" {
			transition.SessionID = session.ID
		}
		run, markErr := o.Coordinator.MarkFailed(transitionCtx, transition)
		if markErr != nil {
			return projectdomain.ProjectRunRecord{}, nil, &StageError{Stage: "transition", Err: markErr}
		}
		return run, err, nil
	}
	run, err = o.Coordinator.MarkRunning(transitionCtx, run.RunID, session.ID)
	if err != nil {
		return projectdomain.ProjectRunRecord{}, nil, &StageError{Stage: "transition", Err: err}
	}
	if err := o.BeforeExec(ctx, run, session); err != nil {
		run, markErr := o.Coordinator.MarkFailed(transitionCtx, TransitionRequest{
			RunID:     run.RunID,
			SessionID: session.ID,
			ExitCode:  1,
			Error:     fmt.Sprintf("agent execution failed: %v", err),
		})
		if markErr != nil {
			return projectdomain.ProjectRunRecord{}, nil, &StageError{Stage: "transition", Err: markErr}
		}
		return run, err, nil
	}
	cell, execErr := o.Execute(ctx, run, session)
	transition := TransitionFromAgentCell(run, session.ID, session.HostDir, cell, execErr)
	if execErr != nil || !cell.Success {
		run, err = o.Coordinator.MarkFailed(transitionCtx, transition)
		if err != nil {
			return projectdomain.ProjectRunRecord{}, nil, &StageError{Stage: "transition", Err: err}
		}
		run = o.cleanup(transitionCtx, run, session)
		return run, execErr, nil
	}
	run, err = o.Coordinator.MarkSucceeded(transitionCtx, transition)
	if err != nil {
		return projectdomain.ProjectRunRecord{}, nil, &StageError{Stage: "transition", Err: err}
	}
	run = o.cleanup(transitionCtx, run, session)
	return run, nil, nil
}

func (o AgentOrchestrator) cleanup(ctx context.Context, run projectdomain.ProjectRunRecord, session SessionRef) projectdomain.ProjectRunRecord {
	if o.Cleanup == nil {
		return run
	}
	return o.Cleanup(ctx, o.Coordinator, run, session)
}

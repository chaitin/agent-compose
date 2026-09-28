package schedulers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/chaitin/agent-compose/pkg/events"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type InvocationResult struct {
	ResultJSON string
	DurationMs int64
	Warnings   []string
}

type InvocationExecutorDependencies struct {
	Engine      SchedulerEngine
	HostFactory RunHostFactory
	EnterRun    func(scheduler domain.Scheduler) bool
	LeaveRun    func(schedulerID string)
	NewID       func() string
}

type InvocationExecutor struct {
	deps InvocationExecutorDependencies
}

func NewInvocationExecutor(deps InvocationExecutorDependencies) *InvocationExecutor {
	return &InvocationExecutor{deps: deps}
}

func (e *InvocationExecutor) Invoke(ctx context.Context, scheduler domain.Scheduler, payloadJSON string) (InvocationResult, error) {
	payloadJSON, err := domain.NormalizeJSONDocument(payloadJSON)
	if err != nil {
		return InvocationResult{}, err
	}
	if e.deps.Engine == nil || e.deps.HostFactory == nil {
		return InvocationResult{}, fmt.Errorf("scheduler invocation runtime is unavailable")
	}
	if e.deps.EnterRun != nil && !e.deps.EnterRun(scheduler) {
		return InvocationResult{}, domain.ResourceError(domain.ErrFailedPrecondition, "scheduler", scheduler.Summary.ID, "scheduler is already running", nil)
	}
	if e.deps.LeaveRun != nil {
		defer e.deps.LeaveRun(scheduler.Summary.ID)
	}

	correlationID := uuid.NewString()
	if e.deps.NewID != nil {
		if generatedID := strings.TrimSpace(e.deps.NewID()); generatedID != "" {
			correlationID = generatedID
		}
	}
	ctx = events.WithPublisherProject(ctx, scheduler.Summary.ProjectID)
	host := e.deps.HostFactory(scheduler, RuntimeExecutionContext{ID: correlationID, Kind: ExecutionKindInvocation}, TriggerEventMetadata{})
	startedAt := time.Now().UTC()
	maxAsyncLLM, maxAsyncAgent := AsyncConcurrencyFromSchedulerEnv(scheduler.EnvItems)
	execution, execErr := e.deps.Engine.Execute(ctx, SchedulerExecutionRequest{
		Runtime:                  scheduler.Summary.Runtime,
		Script:                   scheduler.Script,
		PayloadJSON:              payloadJSON,
		MaxAsyncLLMConcurrency:   maxAsyncLLM,
		MaxAsyncAgentConcurrency: maxAsyncAgent,
	}, host)
	if host != nil {
		host.CleanupCommandSessions(context.WithoutCancel(ctx))
	}
	if execErr != nil {
		return InvocationResult{}, execErr
	}
	return InvocationResult{
		ResultJSON: execution.ResultJSON,
		DurationMs: time.Since(startedAt).Milliseconds(),
		Warnings:   append([]string(nil), execution.Warnings...),
	}, nil
}

func (c *Controller) InvokeScheduler(ctx context.Context, schedulerID, payloadJSON string) (InvocationResult, error) {
	scheduler, _, err := c.LoadSchedulerForRun(ctx, schedulerID, "")
	if err != nil {
		return InvocationResult{}, err
	}
	return c.invocations.Invoke(ctx, scheduler, payloadJSON)
}

package loaders

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	domain "agent-compose/pkg/model"
)

type InvocationResult struct {
	ResultJSON string
	DurationMs int64
	Warnings   []string
}

type InvocationExecutorDependencies struct {
	Engine      LoaderEngine
	HostFactory RunHostFactory
	EnterRun    func(loader domain.Loader) bool
	LeaveRun    func(loaderID string)
	NewID       func() string
}

type InvocationExecutor struct {
	deps InvocationExecutorDependencies
}

func NewInvocationExecutor(deps InvocationExecutorDependencies) *InvocationExecutor {
	return &InvocationExecutor{deps: deps}
}

func (e *InvocationExecutor) Invoke(ctx context.Context, loader domain.Loader, payloadJSON string) (InvocationResult, error) {
	payloadJSON, err := domain.NormalizeJSONDocument(payloadJSON)
	if err != nil {
		return InvocationResult{}, err
	}
	if e.deps.Engine == nil || e.deps.HostFactory == nil {
		return InvocationResult{}, fmt.Errorf("scheduler invocation runtime is unavailable")
	}
	if e.deps.EnterRun != nil && !e.deps.EnterRun(loader) {
		return InvocationResult{}, domain.ResourceError(domain.ErrFailedPrecondition, "scheduler", loader.Summary.ID, "scheduler is already running", nil)
	}
	if e.deps.LeaveRun != nil {
		defer e.deps.LeaveRun(loader.Summary.ID)
	}

	correlationID := uuid.NewString()
	if e.deps.NewID != nil {
		if generatedID := strings.TrimSpace(e.deps.NewID()); generatedID != "" {
			correlationID = generatedID
		}
	}
	host := e.deps.HostFactory(loader, RuntimeExecutionContext{ID: correlationID, Kind: ExecutionKindInvocation}, TriggerEventMetadata{})
	startedAt := time.Now().UTC()
	execution, execErr := e.deps.Engine.Execute(ctx, LoaderExecutionRequest{
		Runtime:     loader.Summary.Runtime,
		Script:      loader.Script,
		PayloadJSON: payloadJSON,
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

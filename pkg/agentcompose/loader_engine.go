package agentcompose

import (
	loaderdomain "agent-compose/internal/agentcompose/loader"
	"context"
	"time"

	"github.com/samber/do/v2"
)

type LoaderHost = loaderdomain.LoaderHost
type LoaderValidationResult = loaderdomain.LoaderValidationResult
type LoaderExecutionRequest = loaderdomain.LoaderExecutionRequest
type LoaderExecutionResult = loaderdomain.LoaderExecutionResult
type LoaderEngine = loaderdomain.LoaderEngine
type QJSLoaderEngine = loaderdomain.QJSLoaderEngine

func NewLoaderEngine(do.Injector) (LoaderEngine, error) {
	return loaderdomain.NewLoaderEngine(), nil
}

func loaderEngineMaxExecutionTime(ctx context.Context) int {
	const defaultMaxExecutionTimeMs = int((60 * time.Minute) / time.Millisecond)
	deadline, ok := ctx.Deadline()
	if !ok {
		return defaultMaxExecutionTimeMs
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 1
	}
	remainingMs := int(remaining / time.Millisecond)
	if remainingMs < 1 {
		return 1
	}
	return remainingMs
}

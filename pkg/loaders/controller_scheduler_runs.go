package loaders

import (
	"context"

	domain "agent-compose/pkg/model"
)

func (c *Controller) RunScheduler(ctx context.Context, request SchedulerRunRequest) (domain.LoaderRunSummary, error) {
	return c.schedulerRuns.Run(ctx, request)
}

func (c *Controller) InvokeScheduler(ctx context.Context, loaderID, payloadJSON string) (InvocationResult, error) {
	loader, _, err := c.LoadLoaderForRun(ctx, loaderID, "")
	if err != nil {
		return InvocationResult{}, err
	}
	return c.invocations.Invoke(ctx, loader, payloadJSON)
}

func (c *Controller) StartSchedulerRun(ctx context.Context, request SchedulerRunRequest) (domain.LoaderRunSummary, error) {
	return c.schedulerRuns.Start(ctx, request)
}

func (c *Controller) GetSchedulerRun(ctx context.Context, loaderID, runID string) (domain.LoaderRunSummary, error) {
	return c.schedulerRuns.Get(ctx, loaderID, runID)
}

func (c *Controller) ListSchedulerRuns(ctx context.Context, loaderID string, limit int) ([]domain.LoaderRunSummary, error) {
	return c.schedulerRuns.List(ctx, loaderID, limit)
}

func (c *Controller) StopSchedulerRun(ctx context.Context, loaderID, runID, reason string) (domain.LoaderRunSummary, bool, error) {
	return c.schedulerRuns.Stop(ctx, loaderID, runID, reason)
}

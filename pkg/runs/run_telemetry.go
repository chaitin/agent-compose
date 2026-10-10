package runs

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/trace"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/telemetry"
	"github.com/chaitin/agent-compose/pkg/volumes"
)

// startRunSpan joins the caller's trace and opens the run's invoke_agent span.
// Detached and attach runs restore only the request-scoped W3C strings, so the
// remote parent is re-attached here when the live request span is absent. The
// caller is responsible for ending the span with endRunSpan.
func (c *Controller) startRunSpan(ctx context.Context, runID, projectID, agentName string) (context.Context, trace.Span) {
	traceContext := domain.TraceContextFromContext(ctx)
	ctx = telemetry.ContextWithRemoteTraceContext(ctx, traceContext.Traceparent, traceContext.Tracestate)
	return c.recorder.Start(ctx, telemetry.SpanInvokeAgent,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			telemetry.AttrRunID.String(runID),
			telemetry.AttrProjectID.String(projectID),
			telemetry.AttrAgentName.String(agentName),
			telemetry.AttrGenAIAgentName.String(agentName),
			telemetry.AttrGenAIOperationName.String(telemetry.SpanInvokeAgent),
		),
	)
}

// endRunSpan closes the run span and records the run's duration and terminal
// status. The recorded status is authoritative once written; early failures
// return before a status exists, so the error decides there.
func (c *Controller) endRunSpan(ctx context.Context, span trace.Span, startedAt time.Time, record domain.ProjectRunRecord, err error) {
	telemetry.EndSpan(span, err)
	c.recorder.RecordRun(ctx, telemetry.RunMeasurement{
		Duration: time.Since(startedAt),
		Status:   runOutcomeStatus(record.Status, err),
		Driver:   c.runDriver(record),
	})
}

// runOutcomeStatus reports the terminal status recorded on a run. Early
// failures return before a status is written, so the error wins there; a run
// that ends without either is reported as unknown rather than guessed.
func runOutcomeStatus(status string, err error) string {
	if status != "" {
		return status
	}
	if err != nil {
		return domain.ProjectRunStatusFailed
	}
	return "unknown"
}

// runDriver reports the driver a run used. Early failures return before the run
// record carries a driver, so fall back to the configured default driver to keep
// the error-rate metric attributable.
func (c *Controller) runDriver(record domain.ProjectRunRecord) string {
	if record.Driver != "" {
		return record.Driver
	}
	if c.config != nil {
		return c.config.RuntimeDriver
	}
	return ""
}

// resolveRunVolumeMounts resolves the run's volume specs and exports the
// operation as a child span of the run. The mount count and driver are recorded
// as attributes; project paths and volume names stay out of telemetry because
// they are host-specific and high cardinality.
func (c *Controller) resolveRunVolumeMounts(ctx context.Context, driver string, specs []domain.VolumeMountSpec, options volumes.ResolveOptions) (mounts []domain.SandboxVolumeMount, warnings []string, err error) {
	if c.volumes == nil {
		return nil, nil, fmt.Errorf("volume resolver is required")
	}
	ctx, span := c.recorder.Start(ctx, telemetry.SpanVolumePrepare,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			telemetry.AttrDriver.String(driver),
			telemetry.AttrVolumeMountCount.Int(len(specs)),
		),
	)
	defer func() { telemetry.EndSpan(span, err) }()
	return c.volumes.ResolveMounts(ctx, specs, options)
}

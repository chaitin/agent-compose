package adapters

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/telemetry"
)

// startRuntimeSpan opens a driver-operation span and returns the derived
// context plus a function that records the operation's outcome. The tracer is
// a no-op when daemon telemetry is not configured, so the call adds no
// attributes and exports nothing in that case.
func startRuntimeSpan(ctx context.Context, tracer *telemetry.Tracer, name string, session *domain.Sandbox, driver string) (context.Context, func(error)) {
	sandboxID := ""
	if session != nil {
		sandboxID = session.Summary.ID
		if driver == "" {
			driver = session.Summary.Driver
		}
	}
	ctx, span := tracer.Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			telemetry.AttrSandboxID.String(sandboxID),
			telemetry.AttrDriver.String(driver),
		),
	)
	return ctx, func(err error) { telemetry.EndSpan(span, err) }
}

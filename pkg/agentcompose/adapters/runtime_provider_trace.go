package adapters

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/trace"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/telemetry"
)

// startRuntimeSpan opens a driver-operation span and returns the derived
// context plus a function that records the operation's outcome and duration.
// The recorder is a no-op when daemon telemetry is not configured, so the call
// adds no attributes and exports nothing in that case.
func startRuntimeSpan(ctx context.Context, recorder *telemetry.Recorder, operation string, session *domain.Sandbox, driver string) (context.Context, func(error)) {
	driver = runtimeDriverName(session, driver)
	sandboxID := ""
	if session != nil {
		sandboxID = session.Summary.ID
	}
	ctx, span := recorder.Start(ctx, operation,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			telemetry.AttrSandboxID.String(sandboxID),
			telemetry.AttrDriver.String(driver),
		),
	)
	startedAt := time.Now()
	return ctx, func(err error) {
		recorder.RecordDriverOperation(ctx, operation, driver, time.Since(startedAt), err)
		telemetry.EndSpan(span, err)
	}
}

// runtimeDriverName resolves the driver a sandbox operation runs on. A new
// sandbox carries no VM state yet, so the driver recorded on the sandbox
// summary is the fallback.
func runtimeDriverName(session *domain.Sandbox, driver string) string {
	if driver != "" {
		return driver
	}
	if session != nil {
		return session.Summary.Driver
	}
	return ""
}

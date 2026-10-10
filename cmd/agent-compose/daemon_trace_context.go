package main

import (
	"strings"

	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel/trace"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/telemetry"
)

// newDaemonTraceContextMiddleware extracts the caller's W3C trace context from
// the inbound request and stores it in the request context. A run execution
// relays it to the sandboxed agent runtime so the caller's trace stays
// continuous. The value is request-scoped and is never persisted; a request
// without a trace context is left untouched.
//
// When daemon telemetry is enabled the middleware also starts a server span
// that is a child of the caller's context, so the daemon's own operations
// export as part of the caller's trace. With telemetry disabled the recorder is
// a no-op and the request is handled exactly as before.
func newDaemonTraceContextMiddleware(recorder *telemetry.Recorder) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			traceparent := strings.TrimSpace(c.Request().Header.Get("traceparent"))
			if traceparent != "" {
				tracestate := strings.TrimSpace(c.Request().Header.Get("tracestate"))
				ctx = domain.NewContextWithTraceContext(ctx, domain.TraceContext{
					Traceparent: traceparent,
					Tracestate:  tracestate,
				})
				ctx = telemetry.ContextWithRemoteTraceContext(ctx, traceparent, tracestate)
			}
			ctx, span := recorder.Start(ctx, requestSpanName(c), trace.WithSpanKind(trace.SpanKindServer))
			var handlerErr error
			defer func() { telemetry.EndSpan(span, handlerErr) }()
			span.SetAttributes(
				telemetry.AttrHTTPRequestMethod.String(c.Request().Method),
				telemetry.AttrHTTPRoute.String(c.Path()),
			)
			c.SetRequest(c.Request().WithContext(ctx))
			handlerErr = next(c)
			return handlerErr
		}
	}
}

// requestSpanName names the server span after the matched route, which is
// stable for Connect RPCs and avoids per-request cardinality from the raw URL.
func requestSpanName(c echo.Context) string {
	route := c.Path()
	if route == "" {
		route = c.Request().URL.Path
	}
	return c.Request().Method + " " + route
}

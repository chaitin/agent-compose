package main

import (
	"strings"

	"github.com/labstack/echo/v4"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// newDaemonTraceContextMiddleware extracts the caller's W3C trace context from
// the inbound request and stores it in the request context. A run execution
// relays it to the sandboxed agent runtime so the caller's trace stays
// continuous. The value is request-scoped and is never persisted; a request
// without a trace context is left untouched.
func newDaemonTraceContextMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			traceparent := strings.TrimSpace(c.Request().Header.Get("traceparent"))
			if traceparent == "" {
				return next(c)
			}
			traceContext := domain.TraceContext{
				Traceparent: traceparent,
				Tracestate:  strings.TrimSpace(c.Request().Header.Get("tracestate")),
			}
			ctx := domain.NewContextWithTraceContext(c.Request().Context(), traceContext)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

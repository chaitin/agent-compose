package proxy

import (
	"log/slog"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
	"github.com/labstack/echo/v4"

	"github.com/chaitin/agent-compose/pkg/llms"
)

// upstreamCall identifies the upstream call a failure is logged for. Model is
// the one requested, because the connection's configured model may be blank on
// a passthrough.
type upstreamCall struct {
	Target   llms.ResolvedTarget
	Model    string
	Protocol protocolbridge.Protocol
}

// logUpstreamFailure records why a proxied call answered 502. A guest that
// hung up mid-call cancels the upstream request too, and without the cause it
// is indistinguishable from an upstream that refused the connection, so the
// two are logged apart.
func (h runtimeLLMHandler) logUpstreamFailure(c echo.Context, msg string, call upstreamCall, err error) {
	ctx := c.Request().Context()
	attrs := []any{
		"sandbox_id", c.Param("sandbox_id"), "route", c.Path(),
		"connection", call.Target.Provider.ID, "model", call.Model,
		"upstream_protocol", string(call.Protocol), "error", err,
	}
	if ctx.Err() != nil {
		h.logger().InfoContext(ctx, msg+": the guest disconnected", attrs...)
		return
	}
	h.logger().WarnContext(ctx, msg, attrs...)
}

func (h runtimeLLMHandler) logger() *slog.Logger {
	if h.opts.Logger != nil {
		return h.opts.Logger
	}
	return slog.Default()
}

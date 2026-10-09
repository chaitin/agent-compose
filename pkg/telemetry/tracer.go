package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Span names for daemon operations. Agent and tool spans use the OpenTelemetry
// GenAI semantic-convention names, which are still Development: the names are
// adopted for consistency but are not a frozen compatibility contract.
const (
	SpanInvokeAgent        = "invoke_agent"
	SpanSandboxEnsure      = "sandbox.ensure"
	SpanSandboxStop        = "sandbox.stop"
	SpanSandboxRemove      = "sandbox.remove"
	SpanSandboxExec        = "sandbox.exec"
	SpanSandboxInteraction = "sandbox.interaction"
)

// Attribute keys carried by daemon spans. agent_compose.* identifies the
// engine's own objects; gen_ai.* follows the OpenTelemetry GenAI conventions.
var (
	AttrSandboxID          = attribute.Key("agent_compose.sandbox.id")
	AttrRunID              = attribute.Key("agent_compose.run.id")
	AttrProjectID          = attribute.Key("agent_compose.project.id")
	AttrAgentName          = attribute.Key("agent_compose.agent.name")
	AttrDriver             = attribute.Key("agent_compose.driver")
	AttrGenAIOperationName = attribute.Key("gen_ai.operation.name")
	AttrGenAIAgentName     = attribute.Key("gen_ai.agent.name")
	AttrHTTPRequestMethod  = attribute.Key("http.request.method")
	AttrHTTPRoute          = attribute.Key("http.route")
)

// Tracer starts daemon spans. A nil Tracer and a Tracer built from a nil
// provider both start no-op spans, so callers never need to branch on whether
// export is enabled.
type Tracer struct {
	tracer trace.Tracer
}

// NewTracer wraps provider. A nil provider yields a no-op tracer.
func NewTracer(provider trace.TracerProvider) *Tracer {
	if provider == nil {
		return &Tracer{}
	}
	return &Tracer{tracer: provider.Tracer(scopeName)}
}

// Start begins a span named name. With export disabled the returned span is a
// no-op that ignores attributes and status, and the context is unchanged.
func (t *Tracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if t == nil || t.tracer == nil {
		return ctx, noop.Span{}
	}
	return t.tracer.Start(ctx, name, opts...)
}

// EndSpan ends span and marks it failed when err is non-nil. It deliberately
// records only the status code and never the error text: error messages can
// embed prompts, paths, or credentials, and this integration keeps content
// capture off by default.
func EndSpan(span trace.Span, err error) {
	if err != nil {
		span.SetStatus(codes.Error, "")
	}
	span.End()
}

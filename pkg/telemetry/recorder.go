package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
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
	SpanImagePull          = "image.pull"
	SpanVolumePrepare      = "volume.prepare"
)

// Attribute keys carried by daemon spans. agent_compose.* identifies the
// engine's own objects; gen_ai.* follows the OpenTelemetry GenAI conventions.
var (
	AttrSandboxID          = attribute.Key("agent_compose.sandbox.id")
	AttrRunID              = attribute.Key("agent_compose.run.id")
	AttrProjectID          = attribute.Key("agent_compose.project.id")
	AttrAgentName          = attribute.Key("agent_compose.agent.name")
	AttrDriver             = attribute.Key("agent_compose.driver")
	AttrRunStatus          = attribute.Key("agent_compose.run.status")
	AttrOperation          = attribute.Key("agent_compose.operation")
	AttrOutcome            = attribute.Key("agent_compose.outcome")
	AttrVolumeMountCount   = attribute.Key("agent_compose.volume.mount_count")
	AttrGenAIOperationName = attribute.Key("gen_ai.operation.name")
	AttrGenAIAgentName     = attribute.Key("gen_ai.agent.name")
	AttrHTTPRequestMethod  = attribute.Key("http.request.method")
	AttrHTTPRoute          = attribute.Key("http.route")
)

// Operation outcomes recorded as metric attributes.
const (
	outcomeSuccess = "success"
	outcomeFailure = "failure"
)

// Recorder starts daemon spans and records daemon metrics. A nil Recorder, and
// a Recorder built from nil providers, start no-op spans and record nothing, so
// callers never need to branch on whether export is enabled.
type Recorder struct {
	tracer  trace.Tracer
	metrics *metricInstruments
}

// NewRecorder wraps the providers that export daemon telemetry. A nil provider
// disables that signal: nil tracing starts no-op spans, nil metrics records
// nothing. It returns an error only when a metric instrument cannot be created,
// which means a programming error in the fixed instrument set.
func NewRecorder(tracerProvider trace.TracerProvider, meterProvider metric.MeterProvider) (*Recorder, error) {
	recorder := &Recorder{}
	if tracerProvider != nil {
		recorder.tracer = tracerProvider.Tracer(scopeName)
	}
	if meterProvider != nil {
		instruments, err := newMetricInstruments(meterProvider.Meter(scopeName))
		if err != nil {
			return nil, err
		}
		recorder.metrics = instruments
	}
	return recorder, nil
}

// Start begins a span named name. With export disabled the returned span is a
// no-op that ignores attributes and status, and the context is unchanged.
func (r *Recorder) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if r == nil || r.tracer == nil {
		return ctx, noop.Span{}
	}
	return r.tracer.Start(ctx, name, opts...)
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

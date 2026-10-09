// Package telemetry owns the daemon's OpenTelemetry tracing: it builds the
// trace provider from the same OTLP endpoint and headers that configure native
// provider export in the guest, so a daemon operation joins the caller's trace
// instead of starting a second one.
//
// An empty endpoint disables the provider. A disabled provider exports
// nothing, opens no network connection, and hands out a tracer whose spans are
// no-ops, so the zero-configuration daemon behaves exactly as it did before.
package telemetry

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	// scopeName is the instrumentation scope daemon spans are attributed to.
	scopeName = "github.com/chaitin/agent-compose"
	// traceSignalPath is appended to the configured endpoint, which is an
	// OTLP/HTTP base URL shared with the guest exporters.
	traceSignalPath = "/v1/traces"
	// serviceName identifies this process in the exported resource.
	serviceName = "agent-compose"
)

// Provider owns the daemon trace provider and its exporter lifecycle. A nil or
// disabled Provider is safe to use and exports nothing.
type Provider struct {
	tracerProvider *sdktrace.TracerProvider
}

// NewProvider builds the daemon trace provider from an OTLP/HTTP base URL and
// optional headers. An empty endpoint returns a disabled provider with no
// exporter and no background worker.
func NewProvider(endpoint string, headers map[string]string, serviceVersion string) (*Provider, error) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return &Provider{}, nil
	}
	options := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(endpoint + traceSignalPath)}
	if len(headers) > 0 {
		// Copy: the exporter keeps the map, and the caller's configuration must
		// not be shared with a background exporter.
		copied := make(map[string]string, len(headers))
		for name, value := range headers {
			copied[name] = value
		}
		options = append(options, otlptracehttp.WithHeaders(copied))
	}
	exporter, err := otlptracehttp.New(context.Background(), options...)
	if err != nil {
		return nil, err
	}
	return &Provider{tracerProvider: sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", serviceName),
			attribute.String("service.version", serviceVersion),
		)),
	)}, nil
}

// Enabled reports whether the provider exports spans.
func (p *Provider) Enabled() bool {
	return p != nil && p.tracerProvider != nil
}

// Tracer returns the daemon tracer. It never returns nil and is a no-op while
// the provider is disabled.
func (p *Provider) Tracer() *Tracer {
	if !p.Enabled() {
		return NewTracer(nil)
	}
	return NewTracer(p.tracerProvider)
}

// Shutdown flushes buffered spans and releases the exporter. It is a no-op for
// a disabled provider.
func (p *Provider) Shutdown(ctx context.Context) error {
	if !p.Enabled() {
		return nil
	}
	return p.tracerProvider.Shutdown(ctx)
}

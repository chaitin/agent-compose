// Package telemetry owns the daemon's OpenTelemetry tracing and metrics: it
// builds both providers from the same OTLP endpoint and headers that configure
// native provider export in the guest, so a daemon operation joins the caller's
// trace instead of starting a second one.
//
// An empty endpoint disables the provider. A disabled provider exports
// nothing, opens no network connection, and hands out a recorder whose spans
// and metric recordings are no-ops, so the zero-configuration daemon behaves
// exactly as it did before.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	// scopeName is the instrumentation scope daemon telemetry is attributed to.
	scopeName = "github.com/chaitin/agent-compose"
	// traceSignalPath and metricSignalPath are appended to the configured
	// endpoint, which is an OTLP/HTTP base URL shared with the guest exporters.
	traceSignalPath  = "/v1/traces"
	metricSignalPath = "/v1/metrics"
	// serviceName identifies this process in the exported resource.
	serviceName = "agent-compose"
)

// Provider owns the daemon trace and metric providers and their exporter
// lifecycle. A nil or disabled Provider is safe to use and exports nothing.
type Provider struct {
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
}

// NewProvider builds the daemon trace and metric providers from an OTLP/HTTP
// base URL and optional headers. An empty endpoint returns a disabled provider
// with no exporter and no background worker.
func NewProvider(endpoint string, headers map[string]string, serviceVersion string) (*Provider, error) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return &Provider{}, nil
	}
	// Copy: the exporters keep the map, and the caller's configuration must not
	// be shared with background exporters.
	headers = copyHeaders(headers)
	traceExporter, err := otlptracehttp.New(context.Background(), otlptracehttp.WithEndpointURL(endpoint+traceSignalPath), otlptracehttp.WithHeaders(headers))
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	metricExporter, err := otlpmetrichttp.New(context.Background(), otlpmetrichttp.WithEndpointURL(endpoint+metricSignalPath), otlpmetrichttp.WithHeaders(headers))
	if err != nil {
		return nil, fmt.Errorf("create OTLP metric exporter: %w", err)
	}
	res := resource.NewSchemaless(
		attribute.String("service.name", serviceName),
		attribute.String("service.version", serviceVersion),
	)
	return &Provider{
		tracerProvider: sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExporter),
			sdktrace.WithResource(res),
		),
		meterProvider: sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
			sdkmetric.WithResource(res),
		),
	}, nil
}

func copyHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	copied := make(map[string]string, len(headers))
	for name, value := range headers {
		copied[name] = value
	}
	return copied
}

// Enabled reports whether the provider exports telemetry.
func (p *Provider) Enabled() bool {
	return p != nil && p.tracerProvider != nil
}

// Recorder returns the daemon recorder. It never returns nil and starts no-op
// spans while the provider is disabled.
func (p *Provider) Recorder() (*Recorder, error) {
	if !p.Enabled() {
		return NewRecorder(nil, nil)
	}
	return NewRecorder(p.tracerProvider, p.meterProvider)
}

// Shutdown flushes buffered spans and metrics and releases the exporters. It is
// a no-op for a disabled provider.
func (p *Provider) Shutdown(ctx context.Context) error {
	if !p.Enabled() {
		return nil
	}
	return errors.Join(p.tracerProvider.Shutdown(ctx), p.meterProvider.Shutdown(ctx))
}

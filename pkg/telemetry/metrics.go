package telemetry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/metric"
)

// Metric names for daemon operations. Names and units follow the OpenTelemetry
// naming conventions for the engine's own objects.
const (
	MetricRunDuration           = "agent_compose.run.duration"
	MetricRunCount              = "agent_compose.run.count"
	MetricSandboxCreateDuration = "agent_compose.sandbox.create.duration"
	MetricDriverOperationCount  = "agent_compose.driver.operation.count"
)

// metricInstruments holds the daemon metric instruments. Metric attributes stay
// low cardinality: driver, operation, outcome and run status. Run, project and
// agent identifiers are trace attributes, never metric labels.
type metricInstruments struct {
	runDuration           metric.Float64Histogram
	runCount              metric.Int64Counter
	sandboxCreateDuration metric.Float64Histogram
	driverOperationCount  metric.Int64Counter
}

func newMetricInstruments(meter metric.Meter) (*metricInstruments, error) {
	runDuration, err := meter.Float64Histogram(MetricRunDuration,
		metric.WithUnit("s"), metric.WithDescription("Wall-clock duration of one agent run."))
	if err != nil {
		return nil, fmt.Errorf("create %s instrument: %w", MetricRunDuration, err)
	}
	runCount, err := meter.Int64Counter(MetricRunCount,
		metric.WithUnit("{run}"), metric.WithDescription("Number of agent runs by terminal status."))
	if err != nil {
		return nil, fmt.Errorf("create %s instrument: %w", MetricRunCount, err)
	}
	sandboxCreateDuration, err := meter.Float64Histogram(MetricSandboxCreateDuration,
		metric.WithUnit("s"), metric.WithDescription("Wall-clock duration of creating and starting a sandbox."))
	if err != nil {
		return nil, fmt.Errorf("create %s instrument: %w", MetricSandboxCreateDuration, err)
	}
	driverOperationCount, err := meter.Int64Counter(MetricDriverOperationCount,
		metric.WithUnit("{operation}"), metric.WithDescription("Number of runtime-driver operations by outcome."))
	if err != nil {
		return nil, fmt.Errorf("create %s instrument: %w", MetricDriverOperationCount, err)
	}
	return &metricInstruments{
		runDuration:           runDuration,
		runCount:              runCount,
		sandboxCreateDuration: sandboxCreateDuration,
		driverOperationCount:  driverOperationCount,
	}, nil
}

// RunMeasurement is the terminal outcome of one agent run. Status is the
// domain's run status, recorded as a low-cardinality attribute.
type RunMeasurement struct {
	Duration time.Duration
	Status   string
	Driver   string
}

// RecordRun records one finished run's duration and terminal status, which
// together give the run success rate per driver.
func (r *Recorder) RecordRun(ctx context.Context, measurement RunMeasurement) {
	if r == nil || r.metrics == nil {
		return
	}
	attributes := metric.WithAttributes(
		AttrRunStatus.String(measurement.Status),
		AttrDriver.String(measurement.Driver),
	)
	r.metrics.runDuration.Record(ctx, measurement.Duration.Seconds(), attributes)
	r.metrics.runCount.Add(ctx, 1, attributes)
}

// RecordSandboxCreate records how long creating and starting a sandbox took and
// whether it succeeded.
func (r *Recorder) RecordSandboxCreate(ctx context.Context, driver string, duration time.Duration, err error) {
	if r == nil || r.metrics == nil {
		return
	}
	r.metrics.sandboxCreateDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(
		AttrDriver.String(driver),
		AttrOutcome.String(operationOutcome(err)),
	))
}

// RecordDriverOperation records one runtime-driver operation by outcome, which
// gives the driver error rate without per-run label cardinality.
func (r *Recorder) RecordDriverOperation(ctx context.Context, operation, driver string, err error) {
	if r == nil || r.metrics == nil {
		return
	}
	r.metrics.driverOperationCount.Add(ctx, 1, metric.WithAttributes(
		AttrOperation.String(operation),
		AttrDriver.String(driver),
		AttrOutcome.String(operationOutcome(err)),
	))
}

func operationOutcome(err error) string {
	if err != nil {
		return outcomeFailure
	}
	return outcomeSuccess
}

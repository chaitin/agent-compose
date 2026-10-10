package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func newMetricRecorder(t *testing.T) (*Recorder, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown meter provider: %v", err)
		}
	})
	recorder, err := NewRecorder(nil, provider)
	if err != nil {
		t.Fatalf("NewRecorder returned error: %v", err)
	}
	return recorder, reader
}

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var resourceMetrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &resourceMetrics); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	collected := map[string]metricdata.Metrics{}
	for _, scopeMetrics := range resourceMetrics.ScopeMetrics {
		if scopeMetrics.Scope.Name != scopeName {
			t.Fatalf("scope name = %q, want %q", scopeMetrics.Scope.Name, scopeName)
		}
		for _, metric := range scopeMetrics.Metrics {
			collected[metric.Name] = metric
		}
	}
	return collected
}

func TestRecorderRecordsRunMetrics(t *testing.T) {
	recorder, reader := newMetricRecorder(t)
	recorder.RecordRun(context.Background(), RunMeasurement{
		Duration: 2500 * time.Millisecond,
		Status:   "succeeded",
		Driver:   "docker",
	})
	recorder.RecordRun(context.Background(), RunMeasurement{
		Duration: 500 * time.Millisecond,
		Status:   "failed",
		Driver:   "docker",
	})

	metrics := collectMetrics(t, reader)
	duration, ok := metrics[MetricRunDuration].Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("run duration data = %T, want histogram", metrics[MetricRunDuration].Data)
	}
	if len(duration.DataPoints) != 2 {
		t.Fatalf("run duration data points = %d, want one per status", len(duration.DataPoints))
	}
	for _, point := range duration.DataPoints {
		status := attributeValue(point.Attributes, AttrRunStatus)
		if status == "succeeded" && point.Sum != 2.5 {
			t.Fatalf("succeeded run duration = %vs, want 2.5s", point.Sum)
		}
		if status != "succeeded" && status != "failed" {
			t.Fatalf("run duration status = %q", status)
		}
	}

	count, ok := metrics[MetricRunCount].Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("run count data = %T, want sum", metrics[MetricRunCount].Data)
	}
	if len(count.DataPoints) != 2 {
		t.Fatalf("run count data points = %d, want one per status", len(count.DataPoints))
	}
	for _, point := range count.DataPoints {
		if point.Value != 1 {
			t.Fatalf("run count value = %d, want 1 per status", point.Value)
		}
		if got := attributeValue(point.Attributes, AttrDriver); got != "docker" {
			t.Fatalf("run count driver = %q, want docker", got)
		}
	}
}

func TestRecorderRecordsSandboxAndDriverOutcomes(t *testing.T) {
	recorder, reader := newMetricRecorder(t)
	recorder.RecordSandboxCreate(context.Background(), "boxlite", 3*time.Second, nil)
	recorder.RecordDriverOperation(context.Background(), SpanSandboxExec, "boxlite", nil)
	recorder.RecordDriverOperation(context.Background(), SpanSandboxExec, "boxlite", errors.New("driver failure"))

	metrics := collectMetrics(t, reader)
	create, ok := metrics[MetricSandboxCreateDuration].Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("sandbox create data = %T, want histogram", metrics[MetricSandboxCreateDuration].Data)
	}
	if len(create.DataPoints) != 1 || create.DataPoints[0].Sum != 3 {
		t.Fatalf("sandbox create data points = %#v, want a single 3s observation", create.DataPoints)
	}
	if got := attributeValue(create.DataPoints[0].Attributes, AttrOutcome); got != outcomeSuccess {
		t.Fatalf("sandbox create outcome = %q, want %q", got, outcomeSuccess)
	}

	operations, ok := metrics[MetricDriverOperationCount].Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("driver operation data = %T, want sum", metrics[MetricDriverOperationCount].Data)
	}
	outcomes := map[string]int64{}
	for _, point := range operations.DataPoints {
		if got := attributeValue(point.Attributes, AttrOperation); got != SpanSandboxExec {
			t.Fatalf("driver operation = %q, want %q", got, SpanSandboxExec)
		}
		outcomes[attributeValue(point.Attributes, AttrOutcome)] += point.Value
	}
	if outcomes[outcomeSuccess] != 1 || outcomes[outcomeFailure] != 1 {
		t.Fatalf("driver operation outcomes = %v, want one success and one failure", outcomes)
	}
}

func attributeValue(attributes attribute.Set, key attribute.Key) string {
	value, ok := attributes.Value(key)
	if !ok {
		return ""
	}
	return value.AsString()
}

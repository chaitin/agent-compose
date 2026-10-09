package telemetry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// TestProviderExportsTracesAndMetricsToCollector is the end-to-end export
// contract: an OTLP/HTTP collector receives both signals from a provider built
// only from an endpoint, the exported span keeps the caller's trace ID, and the
// payload carries no credentials.
func TestProviderExportsTracesAndMetricsToCollector(t *testing.T) {
	type export struct {
		path string
		body []byte
	}
	exports := make(chan export, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		exports <- export{path: r.URL.Path, body: body}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	const secret = "Bearer secret-token"
	provider, err := NewProvider(server.URL, map[string]string{"authorization": secret}, "v9")
	if err != nil {
		t.Fatalf("NewProvider returned error: %v", err)
	}
	recorder, err := provider.Recorder()
	if err != nil {
		t.Fatalf("Recorder returned error: %v", err)
	}

	ctx := ContextWithRemoteTraceContext(context.Background(), testTraceparent, "")
	ctx, span := recorder.Start(ctx, SpanInvokeAgent, trace.WithAttributes(AttrRunID.String("run-1")))
	_, execSpan := recorder.Start(ctx, SpanSandboxExec, trace.WithAttributes(AttrSandboxID.String("sandbox-1")))
	EndSpan(execSpan, nil)
	EndSpan(span, nil)
	recorder.RecordRun(ctx, RunMeasurement{Duration: 1500 * time.Millisecond, Status: "succeeded", Driver: "docker"})
	recorder.RecordSandboxCreate(ctx, "docker", 250*time.Millisecond, nil)
	recorder.RecordDriverOperation(ctx, SpanSandboxExec, "docker", errors.New("driver failure"))
	recorder.RecordDriverOperation(ctx, SpanSandboxExec, "docker", nil)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := provider.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("provider shutdown returned error: %v", err)
	}

	var traceBody, metricBody []byte
	for traceBody == nil || metricBody == nil {
		select {
		case got := <-exports:
			switch got.path {
			case traceSignalPath:
				traceBody = got.body
			case metricSignalPath:
				metricBody = got.body
			}
		case <-shutdownCtx.Done():
			t.Fatalf("timed out waiting for both signals (traces=%t metrics=%t)", traceBody != nil, metricBody != nil)
		}
	}
	for _, body := range [][]byte{traceBody, metricBody} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatal("telemetry payload contains the exporter credential")
		}
	}

	assertExportedTrace(t, traceBody)
	assertExportedMetrics(t, metricBody)
}

func assertExportedTrace(t *testing.T, body []byte) {
	t.Helper()
	request := &coltracepb.ExportTraceServiceRequest{}
	if err := proto.Unmarshal(body, request); err != nil {
		t.Fatalf("decode trace export: %v", err)
	}
	if len(request.GetResourceSpans()) != 1 {
		t.Fatalf("resource spans = %d, want 1", len(request.GetResourceSpans()))
	}
	resourceSpans := request.GetResourceSpans()[0]
	if got := resourceAttribute(resourceSpans.GetResource().GetAttributes(), "service.name"); got != serviceName {
		t.Fatalf("service.name = %q, want %q", got, serviceName)
	}
	if got := resourceAttribute(resourceSpans.GetResource().GetAttributes(), "service.version"); got != "v9" {
		t.Fatalf("service.version = %q, want %q", got, "v9")
	}
	if len(resourceSpans.GetScopeSpans()) != 1 {
		t.Fatalf("scope spans = %#v, want one instrumentation scope", resourceSpans.GetScopeSpans())
	}
	if got := resourceSpans.GetScopeSpans()[0].GetScope().GetName(); got != scopeName {
		t.Fatalf("scope name = %q, want %q", got, scopeName)
	}
	exported := map[string]*tracepb.Span{}
	for _, span := range resourceSpans.GetScopeSpans()[0].GetSpans() {
		exported[span.GetName()] = span
	}
	runSpan, ok := exported[SpanInvokeAgent]
	if !ok {
		t.Fatalf("collector did not receive the %s span (got %v)", SpanInvokeAgent, exported)
	}
	execSpan, ok := exported[SpanSandboxExec]
	if !ok {
		t.Fatalf("collector did not receive the %s span (got %v)", SpanSandboxExec, exported)
	}
	wantTraceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatalf("parse expected trace id: %v", err)
	}
	wantParentID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatalf("parse expected parent span id: %v", err)
	}
	for name, span := range exported {
		if !bytes.Equal(span.GetTraceId(), wantTraceID[:]) {
			t.Fatalf("%s trace id = %x, want caller trace id %x", name, span.GetTraceId(), wantTraceID)
		}
	}
	if !bytes.Equal(runSpan.GetParentSpanId(), wantParentID[:]) {
		t.Fatalf("run parent span id = %x, want the caller span id %x", runSpan.GetParentSpanId(), wantParentID)
	}
	if !bytes.Equal(execSpan.GetParentSpanId(), runSpan.GetSpanId()) {
		t.Fatalf("exec parent span id = %x, want the run span %x", execSpan.GetParentSpanId(), runSpan.GetSpanId())
	}
	if got := spanAttribute(runSpan.GetAttributes(), AttrRunID); got != "run-1" {
		t.Fatalf("run id attribute = %q, want run-1", got)
	}
	if got := spanAttribute(execSpan.GetAttributes(), AttrSandboxID); got != "sandbox-1" {
		t.Fatalf("sandbox id attribute = %q, want sandbox-1", got)
	}
}

func assertExportedMetrics(t *testing.T, body []byte) {
	t.Helper()
	request := &colmetricpb.ExportMetricsServiceRequest{}
	if err := proto.Unmarshal(body, request); err != nil {
		t.Fatalf("decode metric export: %v", err)
	}
	metrics := map[string]metricDataPoints{}
	for _, resourceMetrics := range request.GetResourceMetrics() {
		if got := resourceAttribute(resourceMetrics.GetResource().GetAttributes(), "service.name"); got != serviceName {
			t.Fatalf("service.name = %q, want %q", got, serviceName)
		}
		for _, scopeMetrics := range resourceMetrics.GetScopeMetrics() {
			for _, metric := range scopeMetrics.GetMetrics() {
				metrics[metric.GetName()] = metricDataPointsFrom(metric)
			}
		}
	}
	for _, name := range []string{MetricRunDuration, MetricRunCount, MetricSandboxCreateDuration, MetricDriverOperationCount} {
		if _, ok := metrics[name]; !ok {
			t.Fatalf("metric %q was not exported (got %v)", name, metrics)
		}
	}
	for name, points := range metrics {
		for _, kv := range points.attributes {
			switch attribute.Key(kv.GetKey()) {
			case AttrRunID, AttrProjectID, AttrAgentName, AttrSandboxID:
				t.Fatalf("metric %s carries the identity attribute %s; identity belongs to traces", name, kv.GetKey())
			}
		}
	}
	runStatuses := metrics[MetricRunCount].attributeValues(AttrRunStatus)
	if len(runStatuses) != 1 || runStatuses[0] != "succeeded" {
		t.Fatalf("run statuses = %v, want [succeeded]", runStatuses)
	}
	outcomes := metrics[MetricDriverOperationCount].attributeValues(AttrOutcome)
	if len(outcomes) != 2 {
		t.Fatalf("driver operation outcomes = %v, want one data point per outcome", outcomes)
	}
}

type metricDataPoints struct {
	total      float64
	count      uint64
	attributes []*commonpb.KeyValue
}

func metricDataPointsFrom(metric *metricspb.Metric) metricDataPoints {
	points := metricDataPoints{}
	switch data := metric.GetData().(type) {
	case *metricspb.Metric_Sum:
		for _, point := range data.Sum.GetDataPoints() {
			points.total += float64(point.GetAsInt())
			points.count++
			points.attributes = append(points.attributes, point.GetAttributes()...)
		}
	case *metricspb.Metric_Histogram:
		for _, point := range data.Histogram.GetDataPoints() {
			points.total += point.GetSum()
			points.count += point.GetCount()
			points.attributes = append(points.attributes, point.GetAttributes()...)
		}
	}
	return points
}

func (p metricDataPoints) attributeValues(key attribute.Key) []string {
	values := []string{}
	for _, kv := range p.attributes {
		if kv.Key == string(key) {
			values = append(values, kv.Value.GetStringValue())
		}
	}
	return values
}

func resourceAttribute(attributes []*commonpb.KeyValue, name string) string {
	for _, kv := range attributes {
		if kv.GetKey() == name {
			return kv.GetValue().GetStringValue()
		}
	}
	return ""
}

func spanAttribute(attributes []*commonpb.KeyValue, key attribute.Key) string {
	return resourceAttribute(attributes, string(key))
}

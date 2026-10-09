package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/labstack/echo/v4"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/telemetry"
)

// newRecordingTracer returns a tracer backed by an in-memory exporter.
func newRecordingTracer(t *testing.T) (*telemetry.Tracer, *tracetest.SpanRecorder) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown tracer provider: %v", err)
		}
	})
	return telemetry.NewTracer(provider), recorder
}

func TestDaemonTraceContextMiddleware(t *testing.T) {
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tests := []struct {
		name    string
		headers map[string]string
		want    domain.TraceContext
	}{
		{
			name:    "no trace context",
			headers: map[string]string{"X-Custom": "val"},
		},
		{
			name:    "traceparent",
			headers: map[string]string{"Traceparent": traceparent},
			want:    domain.TraceContext{Traceparent: traceparent},
		},
		{
			name:    "traceparent and tracestate",
			headers: map[string]string{"traceparent": traceparent, "tracestate": "vendor=value"},
			want:    domain.TraceContext{Traceparent: traceparent, Tracestate: "vendor=value"},
		},
		{
			name:    "values are trimmed",
			headers: map[string]string{"traceparent": " " + traceparent + " ", "tracestate": " vendor=value "},
			want:    domain.TraceContext{Traceparent: traceparent, Tracestate: "vendor=value"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := echo.New()
			app.Use(newDaemonTraceContextMiddleware(nil))
			var got domain.TraceContext
			app.Any("/*", func(c echo.Context) error {
				got = domain.TraceContextFromContext(c.Request().Context())
				return c.NoContent(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			for k, v := range test.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("trace context = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestDaemonTraceContextMiddlewareStartsServerSpan(t *testing.T) {
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	wantTraceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("caller traceparent becomes the parent", func(t *testing.T) {
		tracer, recorder := newRecordingTracer(t)
		app := echo.New()
		app.Use(newDaemonTraceContextMiddleware(tracer))
		app.Any("/v1/run", func(c echo.Context) error {
			if got := trace.SpanContextFromContext(c.Request().Context()).TraceID(); got != wantTraceID {
				t.Fatalf("handler trace id = %s, want %s", got, wantTraceID)
			}
			return c.NoContent(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/run", nil)
		req.Header.Set("traceparent", traceparent)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)

		ended := recorder.Ended()
		if len(ended) != 1 {
			t.Fatalf("ended spans = %d, want 1", len(ended))
		}
		if ended[0].Name() != "POST /v1/run" || ended[0].SpanKind() != trace.SpanKindServer {
			t.Fatalf("span = %q kind %v, want server span POST /v1/run", ended[0].Name(), ended[0].SpanKind())
		}
		if ended[0].Parent().TraceID() != wantTraceID || !ended[0].Parent().IsRemote() {
			t.Fatalf("parent = %v, want remote parent in trace %s", ended[0].Parent(), wantTraceID)
		}
		if ended[0].SpanContext().TraceID() != wantTraceID {
			t.Fatalf("span trace id = %s, want caller trace %s", ended[0].SpanContext().TraceID(), wantTraceID)
		}
	})

	t.Run("absent traceparent starts a root span", func(t *testing.T) {
		tracer, recorder := newRecordingTracer(t)
		app := echo.New()
		app.Use(newDaemonTraceContextMiddleware(tracer))
		app.Any("/v1/run", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
		req := httptest.NewRequest(http.MethodGet, "/v1/run", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)

		ended := recorder.Ended()
		if len(ended) != 1 {
			t.Fatalf("ended spans = %d, want 1", len(ended))
		}
		if ended[0].Parent().IsValid() {
			t.Fatalf("parent = %v, want an invalid parent for a root span", ended[0].Parent())
		}
		if got := ended[0].Attributes(); len(got) == 0 {
			t.Fatal("server span has no attributes")
		}
	})

	t.Run("disabled tracer exports nothing", func(t *testing.T) {
		app := echo.New()
		app.Use(newDaemonTraceContextMiddleware(telemetry.NewTracer(nil)))
		app.Any("/v1/run", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
		req := httptest.NewRequest(http.MethodGet, "/v1/run", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})
}

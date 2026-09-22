package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/labstack/echo/v4"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

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
			app.Use(newDaemonTraceContextMiddleware())
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

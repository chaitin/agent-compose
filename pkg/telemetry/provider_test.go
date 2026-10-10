package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewProviderDisabledWithoutEndpoint(t *testing.T) {
	provider, err := NewProvider("", nil, "v1")
	if err != nil {
		t.Fatalf("NewProvider returned error: %v", err)
	}
	if provider.Enabled() {
		t.Fatal("provider without an endpoint is enabled")
	}
	recorder, err := provider.Recorder()
	if err != nil {
		t.Fatalf("Recorder returned error: %v", err)
	}
	if recorder == nil {
		t.Fatal("disabled provider returned a nil recorder")
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("disabled provider shutdown returned error: %v", err)
	}
}

func TestNewProviderEnabledWithEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider, err := NewProvider(server.URL+"/", map[string]string{"authorization": "Bearer token"}, "v1")
	if err != nil {
		t.Fatalf("NewProvider returned error: %v", err)
	}
	if !provider.Enabled() {
		t.Fatal("provider with an endpoint is disabled")
	}
	recorder, err := provider.Recorder()
	if err != nil {
		t.Fatalf("Recorder returned error: %v", err)
	}
	if recorder == nil {
		t.Fatal("enabled provider returned a nil recorder")
	}
	// Record one measurement so the metric reader has data to flush on shutdown.
	recorder.RecordRun(context.Background(), RunMeasurement{Duration: time.Second, Status: "succeeded", Driver: "docker"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := provider.Shutdown(ctx); err != nil {
		t.Fatalf("provider shutdown returned error: %v", err)
	}
}

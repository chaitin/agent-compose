package telemetry

import (
	"context"
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
	if provider.Tracer() == nil {
		t.Fatal("disabled provider returned a nil tracer")
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("disabled provider shutdown returned error: %v", err)
	}
}

func TestNewProviderEnabledWithEndpoint(t *testing.T) {
	provider, err := NewProvider("http://127.0.0.1:4318/", map[string]string{"authorization": "Bearer%20token"}, "v1")
	if err != nil {
		t.Fatalf("NewProvider returned error: %v", err)
	}
	if !provider.Enabled() {
		t.Fatal("provider with an endpoint is disabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// No span is exported, so shutdown must not require a reachable collector.
	if err := provider.Shutdown(ctx); err != nil {
		t.Fatalf("provider shutdown returned error: %v", err)
	}
}

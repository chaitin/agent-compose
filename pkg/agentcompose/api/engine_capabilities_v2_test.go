package api

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/capmatrix"
	"github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/llms"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func testEngineSnapshot(t *testing.T) capmatrix.Snapshot {
	t.Helper()
	facts, err := driver.CompiledRuntimeCapabilities()
	if err != nil {
		t.Fatalf("CompiledRuntimeCapabilities() error = %v", err)
	}
	snapshot, err := capmatrix.BuildSnapshot(facts, time.Unix(1700000000, 0).UTC())
	if err != nil {
		t.Fatalf("BuildSnapshot() error = %v", err)
	}
	return snapshot
}

func TestEngineCapabilitiesHandlerReportsFrozenMatrix(t *testing.T) {
	handler := NewEngineCapabilitiesV2Handler(testEngineSnapshot(t))
	response, err := handler.GetCapabilities(context.Background(), connect.NewRequest(&agentcomposev2.GetEngineCapabilitiesRequest{}))
	if err != nil {
		t.Fatalf("GetCapabilities() error = %v", err)
	}

	message := response.Msg
	if len(message.GetDrivers()) == 0 {
		t.Fatal("response reports no drivers")
	}
	if len(message.GetCompiledDrivers()) != len(message.GetDrivers()) {
		t.Fatalf("compiled_drivers = %v, drivers = %d, want one entry per compiled driver", message.GetCompiledDrivers(), len(message.GetDrivers()))
	}
	if message.GetCompiledDriversNote() == "" {
		t.Fatal("compiled_drivers_note is empty; the compiled_drivers distinction must be explicit")
	}
	if message.GetCapturedAt() == nil {
		t.Fatal("captured_at is missing; the response must expose the snapshot time")
	}

	required := make(map[string]struct{})
	for _, dimension := range capmatrix.RequiredDimensions() {
		required[string(dimension)] = struct{}{}
	}
	for _, driverCapabilities := range message.GetDrivers() {
		if len(driverCapabilities.GetCapabilities()) != len(required) {
			t.Fatalf("driver %q reports %d capabilities, want %d", driverCapabilities.GetDriver(), len(driverCapabilities.GetCapabilities()), len(required))
		}
		for _, capability := range driverCapabilities.GetCapabilities() {
			if _, ok := required[capability.GetDimension()]; !ok {
				t.Fatalf("driver %q reports unknown dimension %q", driverCapabilities.GetDriver(), capability.GetDimension())
			}
			if capability.GetEnforced() && capability.GetMechanism() == "" {
				t.Fatalf("driver %q dimension %q is enforced without a mechanism", driverCapabilities.GetDriver(), capability.GetDimension())
			}
			if capability.GetMechanism() == "" {
				t.Fatalf("driver %q dimension %q has an empty mechanism", driverCapabilities.GetDriver(), capability.GetDimension())
			}
			if capability.GetDefaultBehavior() == "" {
				t.Fatalf("driver %q dimension %q does not expose its default behavior", driverCapabilities.GetDriver(), capability.GetDimension())
			}
			if capability.GetObserved() == "" {
				t.Fatalf("driver %q dimension %q does not expose observed evidence", driverCapabilities.GetDriver(), capability.GetDimension())
			}
		}
	}
}

func TestEngineCapabilitiesHandlerReportsProviderMatrix(t *testing.T) {
	handler := NewEngineCapabilitiesV2Handler(testEngineSnapshot(t))
	response, err := handler.GetCapabilities(context.Background(), connect.NewRequest(&agentcomposev2.GetEngineCapabilitiesRequest{}))
	if err != nil {
		t.Fatalf("GetCapabilities() error = %v", err)
	}

	providers := map[string]*agentcomposev2.EngineProviderCapabilities{}
	for _, provider := range response.Msg.GetProviders() {
		providers[provider.GetProvider()] = provider
	}
	for _, name := range []string{"codex", "claude", "opencode", "pi", "dsh"} {
		provider, ok := providers[name]
		if !ok {
			t.Fatalf("provider %q is missing from the response", name)
		}
		dialect, err := llms.DialectFor(name)
		if err != nil {
			t.Fatalf("DialectFor(%q) error = %v", name, err)
		}
		preference := dialect.PreferredProtocols()
		if len(provider.GetPreferredProtocols()) != len(preference) {
			t.Fatalf("provider %q preferred protocols = %v, want %v", name, provider.GetPreferredProtocols(), preference)
		}
		for index, protocol := range preference {
			if provider.GetPreferredProtocols()[index] != string(protocol) {
				t.Fatalf("provider %q protocol order = %v, want %v", name, provider.GetPreferredProtocols(), preference)
			}
		}
		if len(provider.GetFeatures()) != len(capmatrix.RequiredExecutionFeatures()) {
			t.Fatalf("provider %q reports %d features, want %d", name, len(provider.GetFeatures()), len(capmatrix.RequiredExecutionFeatures()))
		}
	}
}

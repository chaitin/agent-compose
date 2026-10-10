package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/capmatrix"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// TestEngineCapabilitiesHandlerReportsExplicitStateAndSource pins the
// three-state contract: every per-driver claim carries a state and a source,
// and the backward-compatible enforced boolean agrees with the state.
func TestEngineCapabilitiesHandlerReportsExplicitStateAndSource(t *testing.T) {
	handler := NewEngineCapabilitiesV2Handler(testEngineSnapshot(t))
	response, err := handler.GetCapabilities(context.Background(), connect.NewRequest(&agentcomposev2.GetEngineCapabilitiesRequest{}))
	if err != nil {
		t.Fatalf("GetCapabilities() error = %v", err)
	}

	validStates := map[string]bool{
		string(capmatrix.StateEnforced):    true,
		string(capmatrix.StateDegraded):    true,
		string(capmatrix.StateUnsupported): true,
	}
	for _, driverCapabilities := range response.Msg.GetDrivers() {
		for _, capability := range driverCapabilities.GetCapabilities() {
			if !validStates[capability.GetState()] {
				t.Fatalf("driver %q dimension %q state = %q, want an explicit three-state value", driverCapabilities.GetDriver(), capability.GetDimension(), capability.GetState())
			}
			// A per-driver entry is a static declaration; the wire must never
			// claim it was measured.
			if capability.GetSource() != string(capmatrix.SourceDeclared) {
				t.Fatalf("driver %q dimension %q source = %q, want %q", driverCapabilities.GetDriver(), capability.GetDimension(), capability.GetSource(), capmatrix.SourceDeclared)
			}
			if capability.GetEnforced() != (capability.GetState() == string(capmatrix.StateEnforced)) {
				t.Fatalf("driver %q dimension %q enforced=%v disagrees with state=%q", driverCapabilities.GetDriver(), capability.GetDimension(), capability.GetEnforced(), capability.GetState())
			}
		}
	}
}

// TestEngineCapabilitiesHandlerReportsMeasuredSystemLayer pins the measured
// layer: the host probe is serialized with source=measured and never claims to
// be a declaration.
func TestEngineCapabilitiesHandlerReportsMeasuredSystemLayer(t *testing.T) {
	handler := NewEngineCapabilitiesV2Handler(testEngineSnapshot(t))
	response, err := handler.GetCapabilities(context.Background(), connect.NewRequest(&agentcomposev2.GetEngineCapabilitiesRequest{}))
	if err != nil {
		t.Fatalf("GetCapabilities() error = %v", err)
	}

	observations := response.Msg.GetObservations()
	if len(observations) != len(capmatrix.SystemDimensions()) {
		t.Fatalf("observations = %d, want one per system dimension (%d)", len(observations), len(capmatrix.SystemDimensions()))
	}
	seen := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		if observation.GetSource() != string(capmatrix.SourceMeasured) {
			t.Fatalf("observation %q source = %q, want %q", observation.GetDimension(), observation.GetSource(), capmatrix.SourceMeasured)
		}
		if observation.GetState() == "" || observation.GetMechanism() == "" || observation.GetObserved() == "" {
			t.Fatalf("observation = %+v, want state, mechanism, and probe evidence", observation)
		}
		if observation.GetEnforced() != (observation.GetState() == string(capmatrix.StateEnforced)) {
			t.Fatalf("observation %q enforced=%v disagrees with state=%q", observation.GetDimension(), observation.GetEnforced(), observation.GetState())
		}
		if !observation.GetEnforced() && observation.GetMissing() == "" {
			t.Fatalf("observation %q is not enforced but names no missing precondition", observation.GetDimension())
		}
		seen[observation.GetDimension()] = struct{}{}
	}
	for _, dimension := range capmatrix.SystemDimensions() {
		if _, ok := seen[string(dimension)]; !ok {
			t.Fatalf("system dimension %q is missing from the response", dimension)
		}
	}
}

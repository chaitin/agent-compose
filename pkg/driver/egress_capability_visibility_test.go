package driver

import (
	"strings"
	"testing"
)

// TestEgressCapabilityReportsEngineAutoAllow pins the visibility requirement
// for the engine's auto-allowed endpoints. A consumer of GetCapabilities must
// be able to read that default-deny carries an engine-side exemption, so the
// exemption is reported rather than hidden.
func TestEgressCapabilityReportsEngineAutoAllow(t *testing.T) {
	facts, err := CompiledRuntimeCapabilities()
	if err != nil {
		t.Fatalf("CompiledRuntimeCapabilities returned error: %v", err)
	}
	if len(facts) == 0 {
		t.Fatal("no compiled runtime capabilities to check")
	}
	for _, driverFacts := range facts {
		dimension := capabilityDimensionForTest(t, driverFacts, dimensionEgressPolicy)
		if !strings.Contains(dimension.DefaultBehavior, "auto-allows") {
			t.Fatalf("driver %q egress_policy default_behavior = %q, want it to name the engine-owned auto-allow", driverFacts.Driver, dimension.DefaultBehavior)
		}
		if !strings.Contains(dimension.DefaultBehavior, "undeclared network policy is not deny") {
			t.Fatalf("driver %q egress_policy default_behavior = %q, want it to state the D3 undeclared behavior", driverFacts.Driver, dimension.DefaultBehavior)
		}
	}
}

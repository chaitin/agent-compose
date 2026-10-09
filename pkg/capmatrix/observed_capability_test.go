package capmatrix

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/driver"
)

func TestProbeSystemCapabilitiesCoversEverySystemDimensionAndIsMeasured(t *testing.T) {
	observations, err := ProbeSystemCapabilities()
	if err != nil {
		t.Fatalf("ProbeSystemCapabilities() error = %v", err)
	}
	got := make(map[ObservedDimension]ObservedCapability, len(observations))
	for _, capability := range observations {
		if err := capability.Validate(); err != nil {
			t.Fatalf("system dimension %q is invalid: %v", capability.Dimension, err)
		}
		if capability.Source != SourceMeasured {
			t.Fatalf("system dimension %q source = %q, want %q", capability.Dimension, capability.Source, SourceMeasured)
		}
		if capability.Observed == "" {
			t.Fatalf("system dimension %q has no probe evidence", capability.Dimension)
		}
		got[capability.Dimension] = capability
	}
	for _, dimension := range SystemDimensions() {
		if _, ok := got[dimension]; !ok {
			t.Fatalf("the host probe did not report system dimension %q", dimension)
		}
	}
	if len(got) != len(SystemDimensions()) {
		t.Fatalf("host probe reported %d dimensions, want %d", len(got), len(SystemDimensions()))
	}
}

// TestSnapshotFreezesTheSystemProbeWithoutReProbing is the frozen-snapshot
// acceptance test: a query reads the captured measurement, and a caller cannot
// mutate it or trigger another probe.
func TestSnapshotFreezesTheSystemProbeWithoutReProbing(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing-kubeconfig"))

	snapshot := testSnapshot(t)
	first := snapshot.Observations()
	if len(first) == 0 {
		t.Fatal("snapshot has no measured system observations")
	}
	second := snapshot.Observations()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("observations changed between queries: %+v vs %+v", first, second)
	}

	first[0].Observed = "mutated"
	first[0].Preconditions = append(first[0].Preconditions, "mutated")
	if after := snapshot.Observations(); !reflect.DeepEqual(after, second) {
		t.Fatalf("snapshot changed after caller mutation: %+v vs %+v", after, second)
	}
}

func TestNewSnapshotWithObservationsRejectsInvalidMeasurement(t *testing.T) {
	declaration := testDriverCapabilities(t, "docker", nil)
	invalid := []ObservedCapability{{
		Dimension: ObservedSeccomp,
		State:     StateUnsupported,
		Mechanism: ReasonUnsupported,
		Observed:  "evidence",
		// Missing is required when the dimension is not enforced.
		Source: SourceMeasured,
	}}
	if _, err := NewSnapshotWithObservations([]DriverCapabilities{declaration}, nil, []string{"docker"}, invalid, time.Now()); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("NewSnapshotWithObservations() error = %v, want ErrInvalidCapability", err)
	}
}

// TestSimulatedCapabilityNeverSharesTheEnforcedState covers the simulation
// boundary: an emulated mechanism is always labelled and never reported as
// real enforcement.
func TestSimulatedCapabilityNeverSharesTheEnforcedState(t *testing.T) {
	simulated, err := SimulatedCapability(ObservedProcessSeccomp, "emulated_seccomp", "the engine simulated a seccomp filter")
	if err != nil {
		t.Fatalf("SimulatedCapability() error = %v", err)
	}
	if simulated.Enforced || simulated.State == StateEnforced {
		t.Fatalf("simulated capability = %+v, want a non-enforced state", simulated)
	}
	if simulated.Source != SourceSimulated {
		t.Fatalf("source = %q, want %q", simulated.Source, SourceSimulated)
	}

	// A simulated observation that claims enforcement is rejected outright.
	forged := simulated
	forged.Enforced = true
	forged.State = StateEnforced
	forged.Missing = ""
	forged.Mechanism = "emulated_seccomp"
	if err := forged.Validate(); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("simulated enforced Validate() error = %v, want ErrInvalidCapability", err)
	}
}

func TestCapabilityValidateAcceptsDegradedAndRejectsInconsistentStates(t *testing.T) {
	degraded := Capability{
		Dimension:       DimensionEgressPolicy,
		State:           StateDegraded,
		Mechanism:       "best_effort_network_policy",
		Preconditions:   []string{"the CNI must accept a NetworkPolicy"},
		Observed:        "the driver constructs an allow-all policy",
		DefaultBehavior: "outbound access is unrestricted",
	}
	if err := degraded.Validate(); err != nil {
		t.Fatalf("degraded Validate() error = %v", err)
	}
	if degraded.Normalized().Source != SourceDeclared {
		t.Fatalf("Normalized().Source = %q, want %q", degraded.Normalized().Source, SourceDeclared)
	}

	withoutPrecondition := degraded
	withoutPrecondition.Preconditions = nil
	if err := withoutPrecondition.Validate(); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("degraded without precondition error = %v, want ErrInvalidCapability", err)
	}

	enforcedFlagWithUnsupportedState := Capability{
		Dimension:       DimensionEgressPolicy,
		Enforced:        true,
		State:           StateUnsupported,
		Mechanism:       ReasonNotConfigured,
		Observed:        "evidence",
		DefaultBehavior: "default",
	}
	if err := enforcedFlagWithUnsupportedState.Validate(); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("inconsistent enforced/state error = %v, want ErrInvalidCapability", err)
	}
}

func TestCapabilityNormalizedFillsStateAndSource(t *testing.T) {
	enforced := Capability{
		Dimension:       DimensionResourceLimits,
		Enforced:        true,
		Mechanism:       "sdk_sandbox_options",
		Observed:        "evidence",
		DefaultBehavior: "default",
	}
	normalized := enforced.Normalized()
	if normalized.State != StateEnforced || normalized.Source != SourceDeclared {
		t.Fatalf("Normalized() = %+v, want enforced/declared", normalized)
	}

	notConfigured := Capability{
		Dimension:       DimensionEgressPolicy,
		Mechanism:       ReasonNotConfigured,
		Observed:        "evidence",
		DefaultBehavior: "default",
	}
	normalized = notConfigured.Normalized()
	if normalized.State != StateUnsupported || normalized.Source != SourceDeclared {
		t.Fatalf("Normalized() = %+v, want unsupported/declared", normalized)
	}
}

// TestDriverDeclarationsNeverClaimToBeMeasured keeps the "nothing
// self-attests" rule structural: a driver declaration is declared evidence.
func TestDriverDeclarationsNeverClaimToBeMeasured(t *testing.T) {
	facts, err := driver.CompiledRuntimeCapabilities()
	if err != nil {
		t.Fatalf("CompiledRuntimeCapabilities() error = %v", err)
	}
	for _, declaration := range facts {
		for _, dimension := range declaration.Dimensions {
			if dimension.Source != "" && dimension.Source != string(SourceDeclared) {
				t.Fatalf("driver %q dimension %q claims source %q; a driver declaration is declared evidence", declaration.Driver, dimension.Dimension, dimension.Source)
			}
		}
	}
}

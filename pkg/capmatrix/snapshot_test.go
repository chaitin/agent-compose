package capmatrix

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/driver"
)

func testSnapshot(t *testing.T) Snapshot {
	t.Helper()
	facts, err := driver.CompiledRuntimeCapabilities()
	if err != nil {
		t.Fatalf("CompiledRuntimeCapabilities() error = %v", err)
	}
	snapshot, err := BuildSnapshot(facts, time.Unix(1700000000, 0).UTC())
	if err != nil {
		t.Fatalf("BuildSnapshot() error = %v", err)
	}
	return snapshot
}

// TestBuildSnapshotFreezesEveryCompiledDriverWithDefaults asserts the frozen
// snapshot covers every compiled driver, every required dimension, and an
// explicit default behavior per dimension.
func TestBuildSnapshotFreezesEveryCompiledDriverWithDefaults(t *testing.T) {
	snapshot := testSnapshot(t)
	compiled := driver.CompiledRuntimeDrivers()
	if got := snapshot.CompiledDrivers(); !reflect.DeepEqual(got, compiled) {
		t.Fatalf("CompiledDrivers() = %v, want %v", got, compiled)
	}
	if got := len(snapshot.Drivers()); got != len(compiled) {
		t.Fatalf("snapshot has %d drivers, want %d (one per compiled driver)", got, len(compiled))
	}
	if !snapshot.CapturedAt().Equal(time.Unix(1700000000, 0).UTC()) {
		t.Fatalf("CapturedAt() = %v, want the injected capture time", snapshot.CapturedAt())
	}
	for _, declaration := range snapshot.Drivers() {
		if err := declaration.Validate(); err != nil {
			t.Fatalf("driver %q: %v", declaration.Driver, err)
		}
		for _, capability := range declaration.Capabilities {
			if capability.Observed == "" {
				t.Errorf("driver %q dimension %q has no observed evidence", declaration.Driver, capability.Dimension)
			}
			if capability.DefaultBehavior == "" {
				t.Errorf("driver %q dimension %q does not expose its default behavior", declaration.Driver, capability.Dimension)
			}
		}
	}
}

// TestNewSnapshotRejectsInvalidEnforcementClaim is the invalid-state
// acceptance test: enforced = true with an empty mechanism never becomes a
// queryable snapshot.
func TestNewSnapshotRejectsInvalidEnforcementClaim(t *testing.T) {
	declaration := DriverCapabilities{Driver: "docker"}
	for _, dimension := range RequiredDimensions() {
		capability := Capability{Dimension: dimension, Mechanism: ReasonNotConfigured, Observed: "evidence", DefaultBehavior: "default"}
		if dimension == DimensionResourceLimits {
			capability.Enforced = true
			capability.Mechanism = ""
		}
		declaration.Capabilities = append(declaration.Capabilities, capability)
	}
	if _, err := NewSnapshot([]DriverCapabilities{declaration}, nil, []string{"docker"}, time.Now()); err == nil {
		t.Fatal("NewSnapshot() error = nil, want the invalid enforcement claim rejected")
	}

	// The same claim is also invalid when validated on its own.
	invalid := declaration.Capabilities[0]
	if err := invalid.Validate(); err == nil {
		t.Fatal("Capability.Validate() error = nil, want the invalid enforcement claim rejected")
	}
}

func TestNewSnapshotRejectsUncompiledDriverAndInvalidProvider(t *testing.T) {
	declaration := DriverCapabilities{Driver: "docker"}
	for _, dimension := range RequiredDimensions() {
		declaration.Capabilities = append(declaration.Capabilities, Capability{Dimension: dimension, Mechanism: ReasonNotConfigured, Observed: "evidence", DefaultBehavior: "default"})
	}
	if _, err := NewSnapshot([]DriverCapabilities{declaration}, nil, []string{"k8s"}, time.Now()); err == nil {
		t.Fatal("NewSnapshot() error = nil, want a driver that is not compiled rejected")
	}
	invalidProvider := ProviderCapabilities{Provider: "codex", PreferredProtocols: nil}
	if _, err := NewSnapshot([]DriverCapabilities{declaration}, []ProviderCapabilities{invalidProvider}, []string{"docker"}, time.Now()); err == nil {
		t.Fatal("NewSnapshot() error = nil, want a provider without a preferred protocol rejected")
	}
}

// TestSnapshotAccessorsDoNotExposeMutableState proves a caller cannot mutate
// the frozen snapshot.
func TestSnapshotAccessorsDoNotExposeMutableState(t *testing.T) {
	snapshot := testSnapshot(t)
	before := snapshot.Drivers()

	drivers := snapshot.Drivers()
	drivers[0].Driver = "mutated"
	drivers[0].Capabilities[0].Mechanism = "mutated"
	drivers[0].Capabilities[0].Preconditions = append(drivers[0].Capabilities[0].Preconditions, "mutated")

	after := snapshot.Drivers()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("snapshot changed after caller mutation: before %+v after %+v", before, after)
	}
}

// TestSnapshotDoesNotProbeUnavailableDrivers is the no-probe acceptance test:
// every runtime endpoint is deliberately unreachable, yet the snapshot builds
// and the query path returns stable values.
func TestSnapshotDoesNotProbeUnavailableDrivers(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing-kubeconfig"))
	t.Setenv("RUNTIME_DRIVER", "boxlite")

	first := testSnapshot(t)
	second := testSnapshot(t)
	if !reflect.DeepEqual(first.Drivers(), second.Drivers()) {
		t.Fatal("driver declarations changed between two queries")
	}
	if !reflect.DeepEqual(first.Providers(), second.Providers()) {
		t.Fatal("provider declarations changed between two queries")
	}
	if len(first.Drivers()) == 0 {
		t.Fatal("snapshot is empty; the compiled docker driver must always be reported")
	}
}

// TestDriverFactsMatchCapmatrixContract is the drift guard between the
// driver-local dimension strings and this package's canonical dimensions.
func TestDriverFactsMatchCapmatrixContract(t *testing.T) {
	want := make(map[Dimension]struct{}, len(RequiredDimensions()))
	for _, dimension := range RequiredDimensions() {
		want[dimension] = struct{}{}
	}
	for _, name := range []string{driver.RuntimeDriverDocker, driver.RuntimeDriverK8s, driver.RuntimeDriverBoxlite, driver.RuntimeDriverMicrosandbox} {
		facts, err := driver.RuntimeCapabilityFactsFor(name)
		if err != nil {
			t.Fatalf("RuntimeCapabilityFactsFor(%q) error = %v", name, err)
		}
		declaration := driverCapabilitiesFromFacts(facts)
		if err := declaration.Validate(); err != nil {
			t.Fatalf("driver %q declaration does not satisfy the contract: %v", name, err)
		}
		got := make(map[Dimension]struct{}, len(declaration.Capabilities))
		for _, capability := range declaration.Capabilities {
			got[capability.Dimension] = struct{}{}
		}
		if len(got) != len(want) {
			t.Fatalf("driver %q declares dimensions %v, want exactly %v", name, got, want)
		}
	}
}

package capmatrix

import (
	"fmt"
	"time"

	"github.com/chaitin/agent-compose/pkg/driver"
)

// CompiledDriversSemantics is the explicit statement of what compiled_drivers
// means, returned next to the capability snapshot so a consumer never has to
// infer the difference from the field name.
const CompiledDriversSemantics = "compiled_drivers reports what was compiled into this binary; it does not check runtime reachability, KVM access, or whether a driver can start. The capability snapshot reports what the engine enforces and by what mechanism. The two are complementary, not substitutes."

// Snapshot is the frozen engine capability matrix. It is immutable: every
// accessor returns a copy, so a caller cannot mutate the captured facts. The
// snapshot is built once at startup and never re-probed.
type Snapshot struct {
	drivers         []DriverCapabilities
	providers       []ProviderCapabilities
	observations    []ObservedCapability
	compiledDrivers []string
	capturedAt      time.Time
}

// BuildSnapshot converts the driver package's static facts into the contract
// model, derives the provider matrix from pkg/llms, probes the host for the
// measured system layer, and freezes all of it. It returns an error instead of
// a snapshot containing an invalid enforcement claim.
//
// The host probe runs here, once, at composition time. Every query path reads
// the frozen result, so a GetCapabilities call never triggers a live probe.
func BuildSnapshot(driverFacts []driver.RuntimeCapabilityFacts, capturedAt time.Time) (Snapshot, error) {
	drivers := make([]DriverCapabilities, 0, len(driverFacts))
	for _, facts := range driverFacts {
		drivers = append(drivers, driverCapabilitiesFromFacts(facts))
	}
	providers, err := DeclaredProviders()
	if err != nil {
		return Snapshot{}, err
	}
	observations, err := ProbeSystemCapabilities()
	if err != nil {
		return Snapshot{}, err
	}
	return newSnapshot(drivers, providers, driver.CompiledRuntimeDrivers(), observations, capturedAt)
}

// NewSnapshot validates and freezes an already-modeled capability matrix
// without a host measurement. It is the seam the invalid-state test uses.
func NewSnapshot(drivers []DriverCapabilities, providers []ProviderCapabilities, compiledDrivers []string, capturedAt time.Time) (Snapshot, error) {
	return newSnapshot(drivers, providers, compiledDrivers, nil, capturedAt)
}

// NewSnapshotWithObservations freezes a matrix together with explicit
// engine-measured observations. It is the seam a test uses to inject a host
// measurement without running the real probe.
func NewSnapshotWithObservations(drivers []DriverCapabilities, providers []ProviderCapabilities, compiledDrivers []string, observations []ObservedCapability, capturedAt time.Time) (Snapshot, error) {
	return newSnapshot(drivers, providers, compiledDrivers, observations, capturedAt)
}

func newSnapshot(drivers []DriverCapabilities, providers []ProviderCapabilities, compiledDrivers []string, observations []ObservedCapability, capturedAt time.Time) (Snapshot, error) {
	compiled := make(map[string]struct{}, len(compiledDrivers))
	for _, name := range compiledDrivers {
		compiled[name] = struct{}{}
	}
	seenDrivers := make(map[string]struct{}, len(drivers))
	for _, declaration := range drivers {
		if err := declaration.Validate(); err != nil {
			return Snapshot{}, err
		}
		if _, duplicate := seenDrivers[declaration.Driver]; duplicate {
			return Snapshot{}, fmt.Errorf("%w: driver %q is declared more than once", ErrInvalidCapability, declaration.Driver)
		}
		seenDrivers[declaration.Driver] = struct{}{}
		if _, ok := compiled[declaration.Driver]; !ok {
			return Snapshot{}, fmt.Errorf("%w: driver %q is declared but not compiled into this binary", ErrInvalidCapability, declaration.Driver)
		}
	}
	for _, provider := range providers {
		if err := provider.Validate(); err != nil {
			return Snapshot{}, err
		}
	}
	normalizedObservations, err := normalizeObservedCapabilities(observations)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		drivers:         cloneDrivers(drivers),
		providers:       cloneProviders(providers),
		observations:    normalizedObservations,
		compiledDrivers: append([]string(nil), compiledDrivers...),
		capturedAt:      capturedAt,
	}, nil
}

// Validate re-checks every declaration in the snapshot. NewSnapshot already
// rejects an invalid matrix, so this exists for the transport boundary to
// assert the invariant before serializing it.
func (s Snapshot) Validate() error {
	for _, declaration := range s.drivers {
		if err := declaration.Validate(); err != nil {
			return err
		}
	}
	for _, declaration := range s.providers {
		if err := declaration.Validate(); err != nil {
			return err
		}
	}
	for _, observation := range s.observations {
		if err := observation.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Drivers returns the frozen per-driver declarations.
func (s Snapshot) Drivers() []DriverCapabilities {
	return cloneDrivers(s.drivers)
}

// Observations returns the frozen engine-measured layer: the host system probe
// captured at startup plus any measured facts frozen with the snapshot. These
// are measured claims, never driver declarations.
func (s Snapshot) Observations() []ObservedCapability {
	return cloneObservedCapabilities(s.observations)
}

// Providers returns the frozen per-provider declarations.
func (s Snapshot) Providers() []ProviderCapabilities {
	return cloneProviders(s.providers)
}

// CompiledDrivers returns the build-time driver list captured with the
// snapshot. It matches compiled_drivers from /api/version.
func (s Snapshot) CompiledDrivers() []string {
	return append([]string(nil), s.compiledDrivers...)
}

// CapturedAt returns when the snapshot was taken.
func (s Snapshot) CapturedAt() time.Time {
	return s.capturedAt
}

// Driver returns one driver's frozen declaration.
func (s Snapshot) Driver(name string) (DriverCapabilities, bool) {
	for _, declaration := range s.drivers {
		if declaration.Driver == name {
			return cloneDriver(declaration), true
		}
	}
	return DriverCapabilities{}, false
}

// Provider returns one provider's frozen declaration.
func (s Snapshot) Provider(name string) (ProviderCapabilities, bool) {
	for _, declaration := range s.providers {
		if declaration.Provider == name {
			return cloneProvider(declaration), true
		}
	}
	return ProviderCapabilities{}, false
}

func driverCapabilitiesFromFacts(facts driver.RuntimeCapabilityFacts) DriverCapabilities {
	declaration := DriverCapabilities{Driver: facts.Driver}
	for _, dimension := range facts.Dimensions {
		declaration.Capabilities = append(declaration.Capabilities, Capability{
			Dimension:       Dimension(dimension.Dimension),
			Enforced:        dimension.Enforced,
			Mechanism:       dimension.Mechanism,
			Preconditions:   append([]string(nil), dimension.Preconditions...),
			Observed:        dimension.Observed,
			DefaultBehavior: dimension.DefaultBehavior,
			State:           CapabilityState(dimension.State),
			Source:          CapabilitySource(dimension.Source),
		}.Normalized())
	}
	return declaration
}

func cloneDrivers(drivers []DriverCapabilities) []DriverCapabilities {
	out := make([]DriverCapabilities, 0, len(drivers))
	for _, declaration := range drivers {
		out = append(out, cloneDriver(declaration))
	}
	return out
}

func cloneDriver(declaration DriverCapabilities) DriverCapabilities {
	clone := DriverCapabilities{Driver: declaration.Driver, Capabilities: make([]Capability, 0, len(declaration.Capabilities))}
	for _, capability := range declaration.Capabilities {
		capability.Preconditions = append([]string(nil), capability.Preconditions...)
		clone.Capabilities = append(clone.Capabilities, capability)
	}
	return clone
}

func cloneProviders(providers []ProviderCapabilities) []ProviderCapabilities {
	out := make([]ProviderCapabilities, 0, len(providers))
	for _, declaration := range providers {
		out = append(out, cloneProvider(declaration))
	}
	return out
}

func cloneProvider(declaration ProviderCapabilities) ProviderCapabilities {
	return ProviderCapabilities{
		Provider:           declaration.Provider,
		PreferredProtocols: append([]string(nil), declaration.PreferredProtocols...),
		Features:           append([]ProviderFeature(nil), declaration.Features...),
	}
}

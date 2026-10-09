package driver

import (
	"fmt"
	"strings"
)

// RuntimeCapabilityFacts is one runtime driver's static declaration of what it
// actually enforces. It is the driver-side half of the engine capability
// matrix; pkg/capmatrix converts these facts into the queryable contract and
// validates them.
//
// The facts are deliberately a driver-local shape. pkg/llms imports pkg/driver,
// so pkg/driver cannot import pkg/capmatrix without creating an import cycle.
type RuntimeCapabilityFacts struct {
	// Driver is the normalized runtime driver name.
	Driver string
	// Dimensions covers every dimension pkg/capmatrix requires, exactly once.
	Dimensions []RuntimeCapabilityDimensionFacts
}

// RuntimeCapabilityDimensionFacts is one dimension's raw enforcement claim.
type RuntimeCapabilityDimensionFacts struct {
	Dimension       string
	Enforced        bool
	Mechanism       string
	Preconditions   []string
	Observed        string
	DefaultBehavior string
	// State is the three-state answer ("enforced", "degraded", "unsupported").
	// Empty means the reader derives it from Enforced, which keeps an API-7
	// declaration valid unchanged.
	State string
	// Source is "declared", "measured", or "simulated". A driver declaration
	// itself is declared evidence: it states what the driver writes into its
	// runtime configuration, never what it observed. Empty defaults to
	// declared.
	Source string
}

// RuntimeCapabilityFactsFor returns the capability declaration for one runtime
// driver. It is pure: it never contacts a Docker daemon, a KVM host, or a
// Kubernetes cluster, so calling it for a driver that is not compiled or whose
// runtime is unreachable still succeeds.
func RuntimeCapabilityFactsFor(driver string) (RuntimeCapabilityFacts, error) {
	switch resolveRuntimeDriver(driver) {
	case RuntimeDriverDocker:
		return dockerRuntimeCapabilityFacts(), nil
	case RuntimeDriverK8s:
		return k8sRuntimeCapabilityFacts(), nil
	case RuntimeDriverBoxlite:
		return boxliteRuntimeCapabilityFacts(), nil
	case RuntimeDriverMicrosandbox:
		return microsandboxRuntimeCapabilityFacts(), nil
	default:
		return RuntimeCapabilityFacts{}, fmt.Errorf("unsupported agent-compose runtime driver %q", strings.TrimSpace(driver))
	}
}

// CompiledRuntimeCapabilities returns the capability declaration for every
// runtime driver compiled into this binary, in CompiledRuntimeDrivers order.
func CompiledRuntimeCapabilities() ([]RuntimeCapabilityFacts, error) {
	drivers := CompiledRuntimeDrivers()
	facts := make([]RuntimeCapabilityFacts, 0, len(drivers))
	for _, name := range drivers {
		declaration, err := RuntimeCapabilityFactsFor(name)
		if err != nil {
			return nil, fmt.Errorf("capability declaration for compiled driver %q: %w", name, err)
		}
		facts = append(facts, declaration)
	}
	return facts, nil
}

// stoppedRuntimeRetentionFacts derives the retention declaration from the one
// implementation function that already answers the question, so the report and
// the behavior cannot drift.
func stoppedRuntimeRetentionFacts(driver, enforcedMechanism, unsupportedObserved string) RuntimeCapabilityDimensionFacts {
	if RuntimeDriverSupportsStoppedRuntimeRetention(driver) {
		return RuntimeCapabilityDimensionFacts{
			Dimension:       string(dimensionStoppedRuntimeRetention),
			Enforced:        true,
			Mechanism:       enforcedMechanism,
			Observed:        "RuntimeDriverSupportsStoppedRuntimeRetention reports true: a stopped runtime keeps its private writable state for a later resume",
			DefaultBehavior: "a sandbox that stops without an explicit removal policy keeps its runtime unless the daemon's retention cleanup removes it",
		}
	}
	return RuntimeCapabilityDimensionFacts{
		Dimension:       string(dimensionStoppedRuntimeRetention),
		Enforced:        false,
		Mechanism:       reasonUnsupported,
		Observed:        unsupportedObserved,
		DefaultBehavior: "stopping the sandbox ends its runtime; resume creates a fresh runtime from the image",
	}
}

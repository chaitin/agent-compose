package capmatrix

import (
	"fmt"
	"strings"
)

// CapabilityState is the closed three-state answer to "is this dimension
// actually enforced". It is reported next to the backward-compatible
// [Capability.Enforced] boolean:
//
//   - enforced: the engine imposes the dimension with a named mechanism;
//   - degraded: a mechanism exists and is applied on a best-effort basis, but
//     the engine cannot confirm it took effect (for example a lower layer that
//     reports failures it does not otherwise surface);
//   - unsupported: no mechanism is applied.
//
// A consumer must treat degraded as "not enforced" for a hard requirement: it
// is exactly the state that used to be indistinguishable from real
// enforcement before the silenced failure signals were reconnected.
type CapabilityState string

const (
	// StateEnforced means the engine actively imposes the dimension.
	StateEnforced CapabilityState = "enforced"
	// StateDegraded means the dimension is applied best-effort and unverified.
	StateDegraded CapabilityState = "degraded"
	// StateUnsupported means the dimension is not imposed at all.
	StateUnsupported CapabilityState = "unsupported"
)

// CapabilitySource records where a claim came from. It is the explicit
// "declared vs measured" signal; [Capability.Observed] stays evidence text and
// is never repurposed into the measurement flag.
type CapabilitySource string

const (
	// SourceDeclared is a static statement about the code. Every per-driver
	// declaration is declared: nothing self-attests readiness, and a driver
	// declaration is an input, not a measurement.
	SourceDeclared CapabilitySource = "declared"
	// SourceMeasured is a fact the engine established itself, either by
	// probing the host kernel or by observing a runtime's own failure report.
	SourceMeasured CapabilitySource = "measured"
	// SourceSimulated is a fact produced by an emulation or simulation. A
	// simulated capability never shares the enforced state.
	SourceSimulated CapabilitySource = "simulated"
)

// IsCapabilityState reports whether value is one of the closed three states.
func IsCapabilityState(value string) bool {
	switch CapabilityState(strings.TrimSpace(value)) {
	case StateEnforced, StateDegraded, StateUnsupported:
		return true
	default:
		return false
	}
}

// IsCapabilitySource reports whether value is one of the closed claim sources.
func IsCapabilitySource(value string) bool {
	switch CapabilitySource(strings.TrimSpace(value)) {
	case SourceDeclared, SourceMeasured, SourceSimulated:
		return true
	default:
		return false
	}
}

// assertion is the shared shape validated for a declared or measured
// capability. The zero state and source are normalized from the enforcement
// boolean so a declaration written before the three-state model still means
// what it always meant.
type assertion struct {
	label         string
	enforced      bool
	state         CapabilityState
	source        CapabilitySource
	mechanism     string
	preconditions []string
}

// normalize fills in the compatibility defaults and returns the canonical
// assertion, or an error when the claim is internally inconsistent.
//
// Compatibility: an omitted state is derived from Enforced (true -> enforced,
// false -> unsupported) and an omitted source is declared. API-7 declarations
// therefore keep working unchanged; new code should set both explicitly.
func (a assertion) normalize() (assertion, error) {
	if a.state == "" {
		if a.enforced {
			a.state = StateEnforced
		} else {
			a.state = StateUnsupported
		}
	}
	if a.source == "" {
		a.source = SourceDeclared
	}
	if !IsCapabilityState(string(a.state)) {
		return assertion{}, fmt.Errorf("%w: %s has unknown state %q", ErrInvalidCapability, a.label, a.state)
	}
	if !IsCapabilitySource(string(a.source)) {
		return assertion{}, fmt.Errorf("%w: %s has unknown source %q", ErrInvalidCapability, a.label, a.source)
	}
	if a.enforced != (a.state == StateEnforced) {
		return assertion{}, fmt.Errorf("%w: %s has enforced=%v with state %q; the boolean is true only for %q", ErrInvalidCapability, a.label, a.enforced, a.state, StateEnforced)
	}
	if a.source == SourceSimulated && a.state == StateEnforced {
		return assertion{}, fmt.Errorf("%w: %s is simulated but claims state %q; a simulation never shares the enforced state", ErrInvalidCapability, a.label, StateEnforced)
	}
	mechanism := strings.TrimSpace(a.mechanism)
	switch a.state {
	case StateEnforced:
		if mechanism == "" {
			return assertion{}, fmt.Errorf("%w: %s is enforced but names no mechanism", ErrInvalidCapability, a.label)
		}
		if IsNotEnforcedReason(mechanism) {
			return assertion{}, fmt.Errorf("%w: %s is enforced but mechanism %q is a not-enforced reason", ErrInvalidCapability, a.label, mechanism)
		}
	case StateDegraded:
		if mechanism == "" {
			return assertion{}, fmt.Errorf("%w: %s is degraded but names no partial mechanism", ErrInvalidCapability, a.label)
		}
		if IsNotEnforcedReason(mechanism) {
			return assertion{}, fmt.Errorf("%w: %s is degraded but mechanism %q is a not-enforced reason", ErrInvalidCapability, a.label, mechanism)
		}
		if len(a.preconditions) == 0 {
			return assertion{}, fmt.Errorf("%w: %s is degraded but names no unmet precondition", ErrInvalidCapability, a.label)
		}
	case StateUnsupported:
		if !IsNotEnforcedReason(mechanism) {
			return assertion{}, fmt.Errorf("%w: %s is not enforced but mechanism %q is not a closed not-enforced reason", ErrInvalidCapability, a.label, mechanism)
		}
		if len(a.preconditions) > 0 {
			return assertion{}, fmt.Errorf("%w: %s is not enforced but declares preconditions", ErrInvalidCapability, a.label)
		}
	}
	if a.state == StateDegraded {
		a.enforced = false
	}
	return a, nil
}

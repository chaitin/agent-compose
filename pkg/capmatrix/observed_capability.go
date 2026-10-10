package capmatrix

import (
	"fmt"
	"strings"
)

// CapabilityLayer separates the engine-measured layers from the per-driver
// declared matrix. A layer owns a disjoint dimension prefix, so an observation
// can never silently shadow a declared driver dimension.
type CapabilityLayer string

const (
	// LayerSystem is the host mechanism layer: what this machine and kernel
	// actually provide. The engine establishes it by probing, never by asking
	// a driver whether it is ready.
	LayerSystem CapabilityLayer = "system"
	// LayerProcess is the sandboxed-process layer: what the engine observed
	// about the enforcement a lower layer actually applied.
	LayerProcess CapabilityLayer = "process"
)

// ObservedDimension is a stable machine-readable key for a capability the
// engine measured itself. These dimensions are deliberately outside the
// [Dimension] set: no driver declares them, because a driver declaration is an
// input and never a measurement.
type ObservedDimension string

const (
	// ObservedSeccomp covers the host kernel's seccomp syscall.
	ObservedSeccomp ObservedDimension = "system.seccomp"
	// ObservedSeccompUserNotify covers seccomp user-space notification
	// (SECCOMP_RET_USER_NOTIF), the interception primitive a mediated egress
	// path needs.
	ObservedSeccompUserNotify ObservedDimension = "system.seccomp_user_notify"
	// ObservedLandlock covers the Landlock LSM and the ABI version the kernel
	// reports.
	ObservedLandlock ObservedDimension = "system.landlock"
	// ObservedCgroupV2 covers the cgroup v2 unified hierarchy and whether the
	// daemon can write to it.
	ObservedCgroupV2 ObservedDimension = "system.cgroup_v2"
	// ObservedUserNamespaces covers user-namespace creation on the host.
	ObservedUserNamespaces ObservedDimension = "system.user_namespaces"
	// ObservedProcessSeccomp is the sandboxed-process seccomp enforcement the
	// engine observed a lower layer report.
	ObservedProcessSeccomp ObservedDimension = "process.seccomp"
	// ObservedProcessNoNewPrivileges is the sandboxed-process
	// no_new_privileges enforcement the engine observed a lower layer report.
	ObservedProcessNoNewPrivileges ObservedDimension = "process.no_new_privs"
)

var canonicalObservedDimensions = []ObservedDimension{
	ObservedSeccomp,
	ObservedSeccompUserNotify,
	ObservedLandlock,
	ObservedCgroupV2,
	ObservedUserNamespaces,
	ObservedProcessSeccomp,
	ObservedProcessNoNewPrivileges,
}

// SystemDimensions returns every host-mechanism dimension the engine probes, in
// canonical order.
func SystemDimensions() []ObservedDimension {
	return []ObservedDimension{
		ObservedSeccomp,
		ObservedSeccompUserNotify,
		ObservedLandlock,
		ObservedCgroupV2,
		ObservedUserNamespaces,
	}
}

// ProcessDimensions returns every sandboxed-process dimension the engine can
// report from observation, in canonical order.
func ProcessDimensions() []ObservedDimension {
	return []ObservedDimension{
		ObservedProcessSeccomp,
		ObservedProcessNoNewPrivileges,
	}
}

// Layer returns the layer a dimension belongs to. The boolean is false for an
// unknown dimension.
func (d ObservedDimension) Layer() (CapabilityLayer, bool) {
	switch {
	case strings.HasPrefix(string(d), string(LayerSystem)+"."):
		return LayerSystem, true
	case strings.HasPrefix(string(d), string(LayerProcess)+"."):
		return LayerProcess, true
	default:
		return "", false
	}
}

func (d ObservedDimension) valid() bool {
	for _, candidate := range canonicalObservedDimensions {
		if candidate == d {
			return true
		}
	}
	return false
}

// ObservedCapability is one engine-measured claim: what the engine established
// about a host mechanism or about enforcement a lower layer reported, and what
// would be required for it to hold.
type ObservedCapability struct {
	Dimension ObservedDimension
	// Enforced is true only when the measured mechanism is fully usable. It is
	// exactly true for State == enforced and exists for parity with the
	// declared capability shape.
	Enforced bool
	// State is the three-state answer: enforced, degraded, or unsupported.
	State CapabilityState
	// Mechanism names the mechanism that was probed, or a closed not-enforced
	// reason when the dimension is unsupported.
	Mechanism string
	// Preconditions are the conditions a degraded or enforced mechanism
	// depends on.
	Preconditions []string
	// Observed is the probe evidence: what the kernel or the lower layer
	// actually answered.
	Observed string
	// Missing names the unmet precondition when the dimension is not enforced.
	Missing string
	// Source is measured for a real probe and simulated for an emulation. A
	// measurement never claims to be a declaration.
	Source CapabilitySource
}

// Validate enforces the observed-capability invariant, including that a
// simulated observation never claims the enforced state.
func (o ObservedCapability) Validate() error {
	if !o.Dimension.valid() {
		return fmt.Errorf("%w: unknown observed dimension %q", ErrInvalidCapability, o.Dimension)
	}
	switch o.Source {
	case SourceMeasured, SourceSimulated:
	default:
		return fmt.Errorf("%w: observed dimension %q has source %q; a measurement must be measured or simulated", ErrInvalidCapability, o.Dimension, o.Source)
	}
	if _, err := (assertion{
		label:         fmt.Sprintf("observed dimension %q", o.Dimension),
		enforced:      o.Enforced,
		state:         o.State,
		source:        o.Source,
		mechanism:     o.Mechanism,
		preconditions: o.Preconditions,
	}).normalize(); err != nil {
		return err
	}
	if o.State == StateEnforced && strings.TrimSpace(o.Missing) != "" {
		return fmt.Errorf("%w: observed dimension %q is enforced but names a missing precondition", ErrInvalidCapability, o.Dimension)
	}
	if o.State != StateEnforced && strings.TrimSpace(o.Missing) == "" {
		return fmt.Errorf("%w: observed dimension %q is not enforced but does not name what is missing", ErrInvalidCapability, o.Dimension)
	}
	return nil
}

// ProbeSystemCapabilities probes the host for what mechanisms actually exist
// and returns the measured system layer. It is a side-effecting boundary: it
// runs once when the snapshot is built and is never called from a query path.
//
// The probe never reads an OS version string. Each dimension is answered by
// asking the kernel to do something (query an action, create a ruleset) or by
// the kernel's own configured limit.
//
// Host locality: the measurement is of the kernel this daemon process runs on,
// so it is authoritative only when the sandbox runs on the same host (the
// default local Docker driver). A remote Docker daemon or a Kubernetes node may
// run a different kernel, so a system.* claim must be read as "the daemon host"
// and is not by itself a guarantee about the sandbox host. Callers that use a
// system.* measurement as a precondition therefore inherit that assumption.
func ProbeSystemCapabilities() ([]ObservedCapability, error) {
	probed, err := probeSystemCapabilities()
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeObservedCapabilities(probed)
	if err != nil {
		return nil, err
	}
	seen := make(map[ObservedDimension]struct{}, len(normalized))
	for _, capability := range normalized {
		seen[capability.Dimension] = struct{}{}
	}
	for _, dimension := range SystemDimensions() {
		if _, ok := seen[dimension]; !ok {
			return nil, fmt.Errorf("%w: host probe did not report system dimension %q", ErrInvalidCapability, dimension)
		}
	}
	return normalized, nil
}

func normalizeObservedCapabilities(in []ObservedCapability) ([]ObservedCapability, error) {
	out := make([]ObservedCapability, 0, len(in))
	for _, capability := range in {
		if err := capability.Validate(); err != nil {
			return nil, err
		}
		if capability.Enforced != (capability.State == StateEnforced) {
			return nil, fmt.Errorf("%w: observed dimension %q has enforced=%v with state %q", ErrInvalidCapability, capability.Dimension, capability.Enforced, capability.State)
		}
		capability.Preconditions = append([]string(nil), capability.Preconditions...)
		out = append(out, capability)
	}
	return out, nil
}

func cloneObservedCapabilities(in []ObservedCapability) []ObservedCapability {
	out := make([]ObservedCapability, 0, len(in))
	for _, capability := range in {
		capability.Preconditions = append([]string(nil), capability.Preconditions...)
		out = append(out, capability)
	}
	return out
}

// SimulatedCapability builds a labelled simulation of an observed dimension.
// It always reports degraded and source = simulated, so an emulated mechanism
// can never share the enforced state no matter how it is constructed. The
// engine has no simulated drivers today; this exists so the boundary is
// explicit before one is added.
func SimulatedCapability(dimension ObservedDimension, mechanism, observed string) (ObservedCapability, error) {
	if !dimension.valid() {
		return ObservedCapability{}, fmt.Errorf("%w: unknown observed dimension %q", ErrInvalidCapability, dimension)
	}
	capability := ObservedCapability{
		Dimension:     dimension,
		State:         StateDegraded,
		Mechanism:     mechanism,
		Preconditions: []string{"the mechanism is emulated, not enforced by the sandbox host"},
		Observed:      observed,
		Missing:       "simulated enforcement is not real enforcement",
		Source:        SourceSimulated,
	}
	if err := capability.Validate(); err != nil {
		return ObservedCapability{}, err
	}
	return capability, nil
}

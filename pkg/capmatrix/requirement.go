package capmatrix

import (
	"fmt"
	"sort"
	"strings"
)

// RequirementStrength says how hard a declared isolation requirement is.
type RequirementStrength string

const (
	// RequirementRequired fails the start when the dimension is not enforced.
	RequirementRequired RequirementStrength = "required"
	// RequirementBestEffort records a degradation but still starts, so a
	// caller can surface a high-severity warning without breaking a run.
	RequirementBestEffort RequirementStrength = "best_effort"
)

// RequirementDimension names the dimension a sandbox declaration requires. It
// is either a per-driver [Dimension] or an engine-measured
// [ObservedDimension]; an unknown value is itself a fail-closed violation.
type RequirementDimension string

// IsolationRequirement is one declared isolation requirement.
type IsolationRequirement struct {
	Dimension RequirementDimension
	Strength  RequirementStrength
}

// bestEffortRequirementPrefix marks a best-effort entry in a declaration list,
// for example "best_effort:process.seccomp". Everything else is required, so a
// declaration that forgets the prefix fails closed rather than silently
// degrading.
const bestEffortRequirementPrefix = "best_effort:"

// ParseIsolationRequirements converts a declaration's string list into
// requirements. It rejects an unknown dimension instead of dropping it: a typo
// must not turn a required isolation property into a silent no-op.
func ParseIsolationRequirements(declared []string) ([]IsolationRequirement, error) {
	requirements := make([]IsolationRequirement, 0, len(declared))
	for _, entry := range declared {
		value := strings.TrimSpace(entry)
		if value == "" {
			continue
		}
		strength := RequirementRequired
		if strings.HasPrefix(value, bestEffortRequirementPrefix) {
			strength = RequirementBestEffort
			value = strings.TrimSpace(strings.TrimPrefix(value, bestEffortRequirementPrefix))
		}
		dimension := RequirementDimension(value)
		if !dimension.known() {
			return nil, fmt.Errorf("%w: unknown isolation requirement %q", ErrInvalidCapability, entry)
		}
		requirements = append(requirements, IsolationRequirement{Dimension: dimension, Strength: strength})
	}
	return requirements, nil
}

func (d RequirementDimension) known() bool {
	if Dimension(d).valid() {
		return true
	}
	return ObservedDimension(d).valid()
}

// PreflightReason is the closed set of machine-consumable reasons a start
// pre-flight can produce. A consumer branches on these values, never on the
// detail text.
type PreflightReason string

const (
	// PreflightReasonNotEnforced means the dimension is declared but the
	// engine does not impose it.
	PreflightReasonNotEnforced PreflightReason = "not_enforced"
	// PreflightReasonDegraded means the dimension is applied best-effort and
	// the engine cannot confirm it took effect. A hard requirement treats it
	// exactly like not_enforced.
	PreflightReasonDegraded PreflightReason = "degraded"
	// PreflightReasonMissingMechanism means the claim is enforced but names no
	// mechanism, so it cannot be trusted.
	PreflightReasonMissingMechanism PreflightReason = "missing_mechanism"
	// PreflightReasonSystemUnavailable means the driver claims enforcement but
	// a host mechanism it depends on is absent or degraded.
	PreflightReasonSystemUnavailable PreflightReason = "system_unavailable"
	// PreflightReasonUnknownDimension means the declaration names a dimension
	// the engine cannot answer.
	PreflightReasonUnknownDimension PreflightReason = "unknown_dimension"
	// PreflightReasonUnknownDriver means the snapshot has no declaration for
	// the driver that would create the runtime.
	PreflightReasonUnknownDriver PreflightReason = "unknown_driver"
)

// PreflightFinding is one requirement that a required declaration could not
// satisfy. The same shape reports a best-effort degradation.
type PreflightFinding struct {
	Dimension RequirementDimension
	Reason    PreflightReason
	Mechanism string
	Detail    string
}

// ControlPlaneBehavior is the engine's explicit answer for what happens to a
// running sandbox when the control plane is unreachable.
type ControlPlaneBehavior string

const (
	// ControlPlaneContinue means a started sandbox keeps running without the
	// control plane. This is the engine's current behavior: the sandbox is a
	// separate runtime and the daemon is not in its data path.
	ControlPlaneContinue ControlPlaneBehavior = "continue"
	// ControlPlaneFailClosed means a sandbox must stop when the control plane
	// is unreachable.
	ControlPlaneFailClosed ControlPlaneBehavior = "fail_closed"
)

// FenceGuarantees is the explicit answer to the fence-guarantee question set.
// Every answer is a value, never prose: a boolean or a closed enum.
type FenceGuarantees struct {
	// DefaultDenyEgressEstablished is true only when the driver declares an
	// enforced egress dimension with a mechanism.
	DefaultDenyEgressEstablished bool
	// UnmanagedEgressPathPresent is the complement: when default-deny is not
	// established, an unmediated egress path exists.
	UnmanagedEgressPathPresent bool
	// CredentialRevocationVerified is false today. The engine revokes facade
	// tokens on release and on a failed start, but no capability dimension
	// verifies that revocation took effect inside a sandbox, and the engine
	// must not claim otherwise.
	CredentialRevocationVerified bool
	// ControlPlaneUnreachable is the engine's explicit answer.
	ControlPlaneUnreachable ControlPlaneBehavior
}

// StartPreflight is the decision "declared requirement x actual capability".
// It is a pure value: no I/O, no probing, no driver call.
type StartPreflight struct {
	// Allowed is true when no required dimension is violated. An empty
	// requirement set is always allowed, which keeps the default path
	// behavior-identical.
	Allowed bool
	// Driver is the normalized driver the decision was evaluated for.
	Driver string
	// Violations are the required dimensions that failed.
	Violations []PreflightFinding
	// Degradations are the best-effort dimensions that are not enforced. They
	// do not block a start but must be surfaced.
	Degradations []PreflightFinding
	// Fences is the explicit fence-guarantee answer set.
	Fences FenceGuarantees
}

// FailureReason renders a deterministic, machine-consumable reason string for a
// rejected start. It is empty when the start is allowed.
func (d StartPreflight) FailureReason() string {
	if d.Allowed {
		return ""
	}
	parts := make([]string, 0, len(d.Violations))
	for _, violation := range d.Violations {
		parts = append(parts, fmt.Sprintf("%s=%s", violation.Dimension, violation.Reason))
	}
	sort.Strings(parts)
	return "isolation requirements not satisfied for driver " + d.Driver + ": " + strings.Join(parts, ", ")
}

// EvaluateStartPreflight owns the decision "declared requirement x actual
// capability". It is a pure function: capabilities and observations are inputs,
// so the caller controls whether they came from the frozen startup snapshot or
// a test fixture.
func EvaluateStartPreflight(driver string, requirements []IsolationRequirement, declared DriverCapabilities, observations []ObservedCapability) StartPreflight {
	decision := StartPreflight{
		Allowed:      true,
		Driver:       driver,
		Fences:       evaluateFenceGuarantees(declared),
		Violations:   []PreflightFinding{},
		Degradations: []PreflightFinding{},
	}
	if declared.Driver == "" || declared.Driver != driver {
		for _, requirement := range requirements {
			decision.record(requirement, PreflightFinding{
				Dimension: requirement.Dimension,
				Reason:    PreflightReasonUnknownDriver,
				Detail:    fmt.Sprintf("no capability declaration is frozen for driver %q", driver),
			})
		}
		return decision
	}
	observed := indexObservedCapabilities(observations)
	for _, requirement := range requirements {
		finding, ok := evaluateRequirement(requirement, declared, observed)
		if !ok {
			continue
		}
		decision.record(requirement, finding)
	}
	return decision
}

func (d *StartPreflight) record(requirement IsolationRequirement, finding PreflightFinding) {
	if requirement.Strength == RequirementBestEffort {
		d.Degradations = append(d.Degradations, finding)
		return
	}
	d.Violations = append(d.Violations, finding)
	d.Allowed = false
}

// evaluateRequirement returns a finding when the requirement is not satisfied.
func evaluateRequirement(requirement IsolationRequirement, declared DriverCapabilities, observed map[ObservedDimension]ObservedCapability) (PreflightFinding, bool) {
	if observedDimension := ObservedDimension(requirement.Dimension); observedDimension.valid() {
		if finding, blocked := evaluateObservedRequirement(requirement, observedDimension, observed); blocked {
			return finding, true
		}
		// An observed claim is not sufficient on its own either: the host
		// mechanism it rests on is part of the decision, exactly as for a
		// declared dimension.
		return systemPreconditionFinding(requirement.Dimension, observed)
	}
	dimension := Dimension(requirement.Dimension)
	if !dimension.valid() {
		return PreflightFinding{
			Dimension: requirement.Dimension,
			Reason:    PreflightReasonUnknownDimension,
			Detail:    "the engine has no capability for this dimension",
		}, true
	}
	capability, ok := declared.Capability(dimension)
	if !ok {
		return PreflightFinding{
			Dimension: requirement.Dimension,
			Reason:    PreflightReasonUnknownDimension,
			Mechanism: string(dimension),
			Detail:    "the driver does not declare this dimension",
		}, true
	}
	if !fullyEnforced(capability.Enforced, capability.State, capability.Mechanism) {
		return PreflightFinding{
			Dimension: requirement.Dimension,
			Reason:    notSatisfiedReason(capability.Enforced, capability.State, capability.Mechanism),
			Mechanism: capability.Mechanism,
			Detail:    capability.Observed,
		}, true
	}
	if finding, blocked := systemPreconditionFinding(requirement.Dimension, observed); blocked {
		return finding, true
	}
	return PreflightFinding{}, false
}

func evaluateObservedRequirement(requirement IsolationRequirement, dimension ObservedDimension, observed map[ObservedDimension]ObservedCapability) (PreflightFinding, bool) {
	capability, ok := observed[dimension]
	if !ok {
		return PreflightFinding{
			Dimension: requirement.Dimension,
			Reason:    PreflightReasonUnknownDimension,
			Detail:    "the engine has no measurement for this dimension in the frozen snapshot",
		}, true
	}
	if !fullyEnforced(capability.Enforced, capability.State, capability.Mechanism) {
		return PreflightFinding{
			Dimension: requirement.Dimension,
			Reason:    notSatisfiedReason(capability.Enforced, capability.State, capability.Mechanism),
			Mechanism: capability.Mechanism,
			Detail:    capability.Observed,
		}, true
	}
	return PreflightFinding{}, false
}

func fullyEnforced(enforced bool, state CapabilityState, mechanism string) bool {
	if !enforced || state != StateEnforced {
		return false
	}
	trimmed := strings.TrimSpace(mechanism)
	return trimmed != "" && !IsNotEnforcedReason(trimmed)
}

func notSatisfiedReason(enforced bool, state CapabilityState, mechanism string) PreflightReason {
	switch {
	case state == StateDegraded:
		return PreflightReasonDegraded
	case strings.TrimSpace(mechanism) == "":
		return PreflightReasonMissingMechanism
	case !enforced:
		return PreflightReasonNotEnforced
	default:
		return PreflightReasonNotEnforced
	}
}

// systemPreconditionDimensions maps a requirement to the host mechanisms it
// depends on. Only dimensions with a clear kernel dependency are mapped;
// container-level fields such as capability drop do not need one.
func systemPreconditionDimensions(dimension RequirementDimension) []ObservedDimension {
	switch dimension {
	case RequirementDimension(DimensionUserNamespaces):
		return []ObservedDimension{ObservedUserNamespaces}
	case RequirementDimension(ObservedProcessSeccomp), RequirementDimension(ObservedProcessNoNewPrivileges):
		return []ObservedDimension{ObservedSeccomp}
	default:
		return nil
	}
}

func systemPreconditionFinding(dimension RequirementDimension, observed map[ObservedDimension]ObservedCapability) (PreflightFinding, bool) {
	for _, precondition := range systemPreconditionDimensions(dimension) {
		capability, ok := observed[precondition]
		if !ok {
			return PreflightFinding{
				Dimension: dimension,
				Reason:    PreflightReasonSystemUnavailable,
				Mechanism: string(precondition),
				Detail:    "the frozen snapshot has no measurement for the required host mechanism",
			}, true
		}
		if !fullyEnforced(capability.Enforced, capability.State, capability.Mechanism) {
			return PreflightFinding{
				Dimension: dimension,
				Reason:    PreflightReasonSystemUnavailable,
				Mechanism: capability.Mechanism,
				Detail:    capability.Observed,
			}, true
		}
	}
	return PreflightFinding{}, false
}

func evaluateFenceGuarantees(declared DriverCapabilities) FenceGuarantees {
	fences := FenceGuarantees{
		// The engine is the control plane and is not in a started sandbox's
		// data path, so losing it does not stop the sandbox. This is stated as
		// a value because it is a deliberate choice, not an accident.
		ControlPlaneUnreachable:      ControlPlaneContinue,
		CredentialRevocationVerified: false,
	}
	if capability, ok := declared.Capability(DimensionEgressPolicy); ok {
		fences.DefaultDenyEgressEstablished = fullyEnforced(capability.Enforced, capability.State, capability.Mechanism)
	}
	fences.UnmanagedEgressPathPresent = !fences.DefaultDenyEgressEstablished
	return fences
}

func indexObservedCapabilities(observations []ObservedCapability) map[ObservedDimension]ObservedCapability {
	indexed := make(map[ObservedDimension]ObservedCapability, len(observations))
	for _, capability := range observations {
		indexed[capability.Dimension] = capability
	}
	return indexed
}

// EvaluateStartPreflight evaluates a declaration against the frozen snapshot.
// measured carries runtime observations, such as a lower layer reporting that
// seccomp was unavailable on a specific exec; pass nil to evaluate the startup
// snapshot alone.
func (s Snapshot) EvaluateStartPreflight(driver string, requirements []IsolationRequirement, measured []ObservedCapability) StartPreflight {
	declared, _ := s.Driver(driver)
	observations := append(s.Observations(), measured...)
	return EvaluateStartPreflight(driver, requirements, declared, observations)
}

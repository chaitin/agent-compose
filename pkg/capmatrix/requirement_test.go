package capmatrix

import (
	"errors"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/driver"
)

// testDriverCapabilities builds a complete, valid declaration with every
// dimension not configured, then lets a test override specific dimensions.
// It is the fixture for "declared requirement x actual capability" decisions.
func testDriverCapabilities(t *testing.T, name string, override func(*Capability)) DriverCapabilities {
	t.Helper()
	declaration := DriverCapabilities{Driver: name}
	for _, dimension := range RequiredDimensions() {
		declaration.Capabilities = append(declaration.Capabilities, Capability{
			Dimension:       dimension,
			Mechanism:       ReasonNotConfigured,
			Observed:        "the driver writes nothing for this dimension",
			DefaultBehavior: "the dimension is not applied",
		})
	}
	if override != nil {
		for index := range declaration.Capabilities {
			override(&declaration.Capabilities[index])
		}
	}
	if err := declaration.Validate(); err != nil {
		t.Fatalf("test declaration for %q is invalid: %v", name, err)
	}
	return declaration
}

func TestEvaluateStartPreflightAllowsAnEmptyDeclaration(t *testing.T) {
	declaration := testDriverCapabilities(t, "docker", nil)
	decision := EvaluateStartPreflight("docker", nil, declaration, nil)
	if !decision.Allowed {
		t.Fatalf("Allowed = false for an empty requirement set: %+v", decision)
	}
	if len(decision.Violations) != 0 || len(decision.Degradations) != 0 {
		t.Fatalf("decision = %+v, want no findings", decision)
	}
	if reason := decision.FailureReason(); reason != "" {
		t.Fatalf("FailureReason() = %q, want empty for an allowed start", reason)
	}
	// The fence answers are still explicit even with no requirements.
	if decision.Fences.ControlPlaneUnreachable != ControlPlaneContinue {
		t.Fatalf("control-plane answer = %q, want %q", decision.Fences.ControlPlaneUnreachable, ControlPlaneContinue)
	}
	if decision.Fences.CredentialRevocationVerified {
		t.Fatal("CredentialRevocationVerified = true, but no dimension verifies revocation")
	}
	if !decision.Fences.UnmanagedEgressPathPresent || decision.Fences.DefaultDenyEgressEstablished {
		t.Fatalf("fences = %+v, want an unmanaged egress path while egress is not enforced", decision.Fences)
	}
}

// TestEvaluateStartPreflightRejectsDockerDefaultDenyEgress is the acceptance
// test: declaring default-deny egress on Docker must fail with a determinable
// reason until SEC-6 actually implements enforcement.
func TestEvaluateStartPreflightRejectsDockerDefaultDenyEgress(t *testing.T) {
	declaration := testDriverCapabilities(t, "docker", nil)
	decision := EvaluateStartPreflight("docker", []IsolationRequirement{
		{Dimension: RequirementDimension(DimensionEgressPolicy), Strength: RequirementRequired},
	}, declaration, nil)

	if decision.Allowed {
		t.Fatal("Allowed = true, want the required egress dimension to fail")
	}
	if len(decision.Violations) != 1 {
		t.Fatalf("Violations = %+v, want one finding", decision.Violations)
	}
	violation := decision.Violations[0]
	if violation.Dimension != RequirementDimension(DimensionEgressPolicy) || violation.Reason != PreflightReasonNotEnforced {
		t.Fatalf("violation = %+v, want egress_policy=not_enforced", violation)
	}
	if reason := decision.FailureReason(); !strings.Contains(reason, "egress_policy=not_enforced") {
		t.Fatalf("FailureReason() = %q, want the machine-consumable reason", reason)
	}
}

func TestEvaluateStartPreflightRejectsDegradedAndMissingMechanism(t *testing.T) {
	degraded := testDriverCapabilities(t, "boxlite", func(capability *Capability) {
		if capability.Dimension != DimensionEgressPolicy {
			return
		}
		capability.Enforced = false
		capability.State = StateDegraded
		capability.Mechanism = "best_effort_network_policy"
		capability.Preconditions = []string{"the CNI must accept a NetworkPolicy"}
	})
	decision := EvaluateStartPreflight("boxlite", []IsolationRequirement{
		{Dimension: RequirementDimension(DimensionEgressPolicy), Strength: RequirementRequired},
	}, degraded, nil)
	if decision.Allowed || len(decision.Violations) != 1 || decision.Violations[0].Reason != PreflightReasonDegraded {
		t.Fatalf("decision = %+v, want degraded treated as not enforced", decision)
	}

	// A declaration that claims enforcement without a mechanism cannot be
	// trusted. It is constructed directly because Validate rejects it at the
	// snapshot boundary; the pre-flight must still refuse it.
	broken := DriverCapabilities{Driver: "docker", Capabilities: []Capability{{
		Dimension: DimensionEgressPolicy,
		Enforced:  true,
		State:     StateEnforced,
		Mechanism: "",
	}}}
	decision = EvaluateStartPreflight("docker", []IsolationRequirement{
		{Dimension: RequirementDimension(DimensionEgressPolicy), Strength: RequirementRequired},
	}, broken, nil)
	if decision.Allowed || decision.Violations[0].Reason != PreflightReasonMissingMechanism {
		t.Fatalf("decision = %+v, want missing_mechanism", decision)
	}
}

func TestEvaluateStartPreflightRejectsUnknownDimensionAndDriver(t *testing.T) {
	declaration := testDriverCapabilities(t, "docker", nil)
	decision := EvaluateStartPreflight("docker", []IsolationRequirement{
		{Dimension: RequirementDimension("made_up_dimension"), Strength: RequirementRequired},
	}, declaration, nil)
	if decision.Allowed || decision.Violations[0].Reason != PreflightReasonUnknownDimension {
		t.Fatalf("decision = %+v, want unknown_dimension", decision)
	}

	decision = EvaluateStartPreflight("k8s", []IsolationRequirement{
		{Dimension: RequirementDimension(DimensionResourceLimits), Strength: RequirementRequired},
	}, declaration, nil)
	if decision.Allowed || decision.Violations[0].Reason != PreflightReasonUnknownDriver {
		t.Fatalf("decision = %+v, want unknown_driver", decision)
	}
}

func TestEvaluateStartPreflightRecordsBestEffortAsDegradation(t *testing.T) {
	declaration := testDriverCapabilities(t, "docker", nil)
	decision := EvaluateStartPreflight("docker", []IsolationRequirement{
		{Dimension: RequirementDimension(DimensionEgressPolicy), Strength: RequirementBestEffort},
	}, declaration, nil)
	if !decision.Allowed {
		t.Fatalf("Allowed = false, want a best-effort requirement to start: %+v", decision)
	}
	if len(decision.Violations) != 0 || len(decision.Degradations) != 1 {
		t.Fatalf("decision = %+v, want exactly one degradation and no violation", decision)
	}
}

// TestEvaluateStartPreflightRejectsUnavailableSystemPrecondition proves a
// driver claim is not sufficient on its own: the host mechanism it depends on
// is part of the decision.
func TestEvaluateStartPreflightRejectsUnavailableSystemPrecondition(t *testing.T) {
	declaration := testDriverCapabilities(t, "docker", func(capability *Capability) {
		if capability.Dimension != DimensionUserNamespaces {
			return
		}
		capability.Enforced = true
		capability.State = StateEnforced
		capability.Mechanism = "userns_remap"
	})
	observations := []ObservedCapability{{
		Dimension: ObservedUserNamespaces,
		State:     StateUnsupported,
		Mechanism: ReasonUnsupported,
		Observed:  "the kernel reports user.max_user_namespaces=0",
		Missing:   "unprivileged user namespaces are disabled",
		Source:    SourceMeasured,
	}}
	decision := EvaluateStartPreflight("docker", []IsolationRequirement{
		{Dimension: RequirementDimension(DimensionUserNamespaces), Strength: RequirementRequired},
	}, declaration, observations)
	if decision.Allowed || decision.Violations[0].Reason != PreflightReasonSystemUnavailable {
		t.Fatalf("decision = %+v, want system_unavailable", decision)
	}
}

// TestEvaluateStartPreflightRejectsUnavailableSystemPreconditionForObserved
// proves the host precondition is checked for an engine-measured process claim
// too. Without it the process-level host mapping is unreachable, because an
// observed dimension never reaches the declared-dimension path.
func TestEvaluateStartPreflightRejectsUnavailableSystemPreconditionForObserved(t *testing.T) {
	declaration := testDriverCapabilities(t, "boxlite", nil)
	observations := []ObservedCapability{
		{
			Dimension: ObservedProcessSeccomp,
			Enforced:  true,
			State:     StateEnforced,
			Mechanism: "seccomp_filter_applied",
			Observed:  "the lower layer reported it applied a seccomp filter",
			Source:    SourceMeasured,
		},
		{
			Dimension: ObservedSeccomp,
			State:     StateUnsupported,
			Mechanism: ReasonUnsupported,
			Observed:  "prctl(PR_GET_SECCOMP) failed with EINVAL",
			Missing:   "the kernel cannot enforce seccomp",
			Source:    SourceMeasured,
		},
	}
	decision := EvaluateStartPreflight("boxlite", []IsolationRequirement{
		{Dimension: RequirementDimension(ObservedProcessSeccomp), Strength: RequirementRequired},
	}, declaration, observations)
	if decision.Allowed || decision.Violations[0].Reason != PreflightReasonSystemUnavailable {
		t.Fatalf("decision = %+v, want system_unavailable when the observed process claim rests on an absent host mechanism", decision)
	}
}

// TestMeasuredLowerLayerSeccompFailureIsReportedNotEnforced is the
// lower-layer-injection acceptance test: a libcontainer report that seccomp was
// unavailable becomes an honest "not enforced" assertion, and a required
// process.seccomp requirement then fails closed.
func TestMeasuredLowerLayerSeccompFailureIsReportedNotEnforced(t *testing.T) {
	measured := MeasuredProcessIsolationCapabilities("boxlite", driver.ExecSecurityFacts{SeccompUnavailable: 1})
	if len(measured) != 1 {
		t.Fatalf("measured capabilities = %+v, want one seccomp assertion", measured)
	}
	assertion := measured[0]
	if assertion.Dimension != ObservedProcessSeccomp {
		t.Fatalf("dimension = %q, want %q", assertion.Dimension, ObservedProcessSeccomp)
	}
	if assertion.Enforced || assertion.State != StateUnsupported {
		t.Fatalf("assertion = %+v, want enforced=false and state=unsupported", assertion)
	}
	if assertion.Mechanism != ReasonLowerLayerUnavailable {
		t.Fatalf("mechanism = %q, want %q", assertion.Mechanism, ReasonLowerLayerUnavailable)
	}
	if assertion.Source != SourceMeasured {
		t.Fatalf("source = %q, want %q", assertion.Source, SourceMeasured)
	}
	if err := assertion.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	declaration := testDriverCapabilities(t, "boxlite", nil)
	decision := EvaluateStartPreflight("boxlite", []IsolationRequirement{
		{Dimension: RequirementDimension(ObservedProcessSeccomp), Strength: RequirementRequired},
	}, declaration, measured)
	if decision.Allowed {
		t.Fatal("Allowed = true, want the observed seccomp failure to fail a required dimension")
	}
	if decision.Violations[0].Reason != PreflightReasonNotEnforced {
		t.Fatalf("violation = %+v, want not_enforced", decision.Violations[0])
	}
	if !strings.Contains(decision.Violations[0].Detail, "seccomp privileges were not set") {
		t.Fatalf("detail = %q, want the observed lower-layer evidence", decision.Violations[0].Detail)
	}
}

func TestParseIsolationRequirements(t *testing.T) {
	requirements, err := ParseIsolationRequirements([]string{"egress_policy", " best_effort:process.seccomp ", ""})
	if err != nil {
		t.Fatalf("ParseIsolationRequirements() error = %v", err)
	}
	want := []IsolationRequirement{
		{Dimension: RequirementDimension(DimensionEgressPolicy), Strength: RequirementRequired},
		{Dimension: RequirementDimension(ObservedProcessSeccomp), Strength: RequirementBestEffort},
	}
	if len(requirements) != len(want) {
		t.Fatalf("requirements = %+v, want %+v", requirements, want)
	}
	for index := range want {
		if requirements[index] != want[index] {
			t.Fatalf("requirement[%d] = %+v, want %+v", index, requirements[index], want[index])
		}
	}
	if _, err := ParseIsolationRequirements([]string{"not_a_dimension"}); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("unknown requirement error = %v, want ErrInvalidCapability", err)
	}
}

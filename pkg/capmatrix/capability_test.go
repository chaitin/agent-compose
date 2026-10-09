package capmatrix

import (
	"errors"
	"testing"
)

func validCapability(dimension Dimension) Capability {
	return Capability{
		Dimension:       dimension,
		Enforced:        false,
		Mechanism:       ReasonNotConfigured,
		Observed:        "observed behavior",
		DefaultBehavior: "default behavior",
	}
}

func TestCapabilityValidateRejectsEnforcedWithEmptyMechanism(t *testing.T) {
	capability := validCapability(DimensionResourceLimits)
	capability.Enforced = true
	capability.Mechanism = ""

	err := capability.Validate()
	if !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("Validate() error = %v, want ErrInvalidCapability", err)
	}
}

func TestCapabilityValidateRejectsEnforcedWithReason(t *testing.T) {
	capability := validCapability(DimensionEgressPolicy)
	capability.Enforced = true
	capability.Mechanism = ReasonUnsupported

	if err := capability.Validate(); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("Validate() error = %v, want ErrInvalidCapability", err)
	}
}

func TestCapabilityValidateRejectsNotEnforcedWithoutClosedReason(t *testing.T) {
	for _, mechanism := range []string{"", "host_config", "HOST_CONFIG"} {
		capability := validCapability(DimensionEgressPolicy)
		capability.Mechanism = mechanism
		if err := capability.Validate(); !errors.Is(err, ErrInvalidCapability) {
			t.Fatalf("mechanism %q: Validate() error = %v, want ErrInvalidCapability", mechanism, err)
		}
	}
}

func TestCapabilityValidateAcceptsBothShapes(t *testing.T) {
	enforced := Capability{
		Dimension:       DimensionResourceLimits,
		Enforced:        true,
		Mechanism:       "sdk_sandbox_options",
		Observed:        "the SDK option call",
		DefaultBehavior: "the daemon default",
	}
	if err := enforced.Validate(); err != nil {
		t.Fatalf("enforced Validate() error = %v", err)
	}
	if err := validCapability(DimensionEgressPolicy).Validate(); err != nil {
		t.Fatalf("not-enforced Validate() error = %v", err)
	}
}

func TestCapabilityValidateRejectsUnknownDimension(t *testing.T) {
	capability := validCapability(Dimension("made_up_dimension"))
	if err := capability.Validate(); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("Validate() error = %v, want ErrInvalidCapability", err)
	}
}

func TestDriverCapabilitiesValidateRequiresEveryDimensionExactlyOnce(t *testing.T) {
	complete := DriverCapabilities{Driver: "docker"}
	for _, dimension := range RequiredDimensions() {
		complete.Capabilities = append(complete.Capabilities, validCapability(dimension))
	}
	if err := complete.Validate(); err != nil {
		t.Fatalf("complete declaration Validate() error = %v", err)
	}

	missing := DriverCapabilities{Driver: "docker"}
	for _, dimension := range RequiredDimensions()[:len(RequiredDimensions())-1] {
		missing.Capabilities = append(missing.Capabilities, validCapability(dimension))
	}
	if err := missing.Validate(); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("missing dimension Validate() error = %v, want ErrInvalidCapability", err)
	}

	duplicated := DriverCapabilities{Driver: "docker", Capabilities: append(complete.Capabilities, validCapability(DimensionResourceLimits))}
	if err := duplicated.Validate(); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("duplicate dimension Validate() error = %v, want ErrInvalidCapability", err)
	}
}

// Package capmatrix owns the engine's declared isolation and runtime
// capability matrix: the answer to "what can this engine enforce, and by
// what".
//
// It is deliberately not part of pkg/capability or pkg/capabilities. Those
// packages are the capability-gateway (capset/catalog) client and its
// transport-facing types. This package owns a different concept: the drive
// and provider capability snapshot a later isolation requirement check
// (SEC-3) can depend on without touching a driver.
//
// The matrix is a startup snapshot. Declarations are static facts about the
// code, so building the snapshot never contacts a Docker daemon, a KVM host,
// or a Kubernetes cluster; a query can therefore succeed while every runtime
// is unreachable.
package capmatrix

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidCapability reports a capability that cannot be represented in the
// contract, most importantly enforced == true with an empty mechanism.
var ErrInvalidCapability = errors.New("invalid engine capability")

// Dimension is a stable machine-readable capability key. Consumers must treat
// it as an exact-match string and must not depend on declaration order.
type Dimension string

const (
	// DimensionResourceLimits covers CPU, memory, and disk limits written into
	// the sandbox runtime configuration.
	DimensionResourceLimits Dimension = "resource_limits"
	// DimensionCapabilityDrop covers dropping Linux capabilities in the
	// sandbox workload.
	DimensionCapabilityDrop Dimension = "security_context.capability_drop"
	// DimensionReadOnlyRootfs covers mounting the sandbox root filesystem
	// read-only for the workload.
	DimensionReadOnlyRootfs Dimension = "security_context.read_only_rootfs"
	// DimensionNonRootUser covers running the sandbox workload as a non-root
	// user.
	DimensionNonRootUser Dimension = "security_context.non_root_user"
	// DimensionUserNamespaces covers user-namespace remapping for the sandbox
	// workload.
	DimensionUserNamespaces Dimension = "security_context.user_namespaces"
	// DimensionEgressPolicy covers how strongly outbound network access is
	// restricted and enforced.
	DimensionEgressPolicy Dimension = "egress_policy"
	// DimensionCredentialPlaceholder covers injecting an opaque placeholder
	// into the sandbox instead of a real credential value.
	DimensionCredentialPlaceholder Dimension = "credential_placeholder_injection"
	// DimensionStoppedRuntimeRetention covers stopping a sandbox while
	// preserving its private writable runtime for a later resume.
	DimensionStoppedRuntimeRetention Dimension = "stopped_runtime_retention"
	// DimensionCheckpointRestore covers checkpointing a running sandbox and
	// restoring it later.
	DimensionCheckpointRestore Dimension = "checkpoint_restore"
	// DimensionGPUAndDevices covers exposing host GPUs or device nodes to the
	// sandbox.
	DimensionGPUAndDevices Dimension = "gpu_and_devices"
)

// Not-enforced reasons. A capability that is not enforced must carry exactly
// one of these, so a consumer can distinguish "the driver cannot" from "the
// engine does not" without parsing free text.
const (
	// ReasonUnsupported means the driver has no configuration surface for the
	// dimension.
	ReasonUnsupported = "unsupported"
	// ReasonNotConfigured means a configuration surface exists but the engine
	// does not set it.
	ReasonNotConfigured = "not_configured"
	// ReasonLowerLayerUnavailable means the engine handed the dimension to a
	// lower layer and that layer reported it could not enforce it. It is the
	// honest answer for the libcontainer seccomp/no_new_privileges failures
	// that used to be filtered out of exec stderr.
	ReasonLowerLayerUnavailable = "lower_layer_unavailable"
)

var canonicalDimensions = []Dimension{
	DimensionResourceLimits,
	DimensionCapabilityDrop,
	DimensionReadOnlyRootfs,
	DimensionNonRootUser,
	DimensionUserNamespaces,
	DimensionEgressPolicy,
	DimensionCredentialPlaceholder,
	DimensionStoppedRuntimeRetention,
	DimensionCheckpointRestore,
	DimensionGPUAndDevices,
}

// RequiredDimensions returns every dimension the engine must report, in the
// canonical order. The minimal coverage required by API-7 is exactly this
// set, and every driver declaration must cover all of it.
func RequiredDimensions() []Dimension {
	return append([]Dimension(nil), canonicalDimensions...)
}

// IsNotEnforcedReason reports whether value is one of the closed reasons used
// when a capability is not enforced.
func IsNotEnforcedReason(value string) bool {
	switch strings.TrimSpace(value) {
	case ReasonUnsupported, ReasonNotConfigured, ReasonLowerLayerUnavailable:
		return true
	default:
		return false
	}
}

// Capability is one dimension's enforcement claim together with the evidence
// behind it.
type Capability struct {
	Dimension Dimension
	// Enforced is true only when the engine actively imposes the dimension. It
	// is exactly true for State == enforced and is retained for backward
	// compatibility with consumers of the API-7 contract.
	Enforced bool
	// Mechanism names what enforces the dimension when Enforced is true, and
	// one of the not-enforced reasons otherwise. It is never empty.
	Mechanism string
	// Preconditions are the conditions that must hold for the mechanism to
	// take effect, for example a minimum kernel version.
	Preconditions []string
	// Observed records what the driver actually writes into its runtime
	// configuration today, so the claim can be cross-checked against code.
	Observed string
	// DefaultBehavior states the engine's behavior when the declaration is
	// absent or silent.
	DefaultBehavior string
	// State is the three-state enforcement answer. An omitted state is derived
	// from Enforced so an API-7-era declaration keeps its meaning.
	State CapabilityState
	// Source distinguishes a code declaration from a measured fact and from a
	// simulation. An omitted source defaults to declared.
	Source CapabilitySource
}

// Validate enforces the capability invariant: an enforced capability always
// names a non-empty mechanism, a capability that is not enforced always
// carries a closed reason instead of a mechanism, and a simulated capability
// never claims the enforced state.
func (c Capability) Validate() error {
	if !c.Dimension.valid() {
		return fmt.Errorf("%w: unknown dimension %q", ErrInvalidCapability, c.Dimension)
	}
	if _, err := (assertion{
		label:         fmt.Sprintf("dimension %q", c.Dimension),
		enforced:      c.Enforced,
		state:         c.State,
		source:        c.Source,
		mechanism:     c.Mechanism,
		preconditions: c.Preconditions,
	}).normalize(); err != nil {
		return err
	}
	return nil
}

// Normalized returns the capability with the explicit state and source filled
// in. An invalid claim is returned unchanged so Validate remains the single
// authority on rejection; callers serialize only validated capabilities.
func (c Capability) Normalized() Capability {
	normalized, err := (assertion{
		label:         fmt.Sprintf("dimension %q", c.Dimension),
		enforced:      c.Enforced,
		state:         c.State,
		source:        c.Source,
		mechanism:     c.Mechanism,
		preconditions: c.Preconditions,
	}).normalize()
	if err != nil {
		return c
	}
	c.Enforced = normalized.enforced
	c.State = normalized.state
	c.Source = normalized.source
	return c
}

func (d Dimension) valid() bool {
	for _, candidate := range canonicalDimensions {
		if candidate == d {
			return true
		}
	}
	return false
}

// DriverCapabilities is one runtime driver's whole declaration.
type DriverCapabilities struct {
	// Driver is the normalized runtime driver name, for example "docker".
	Driver string
	// Capabilities covers RequiredDimensions exactly once each.
	Capabilities []Capability
}

// Validate rejects a driver declaration that omits, duplicates, or
// misrepresents any required dimension.
func (d DriverCapabilities) Validate() error {
	if strings.TrimSpace(d.Driver) == "" {
		return fmt.Errorf("%w: driver name is empty", ErrInvalidCapability)
	}
	seen := make(map[Dimension]struct{}, len(d.Capabilities))
	for _, capability := range d.Capabilities {
		if _, duplicate := seen[capability.Dimension]; duplicate {
			return fmt.Errorf("%w: driver %q declares dimension %q more than once", ErrInvalidCapability, d.Driver, capability.Dimension)
		}
		seen[capability.Dimension] = struct{}{}
		if err := capability.Validate(); err != nil {
			return fmt.Errorf("driver %q: %w", d.Driver, err)
		}
	}
	for _, dimension := range canonicalDimensions {
		if _, ok := seen[dimension]; !ok {
			return fmt.Errorf("%w: driver %q does not declare required dimension %q", ErrInvalidCapability, d.Driver, dimension)
		}
	}
	return nil
}

// Capability returns the declaration for dimension. The boolean is false when
// the driver does not declare it.
func (d DriverCapabilities) Capability(dimension Dimension) (Capability, bool) {
	for _, capability := range d.Capabilities {
		if capability.Dimension == dimension {
			return capability, true
		}
	}
	return Capability{}, false
}

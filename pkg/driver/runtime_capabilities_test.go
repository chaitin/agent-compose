package driver

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

func capabilityFactsForTest(t *testing.T, driver string) RuntimeCapabilityFacts {
	t.Helper()
	facts, err := RuntimeCapabilityFactsFor(driver)
	if err != nil {
		t.Fatalf("RuntimeCapabilityFactsFor(%q) error = %v", driver, err)
	}
	return facts
}

func capabilityDimensionForTest(t *testing.T, facts RuntimeCapabilityFacts, dimension string) RuntimeCapabilityDimensionFacts {
	t.Helper()
	for _, candidate := range facts.Dimensions {
		if candidate.Dimension == dimension {
			return candidate
		}
	}
	t.Fatalf("driver %q does not declare dimension %q", facts.Driver, dimension)
	return RuntimeCapabilityDimensionFacts{}
}

// TestDockerCapabilitiesMatchHostConfig cross-asserts the Docker declaration
// against the exact HostConfig the driver hands to the Docker daemon. If a
// later change starts setting one of these fields, the declaration test fails
// until the report is updated.
func TestDockerCapabilitiesMatchHostConfig(t *testing.T) {
	facts := capabilityFactsForTest(t, RuntimeDriverDocker)
	hostConfig := dockerSandboxHostConfig(nil, nil, "default")

	if hostConfig.Memory != 0 || hostConfig.MemorySwap != 0 || hostConfig.NanoCPUs != 0 || hostConfig.CPUShares != 0 || hostConfig.PidsLimit != nil {
		t.Fatalf("HostConfig now sets resource limits %+v; the resource_limits declaration is stale", hostConfig)
	}
	if resources := capabilityDimensionForTest(t, facts, dimensionResourceLimits); resources.Enforced || resources.Mechanism != reasonNotConfigured {
		t.Fatalf("resource_limits declaration = %+v, want not_configured", resources)
	}

	if len(hostConfig.CapDrop) != 0 || len(hostConfig.CapAdd) != 0 || len(hostConfig.SecurityOpt) != 0 {
		t.Fatalf("HostConfig now sets capability/security options %+v", hostConfig)
	}
	if capabilityDrop := capabilityDimensionForTest(t, facts, dimensionCapabilityDrop); capabilityDrop.Enforced {
		t.Fatalf("capability_drop declaration = %+v, want not enforced", capabilityDrop)
	}

	if hostConfig.ReadonlyRootfs {
		t.Fatal("HostConfig.ReadonlyRootfs is now true; the read_only_rootfs declaration is stale")
	}
	if readOnly := capabilityDimensionForTest(t, facts, dimensionReadOnlyRootfs); readOnly.Enforced {
		t.Fatalf("read_only_rootfs declaration = %+v, want not enforced", readOnly)
	}

	if string(hostConfig.UsernsMode) != "" {
		t.Fatalf("HostConfig.UsernsMode = %q, want unset", hostConfig.UsernsMode)
	}
	if userns := capabilityDimensionForTest(t, facts, dimensionUserNamespaces); userns.Enforced {
		t.Fatalf("user_namespaces declaration = %+v, want not enforced", userns)
	}

	if len(hostConfig.DeviceRequests) != 0 || len(hostConfig.Devices) != 0 || hostConfig.Privileged {
		t.Fatalf("HostConfig now exposes devices or privileges %+v", hostConfig)
	}
	if devices := capabilityDimensionForTest(t, facts, dimensionGPUAndDevices); devices.Enforced {
		t.Fatalf("gpu_and_devices declaration = %+v, want not enforced", devices)
	}

	// A declared default-deny policy is refused at the top of EnsureSandbox
	// (RequireSandboxNetworkEnforcement), so the driver never selects Docker's
	// "none" network: that is a real outer deny, but it would also sever the
	// engine's own LLM facade and telemetry endpoints and leave a
	// healthy-looking sandbox that cannot call its model. The declaration must
	// therefore report no egress enforcement for Docker.
	if hostConfig.NetworkMode == "none" {
		t.Fatal("HostConfig.NetworkMode is unexpectedly none for the default topology")
	}
	egressCapability := capabilityDimensionForTest(t, facts, dimensionEgressPolicy)
	if egressCapability.Enforced || egressCapability.Mechanism != reasonNotConfigured {
		t.Fatalf("egress_policy declaration = %+v, want not enforced via %q", egressCapability, reasonNotConfigured)
	}
	if !strings.Contains(egressCapability.Observed, string(SandboxEgressStrengthNone)) {
		t.Fatalf("egress_policy Observed = %q, want it to report strength=%s", egressCapability.Observed, SandboxEgressStrengthNone)
	}
	if !strings.Contains(egressCapability.Observed, "refused before any container is created") {
		t.Fatalf("egress_policy Observed = %q, want it to state that a declared default-deny policy is refused", egressCapability.Observed)
	}
	if !strings.Contains(egressCapability.Observed, "LLM facade") {
		t.Fatalf("egress_policy Observed = %q, want it to name the engine endpoints the refusal protects", egressCapability.Observed)
	}
}

// TestMicroVMCapabilitiesMatchSharedResourceConfig asserts the enforced
// microVM resource declaration is backed by the shared config struct both
// drivers feed into their SDK option types.
func TestMicroVMCapabilitiesMatchSharedResourceConfig(t *testing.T) {
	resources := configuredSandboxResources(&appconfig.Config{})
	if resources.CPUs != appconfig.DefaultSandboxCPUs || resources.MemoryMiB != appconfig.DefaultSandboxMemoryMiB || resources.DiskSizeGB != appconfig.DefaultSandboxDiskSizeGB {
		t.Fatalf("configuredSandboxResources defaults = %+v, want the package defaults", resources)
	}
	if resources.CPUs == 0 || resources.MemoryMiB == 0 || resources.DiskSizeGB == 0 {
		t.Fatalf("configuredSandboxResources defaults = %+v, want non-zero limits", resources)
	}
	for _, driver := range []string{RuntimeDriverBoxlite, RuntimeDriverMicrosandbox} {
		facts := capabilityFactsForTest(t, driver)
		dimension := capabilityDimensionForTest(t, facts, dimensionResourceLimits)
		if !dimension.Enforced || dimension.Mechanism != mechanismSDKSandboxOptions {
			t.Fatalf("driver %q resource_limits = %+v, want enforced via %q", driver, dimension, mechanismSDKSandboxOptions)
		}
	}
}

// TestStoppedRuntimeRetentionDeclarationMatchesImplementation pins the
// declaration to the one function that already answers the question.
func TestStoppedRuntimeRetentionDeclarationMatchesImplementation(t *testing.T) {
	for _, driver := range []string{RuntimeDriverDocker, RuntimeDriverBoxlite, RuntimeDriverMicrosandbox, RuntimeDriverK8s} {
		facts := capabilityFactsForTest(t, driver)
		dimension := capabilityDimensionForTest(t, facts, dimensionStoppedRuntimeRetention)
		if want := RuntimeDriverSupportsStoppedRuntimeRetention(driver); dimension.Enforced != want {
			t.Fatalf("driver %q stopped_runtime_retention enforced = %v, want %v (declaration %+v)", driver, dimension.Enforced, want, dimension)
		}
	}
}

// TestRuntimeCapabilityFactsCoverEveryDimension keeps the driver-local
// dimension strings aligned with the capmatrix contract without importing it.
func TestRuntimeCapabilityFactsCoverEveryDimension(t *testing.T) {
	required := []string{
		dimensionResourceLimits, dimensionCapabilityDrop, dimensionReadOnlyRootfs, dimensionNonRootUser,
		dimensionUserNamespaces, dimensionEgressPolicy, dimensionCredentialPlaceholder,
		dimensionStoppedRuntimeRetention, dimensionCheckpointRestore, dimensionGPUAndDevices,
	}
	for _, driver := range []string{RuntimeDriverDocker, RuntimeDriverK8s, RuntimeDriverBoxlite, RuntimeDriverMicrosandbox} {
		facts := capabilityFactsForTest(t, driver)
		if facts.Driver != driver {
			t.Fatalf("facts driver = %q, want %q", facts.Driver, driver)
		}
		seen := make(map[string]struct{}, len(facts.Dimensions))
		for _, dimension := range facts.Dimensions {
			if _, duplicate := seen[dimension.Dimension]; duplicate {
				t.Fatalf("driver %q declares %q more than once", driver, dimension.Dimension)
			}
			seen[dimension.Dimension] = struct{}{}
			if dimension.Mechanism == "" {
				t.Fatalf("driver %q dimension %q has an empty mechanism", driver, dimension.Dimension)
			}
			if dimension.Enforced && IsNotEnforcedReasonForTest(dimension.Mechanism) {
				t.Fatalf("driver %q dimension %q is enforced but uses reason %q", driver, dimension.Dimension, dimension.Mechanism)
			}
			if !dimension.Enforced && !IsNotEnforcedReasonForTest(dimension.Mechanism) {
				t.Fatalf("driver %q dimension %q is not enforced but mechanism %q is not a reason", driver, dimension.Dimension, dimension.Mechanism)
			}
			if dimension.Observed == "" || dimension.DefaultBehavior == "" {
				t.Fatalf("driver %q dimension %q lacks observed/default evidence", driver, dimension.Dimension)
			}
		}
		for _, dimension := range required {
			if _, ok := seen[dimension]; !ok {
				t.Fatalf("driver %q does not declare required dimension %q", driver, dimension)
			}
		}
	}
}

// IsNotEnforcedReasonForTest mirrors capmatrix's closed reason set locally,
// because pkg/driver cannot import pkg/capmatrix.
func IsNotEnforcedReasonForTest(value string) bool {
	return value == reasonUnsupported || value == reasonNotConfigured
}

// TestRuntimeCapabilityFactsAreStaticAndProbeFree proves the declaration path
// never touches a runtime: it succeeds for drivers that are not compiled into
// this binary and while every runtime endpoint is deliberately unreachable.
func TestRuntimeCapabilityFactsAreStaticAndProbeFree(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing-kubeconfig"))
	t.Setenv("RUNTIME_DRIVER", RuntimeDriverBoxlite)

	for _, driver := range []string{RuntimeDriverDocker, RuntimeDriverK8s, RuntimeDriverBoxlite, RuntimeDriverMicrosandbox} {
		first := capabilityFactsForTest(t, driver)
		second := capabilityFactsForTest(t, driver)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("driver %q returned different declarations across calls", driver)
		}
	}
}

package driver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/driver/sandboxhost"
	containerapi "github.com/docker/docker/api/types/container"
)

func TestMicroVMSandboxResources(t *testing.T) {
	config := &appconfig.Config{SandboxCPUs: 4, SandboxMemoryMiB: 4096, SandboxDiskSizeGB: 6}

	undeclared, err := microVMSandboxResources(config, nil)
	if err != nil || undeclared.CPUs != 4 || undeclared.MemoryMiB != 4096 || undeclared.DiskSizeGB != 6 {
		t.Fatalf("undeclared = %+v, %v; want the daemon defaults", undeclared, err)
	}

	memoryOnly, err := microVMSandboxResources(config, &SandboxResourceLimits{MemoryMiB: 1024})
	if err != nil || memoryOnly.CPUs != 4 || memoryOnly.MemoryMiB != 1024 {
		t.Fatalf("memory only = %+v, %v; want default CPUs and declared memory", memoryOnly, err)
	}

	whole, err := microVMSandboxResources(config, &SandboxResourceLimits{CPUMillis: 2000})
	if err != nil || whole.CPUs != 2 || whole.MemoryMiB != 4096 {
		t.Fatalf("cpus only = %+v, %v; want declared vCPUs and default memory", whole, err)
	}

	if _, err := microVMSandboxResources(config, &SandboxResourceLimits{CPUMillis: 1500}); !errors.Is(err, ErrSandboxResourceLimitsUnavailable) {
		t.Fatalf("fractional error = %v, want ErrSandboxResourceLimitsUnavailable", err)
	}
	if _, err := microVMSandboxResources(config, &SandboxResourceLimits{CPUMillis: 256_000}); !errors.Is(err, ErrSandboxResourceLimitsUnavailable) {
		t.Fatalf("256 vCPUs error = %v, want ErrSandboxResourceLimitsUnavailable", err)
	}
}

func TestRequireSandboxResourceLimits(t *testing.T) {
	limits := &SandboxResourceLimits{CPUMillis: 2000, MemoryMiB: 2048}
	for _, driver := range []string{RuntimeDriverDocker, RuntimeDriverK8s, RuntimeDriverBoxlite, RuntimeDriverMicrosandbox} {
		if err := RequireSandboxResourceLimits(driver, limits); err != nil {
			t.Fatalf("RequireSandboxResourceLimits(%s) = %v, want every compiled driver to enforce resource_limits", driver, err)
		}
	}
	if err := RequireSandboxResourceLimits("mystery", limits); !errors.Is(err, ErrSandboxResourceLimitsUnavailable) {
		t.Fatalf("unknown driver error = %v, want ErrSandboxResourceLimitsUnavailable", err)
	}
	if err := RequireSandboxResourceLimits("mystery", nil); err != nil {
		t.Fatalf("undeclared limits on an unknown driver = %v, want nil (undeclared never blocks)", err)
	}
	for _, driver := range []string{RuntimeDriverBoxlite, RuntimeDriverMicrosandbox} {
		if err := RequireSandboxResourceLimits(driver, &SandboxResourceLimits{CPUMillis: 500}); !errors.Is(err, ErrSandboxResourceLimitsUnavailable) {
			t.Fatalf("fractional %s error = %v, want the daemon-side refusal", driver, err)
		}
	}
	if err := RequireSandboxResourceLimits(RuntimeDriverDocker, &SandboxResourceLimits{CPUMillis: 500}); err != nil {
		t.Fatalf("fractional docker = %v, want accepted", err)
	}
}

// The gate must agree with the capability matrix: a driver the matrix reports
// as enforcing resource_limits is exactly a driver the gate accepts.
func TestRequireSandboxResourceLimitsMatchesCapabilityFacts(t *testing.T) {
	for _, driver := range []string{RuntimeDriverDocker, RuntimeDriverK8s, RuntimeDriverBoxlite, RuntimeDriverMicrosandbox} {
		facts := capabilityFactsForTest(t, driver)
		enforced := capabilityDimensionForTest(t, facts, dimensionResourceLimits).Enforced
		accepted := RequireSandboxResourceLimits(driver, &SandboxResourceLimits{CPUMillis: 1000}) == nil
		if enforced != accepted {
			t.Fatalf("driver %s: matrix enforced=%v but gate accepted=%v", driver, enforced, accepted)
		}
	}
}

type recordingContainerRemover struct{ removed []string }

func (r *recordingContainerRemover) ContainerRemove(_ context.Context, id string, _ containerapi.RemoveOptions) error {
	r.removed = append(r.removed, id)
	return nil
}

func dockerInspectWith(hostConfig *containerapi.HostConfig) containerapi.InspectResponse {
	return containerapi.InspectResponse{ContainerJSONBase: &containerapi.ContainerJSONBase{ID: "container-1", HostConfig: hostConfig}}
}

func TestValidateCreatedDockerResourceLimits(t *testing.T) {
	limits := &SandboxResourceLimits{CPUMillis: 1000, MemoryMiB: 512}
	applied := &containerapi.HostConfig{}
	applyDockerResourceLimits(applied, limits)

	declaring := &Sandbox{Summary: SandboxSummary{ID: "sandbox-1"}, ResourceLimits: limits}
	remover := &recordingContainerRemover{}
	if err := validateCreatedDockerResourceLimits(context.Background(), remover, declaring, dockerInspectWith(applied), nil); err != nil || len(remover.removed) != 0 {
		t.Fatalf("applied limits = %v, removed %v; want accepted and kept", err, remover.removed)
	}

	// The Docker daemon resets Memory to zero when the host has no memory
	// controller, and reports it only as a warning.
	discarded := *applied
	discarded.Memory, discarded.MemorySwap = 0, 0
	warning := "Your kernel does not support memory limit capabilities or the cgroup is not mounted. Limitation discarded."
	err := validateCreatedDockerResourceLimits(context.Background(), remover, declaring, dockerInspectWith(&discarded), []string{warning})
	if !errors.Is(err, ErrSandboxResourceLimitsUnavailable) || !strings.Contains(err.Error(), "memory") || !strings.Contains(err.Error(), "Limitation discarded") {
		t.Fatalf("discarded memory error = %v, want ErrSandboxResourceLimitsUnavailable naming memory and the warning", err)
	}
	if len(remover.removed) != 1 || remover.removed[0] != "container-1" {
		t.Fatalf("removed = %v, want the unconstrained container removed", remover.removed)
	}

	remover = &recordingContainerRemover{}
	if err := validateCreatedDockerResourceLimits(context.Background(), remover, &Sandbox{Summary: SandboxSummary{ID: "sandbox-1"}}, dockerInspectWith(&containerapi.HostConfig{}), nil); err != nil || len(remover.removed) != 0 {
		t.Fatalf("undeclared = %v, removed %v; want no check at all", err, remover.removed)
	}
}

func TestSandboxHostMarshalCarriesResourceLimits(t *testing.T) {
	sandbox := &Sandbox{Summary: SandboxSummary{ID: "sandbox-1"}, ResourceLimits: &SandboxResourceLimits{CPUMillis: 1500, MemoryMiB: 2048}}
	got := unmarshalSandbox(marshalSandbox(sandbox))
	if got.ResourceLimits == nil || *got.ResourceLimits != *sandbox.ResourceLimits {
		t.Fatalf("round-tripped limits = %+v, want %+v", got.ResourceLimits, sandbox.ResourceLimits)
	}
	undeclared := unmarshalSandbox(marshalSandbox(&Sandbox{Summary: SandboxSummary{ID: "sandbox-2"}}))
	if undeclared.ResourceLimits != nil {
		t.Fatalf("undeclared limits round-tripped as %+v, want nil", undeclared.ResourceLimits)
	}
}

func TestRequireResourceLimitsFeature(t *testing.T) {
	limits := &SandboxResourceLimits{MemoryMiB: 1024}
	if err := requireResourceLimitsFeature(false, limits); !errors.Is(err, ErrSandboxResourceLimitsUnavailable) {
		t.Fatalf("host without the feature error = %v, want a fail-closed refusal", err)
	}
	if err := requireResourceLimitsFeature(true, limits); err != nil {
		t.Fatalf("host with the feature error = %v, want nil", err)
	}
	if err := requireResourceLimitsFeature(false, nil); err != nil {
		t.Fatalf("undeclared on any host = %v, want nil", err)
	}
}

// A host that gains the feature must stay usable by an older daemon, so the
// feature is a handshake field, not a protocol bump.
func TestSandboxHostAdvertisesResourceLimitsWithoutAProtocolBump(t *testing.T) {
	if sandboxHostProtocolVersion != sandboxHostDenyProtocolVersion {
		t.Fatalf("protocol version = %d; resource limits must not bump it past %d, or a daemon one version behind refuses the host", sandboxHostProtocolVersion, sandboxHostDenyProtocolVersion)
	}
	handshake := newSandboxHostHandshake(&sandboxhost.HandshakeResponse{ProtocolVersion: sandboxHostProtocolVersion, Features: sandboxHostFeatures})
	if !handshake.features[sandboxHostFeatureResourceLimits] {
		t.Fatalf("handshake features = %v, want %q", handshake.features, sandboxHostFeatureResourceLimits)
	}
	old := newSandboxHostHandshake(&sandboxhost.HandshakeResponse{ProtocolVersion: sandboxHostProtocolVersion})
	if old.features[sandboxHostFeatureResourceLimits] {
		t.Fatal("a host that sent no features must not read as supporting resource limits")
	}
}

// A refusal raised inside the sandbox-host must reach the daemon as the
// resource-limits sentinel, not as the guest-not-ready sentinel that shares
// FailedPrecondition, or the daemon would retry a request that cannot succeed.
func TestSandboxHostStatusCarriesResourceLimitsRefusal(t *testing.T) {
	refusal := fmt.Errorf("%w: declared 1500 millicpus is not a whole number of vCPUs", ErrSandboxResourceLimitsUnavailable)
	got := fromGRPCError(sandboxHostStatus(refusal))
	if !errors.Is(got, ErrSandboxResourceLimitsUnavailable) || errors.Is(got, ErrGuestRuntimeNotReady) {
		t.Fatalf("decoded refusal = %v, want ErrSandboxResourceLimitsUnavailable only", got)
	}
	notReady := fromGRPCError(sandboxHostStatus(fmt.Errorf("%w: booting", ErrGuestRuntimeNotReady)))
	if !errors.Is(notReady, ErrGuestRuntimeNotReady) || errors.Is(notReady, ErrSandboxResourceLimitsUnavailable) {
		t.Fatalf("decoded not-ready = %v, want ErrGuestRuntimeNotReady only", notReady)
	}
}

func TestRequireReusedRuntimeResourceLimits(t *testing.T) {
	declared := &SandboxResourceLimits{CPUMillis: 2000, MemoryMiB: 1024}
	if err := requireReusedRuntimeResourceLimits("runtime r1", declared, *declared); err != nil {
		t.Fatalf("matching runtime = %v, want reused", err)
	}
	if err := requireReusedRuntimeResourceLimits("runtime r1", nil, SandboxResourceLimits{}); err != nil {
		t.Fatalf("undeclared = %v, want any runtime reused", err)
	}
	// Only declared dimensions are compared: memory is not declared here.
	if err := requireReusedRuntimeResourceLimits("runtime r1", &SandboxResourceLimits{CPUMillis: 2000}, SandboxResourceLimits{CPUMillis: 2000, MemoryMiB: 4096}); err != nil {
		t.Fatalf("undeclared memory compared: %v", err)
	}
	err := requireReusedRuntimeResourceLimits("docker container c1", declared, SandboxResourceLimits{})
	if !errors.Is(err, ErrSandboxResourceLimitsUnavailable) || !strings.Contains(err.Error(), "docker container c1") ||
		!strings.Contains(err.Error(), "cpus unlimited (declared 2)") || !strings.Contains(err.Error(), "memory unlimited (declared 1024Mi)") ||
		!strings.Contains(err.Error(), "remove the sandbox") {
		t.Fatalf("unlimited runtime error = %v, want a refusal naming the runtime, both dimensions, and the fix", err)
	}
	if err := requireReusedRuntimeResourceLimits("r", &SandboxResourceLimits{CPUMillis: 500}, SandboxResourceLimits{CPUMillis: 1500}); err == nil || !strings.Contains(err.Error(), "cpus 1.500 (declared 0.500)") {
		t.Fatalf("fractional mismatch error = %v", err)
	}
}

// TestValidateReusedDockerContainerResourceLimits covers the reuse path: a
// container created before the declaration existed carries no limit and must
// not be reused for a declaring sandbox.
func TestValidateReusedDockerContainerResourceLimits(t *testing.T) {
	limits := &SandboxResourceLimits{CPUMillis: 1000, MemoryMiB: 512}
	declaring := &Sandbox{Summary: SandboxSummary{ID: "sandbox-1"}, ResourceLimits: limits}

	unlimited := dockerInspectWith(&containerapi.HostConfig{})
	if err := validateReusedDockerContainer(unlimited, declaring, false); !errors.Is(err, ErrSandboxResourceLimitsUnavailable) {
		t.Fatalf("reusing an unlimited container = %v, want a refusal", err)
	}
	limited := &containerapi.HostConfig{}
	applyDockerResourceLimits(limited, limits)
	if err := validateReusedDockerContainer(dockerInspectWith(limited), declaring, false); err != nil {
		t.Fatalf("reusing a container with the declared limits = %v, want reused", err)
	}
	// A memory limit host swap can stretch is not the declared ceiling.
	swappable := *limited
	swappable.MemorySwap = 0
	if err := validateReusedDockerContainer(dockerInspectWith(&swappable), declaring, false); !errors.Is(err, ErrSandboxResourceLimitsUnavailable) {
		t.Fatalf("reusing a swappable container = %v, want a refusal", err)
	}
	if err := validateReusedDockerContainer(unlimited, &Sandbox{Summary: SandboxSummary{ID: "sandbox-1"}}, false); err != nil {
		t.Fatalf("undeclared sandbox = %v, want any container reused", err)
	}
}

func TestBoxliteBoxResourceLimits(t *testing.T) {
	if got := boxliteBoxResourceLimits(2, 2048); got != (SandboxResourceLimits{CPUMillis: 2000, MemoryMiB: 2048}) {
		t.Fatalf("boxliteBoxResourceLimits(2, 2048) = %+v", got)
	}
	if got := boxliteBoxResourceLimits(0, 0); got.Declared() {
		t.Fatalf("an unreported size = %+v, want zero", got)
	}
}

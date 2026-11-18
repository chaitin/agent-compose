package driver

import (
	"context"
	"errors"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	containerapi "github.com/docker/docker/api/types/container"
)

func TestMicroVMSandboxResources(t *testing.T) {
	config := &appconfig.Config{SandboxCPUs: 4, SandboxMemoryMiB: 4096, SandboxDiskSizeGB: 6}

	undeclared, err := microVMSandboxResources(config, nil, true)
	if err != nil || undeclared.CPUs != 4 || undeclared.CPUMillis != 4000 || undeclared.MemoryMiB != 4096 || undeclared.DiskSizeGB != 6 {
		t.Fatalf("undeclared = %+v, %v; want the daemon defaults", undeclared, err)
	}

	memoryOnly, err := microVMSandboxResources(config, &SandboxResourceLimits{MemoryMiB: 1024}, true)
	if err != nil || memoryOnly.CPUs != 4 || memoryOnly.MemoryMiB != 1024 {
		t.Fatalf("memory only = %+v, %v; want default CPUs and declared memory", memoryOnly, err)
	}

	fractional, err := microVMSandboxResources(config, &SandboxResourceLimits{CPUMillis: 1500}, false)
	if err != nil || fractional.CPUMillis != 1500 || fractional.CPUs != 2 {
		t.Fatalf("fractional (boxlite) = %+v, %v; want exact millis and a rounded-up vCPU count", fractional, err)
	}

	if _, err := microVMSandboxResources(config, &SandboxResourceLimits{CPUMillis: 1500}, true); !errors.Is(err, ErrSandboxResourceLimitsUnavailable) {
		t.Fatalf("fractional on a whole-vCPU SDK error = %v, want ErrSandboxResourceLimitsUnavailable", err)
	}
	if _, err := microVMSandboxResources(config, &SandboxResourceLimits{CPUMillis: 256_000}, false); !errors.Is(err, ErrSandboxResourceLimitsUnavailable) {
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
	if err := RequireSandboxResourceLimits(RuntimeDriverMicrosandbox, &SandboxResourceLimits{CPUMillis: 500}); !errors.Is(err, ErrSandboxResourceLimitsUnavailable) {
		t.Fatalf("fractional microsandbox error = %v, want the daemon-side refusal", err)
	}
	if err := RequireSandboxResourceLimits(RuntimeDriverBoxlite, &SandboxResourceLimits{CPUMillis: 500}); err != nil {
		t.Fatalf("fractional boxlite = %v, want accepted", err)
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

func TestRequireResourceLimitsProtocolVersion(t *testing.T) {
	limits := &SandboxResourceLimits{MemoryMiB: 1024}
	if err := requireResourceLimitsProtocolVersion(sandboxHostResourceLimitsProtocolVersion-1, limits); !errors.Is(err, ErrSandboxResourceLimitsUnavailable) {
		t.Fatalf("old host error = %v, want a fail-closed refusal", err)
	}
	if err := requireResourceLimitsProtocolVersion(sandboxHostResourceLimitsProtocolVersion, limits); err != nil {
		t.Fatalf("current host error = %v, want nil", err)
	}
	if err := requireResourceLimitsProtocolVersion(0, nil); err != nil {
		t.Fatalf("undeclared on any host = %v, want nil", err)
	}
}

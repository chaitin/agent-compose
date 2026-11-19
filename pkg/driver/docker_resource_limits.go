package driver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	containerapi "github.com/docker/docker/api/types/container"
)

// applyDockerResourceLimits writes a declared limit into the container's
// HostConfig. An undeclared limit leaves every resource field at its zero
// value, which is Docker's "unlimited" and the driver's behavior before
// policy.resources existed.
//
// MemorySwap equals Memory, so the ceiling cannot be stretched with host swap:
// a sandbox that declared 2Gi is OOM-killed at 2Gi instead of paging on.
func applyDockerResourceLimits(hostConfig *containerapi.HostConfig, limits *SandboxResourceLimits) {
	if hostConfig == nil || !limits.Declared() {
		return
	}
	if limits.CPUMillis > 0 {
		hostConfig.NanoCPUs = limits.CPUMillis * 1_000_000
	}
	if limits.MemoryMiB > 0 {
		memory := int64(limits.MemoryMiB) * mib
		hostConfig.Memory = memory
		hostConfig.MemorySwap = memory
	}
}

// dockerContainerResourceLimits reads the limit a container actually carries.
// A memory limit that host swap can stretch is not the declared ceiling, so it
// reads as unlimited.
func dockerContainerResourceLimits(info containerapi.InspectResponse) SandboxResourceLimits {
	if info.ContainerJSONBase == nil || info.HostConfig == nil {
		return SandboxResourceLimits{}
	}
	actual := SandboxResourceLimits{CPUMillis: info.HostConfig.NanoCPUs / 1_000_000}
	if memory := info.HostConfig.Memory; memory > 0 && info.HostConfig.MemorySwap == memory && memory%mib == 0 {
		actual.MemoryMiB = uint32(memory / mib)
	}
	return actual
}

// requireReusedDockerResourceLimits refuses to reuse a container whose limit
// differs from the declaration (see requireReusedRuntimeResourceLimits).
func requireReusedDockerResourceLimits(info containerapi.InspectResponse, limits *SandboxResourceLimits) error {
	return requireReusedRuntimeResourceLimits("docker container "+info.ID, limits, dockerContainerResourceLimits(info))
}

// dockerResourceLimitsApplied reports whether the created container carries
// the declared limit. The Docker daemon accepts a memory limit on a host
// without the cgroup memory controller (rootless Docker without cgroup v2
// delegation, for example), returns only a create warning, and stores the
// container with Memory reset to zero. Reading the limit back from the
// container is what tells "applied" from "silently discarded".
func dockerResourceLimitsApplied(info containerapi.InspectResponse, limits *SandboxResourceLimits) error {
	if !limits.Declared() {
		return nil
	}
	if info.ContainerJSONBase == nil || info.HostConfig == nil {
		return fmt.Errorf("created container reports no HostConfig")
	}
	var missing []string
	if limits.CPUMillis > 0 && info.HostConfig.NanoCPUs != limits.CPUMillis*1_000_000 {
		missing = append(missing, fmt.Sprintf("cpus (NanoCPUs=%d, want %d)", info.HostConfig.NanoCPUs, limits.CPUMillis*1_000_000))
	}
	if limits.MemoryMiB > 0 {
		want := int64(limits.MemoryMiB) * mib
		if info.HostConfig.Memory != want || info.HostConfig.MemorySwap != want {
			missing = append(missing, fmt.Sprintf("memory (Memory=%d MemorySwap=%d, want %d)", info.HostConfig.Memory, info.HostConfig.MemorySwap, want))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("the docker daemon did not apply %s", strings.Join(missing, ", "))
}

// validateCreatedDockerResourceLimits removes a just-created container whose
// declared limit the Docker daemon discarded. A declared limit is a hard
// requirement, so the sandbox must not start without it.
func validateCreatedDockerResourceLimits(ctx context.Context, remover dockerContainerRemover, sandbox *Sandbox, info containerapi.InspectResponse, warnings []string) error {
	if sandbox == nil {
		return nil
	}
	applied := dockerResourceLimitsApplied(info, sandbox.ResourceLimits)
	if applied == nil {
		return nil
	}
	cause := fmt.Errorf("%w for sandbox %s: %w", ErrSandboxResourceLimitsUnavailable, sandbox.Summary.ID, applied)
	if len(warnings) > 0 {
		cause = fmt.Errorf("%w (docker warnings: %s)", cause, strings.Join(warnings, "; "))
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dockerStopFallbackActionTimeout)
	defer cancel()
	if err := remover.ContainerRemove(cleanupCtx, info.ID, containerapi.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !isDockerNotFound(err) {
		return errors.Join(cause, fmt.Errorf("remove docker container without its resource limits: %w", err))
	}
	return cause
}

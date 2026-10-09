package driver

// securityContextObserved carries the per-dimension evidence string for the
// four security-context dimensions.
type securityContextObserved struct {
	CapabilityDrop string
	ReadOnlyRootfs string
	NonRootUser    string
	UserNamespaces string
}

// securityContextFacts builds the four security-context dimension
// declarations. They share one reason because a driver either exposes a
// container security context or it does not.
func securityContextFacts(reason string, observed securityContextObserved) []RuntimeCapabilityDimensionFacts {
	return []RuntimeCapabilityDimensionFacts{
		{
			Dimension:       dimensionCapabilityDrop,
			Mechanism:       reason,
			Observed:        observed.CapabilityDrop,
			DefaultBehavior: "the workload keeps the image's default Linux capability set",
		},
		{
			Dimension:       dimensionReadOnlyRootfs,
			Mechanism:       reason,
			Observed:        observed.ReadOnlyRootfs,
			DefaultBehavior: "the sandbox root filesystem stays writable for the workload",
		},
		{
			Dimension:       dimensionNonRootUser,
			Mechanism:       reason,
			Observed:        observed.NonRootUser,
			DefaultBehavior: "the workload runs as the image's default user",
		},
		{
			Dimension:       dimensionUserNamespaces,
			Mechanism:       reason,
			Observed:        observed.UserNamespaces,
			DefaultBehavior: "no user-namespace remapping is applied to the workload",
		},
	}
}

// dockerRuntimeCapabilityFacts declares what the Docker driver actually writes
// into a sandbox container today.
//
// Every "not_configured" entry below corresponds to a field the driver leaves
// at its zero value in dockerSandboxHostConfig or the container Config; the
// cross-assertion test verifies that directly against the config struct.
func dockerRuntimeCapabilityFacts() RuntimeCapabilityFacts {
	dimensions := []RuntimeCapabilityDimensionFacts{
		{
			Dimension:       dimensionResourceLimits,
			Mechanism:       reasonNotConfigured,
			Observed:        "dockerSandboxHostConfig sets no Memory, MemorySwap, NanoCPUs, CpuShares, or PidsLimit",
			DefaultBehavior: "an undeclared resource limit is not applied to the sandbox container",
		},
	}
	dimensions = append(dimensions, securityContextFacts(reasonNotConfigured, securityContextObserved{
		CapabilityDrop: "HostConfig.SecurityOpt and HostConfig.CapDrop are unset",
		ReadOnlyRootfs: "HostConfig.ReadonlyRootfs is false",
		NonRootUser:    "the sandbox container Config.User is unset and the exec control path runs as User \"0\"",
		UserNamespaces: "HostConfig.UsernsMode is unset",
	})...)
	dimensions = append(dimensions,
		RuntimeCapabilityDimensionFacts{
			Dimension:       dimensionEgressPolicy,
			Mechanism:       reasonNotConfigured,
			Observed:        "HostConfig.NetworkMode joins the daemon's own container network (or default) and no egress rule is written",
			DefaultBehavior: "outbound access from the sandbox is unrestricted",
		},
		RuntimeCapabilityDimensionFacts{
			Dimension:       dimensionCredentialPlaceholder,
			Mechanism:       reasonUnsupported,
			Observed:        "the container path has no placeholder substitution: guest environment values are written verbatim",
			DefaultBehavior: "a credential reaches the sandbox as the literal value the declaration provided",
		},
		stoppedRuntimeRetentionFacts(RuntimeDriverDocker, mechanismStoppedContainerRuntime, ""),
		RuntimeCapabilityDimensionFacts{
			Dimension:       dimensionCheckpointRestore,
			Mechanism:       reasonUnsupported,
			Observed:        "no checkpoint or restore path exists for Docker containers",
			DefaultBehavior: "a sandbox cannot be checkpointed; the running process ends when it stops",
		},
		RuntimeCapabilityDimensionFacts{
			Dimension:       dimensionGPUAndDevices,
			Mechanism:       reasonNotConfigured,
			Observed:        "HostConfig.DeviceRequests and HostConfig.Devices are unset",
			DefaultBehavior: "no host GPU or device node is exposed to the sandbox",
		},
	)
	return RuntimeCapabilityFacts{Driver: RuntimeDriverDocker, Dimensions: dimensions}
}

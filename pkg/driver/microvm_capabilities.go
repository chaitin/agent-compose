package driver

// microVMObserved carries the driver-specific evidence strings for the shared
// microVM capability declaration.
type microVMObserved struct {
	ResourceLimits        string
	SecurityContext       securityContextObserved
	Egress                string
	CredentialPlaceholder string
}

// microVMRuntimeCapabilityFacts declares the capabilities shared by the two
// microVM drivers. The VM boundary itself is not a reported dimension here:
// this matrix reports what the engine binds through the SDK, not what the
// hypervisor does implicitly.
func microVMRuntimeCapabilityFacts(driver string, observed microVMObserved) RuntimeCapabilityFacts {
	dimensions := []RuntimeCapabilityDimensionFacts{
		{
			Dimension:       dimensionResourceLimits,
			Enforced:        true,
			Mechanism:       mechanismSDKSandboxOptions,
			Observed:        observed.ResourceLimits,
			DefaultBehavior: "the daemon-wide default of 4 CPUs, 4096 MiB memory, and 6 GiB disk applies when no explicit sandbox resource is configured",
		},
	}
	dimensions = append(dimensions, securityContextFacts(reasonUnsupported, observed.SecurityContext)...)
	dimensions = append(dimensions,
		RuntimeCapabilityDimensionFacts{
			Dimension:       dimensionEgressPolicy,
			Mechanism:       reasonNotConfigured,
			Observed:        observed.Egress,
			DefaultBehavior: "outbound access from the guest is unrestricted; an undeclared network policy is not deny, and when a sandbox declares default: deny the engine auto-allows its own runtime LLM facade and telemetry endpoints as non-overridable engine-side rules",
		},
		RuntimeCapabilityDimensionFacts{
			Dimension:       dimensionCredentialPlaceholder,
			Mechanism:       reasonNotConfigured,
			Observed:        observed.CredentialPlaceholder,
			DefaultBehavior: "a credential reaches the guest as the literal value the declaration provided",
		},
		stoppedRuntimeRetentionFacts(driver, mechanismStoppedMicroVMRuntime, ""),
		RuntimeCapabilityDimensionFacts{
			Dimension:       dimensionCheckpointRestore,
			Mechanism:       reasonUnsupported,
			Observed:        "no checkpoint or restore path is bound for the microVM SDK",
			DefaultBehavior: "a sandbox cannot be checkpointed; stopping ends the running guest",
		},
		RuntimeCapabilityDimensionFacts{
			Dimension:       dimensionGPUAndDevices,
			Mechanism:       reasonUnsupported,
			Observed:        "the microVM SDK exposes no GPU or device passthrough the engine binds",
			DefaultBehavior: "no host GPU or device node is exposed to the guest",
		},
	)
	return RuntimeCapabilityFacts{Driver: driver, Dimensions: dimensions}
}

package driver

// microVMObserved carries the driver-specific evidence strings for the shared
// microVM capability declaration.
type microVMObserved struct {
	ResourceLimits        string
	SecurityContext       securityContextObserved
	Egress                string
	CredentialPlaceholder string
	// CheckpointReason and CheckpointObserved describe the driver's own
	// checkpoint/restore surface. One microVM SDK exposes one and the other does
	// not, so the reason is per driver rather than shared.
	CheckpointReason   string
	CheckpointObserved string
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
	// The microVM SDKs expose no per-dimension capability-drop, read-only
	// rootfs, or user-namespace option. They do expose a per-command user
	// override, which the drivers leave unset, so non_root_user is
	// not_configured rather than unsupported.
	dimensions = append(dimensions, securityContextFacts(securityContextReasons{
		CapabilityDrop: reasonUnsupported,
		ReadOnlyRootfs: reasonUnsupported,
		NonRootUser:    reasonNotConfigured,
		UserNamespaces: reasonUnsupported,
	}, observed.SecurityContext)...)
	dimensions = append(dimensions,
		RuntimeCapabilityDimensionFacts{
			Dimension:       dimensionEgressPolicy,
			Mechanism:       reasonNotConfigured,
			Observed:        observed.Egress,
			DefaultBehavior: "outbound access from the guest is unrestricted",
		},
		credentialPlaceholderFacts(observed.CredentialPlaceholder),
		stoppedRuntimeRetentionFacts(driver, mechanismStoppedMicroVMRuntime, ""),
		RuntimeCapabilityDimensionFacts{
			Dimension:       dimensionCheckpointRestore,
			Mechanism:       observed.CheckpointReason,
			Observed:        observed.CheckpointObserved,
			DefaultBehavior: "the engine has no checkpoint or restore path; stopping a sandbox ends the running guest",
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

package driver

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
	dimensions = append(dimensions, securityContextFacts(uniformSecurityContextReasons(reasonNotConfigured), securityContextObserved{
		CapabilityDrop: "dockerSandboxHostConfig sets CapDrop to exactly [NET_RAW] and leaves every other Docker default capability in place",
		ReadOnlyRootfs: "HostConfig.ReadonlyRootfs is false",
		NonRootUser:    "the sandbox container Config.User is unset and the exec control path runs as User \"0\"",
		UserNamespaces: "HostConfig.UsernsMode is unset",
	}, mechanismDockerCapabilityDrop)...)
	dimensions = append(dimensions,
		egressPolicyFacts(RuntimeDriverDocker),
		credentialPlaceholderFacts("the container path has no credential substitution of its own: every other declared environment value is written verbatim"),
		stoppedRuntimeRetentionFacts(RuntimeDriverDocker, mechanismStoppedContainerRuntime, ""),
		RuntimeCapabilityDimensionFacts{
			Dimension:       dimensionCheckpointRestore,
			Mechanism:       reasonNotConfigured,
			Observed:        "the Docker API exposes CheckpointCreate, CheckpointList, CheckpointDelete, and a start-from-checkpoint option; the driver binds none of them",
			DefaultBehavior: "the engine has no checkpoint or restore path; the running process ends when the sandbox stops",
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

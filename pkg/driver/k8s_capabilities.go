package driver

// k8sRuntimeCapabilityFacts declares what the Kubernetes driver actually
// writes into a sandbox Pod today.
//
// Every "not_configured" entry below is verified by the cross-assertion test
// against the Pod object createPod returns.
func k8sRuntimeCapabilityFacts() RuntimeCapabilityFacts {
	dimensions := []RuntimeCapabilityDimensionFacts{
		{
			Dimension:       dimensionResourceLimits,
			Mechanism:       reasonNotConfigured,
			Observed:        "the Pod container declares no ResourceRequirements requests or limits",
			DefaultBehavior: "an undeclared resource limit is not applied to the Pod",
		},
	}
	dimensions = append(dimensions, securityContextFacts(uniformSecurityContextReasons(reasonNotConfigured), securityContextObserved{
		CapabilityDrop: "the container SecurityContext sets Capabilities.Drop to exactly [NET_RAW] and leaves every other runtime default capability in place",
		ReadOnlyRootfs: "the container SecurityContext sets only capabilities and leaves ReadonlyRootfs unset, so its root filesystem stays writable",
		NonRootUser:    "no PodSecurityContext runAsNonRoot or runAsUser is set, and ServiceAccountName is empty",
		UserNamespaces: "no PodSecurityContext is set, so no user-namespace remapping is requested",
	}, mechanismK8sCapabilityDrop)...)
	dimensions = append(dimensions,
		egressPolicyFacts(RuntimeDriverK8s),
		credentialPlaceholderFacts("the Pod path has no credential substitution of its own: every other declared environment value is written verbatim"),
		stoppedRuntimeRetentionFacts(RuntimeDriverK8s, "", "stopping a Pod deletes it and resume creates a new Pod from the image"),
		RuntimeCapabilityDimensionFacts{
			Dimension:       dimensionCheckpointRestore,
			Mechanism:       reasonUnsupported,
			Observed:        "no checkpoint or restore path exists for Pods",
			DefaultBehavior: "a sandbox cannot be checkpointed; the Pod is recreated from the image on resume",
		},
		RuntimeCapabilityDimensionFacts{
			Dimension:       dimensionGPUAndDevices,
			Mechanism:       reasonNotConfigured,
			Observed:        "the Pod container declares no nvidia.com/gpu or other device resource limit",
			DefaultBehavior: "no host GPU or device node is exposed to the Pod",
		},
	)
	return RuntimeCapabilityFacts{Driver: RuntimeDriverK8s, Dimensions: dimensions}
}

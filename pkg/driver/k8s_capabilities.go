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
	dimensions = append(dimensions, securityContextFacts(reasonNotConfigured, securityContextObserved{
		CapabilityDrop: "neither the Pod nor the container declares a SecurityContext",
		ReadOnlyRootfs: "the container SecurityContext is unset, so its root filesystem stays writable",
		NonRootUser:    "no PodSecurityContext runAsNonRoot or runAsUser is set, and ServiceAccountName is empty",
		UserNamespaces: "no PodSecurityContext is set, so no user-namespace remapping is requested",
	})...)
	dimensions = append(dimensions,
		RuntimeCapabilityDimensionFacts{
			Dimension:       dimensionEgressPolicy,
			Mechanism:       reasonNotConfigured,
			Observed:        "createPod creates only a Pod; no NetworkPolicy is created and Pod egress is unrestricted",
			DefaultBehavior: "outbound access from the Pod is unrestricted",
		},
		RuntimeCapabilityDimensionFacts{
			Dimension:       dimensionCredentialPlaceholder,
			Mechanism:       reasonUnsupported,
			Observed:        "the Pod path has no placeholder substitution: guest environment values are written verbatim",
			DefaultBehavior: "a credential reaches the Pod as the literal value the declaration provided",
		},
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

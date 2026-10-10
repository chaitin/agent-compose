package driver

// microsandboxRuntimeCapabilityFacts declares what the Microsandbox driver
// binds through its Go SDK options today.
func microsandboxRuntimeCapabilityFacts() RuntimeCapabilityFacts {
	return microVMRuntimeCapabilityFacts(RuntimeDriverMicrosandbox, microVMObserved{
		ResourceLimits: "microsandbox.WithMemory and microsandbox.WithCPUs receive configuredSandboxResources, and bind mounts receive its disk quota",
		SecurityContext: securityContextObserved{
			CapabilityDrop: "the bound Go SDK option surface has no capability-drop option",
			ReadOnlyRootfs: "the bound Go SDK option surface has no read-only rootfs option",
			NonRootUser:    "the guest workload runs as root; the bound Go SDK option surface has no user override",
			UserNamespaces: "the bound Go SDK option surface has no user-namespace option; isolation comes from the VM boundary instead",
		},
		CredentialPlaceholder: "the SDK exposes SecretEntry but the engine binds no secret",
	})
}

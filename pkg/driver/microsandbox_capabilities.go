package driver

// microsandboxRuntimeCapabilityFacts declares what the Microsandbox driver
// binds through its Go SDK options today.
func microsandboxRuntimeCapabilityFacts() RuntimeCapabilityFacts {
	return microVMRuntimeCapabilityFacts(RuntimeDriverMicrosandbox, microVMObserved{
		ResourceLimits: "microsandbox.WithMemory and microsandbox.WithCPUs receive configuredSandboxResources, and bind mounts receive its disk quota",
		SecurityContext: securityContextObserved{
			CapabilityDrop: "the bound Go SDK option surface has no capability-drop option",
			ReadOnlyRootfs: "the bound Go SDK option surface has no read-only rootfs option",
			NonRootUser:    "the driver binds no microsandbox.WithUser, so the guest runs as root; the Go SDK exposes WithUser and WithExecUser",
			UserNamespaces: "the bound Go SDK option surface has no user-namespace option; isolation comes from the VM boundary instead",
		},
		Egress:                "the driver constructs microsandbox.NetworkPolicy.AllowAll and disables DNS rebind protection",
		CredentialPlaceholder: "the Go SDK exposes microsandbox.WithSecrets but the driver binds no secret",
		CheckpointReason:      reasonNotConfigured,
		CheckpointObserved:    "the Go SDK exposes SandboxHandle.Snapshot and microsandbox.RestoreSandbox but the driver binds neither",
	})
}

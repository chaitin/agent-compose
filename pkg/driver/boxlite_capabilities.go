package driver

// boxliteRuntimeCapabilityFacts declares what the BoxLite driver binds through
// its C SDK options today.
func boxliteRuntimeCapabilityFacts() RuntimeCapabilityFacts {
	return microVMRuntimeCapabilityFacts(RuntimeDriverBoxlite, microVMObserved{
		ResourceLimits: "boxlite_options_set_cpus, boxlite_options_set_memory, and boxlite_options_set_disk_size_gb receive configuredSandboxResources",
		SecurityContext: securityContextObserved{
			CapabilityDrop: "the bound C option surface has no capability-drop call",
			ReadOnlyRootfs: "the bound C option surface has no read-only rootfs call",
			NonRootUser:    "the guest workload runs as root; the bound C option surface has no user override",
			UserNamespaces: "the bound C option surface has no user-namespace option; isolation comes from the VM boundary instead",
		},
		CredentialPlaceholder: "the SDK exposes boxlite_options_add_secret but the engine binds no secret",
	})
}

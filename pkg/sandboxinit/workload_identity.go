package sandboxinit

// Environment variables that select the optional non-root workload identity.
// The constants live in a platform-neutral file because the daemon maps its
// configuration into them on every platform, while only the Linux init consumes
// them.
const (
	// WorkloadUIDEnvVar selects the numeric uid the exec trampoline switches to
	// immediately before exec. Zero (the default) leaves the workload as root.
	WorkloadUIDEnvVar = "SANDBOX_INIT_WORKLOAD_UID"
	// WorkloadGIDEnvVar selects the numeric gid used with WorkloadUIDEnvVar.
	WorkloadGIDEnvVar = "SANDBOX_INIT_WORKLOAD_GID"
)

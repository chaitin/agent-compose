package driver

// Capability dimension keys and not-enforced reasons.
//
// They intentionally mirror pkg/capmatrix's contract strings. pkg/driver cannot
// import pkg/capmatrix (pkg/llms imports pkg/driver), so the mirror is checked
// by TestRuntimeCapabilityFactsCoverTheCapmatrixContract, which compares these
// declarations against capmatrix.RequiredDimensions.
const (
	dimensionResourceLimits          = "resource_limits"
	dimensionCapabilityDrop          = "security_context.capability_drop"
	dimensionReadOnlyRootfs          = "security_context.read_only_rootfs"
	dimensionNonRootUser             = "security_context.non_root_user"
	dimensionUserNamespaces          = "security_context.user_namespaces"
	dimensionEgressPolicy            = "egress_policy"
	dimensionCredentialPlaceholder   = "credential_placeholder_injection"
	dimensionStoppedRuntimeRetention = "stopped_runtime_retention"
	dimensionCheckpointRestore       = "checkpoint_restore"
	dimensionGPUAndDevices           = "gpu_and_devices"
	reasonUnsupported                = "unsupported"
	reasonNotConfigured              = "not_configured"
)

// Capability mechanisms the current drivers can report as enforced.
const (
	mechanismSDKSandboxOptions       = "sdk_sandbox_options"
	mechanismStoppedContainerRuntime = "stopped_container_retention"
	mechanismStoppedMicroVMRuntime   = "stopped_microvm_retention"
	mechanismSDKNetworkPolicy        = "sdk_network_policy"
	mechanismNetworkPolicyEgress     = "networkpolicy_egress"
)

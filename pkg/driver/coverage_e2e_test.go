package driver

import "testing"

// TestDriverWorkflowE2E runs the driver workflow and network-policy fixtures as
// one end-to-end suite so the E2E coverage shape exercises the full driver
// boundary, not only the driver-specific sub-tests selected by name.
func TestDriverWorkflowE2E(t *testing.T) {
	suite := []struct {
		name string
		fn   func(*testing.T)
	}{
		{"runtime-workflow", TestIntegrationRuntimeDriverWorkflow},
		{"network-workflow", TestIntegrationRuntimeNetworkPolicyWorkflows},
		{"sandbox-host", TestIntegrationSandboxHostWorkflow},
	}
	for _, tc := range suite {
		t.Run(tc.name, tc.fn)
	}
}

func TestDriverDockerUnitCoverageE2E(t *testing.T) {
	suite := []struct {
		name string
		fn   func(*testing.T)
	}{
		{"exec-options", TestDockerCommandExecOptionsFromRuntimeStartSpec},
		{"exec-nontty", TestDockerCommandExecOptionsNonTTYAttachesStderr},
		{"exec-env", TestDockerExecEnvMarkerIsDriverOwnedAndIsolatesExecutions},
		{"exec-collector", TestDockerExecCollectorMapsStdioStreams},
		{"exec-utf8", TestDockerExecWriterPreservesSplitUTF8},
		{"mount-identity", TestDockerMountIdentityRejectsStaleExtraAndDuplicateBinds},
		{"mount-readonly", TestDockerMountIdentityRequiresRecursiveReadOnlyContract},
		{"runtime-bind", TestDockerRuntimeBindRuntimeMountSourceUsesHostSandboxRoot},
		{"sandbox-lookup", TestDockerSandboxLookupUsesSandboxLabel},
		{"interaction", TestDockerRuntimeInteractionCapabilities},
		{"require-image", TestRequireLocalDockerImage},
		{"match-image", TestMatchLocalDockerImageRefUsesExactLocalTag},
		{"image-internals", TestDockerImageRefMatchingInternals},
		{"pull-stream", TestConsumeDockerPullStream},
		{"bind-policies", TestDockerBindSourceMappingPolicies},
		{"bind-precedence", TestDockerBindSourceExplicitMappingPrecedenceAndScope},
		{"bind-failure", TestDockerBindSourceResolutionFailureContracts},
		{"workspace-source", TestDockerWorkspaceSourceResolvesProcessTopologyBeforeContainerName},
		{"boxlite-bootstrap", TestBoxLiteBootstrapExecSpecRunsFromRoot},
		{"boxlite-error", TestBoxLiteBootstrapErrorIncludesContextAndOutput},
		{"micro-bootstrap", TestMicrosandboxBootstrapExecSpecRunsFromRoot},
		{"micro-error", TestMicrosandboxBootstrapErrorIncludesContextAndOutput},
		{"jupyter-client", TestNewJupyterReadyHTTPClientDisablesProxyFromEnvironment},
		{"image-layout", TestResolveBoxliteImageLayoutUsesDockerWhenAvailable},
		{"image-root", TestImageCacheRootForDriverDefaultsToDataRoot},
		{"boxlite-lookup", TestBoxliteLookupKey},
		{"boxlite-name", TestBoxliteSandboxName},
		{"shim-reap", TestReapBoxliteShimProcessesRejectsMalformedInput},
		{"sandbox-init-label", TestDockerSandboxUsesInitReadsTheContainerLabel},
		{"sandbox-init-mount", TestPrepareSandboxInitMountCreatesHostDirectory},
		{"sandbox-init-paths", TestSandboxInitPathsUseTheRuntimeDirectoryAndSandboxID},
		{"sandbox-init-defaults", TestDockerSandboxInitBinaryDefaults},
		{"sandbox-init-overlong", TestPrepareSandboxInitMountRejectsAnOverlongSocketPath},
		{"sandbox-supervisor-label", TestDockerSandboxUsesSupervisorLabel},
		{"sandbox-supervisor-network", TestValidateDockerSandboxSupervisorNetwork},
		{"sandbox-supervisor-proxy", TestSandboxSupervisorProxyURL},
		{"sandbox-supervisor-endpoints", TestBuildSandboxSupervisorEndpointsResolvesEngineHosts},
		{"sandbox-supervisor-endpoints-fail", TestBuildSandboxSupervisorEndpointsFailsClosedOnUnresolvableHost},
		{"sandbox-supervisor-env", TestSandboxSupervisorInitEnv},
		{"sandbox-supervisor-token", TestSandboxSupervisorTokenIsPersistedWithPrivatePermissions},
		{"sandbox-supervisor-track", TestDockerSupervisorManagerTracksRunningProcesses},
		{"sandbox-supervisor-exit", TestWaitForSandboxSupervisorFailsWhenTheProcessExits},
		{"docker-network-select", TestSelectDockerNetworkNamePrefersUserDefinedNetwork},
		{"docker-network-single", TestSelectDockerNetworkNameSingleUserDefinedNetwork},
		{"docker-network-default", TestSelectDockerNetworkNameFallsBackToDefaultNetwork},
		{"docker-network-none", TestSelectDockerNetworkNameReturnsFalseWithoutNetworks},
		{"docker-network-names", TestDockerNetworkNamesReturnsSortedAttachedNames},
		{"docker-network-endpoints", TestDockerDaemonEndpointsCapturesAliasesAndAddresses},
		{"docker-network-inspect", TestDockerSandboxNetworkInfoFromInspect},
		{"docker-network-topology", TestSelectDockerDaemonTopologyHonorsExplicitSandboxNetwork},
		{"docker-network-strict", TestSelectDockerDaemonTopologyStrictInternalNetwork},
		{"docker-network-strict-builtin", TestSelectDockerDaemonTopologyStrictRejectsBuiltinWithoutInspecting},
		{"docker-network-strict-requested", TestDockerSandboxNetworkStrictRequested},
		{"docker-network-strict-inert", TestDockerSandboxNetworkStrictIsInertOutsideEnforceMode},
		{"docker-network-trim", TestSelectDockerDaemonTopologyTrimsExplicitNetworkForInspect},
		{"docker-network-validate", TestValidateDockerSandboxStrictNetwork},
		{"docker-network-attach", TestDockerSandboxNetworkAttachEnabled},
	}
	for _, tc := range suite {
		t.Run(tc.name, tc.fn)
	}
}

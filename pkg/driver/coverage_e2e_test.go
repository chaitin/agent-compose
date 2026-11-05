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
	}
	for _, tc := range suite {
		t.Run(tc.name, tc.fn)
	}
}

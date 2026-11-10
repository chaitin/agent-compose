//go:build linux

package driver

import "testing"

// TestIntegrationSandboxInitWorkflow runs the sandbox init, supervisor, and
// network-policy unit fixtures as one integration workflow so the integration
// coverage shape exercises the adapter boundary they share.
func TestIntegrationSandboxInitWorkflow(t *testing.T) {
	suite := []struct {
		name string
		fn   func(*testing.T)
	}{
		{"init-label", TestDockerSandboxUsesInitReadsTheContainerLabel},
		{"init-mount", TestPrepareSandboxInitMountCreatesHostDirectory},
		{"init-paths", TestSandboxInitPathsUseTheRuntimeDirectoryAndSandboxID},
		{"init-defaults", TestDockerSandboxInitBinaryDefaults},
		{"init-overlong", TestPrepareSandboxInitMountRejectsAnOverlongSocketPath},
		{"supervisor-label", TestDockerSandboxUsesSupervisorLabel},
		{"supervisor-network", TestValidateDockerSandboxSupervisorNetwork},
		{"supervisor-proxy", TestSandboxSupervisorProxyURL},
		{"supervisor-endpoints", TestBuildSandboxSupervisorEndpointsResolvesEngineHosts},
		{"supervisor-endpoints-fail", TestBuildSandboxSupervisorEndpointsFailsClosedOnUnresolvableHost},
		{"supervisor-env", TestSandboxSupervisorInitEnv},
		{"supervisor-token", TestSandboxSupervisorTokenIsPersistedWithPrivatePermissions},
		{"supervisor-track", TestDockerSupervisorManagerTracksRunningProcesses},
		{"supervisor-exit", TestWaitForSandboxSupervisorFailsWhenTheProcessExits},
		{"network-select", TestSelectDockerNetworkNamePrefersUserDefinedNetwork},
		{"network-strict", TestSelectDockerDaemonTopologyStrictInternalNetwork},
		{"network-validate", TestValidateDockerSandboxStrictNetwork},
		{"layer-materialize", TestExtractOCILayerMaterializesFilesAndLinks},
		{"layer-escaping", TestExtractOCILayerRejectsEscapingEntries},
		{"layer-whiteout", TestExtractOCILayerAppliesWhiteouts},
	}
	for _, tc := range suite {
		t.Run(tc.name, tc.fn)
	}
}

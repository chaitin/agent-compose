package driver

import "testing"

func TestRuntimeDriverWorkflow(t *testing.T) {
	TestDockerRuntimeSandboxProxyStateUsesContainerNameAndGuestPort(t)
	TestPrepareRuntimeMountManifestForDockerIncludesRequiredMountsOnly(t)
	TestPrepareRuntimeMountManifestCreatesSourcesAndWritesFile(t)
	TestPrepareRuntimeMountManifestForDirectoryOnlyDriversMountsSingleSandboxDirectory(t)
	testRuntimeMountManifestDriverSpecificStartPreparationWorkflow(t)
	testDockerImageRefMatchingInternals(t)
	testConsumeDockerPullStream(t)
	TestExecOutputFilterReportsKnownSeccompWarning(t)
	TestExecOutputFilterHandlesSplitWarning(t)
	TestExecOutputFilterPreservesRealStderr(t)
	TestExecOutputFilterKeepsStderrAfterWarning(t)
	testJupyterGuestCoverageWorkflow(t)
}

func TestIntegrationRuntimeDriverWorkflow(t *testing.T) {
	TestDockerRuntimeSandboxProxyStateUsesContainerNameAndGuestPort(t)
	TestPrepareRuntimeMountManifestForDockerIncludesRequiredMountsOnly(t)
	TestPrepareRuntimeMountManifestCreatesSourcesAndWritesFile(t)
	TestPrepareRuntimeMountManifestForDirectoryOnlyDriversMountsSingleSandboxDirectory(t)
	testRuntimeMountManifestDriverSpecificStartPreparationWorkflow(t)
	testDockerImageRefMatchingInternals(t)
	testConsumeDockerPullStream(t)
	TestExecOutputFilterReportsKnownSeccompWarning(t)
	TestExecOutputFilterHandlesSplitWarning(t)
	TestExecOutputFilterPreservesRealStderr(t)
	TestExecOutputFilterKeepsStderrAfterWarning(t)
	testJupyterGuestCoverageWorkflow(t)
	TestValidateRuntimeDriverK8s(t)
	TestRuntimeDriverSupportsStoppedRuntimeRetention(t)
	TestDriverDefaultsForK8s(t)
	TestPrepareRuntimeMountManifestForK8sHasNoMounts(t)
}

func TestE2ERuntimeDriverWorkflow(t *testing.T) {
	TestDockerRuntimeSandboxProxyStateUsesContainerNameAndGuestPort(t)
	TestPrepareRuntimeMountManifestForDockerIncludesRequiredMountsOnly(t)
	TestPrepareRuntimeMountManifestCreatesSourcesAndWritesFile(t)
	TestPrepareRuntimeMountManifestForDirectoryOnlyDriversMountsSingleSandboxDirectory(t)
	testRuntimeMountManifestDriverSpecificStartPreparationWorkflow(t)
	testDockerImageRefMatchingInternals(t)
	testConsumeDockerPullStream(t)
	TestExecOutputFilterReportsKnownSeccompWarning(t)
	TestExecOutputFilterHandlesSplitWarning(t)
	TestExecOutputFilterPreservesRealStderr(t)
	TestExecOutputFilterKeepsStderrAfterWarning(t)
	testJupyterGuestCoverageWorkflow(t)
	TestValidateRuntimeDriverK8s(t)
	TestRuntimeDriverSupportsStoppedRuntimeRetention(t)
	TestDriverDefaultsForK8s(t)
	TestPrepareRuntimeMountManifestForK8sHasNoMounts(t)
}

// The network policy and capability seam decides whether a declared policy is
// enforced, refused, or ignored, so it must carry coverage in the broader
// shapes and not only in the unit shape.
func TestIntegrationRuntimeNetworkPolicyWorkflows(t *testing.T) {
	t.Run("plan microsandbox network", TestPlanMicrosandboxNetwork)
	t.Run("plan microsandbox deny", TestPlanMicrosandboxDenyPermitsEngineEndpointsAndBlocksOthers)
	t.Run("network egress decision uses the egress model", TestDecideSandboxNetworkEgressUsesTheEgressModel)
	t.Run("enforcement gate fails closed", TestRequireSandboxNetworkEnforcementFailsClosed)
	t.Run("enforcement gate rejects a malformed policy", TestRequireSandboxNetworkEnforcementRejectsMalformedPolicy)
	t.Run("permissive policy is allow-all", TestSandboxNetworkEgressPolicyPermissiveIsAllowAll)
	t.Run("enforcement matrix", TestSandboxNetworkEnforcementFor)
	t.Run("new policy normalizes and validates", TestNewSandboxNetworkPolicyNormalizesAndValidates)
	t.Run("policy deny by default", TestSandboxNetworkPolicyDenyByDefault)
	t.Run("declaration seam", TestSandboxNetworkPolicyFromDeclarationIsTheComposeSeam)
	t.Run("permitting endpoints", TestSandboxNetworkPolicyPermittingEndpoints)
	t.Run("policy rejects a malformed host", TestSandboxNetworkPolicyRejectsMalformedHost)
	t.Run("compose declaration seam", TestSandboxNetworkPolicyFromComposeDeclaration)
	t.Run("undeclared compose block", TestSandboxNetworkPolicyFromUndeclaredComposeBlock)
	t.Run("egress capability reports engine auto allow", TestEgressCapabilityReportsEngineAutoAllow)
	t.Run("docker capabilities match host config", TestDockerCapabilitiesMatchHostConfig)
	t.Run("microvm capabilities match shared resources", TestMicroVMCapabilitiesMatchSharedResourceConfig)
	t.Run("runtime capability facts are probe free", TestRuntimeCapabilityFactsAreStaticAndProbeFree)
	t.Run("runtime capability facts cover every dimension", TestRuntimeCapabilityFactsCoverEveryDimension)
	t.Run("stopped runtime retention declaration", TestStoppedRuntimeRetentionDeclarationMatchesImplementation)
	t.Run("no new privileges warning", TestExecOutputFilterReportsNoNewPrivilegesWarning)
	t.Run("security facts merge", TestExecSecurityFactsMergeAccumulates)
	t.Run("interaction writer projects frames", TestDockerInteractionWriterProjectsFramesAndReportsStderr)
}

func TestE2ERuntimeNetworkPolicyWorkflows(t *testing.T) {
	TestIntegrationRuntimeNetworkPolicyWorkflows(t)
}

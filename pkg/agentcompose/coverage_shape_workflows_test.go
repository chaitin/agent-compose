package agentcompose

import "testing"

func TestIntegrationServiceGraphRegistersV2Routes(t *testing.T) {
	testSupportSetupRegistersServiceGraph(t)
}

func TestIntegrationWebhookWorkspaceAndLoaderWorkflow(t *testing.T) {
	testServiceConfigAndLoaderAPIs(t)
	testServiceSessionKernelAgentAndLLMAPIs(t)
	testServiceProxyRoutesRedirectAndProxy(t)
	TestServiceProxyRoutesUseGuestHostTarget(t)
	testServiceEnsureProxyReadyStartPaths(t)
	testServiceStreamingAPIs(t)
	testSupportConstructorsAndHelpers(t)
	testSupportControlPlaneStartAndConfigHelpers(t)
	testSupportSetupRegistersServiceGraph(t)
	testControlPlaneHelperErrorAndParsingBranches(t)
	TestModelSessionConfigAndBusBranchCoverage(t)
	testServiceReconcilePersistedSessionsMarksStalePendingFailed(t)
	TestServiceAndBridgeReconcileMicrosandboxRuntimeTypeBranches(t)
	testServiceProtoConversionHelpers(t)
}

func TestE2EServiceGraphRegistersV2Routes(t *testing.T) {
	testSupportSetupRegistersServiceGraph(t)
}

func TestE2EWebhookWorkspaceAndLoaderWorkflow(t *testing.T) {
	testServiceConfigAndLoaderAPIs(t)
	testServiceSessionKernelAgentAndLLMAPIs(t)
	testServiceProxyRoutesRedirectAndProxy(t)
	TestServiceProxyRoutesUseGuestHostTarget(t)
	testServiceEnsureProxyReadyStartPaths(t)
	testServiceStreamingAPIs(t)
	testSupportConstructorsAndHelpers(t)
	testSupportControlPlaneStartAndConfigHelpers(t)
	testSupportSetupRegistersServiceGraph(t)
	testControlPlaneHelperErrorAndParsingBranches(t)
	TestModelSessionConfigAndBusBranchCoverage(t)
	testServiceReconcilePersistedSessionsMarksStalePendingFailed(t)
	TestServiceAndBridgeReconcileMicrosandboxRuntimeTypeBranches(t)
	testServiceProtoConversionHelpers(t)
	testServiceReconcilePersistedSessionsMarksStaleProjectRunsFailed(t)
}

package app

import "testing"

func TestIntegrationSessionRPCBridgeWorkflow(t *testing.T) {
	testSessionRPCBridgeCallJSONSupportsAllSessionRPCs(t)
}

func TestIntegrationLoaderEngineWorkflow(t *testing.T) {
	testLoaderEngineExecuteSupportsSessionRPCBindings(t)
	testLoaderEngineExecuteSupportsAgentAndLLMBindings(t)
	testLoaderEngineExecuteSupportsCommandBindings(t)
	TestLoaderEngineJSONAndRegistrationBranches(t)
}

func TestIntegrationServiceGraphRegistersV2Routes(t *testing.T) {
	testSupportSetupRegistersServiceGraph(t)
}

func TestIntegrationWebhookWorkspaceAndLoaderWorkflow(t *testing.T) {
	testServiceConfigAndLoaderAPIs(t)
	testServiceSessionKernelAgentAndLLMAPIs(t)
	testServiceProxyRoutesRedirectAndProxy(t)
	TestServiceProxyRoutesUseGuestHostTarget(t)
	testServiceEnsureProxyReadyStartPaths(t)
	TestJupyterTargetReachableUsesGuestHostTarget(t)
	testServiceStreamingAPIs(t)
	testWebhookHandlerStoresEvent(t)
	testEventQueryHandlers(t)
	testSupportConstructorsAndHelpers(t)
	testSupportControlPlaneStartAndConfigHelpers(t)
	testSupportSetupRegistersServiceGraph(t)
	testControlPlaneHelperErrorAndParsingBranches(t)
	testServiceReconcilePersistedSessionsMarksStalePendingFailed(t)
	testServiceProtoConversionHelpers(t)
	testAgentRunSummariesScansAllSessions(t)
	testAgentDefinitionConfigStoreCRUDAndWorkspaceProtection(t)
	testLoaderCreateBindsAgentDefinitionProvider(t)
	testAgentDefinitionValidationAndProtoMapping(t)
	testAgentDefinitionCreateSession(t)
	testDeleteAgentDefinitionStopsSessionsAndKeepsDeletedInList(t)
	testDashboardOverviewAggregatorCountsRuns(t)
	testDashboardOverviewHubWatchInitialAndNotify(t)
	testWebhookIntegrationEventDispatchRunsMatchingLoader(t)
}

func TestE2ESessionRPCBridgeWorkflow(t *testing.T) {
	testSessionRPCBridgeCallJSONSupportsAllSessionRPCs(t)
}

func TestE2ELoaderEngineWorkflow(t *testing.T) {
	testLoaderEngineExecuteSupportsSessionRPCBindings(t)
	testLoaderEngineExecuteSupportsAgentAndLLMBindings(t)
	testLoaderEngineExecuteSupportsCommandBindings(t)
	TestLoaderEngineJSONAndRegistrationBranches(t)
}

func TestE2EServiceGraphRegistersV2Routes(t *testing.T) {
	testSupportSetupRegistersServiceGraph(t)
}

func TestE2EWebhookWorkspaceAndLoaderWorkflow(t *testing.T) {
	TestIntegrationWebhookWorkspaceAndLoaderWorkflow(t)
}

func testRuntimeLLMFacadeCoverageWorkflows(t *testing.T) {
	t.Helper()
	TestServiceGenerateLLMChatCompletionsProtocol(t)
}

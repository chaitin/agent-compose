package agentcompose

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
	testServiceStreamingAPIs(t)
	testSupportConstructorsAndHelpers(t)
	testSupportControlPlaneStartAndConfigHelpers(t)
	testSupportSetupRegistersServiceGraph(t)
	testControlPlaneHelperErrorAndParsingBranches(t)
	TestModelSessionConfigAndBusBranchCoverage(t)
	TestTopicEventModelAndStoreErrorBranches(t)
	testServiceReconcilePersistedSessionsMarksStalePendingFailed(t)
	TestServiceAndBridgeReconcileMicrosandboxRuntimeTypeBranches(t)
	testServiceProtoConversionHelpers(t)
	testAgentDefinitionConfigStoreCRUDAndWorkspaceProtection(t)
	testLoaderCreateBindsAgentDefinitionProvider(t)
	testAgentDefinitionValidationAndProtoMapping(t)
	testAgentDefinitionCreateSession(t)
	testDeleteAgentDefinitionStopsSessionsAndKeepsDeletedInList(t)
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
	TestTopicEventModelAndStoreErrorBranches(t)
	testServiceReconcilePersistedSessionsMarksStalePendingFailed(t)
	TestServiceAndBridgeReconcileMicrosandboxRuntimeTypeBranches(t)
	testServiceProtoConversionHelpers(t)
	testAgentDefinitionConfigStoreCRUDAndWorkspaceProtection(t)
	testLoaderCreateBindsAgentDefinitionProvider(t)
	testAgentDefinitionValidationAndProtoMapping(t)
	testAgentDefinitionCreateSession(t)
	testDeleteAgentDefinitionStopsSessionsAndKeepsDeletedInList(t)
	testServiceReconcilePersistedSessionsMarksStaleProjectRunsFailed(t)
	testWebhookIntegrationEventDispatchRunsMatchingLoader(t)
}

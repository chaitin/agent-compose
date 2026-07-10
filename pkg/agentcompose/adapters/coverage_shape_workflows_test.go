package adapters

import "testing"

func TestIntegrationAdapterRuntimeWorkflows(t *testing.T) {
	t.Run("agent executor", TestAgentExecutorExecuteAgentRequestPersistsCellAndEvents)
	t.Run("agent executor stream failure", TestAgentExecutorPersistsFailedCellWhenStreamCallbackFails)
	t.Run("agent runner", TestAgentRunnerExecuteAgentRunWritesSystemPromptAndParsesResult)
	t.Run("agent runner mcp", TestAgentRunnerPrepareManagedMCPConfigForProviders)
	t.Run("cell executor", TestCellExecutorExecuteCellPersistsCellAndEvent)
	t.Run("sandbox driver", TestSandboxDriverStartSandboxVMSavesRuntimeState)
	t.Run("sandbox stop and remove lifecycle", TestSandboxDriverStopPreservesFacadeTokensUntilRemove)
	t.Run("sandbox resume reuses runtime", TestSandboxDriverResumeReusesRuntimeWithoutRefreshingStartupEnv)
	t.Run("sandbox rpc", TestSandboxRPCBridgeCallJSONSupportsSessionRPCs)
	t.Run("capability guide lifecycle", TestSandboxRPCBridgeCapabilityGuideLifecycle)
	t.Run("capability guide best effort", TestSandboxRPCBridgeCapabilityGuideIsBestEffort)
	t.Run("runtime liveness", TestSandboxRuntimeLivenessAndNotifierBranches)
	t.Run("capability guide http", TestSandboxRPCBridgeCapabilityGuideFromHTTPProvider)
	t.Run("adapter helpers", TestAdapterHelperCoverage)
	t.Run("capability sandbox resolver", TestCapabilitySandboxResolverCoverage)
}

func TestE2EAdapterRuntimeWorkflows(t *testing.T) {
	TestIntegrationAdapterRuntimeWorkflows(t)
}

package adapters

import "testing"

func TestIntegrationAdapterRuntimeWorkflows(t *testing.T) {
	t.Run("agent executor", TestAgentExecutorExecuteAgentRequestPersistsCellAndEvents)
	t.Run("agent executor stream failure", TestAgentExecutorPersistsFailedCellWhenStreamCallbackFails)
	t.Run("agent runner", TestAgentRunnerExecuteAgentRunWritesSystemPromptAndParsesResult)
	t.Run("cell executor", TestCellExecutorExecuteCellPersistsCellAndEvent)
	t.Run("session driver", TestSessionDriverStartSessionVMSavesRuntimeState)
	t.Run("session rpc", TestSessionRPCBridgeCallJSONSupportsSessionRPCs)
	t.Run("capability guide lifecycle", TestSessionRPCBridgeCapabilityGuideLifecycle)
	t.Run("capability guide best effort", TestSessionRPCBridgeCapabilityGuideIsBestEffort)
	t.Run("runtime liveness", TestSessionRuntimeLivenessAndNotifierBranches)
	t.Run("capability guide http", TestSessionRPCBridgeCapabilityGuideFromHTTPProvider)
	t.Run("adapter helpers", TestAdapterHelperCoverage)
	t.Run("capability session resolver", TestCapabilitySessionResolverCoverage)
}

func TestE2EAdapterRuntimeWorkflows(t *testing.T) {
	TestIntegrationAdapterRuntimeWorkflows(t)
}

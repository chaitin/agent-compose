package app

import "testing"

func TestIntegrationAppSetupWorkflow(t *testing.T) {
	TestSetupRegistersServiceGraph(t)
	// The engine capability snapshot is built through this package's dependency
	// wiring, so its guest ABI path carries coverage in the broader shapes too.
	t.Run("engine capability snapshot falls back to static providers", TestNewEngineCapabilitySnapshotFallsBackToStaticProviders)
	t.Run("engine capability snapshot uses a guest ABI manifest", TestNewEngineCapabilitySnapshotUsesGuestABIManifest)
}

func TestE2EAppSetupWorkflow(t *testing.T) {
	TestIntegrationAppSetupWorkflow(t)
	t.Run("rejected network policy admission", TestIntegrationRejectedNetworkPolicyLeavesNoProject)
	t.Run("network policy reports", func(t *testing.T) {
		TestAgentNetworkPolicyReportsNameTheMechanismAndTheGap(t)
		TestAgentNetworkPolicyReportsStateTheOutcomeOfAnUnenforceableDeclaration(t)
		TestAgentNetworkPolicyReportsRejectADeclarationTheEngineCannotRead(t)
		TestAgentNetworkPolicyReportsIncludeTheDaemonGlobalPolicy(t)
		TestAgentNetworkPolicyReportsCarryPolicyProvenance(t)
		TestNetworkPolicyConnectErrorClassifiesTheDeclaration(t)
	})
}

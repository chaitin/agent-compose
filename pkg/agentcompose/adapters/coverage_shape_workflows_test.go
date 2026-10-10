package adapters

import "testing"

func TestIntegrationAdapterRuntimeWorkflows(t *testing.T) {
	t.Run("runtime provider configured capability", TestNewRuntimeProviderValidatesConfiguredDefaultCompiled)
	t.Run("runtime provider lazy construction", TestNewRuntimeProviderConstructionIsLazy)
	t.Run("runtime provider validation ordering", TestRuntimeProviderForDriverValidationOrdering)
	t.Run("historical runtime state", TestHistoricalUncompiledRuntimeOperationsPreserveState)
	t.Run("historical exec preflight", TestHistoricalUncompiledExecPreflightHasNoArtifactsOrRecords)
	t.Run("agent executor", TestAgentExecutorExecuteAgentRequestPersistsCellAndEvents)
	t.Run("agent executor stream failure", TestAgentExecutorPersistsFailedCellWhenStreamCallbackFails)
	t.Run("agent runner", TestAgentRunnerExecuteAgentRunWritesSystemPromptAndParsesResult)
	t.Run("agent runner mcp", TestAgentRunnerPrepareManagedMCPConfigForProviders)
	t.Run("cell executor", TestCellExecutorExecuteCellPersistsCellAndEvent)
	t.Run("sandbox driver", TestSandboxDriverStartSandboxVMSavesRuntimeState)
	t.Run("sandbox stop and remove lifecycle", TestSandboxDriverStopPreservesFacadeTokensUntilRuntimeRelease)
	t.Run("sandbox resume reuses runtime", TestSandboxDriverResumeReusesRuntimeWithoutRefreshingStartupEnv)
	t.Run("sandbox rpc", TestSandboxRPCBridgeCallJSONSupportsSandboxRPCs)
	t.Run("sandbox rpc unsupported history", TestSandboxRPCBridgeHistoricalUnsupportedRuntimeIsUnimplementedAndPreservesSummary)
	t.Run("sandbox rpc unsupported persistence boundary", TestSandboxRPCBridgeRejectsUncompiledDriverBeforePersistence)
	t.Run("capability guide lifecycle", TestSandboxRPCBridgeCapabilityGuideLifecycle)
	t.Run("capability guide best effort", TestSandboxRPCBridgeCapabilityGuideIsBestEffort)
	t.Run("runtime liveness", TestSandboxRuntimeLivenessAndNotifierBranches)
	t.Run("capability guide http", TestSandboxRPCBridgeCapabilityGuideFromHTTPProvider)
	t.Run("adapter helpers", TestAdapterHelperCoverage)
	t.Run("capability sandbox resolver", TestCapabilitySandboxResolverCoverage)
	t.Run("scheduler sticky unsupported history", TestSchedulerSandboxRunnerRejectsUnsupportedStickyResumeBeforeSideEffects)
	t.Run("agent runner k8s guest dir sync", TestAgentRunnerSyncsWorkspaceAndHomeForGuestDirRuntime)
	t.Run("cell executor guest exec push", TestCellExecutorPushesScriptBeforeGuestExec)
	t.Run("scheduler command guest files", TestSchedulerCommandExecutorTransfersGuestRequestAndArtifacts)
	t.Run("sandbox driver rejects k8s stopped runtime retention", TestSandboxDriverRejectsStoppedRuntimeRetentionForK8s)
}

func TestE2EAdapterRuntimeWorkflows(t *testing.T) {
	TestIntegrationAdapterRuntimeWorkflows(t)
}

// The adapter is where a declared network policy meets the driver boundary, and
// where the isolation preflight refuses a start, so it must carry coverage in
// the broader shapes and not only in the unit shape.
func TestIntegrationAdapterSandboxNetworkWorkflows(t *testing.T) {
	t.Run("policy reaches the driver boundary", TestEnsureSandboxCarriesDeclaredNetworkPolicyToDriverBoundary)
	t.Run("declared deny fires the gate", TestEnsureSandboxDeclaredDenyFiresTheEnforcementGate)
	t.Run("declaration failure fails closed", TestEnsureSandboxNetworkDeclarationFailureFailsClosed)
	t.Run("undeclared leaves the gate inert", TestEnsureSandboxUndeclaredLeavesGateInert)
	t.Run("guest file paths carry the policy", TestGuestFilePathsCarryDeclaredNetworkPolicy)
	t.Run("start allows a best effort requirement", TestStartSandboxVMAllowsABestEffortIsolationRequirement)
	t.Run("default start path is unchanged", TestStartSandboxVMDefaultPathIsUnchangedByTheIsolationGate)
	t.Run("start fails closed on default deny egress", TestStartSandboxVMFailsClosedWhenDefaultDenyEgressIsDeclared)
	t.Run("start rejects an unknown isolation requirement", TestStartSandboxVMRejectsAnUnknownIsolationRequirement)
}

func TestE2EAdapterSandboxNetworkWorkflows(t *testing.T) {
	TestIntegrationAdapterSandboxNetworkWorkflows(t)
}

package api

import "testing"

func TestE2EPublicTransportBoundaryWorkflows(t *testing.T) {
	t.Run("cache lifecycle", func(t *testing.T) {
		TestCacheHandlerListCachesMapsFilterAndResponse(t)
		TestCacheHandlerInspectCache(t)
		TestCacheHandlerInspectCacheAllowsIDPrefix(t)
		TestCacheHandlerInspectCacheNotFound(t)
		TestCacheHandlerPruneCachesMapsRequestAndResult(t)
		TestCacheHandlerRemoveCacheProtectedSkipped(t)
		TestCacheHandlerValidationErrors(t)
	})
	t.Run("volume lifecycle", TestVolumeHandlerWorkflows)
	t.Run("settings secret update", TestSettingsGlobalEnvDistinguishesRetainAndClearSecret)
	t.Run("resource resolution", TestResourceHandlerResolveID)
	t.Run("sandbox pruning", TestPruneSandboxesMapsRequestAndCandidates)
	t.Run("sandbox prune validation", TestPruneSandboxesValidatesDurationAndCoordinatorErrors)
	t.Run("image build stream", TestImageBuildStreamsEvents)
	t.Run("exec attach start frame", TestExecAttachRequiresStartFrame)
	t.Run("exec attach unsupported runtime", TestExecAttachRuntimeUnsupportedIsUnimplemented)
	t.Run("exec attach output", TestExecAttachProjectsStdoutAndResult)
	t.Run("exec attach interaction", TestExecAttachRunnerProjectsInteractionFramesWithoutRPC)
	t.Run("exec attach prompt", TestExecAttachPromptDelegatesToRunAttach)
	t.Run("exec attach input", TestExecAttachInputFrameMapping)
	t.Run("network policy report mapping", func(t *testing.T) {
		TestNetworkPolicyReportsToProtoCarriesTheDeclaredAccount(t)
		TestNetworkPolicyReportsToProtoKeepsTheGapVisible(t)
	})
	t.Run("legacy network protocol rejection", func(t *testing.T) {
		TestLegacyNetworkProtocolIssuesRejectsNamedProtocol(t)
		TestLegacyNetworkProtocolIssuesRejectsDeniedProtocol(t)
		TestLegacyNetworkProtocolIssuesAcceptsDefault(t)
		TestLegacyNetworkProtocolIssuesIgnoresUndeclaredNetwork(t)
	})
}

package runs

import "testing"

// The guest-side detach workflows are exercised as unit tests. This file
// mirrors the established coverage_shape_workflows_test.go mechanism: each
// workflow is also run under the integration and E2E test shapes so the detach
// code (start, follow, reattach, and interactive reattach) is covered at those
// boundaries and the per-shape coverage gate is satisfied. The wrappers reuse
// the existing assertions rather than duplicating them.

// Reattach flows.

func TestIntegrationReattachDetachedRunMarksFailedWhenSandboxNotRunning(t *testing.T) {
	TestReattachDetachedRunMarksFailedWhenSandboxNotRunning(t)
}

func TestE2EReachDetachedRunMarksFailedWhenSandboxNotRunning(t *testing.T) {
	TestReattachDetachedRunMarksFailedWhenSandboxNotRunning(t)
}

func TestIntegrationReattachDetachedRunFollowsJournalAndCompletes(t *testing.T) {
	TestReattachDetachedRunFollowsJournalAndCompletes(t)
}

func TestE2EReachDetachedRunFollowsJournalAndCompletes(t *testing.T) {
	TestReattachDetachedRunFollowsJournalAndCompletes(t)
}

func TestIntegrationReattachDetachedRunFailsOnNonZeroExit(t *testing.T) {
	TestReattachDetachedRunFailsOnNonZeroExit(t)
}

func TestE2EReachDetachedRunFailsOnNonZeroExit(t *testing.T) {
	TestReattachDetachedRunFailsOnNonZeroExit(t)
}

func TestIntegrationReattachDetachedRunFailsWithoutResultPayload(t *testing.T) {
	TestReattachDetachedRunFailsWithoutResultPayload(t)
}

func TestE2EReachDetachedRunFailsWithoutResultPayload(t *testing.T) {
	TestReattachDetachedRunFailsWithoutResultPayload(t)
}

func TestIntegrationReattachDetachedRunFallsBackToResultFileWhenCursorPassedPayload(t *testing.T) {
	TestReattachDetachedRunFallsBackToResultFileWhenCursorPassedPayload(t)
}

func TestE2EReachDetachedRunFallsBackToResultFileWhenCursorPassedPayload(t *testing.T) {
	TestReattachDetachedRunFallsBackToResultFileWhenCursorPassedPayload(t)
}

func TestIntegrationReattachDetachedAgentRunDeletesRetainedTokensWhenResumeUnsupported(t *testing.T) {
	TestReattachDetachedAgentRunDeletesRetainedTokensWhenResumeUnsupported(t)
}

func TestE2EReachDetachedAgentRunDeletesRetainedTokensWhenResumeUnsupported(t *testing.T) {
	TestReattachDetachedAgentRunDeletesRetainedTokensWhenResumeUnsupported(t)
}

// Agent reattach flows.

func TestIntegrationReattachDetachedAgentRunResumesItsCell(t *testing.T) {
	TestReattachDetachedAgentRunResumesItsCell(t)
}

func TestE2EReachDetachedAgentRunResumesItsCell(t *testing.T) {
	TestReattachDetachedAgentRunResumesItsCell(t)
}

func TestIntegrationReattachDetachedAgentRunFailsWithTheCell(t *testing.T) {
	TestReattachDetachedAgentRunFailsWithTheCell(t)
}

func TestE2EReachDetachedAgentRunFailsWithTheCell(t *testing.T) {
	TestReattachDetachedAgentRunFailsWithTheCell(t)
}

func TestIntegrationReattachDetachedAgentRunDeletesTokensWhenResumeFailsEarly(t *testing.T) {
	TestReattachDetachedAgentRunDeletesTokensWhenResumeFailsEarly(t)
}

func TestE2EReachDetachedAgentRunDeletesTokensWhenResumeFailsEarly(t *testing.T) {
	TestReattachDetachedAgentRunDeletesTokensWhenResumeFailsEarly(t)
}

func TestIntegrationReattachDetachedAgentRunStaysRunningOnDisconnect(t *testing.T) {
	TestReattachDetachedAgentRunStaysRunningOnDisconnect(t)
}

func TestE2EReachDetachedAgentRunStaysRunningOnDisconnect(t *testing.T) {
	TestReattachDetachedAgentRunStaysRunningOnDisconnect(t)
}

func TestIntegrationReattachDetachedAgentRunKeepsTokensWhenTerminationIsUnconfirmed(t *testing.T) {
	TestReattachDetachedAgentRunKeepsTokensWhenTerminationIsUnconfirmed(t)
}

func TestE2EReachDetachedAgentRunKeepsTokensWhenTerminationIsUnconfirmed(t *testing.T) {
	TestReattachDetachedAgentRunKeepsTokensWhenTerminationIsUnconfirmed(t)
}

func TestIntegrationReattachDetachedAgentRunWithoutCellFails(t *testing.T) {
	TestReattachDetachedAgentRunWithoutCellFails(t)
}

func TestE2EReachDetachedAgentRunWithoutCellFails(t *testing.T) {
	TestReattachDetachedAgentRunWithoutCellFails(t)
}

func TestIntegrationAgentRunDetachRecordsAgentKindOnTheStoredRun(t *testing.T) {
	TestAgentRunDetachRecordsAgentKindOnTheStoredRun(t)
}

func TestE2EAgentRunDetachRecordsAgentKindOnTheStoredRun(t *testing.T) {
	TestAgentRunDetachRecordsAgentKindOnTheStoredRun(t)
}

func TestIntegrationAgentRunDetachKeepsLegacyGuestsDirect(t *testing.T) {
	TestAgentRunDetachKeepsLegacyGuestsDirect(t)
}

func TestE2EAgentRunDetachKeepsLegacyGuestsDirect(t *testing.T) {
	TestAgentRunDetachKeepsLegacyGuestsDirect(t)
}

// Interactive reattach flows.

func TestIntegrationReattachInteractiveCommandRunServesNewClients(t *testing.T) {
	TestReattachInteractiveCommandRunServesNewClients(t)
}

func TestE2EReachInteractiveCommandRunServesNewClients(t *testing.T) {
	TestReattachInteractiveCommandRunServesNewClients(t)
}

func TestIntegrationInteractiveRunDetachOnlyForClientlessNonTTYRuns(t *testing.T) {
	TestInteractiveRunDetachOnlyForClientlessNonTTYRuns(t)
}

func TestE2EInteractiveRunDetachOnlyForClientlessNonTTYRuns(t *testing.T) {
	TestInteractiveRunDetachOnlyForClientlessNonTTYRuns(t)
}

// Detached interaction flows.

func TestIntegrationDetachedInteractionTranslatesAttachRecords(t *testing.T) {
	TestDetachedInteractionTranslatesAttachRecords(t)
}

func TestE2EDetachedInteractionTranslatesAttachRecords(t *testing.T) {
	TestDetachedInteractionTranslatesAttachRecords(t)
}

func TestIntegrationDetachedInteractionStopsTaskOnUserStop(t *testing.T) {
	TestDetachedInteractionStopsTaskOnUserStop(t)
}

func TestE2EDetachedInteractionStopsTaskOnUserStop(t *testing.T) {
	TestDetachedInteractionStopsTaskOnUserStop(t)
}

// Detached execution flows.

func TestIntegrationFollowDetachedCommandDoesNotStopOnShutdown(t *testing.T) {
	TestFollowDetachedCommandDoesNotStopOnShutdown(t)
}

func TestE2EFollowDetachedCommandDoesNotStopOnShutdown(t *testing.T) {
	TestFollowDetachedCommandDoesNotStopOnShutdown(t)
}

func TestIntegrationFollowDetachedCommandTerminatesTreeAfterAbnormalExit(t *testing.T) {
	TestFollowDetachedCommandTerminatesTreeAfterAbnormalExit(t)
}

func TestE2EFollowDetachedCommandTerminatesTreeAfterAbnormalExit(t *testing.T) {
	TestFollowDetachedCommandTerminatesTreeAfterAbnormalExit(t)
}

func TestIntegrationFollowDetachedCommandTerminatesTreeWhenStopFails(t *testing.T) {
	TestFollowDetachedCommandTerminatesTreeWhenStopFails(t)
}

func TestE2EFollowDetachedCommandTerminatesTreeWhenStopFails(t *testing.T) {
	TestFollowDetachedCommandTerminatesTreeWhenStopFails(t)
}

func TestIntegrationFollowDetachedCommandTerminatesTreeWithFreshContextAfterStopTimeout(t *testing.T) {
	TestFollowDetachedCommandTerminatesTreeWithFreshContextAfterStopTimeout(t)
}

func TestE2EFollowDetachedCommandTerminatesTreeWithFreshContextAfterStopTimeout(t *testing.T) {
	TestFollowDetachedCommandTerminatesTreeWithFreshContextAfterStopTimeout(t)
}

func TestIntegrationFollowDetachedCommandTerminatesTreeWhenStopExitsNonZero(t *testing.T) {
	TestFollowDetachedCommandTerminatesTreeWhenStopExitsNonZero(t)
}

func TestE2EFollowDetachedCommandTerminatesTreeWhenStopExitsNonZero(t *testing.T) {
	TestFollowDetachedCommandTerminatesTreeWhenStopExitsNonZero(t)
}

func TestIntegrationFollowDetachedCommandTerminatesTreeWhenStopDoesNotLand(t *testing.T) {
	TestFollowDetachedCommandTerminatesTreeWhenStopDoesNotLand(t)
}

func TestE2EFollowDetachedCommandTerminatesTreeWhenStopDoesNotLand(t *testing.T) {
	TestFollowDetachedCommandTerminatesTreeWhenStopDoesNotLand(t)
}

func TestIntegrationStartDetachedCommandSurfacesNonZeroExit(t *testing.T) {
	TestStartDetachedCommandSurfacesNonZeroExit(t)
}

func TestE2EStartDetachedCommandSurfacesNonZeroExit(t *testing.T) {
	TestStartDetachedCommandSurfacesNonZeroExit(t)
}

func TestIntegrationStartDetachedCommandTerminatesTreeOnFailure(t *testing.T) {
	TestStartDetachedCommandTerminatesTreeOnFailure(t)
}

func TestE2EStartDetachedCommandTerminatesTreeOnFailure(t *testing.T) {
	TestStartDetachedCommandTerminatesTreeOnFailure(t)
}

func TestIntegrationStartDetachedCommandDoesNotTerminateTreeOnShutdown(t *testing.T) {
	TestStartDetachedCommandDoesNotTerminateTreeOnShutdown(t)
}

func TestE2EStartDetachedCommandDoesNotTerminateTreeOnShutdown(t *testing.T) {
	TestStartDetachedCommandDoesNotTerminateTreeOnShutdown(t)
}

// Detached lifecycle flows.

func TestIntegrationFollowDetachedCommandCleansUpAfterExit(t *testing.T) {
	TestFollowDetachedCommandCleansUpAfterExit(t)
}

func TestE2EFollowDetachedCommandCleansUpAfterExit(t *testing.T) {
	TestFollowDetachedCommandCleansUpAfterExit(t)
}

func TestIntegrationFollowDetachedCommandKeepsStateOnDisconnect(t *testing.T) {
	TestFollowDetachedCommandKeepsStateOnDisconnect(t)
}

func TestE2EFollowDetachedCommandKeepsStateOnDisconnect(t *testing.T) {
	TestFollowDetachedCommandKeepsStateOnDisconnect(t)
}

func TestIntegrationStartDetachedCommandHandsRemainingTimeoutToSupervisor(t *testing.T) {
	TestStartDetachedCommandHandsRemainingTimeoutToSupervisor(t)
}

func TestE2EStartDetachedCommandHandsRemainingTimeoutToSupervisor(t *testing.T) {
	TestStartDetachedCommandHandsRemainingTimeoutToSupervisor(t)
}

func TestIntegrationDetachFollowerPersistsCursorOncePerChunk(t *testing.T) {
	TestDetachFollowerPersistsCursorOncePerChunk(t)
}

func TestE2EDetachFollowerPersistsCursorOncePerChunk(t *testing.T) {
	TestDetachFollowerPersistsCursorOncePerChunk(t)
}

func TestIntegrationDetachSupportedRetriesAfterProbeError(t *testing.T) {
	TestDetachSupportedRetriesAfterProbeError(t)
}

func TestE2EDetachSupportedRetriesAfterProbeError(t *testing.T) {
	TestDetachSupportedRetriesAfterProbeError(t)
}

func TestIntegrationDetachSupportedCachesLegacyRuntimeAnswer(t *testing.T) {
	TestDetachSupportedCachesLegacyRuntimeAnswer(t)
}

func TestE2EDetachSupportedCachesLegacyRuntimeAnswer(t *testing.T) {
	TestDetachSupportedCachesLegacyRuntimeAnswer(t)
}

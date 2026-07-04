package main

import "testing"

func TestDaemonListenConfigWorkflow(t *testing.T) {
	testDaemonListenConfigWorkflow(t)
}

func TestIntegrationDaemonListenConfigWorkflow(t *testing.T) {
	testDaemonListenConfigWorkflow(t)
}

func TestE2EDaemonListenConfigWorkflow(t *testing.T) {
	testDaemonListenConfigWorkflow(t)
}

func testDaemonListenConfigWorkflow(t *testing.T) {
	t.Helper()
	t.Run("defaults to socket only config", testNewDaemonAppDefaultsToSocketOnlyConfig)
	t.Run("builds handler without listening", testNewDaemonAppBuildsHandlerWithoutListening)
	t.Run("registers core routes", testDaemonAppRegistersCoreRoutes)
	t.Run("does not register static web routes", testDaemonAppDoesNotRegisterStaticWebRoutes)
	t.Run("starts background once", testDaemonAppStartsBackgroundOnce)
	t.Run("serves unix socket and optional tcp", testDaemonAppServesUnixSocketAndOptionalTCP)
	t.Run("cleans stale unix socket", testDaemonAppCleansStaleUnixSocket)
	t.Run("reports tcp port conflict", testDaemonAppReportsTCPPortConflict)
	t.Run("reports uncreatable unix socket path", testDaemonAppReportsUncreatableUnixSocketPath)
	t.Run("cli client config priority", testCLIClientConfigPriority)
	t.Run("status command uses host flag before environment", testStatusCommandUsesHostFlagBeforeEnvironment)
	t.Run("status command uses environment host", testStatusCommandUsesEnvironmentHost)
	t.Run("status command uses default unix socket", testStatusCommandUsesDefaultUnixSocket)
	t.Run("status command reports unreadable daemon", testStatusCommandReportsUnreadableDaemon)
	t.Run("cli up applies project first repeated modified and json", testCLIUpAppliesProjectFirstRepeatedModifiedAndJSON)
	t.Run("cli down first repeated partial and json", testCLIDownFirstRepeatedPartialAndJSON)
	t.Run("cli run streams output and supports session reuse", TestIntegrationCLIRunStreamsOutputAndSupportsSessionReuse)
	t.Run("cli run failure returns stable exit code", TestIntegrationCLIRunFailureReturnsStableExitCode)
	t.Run("cli logs filters run agent session and json", TestIntegrationCLILogsFiltersRunAgentSessionAndJSON)
	t.Run("cli logs follow polls until terminal", TestIntegrationCLILogsFollowPollsUntilTerminal)
	t.Run("cli ps table and json", TestIntegrationCLIPSTableAndJSON)
	t.Run("cli exec streams and json", TestIntegrationCLIExecStreamsAndSupportsJSON)
	t.Run("cli exec ambiguous session is usage error", TestIntegrationCLIExecAmbiguousSessionIsUsageError)
	t.Run("cli inspect project agent run session json", TestIntegrationCLIInspectProjectAgentRunSandboxSessionJSON)
	t.Run("cli images aliases and json", TestIntegrationCLIImagesAliasesAndJSON)
	t.Run("cli image pull aliases and json", TestIntegrationCLIImagePullAliasesAndJSON)
	t.Run("cli image remove aliases and json", TestIntegrationCLIImageRemoveAliasesAndJSON)
	t.Run("cli image inspect json", TestIntegrationCLIImageInspectJSON)
	t.Run("cli images json accepts OCI store status", TestIntegrationCLIImagesJSONAcceptsOCIStoreStatus)
	t.Run("cli image Docker error is clear", TestIntegrationCLIImageDockerErrorIsClear)
}

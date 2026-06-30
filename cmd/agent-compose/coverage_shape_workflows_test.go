package main

import "testing"

func TestIntegrationCLIConfigAndOutputWorkflow(t *testing.T) {
	testCLIConfigAndOutputWorkflow(t)
}

func TestE2ECLIConfigAndOutputWorkflow(t *testing.T) {
	testCLIConfigAndOutputWorkflow(t)
}

func testCLIConfigAndOutputWorkflow(t *testing.T) {
	t.Helper()
	TestRootCommandPrintsHelpWithoutStartingDaemon(t)
	TestUnknownCommandFailsWithoutStartingDaemon(t)
	TestVersionCommandPrintsBuildVersionWithoutStartingDaemon(t)
	TestConfigCommandPrintsNormalizedJSONWithoutStartingDaemon(t)
	TestConfigCommandPrintsNormalizedYAMLWithoutStartingDaemon(t)
	TestConfigCommandQuietOnlyValidates(t)
	TestConfigCommandQuietFailureIncludesComposePathAndField(t)
	TestConfigCommandMissingComposeFileWritesStderrAndExitCode(t)
	TestConfigCommandUsesGlobalFileProjectNameAndJSON(t)
	TestCLIClientConfigPriority(t)
	TestCLIClientConfigRejectsInvalidHost(t)
	TestStatusCommandUsesHostFlagBeforeEnvironment(t)
	TestStatusCommandUsesEnvironmentHost(t)
	TestStatusCommandUsesDefaultUnixSocket(t)
	TestStatusCommandUnavailableWritesStderrAndExitCode(t)
	TestStatusCommandReportsUnreadableDaemon(t)
	TestCLIDownFirstRepeatedPartialAndJSON(t)
	TestComposeImageOutputFromProtoAcceptsOCIStatus(t)
	TestLogsJSONFollowIsUsageError(t)
	TestInvalidHostWritesStderrAndUsageExitCode(t)
}

package compose

import "testing"

func TestIntegrationComposeManifestWorkflow(t *testing.T) {
	testComposeManifestWorkflow(t)
}

func TestE2EComposeManifestWorkflow(t *testing.T) {
	testComposeManifestWorkflow(t)
}

func testComposeManifestWorkflow(t *testing.T) {
	t.Helper()
	TestParseMinimalSpec(t)
	TestParseFullSpec(t)
	TestParseSchedulerScript(t)
	TestParseUnknownFieldIncludesPath(t)
	TestParseInvalidYAML(t)
	TestParseTypeErrorIncludesPath(t)
	TestParseSchedulerScriptTypeErrorIncludesPath(t)
	TestParseFileIncludesPath(t)
	TestNormalizeDefaultsProjectNameFromComposeDirectory(t)
	TestNormalizeExplicitProjectNameWinsOverDirectory(t)
	TestNormalizeRequiresProjectNameWithoutDefaultPath(t)
	TestNormalizeSortsAgentsForStableOutput(t)
	TestNormalizeAgentCapsetIDs(t)
	TestNormalizeServicesAndProjectTriggers(t)
	TestNormalizeServiceSchemas(t)
	TestNormalizeRejectsInvalidServiceSchemas(t)
	TestNormalizeRejectsInvalidEnvName(t)
	TestNormalizeRejectsServiceWithoutEntry(t)
	TestNormalizeRejectsTriggerWithoutTarget(t)
	TestNormalizePreservesValidAgentNames(t)
	TestNormalizeRejectsInvalidAgentName(t)
	TestNormalizeDefaultsDriverAndNetwork(t)
	TestNormalizeInterpolatesAgentModelFromEnvironment(t)
	TestNormalizeRequiresAgentModelEnvironmentReference(t)
	TestNormalizeRejectsEmptyDriver(t)
	TestNormalizeRejectsMultipleDrivers(t)
	TestNormalizeRejectsFirecrackerDriver(t)
	TestNormalizeAcceptsSupportedDriverAndDefaultNetwork(t)
	TestNormalizeAcceptsEmptyNetworkAsDefault(t)
	TestNormalizeRejectsUnsupportedNetwork(t)
	TestNormalizeRejectsInvalidTrigger(t)
	TestNormalizePreservesSchedulerScript(t)
	TestNormalizeTreatsBlankSchedulerScriptAsUnset(t)
	TestNormalizeRejectsSchedulerScriptWithTriggers(t)
	TestNormalizePreservesSchedulerTriggersWithoutScript(t)
	TestNormalizeRejectsInvalidTriggerPayloads(t)
	TestNormalizeRejectsTriggerWithoutKind(t)
	TestParseRejectsDuplicateAgentKeys(t)
}

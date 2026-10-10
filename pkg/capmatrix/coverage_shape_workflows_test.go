package capmatrix

import "testing"

// TestIntegrationCapabilityMatrixWorkflows and
// TestE2ECapabilityMatrixWorkflows run the package's capability and provider
// matrix tests inside the integration and E2E coverage shapes. The capability
// report is an operator-facing claim about what each compiled driver enforces,
// so its validation belongs to every shape rather than only to unit coverage.
func TestIntegrationCapabilityMatrixWorkflows(t *testing.T) {
	t.Run("capability accepts both shapes", TestCapabilityValidateAcceptsBothShapes)
	t.Run("capability rejects unknown dimension", TestCapabilityValidateRejectsUnknownDimension)
	t.Run("capability rejects enforced with reason", TestCapabilityValidateRejectsEnforcedWithReason)
	t.Run("capability rejects enforced with empty mechanism", TestCapabilityValidateRejectsEnforcedWithEmptyMechanism)
	t.Run("capability rejects not enforced without closed reason", TestCapabilityValidateRejectsNotEnforcedWithoutClosedReason)
	t.Run("driver capabilities require every dimension once", TestDriverCapabilitiesValidateRequiresEveryDimensionExactlyOnce)
	t.Run("driver facts match the contract", TestDriverFactsMatchCapmatrixContract)
	t.Run("build snapshot freezes compiled drivers", TestBuildSnapshotFreezesEveryCompiledDriverWithDefaults)
	t.Run("new snapshot rejects invalid enforcement claim", TestNewSnapshotRejectsInvalidEnforcementClaim)
	t.Run("new snapshot rejects uncompiled driver and invalid provider", TestNewSnapshotRejectsUncompiledDriverAndInvalidProvider)
	t.Run("snapshot does not probe unavailable drivers", TestSnapshotDoesNotProbeUnavailableDrivers)
	t.Run("snapshot accessors do not expose mutable state", TestSnapshotAccessorsDoNotExposeMutableState)
	t.Run("declared provider feature matrix", TestDeclaredProviderFeatureMatrix)
	t.Run("declared providers reject missing feature", TestDeclaredProvidersRejectsMissingFeature)
	t.Run("declared provider protocol order matches dialect", TestDeclaredProviderProtocolOrderMatchesDialect)
}

func TestE2ECapabilityMatrixWorkflows(t *testing.T) {
	TestIntegrationCapabilityMatrixWorkflows(t)
}

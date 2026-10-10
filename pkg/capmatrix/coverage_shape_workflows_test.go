package capmatrix

import "testing"

// The capability matrix feeds the start preflight and the engine capability API,
// so it must carry coverage in the broader shapes and not only in the unit
// shape. These aggregators re-run the package's behavior tests under the shape
// markers the coverage gate keys on, as the other packages do.
func TestIntegrationCapabilityMatrixWorkflows(t *testing.T) {
	t.Run("enforced requires a mechanism", TestCapabilityValidateRejectsEnforcedWithEmptyMechanism)
	t.Run("enforced rejects a reason", TestCapabilityValidateRejectsEnforcedWithReason)
	t.Run("not enforced requires a closed reason", TestCapabilityValidateRejectsNotEnforcedWithoutClosedReason)
	t.Run("accepts both shapes", TestCapabilityValidateAcceptsBothShapes)
	t.Run("rejects unknown dimension", TestCapabilityValidateRejectsUnknownDimension)
	t.Run("driver capabilities cover every dimension once", TestDriverCapabilitiesValidateRequiresEveryDimensionExactlyOnce)
	t.Run("system probe covers every system dimension", TestProbeSystemCapabilitiesCoversEverySystemDimensionAndIsMeasured)
	t.Run("snapshot freezes the system probe", TestSnapshotFreezesTheSystemProbeWithoutReProbing)
	t.Run("snapshot rejects invalid measurement", TestNewSnapshotWithObservationsRejectsInvalidMeasurement)
	t.Run("simulated never shares the enforced state", TestSimulatedCapabilityNeverSharesTheEnforcedState)
	t.Run("accepts degraded and rejects inconsistent states", TestCapabilityValidateAcceptsDegradedAndRejectsInconsistentStates)
	t.Run("normalized fills state and source", TestCapabilityNormalizedFillsStateAndSource)
	t.Run("declarations are never measured", TestDriverDeclarationsNeverClaimToBeMeasured)
	t.Run("declared provider protocol order", TestDeclaredProviderProtocolOrderMatchesDialect)
	t.Run("declared provider feature matrix", TestDeclaredProviderFeatureMatrix)
	t.Run("declared providers require a feature", TestDeclaredProvidersRejectsMissingFeature)
	t.Run("empty declaration is allowed", TestEvaluateStartPreflightAllowsAnEmptyDeclaration)
	t.Run("docker default deny egress is rejected", TestEvaluateStartPreflightRejectsDockerDefaultDenyEgress)
	t.Run("degraded and missing mechanism are rejected", TestEvaluateStartPreflightRejectsDegradedAndMissingMechanism)
	t.Run("unknown dimension and driver are rejected", TestEvaluateStartPreflightRejectsUnknownDimensionAndDriver)
	t.Run("best effort is recorded as degradation", TestEvaluateStartPreflightRecordsBestEffortAsDegradation)
	t.Run("unavailable system precondition is rejected", TestEvaluateStartPreflightRejectsUnavailableSystemPrecondition)
	t.Run("measured lower layer failure is reported", TestMeasuredLowerLayerSeccompFailureIsReportedNotEnforced)
	t.Run("parse isolation requirements", TestParseIsolationRequirements)
	t.Run("build snapshot freezes every compiled driver", TestBuildSnapshotFreezesEveryCompiledDriverWithDefaults)
	t.Run("snapshot rejects an invalid enforcement claim", TestNewSnapshotRejectsInvalidEnforcementClaim)
	t.Run("snapshot rejects uncompiled driver and invalid provider", TestNewSnapshotRejectsUncompiledDriverAndInvalidProvider)
	t.Run("snapshot accessors do not expose mutable state", TestSnapshotAccessorsDoNotExposeMutableState)
	t.Run("snapshot does not probe unavailable drivers", TestSnapshotDoesNotProbeUnavailableDrivers)
	t.Run("driver facts match the capmatrix contract", TestDriverFactsMatchCapmatrixContract)
}

func TestE2ECapabilityMatrixWorkflows(t *testing.T) {
	TestIntegrationCapabilityMatrixWorkflows(t)
}

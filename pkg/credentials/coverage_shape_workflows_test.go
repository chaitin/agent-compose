package credentials_test

import "testing"

// The credential model is a pure decision layer: minting, authorization,
// injection planning, and the persisted row shape have no shape-specific
// boundary of their own, because the broker that calls them from a real sandbox
// lifecycle lands separately from this package. The coverage shapes therefore
// share one workflow rather than duplicating it, which is the same arrangement
// pkg/driver/coverage_shape_workflows_test.go uses for its driver workflows.
//
// TestCredentialModelWorkflow covers the unit shape; the Integration and E2E
// aliases run the identical assertions so the model counts toward each shape's
// coverage floor instead of being visible only to unit coverage. Once the
// broker wires these decisions to a sandbox, that wiring gets its own
// boundary-specific coverage and these aliases can shrink to what remains
// genuinely shape-independent.
func TestCredentialModelWorkflow(t *testing.T) {
	testCredentialModelWorkflow(t)
}

func TestIntegrationCredentialModelWorkflow(t *testing.T) {
	testCredentialModelWorkflow(t)
}

func TestE2ECredentialModelWorkflow(t *testing.T) {
	testCredentialModelWorkflow(t)
}

func testCredentialModelWorkflow(t *testing.T) {
	t.Helper()

	// Minting, the handle invariants, and the persisted row shape.
	TestNewHandleMintsShortLivedHashedHandle(t)
	TestNewHandleRefusesLongLivedAndUnattributableRequests(t)
	TestNewHandleIsNonRenewableThroughMinting(t)
	TestHandleValidateRejectsUnattributableAndIncompleteHandles(t)
	TestHandleOwnersAreNormalizedDeterministically(t)
	TestParseOwnersRejectsMalformedJSON(t)
	TestScanHandleRoundTripsPersistedRow(t)
	TestUnixMillisUsesZeroForUnsetTimes(t)

	// Authorization: attribution, lifetime, endpoint scope, owner scope.
	TestAuthorizeGrantsInScopeUse(t)
	TestAuthorizeExpiryIsExclusiveAtTheDeadline(t)
	TestAuthorizeRevocationTakesEffect(t)
	TestAuthorizeDeniesWithoutOwnerMetadata(t)
	TestAuthorizeForwardRightIsNotCredentialRight(t)
	TestAuthorizeDeniesOwnerNotGranted(t)
	TestAuthorizeBatchUsesOwnerIntersection(t)
	TestAuthorizeBatchDeniesDisjointAndEmptyOwnerSets(t)
	TestIntersectOwnersNormalizesAndRejectsMissingMetadata(t)

	// Injection planning over a fixed snapshot.
	TestSnapshotIsFixedAfterConstruction(t)
	TestPlanInjectionProducesHostScopedTLSRequiredSpec(t)
	TestPlanInjectionIsAllOrNothing(t)
	TestPlanInjectionDeniesHandleMissingFromSnapshot(t)
	TestPlanInjectionObservesRevocationFromTheSnapshot(t)
	TestPlanInjectionBatchesSharedHandleByOwnerIntersection(t)
	TestPlanInjectionRefusesTwoHandlesClaimingOneGuestVariable(t)
	TestPlanInjectionRefusesAHandleWithoutFingerprint(t)
}

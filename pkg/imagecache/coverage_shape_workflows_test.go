package imagecache

import "testing"

func TestIntegrationImageCacheWorkflow(t *testing.T) {
	testImageCacheWorkflow(t)
}

func TestE2EImageCacheWorkflow(t *testing.T) {
	testImageCacheWorkflow(t)
}

func testImageCacheWorkflow(t *testing.T) {
	t.Helper()
	TestCachePathsAndEnsure(t)
	TestMetadataLoadSaveRoundTrip(t)
	TestLoadMetadataRejectsCorruptJSON(t)
	TestCacheLockTempDirAndReadyFlag(t)
	TestErrorKindSupportsErrorsIsAndAs(t)
	TestParseReferenceUsesGoContainerRegistry(t)
	TestMaterializeOCILayoutCopiesValidLayoutAndReadyFlag(t)
	TestMaterializeOCILayoutReadyCacheHitDoesNotOverwrite(t)
	TestMaterializeOCILayoutReturnsNotFound(t)
	TestListFiltersMetadataByQuery(t)
	TestInspectFindsMetadataByRefsAndDigests(t)
	TestInspectReturnsNotFoundError(t)
	TestRemoveDeletesSingleMetadataReference(t)
	TestRemoveConflictsWhenMultipleRefsShareImage(t)
	TestRemoveForceDeletesAllRefsSharingImage(t)
	TestNormalizeReferenceDefaultsDockerStyleReference(t)
	TestNormalizeReferenceUsesConfiguredDefaultRegistry(t)
	TestNormalizeReferenceKeepsFullyQualifiedReference(t)
	TestNormalizeReferenceParsesDigestReference(t)
	TestNewImageMetadataCompletesFieldsAndPreservesRequestedRef(t)
	TestMetadataReloadKeepsLookupStableAndRequestedRef(t)
	TestMaterializeRootFSMergesLayersAndWhiteouts(t)
	TestMaterializeRootFSHandlesSymlinkHardlinkAndReadyHit(t)
	TestMaterializeRootFSRejectsPathEscape(t)
	TestMaterializeRootFSRejectsSymlinkParentEscape(t)
}

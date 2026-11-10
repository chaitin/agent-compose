//go:build linux

package driver

import "testing"

// TestOCILayerExtractionE2E runs the OCI layer extraction tests as one
// end-to-end suite so the E2E coverage shape exercises the warmup rootfs
// boundary, not only the individual unit cases selected by name.
func TestOCILayerExtractionE2E(t *testing.T) {
	suite := []struct {
		name string
		fn   func(*testing.T)
	}{
		{"materialize", TestExtractOCILayerMaterializesFilesAndLinks},
		{"plain-tar", TestExtractOCILayerAcceptsPlainTar},
		{"corrupt-gzip", TestExtractOCILayerRejectsCorruptGzip},
		{"escaping-entry", TestExtractOCILayerRejectsEscapingEntries},
		{"escaping-symlink", TestExtractOCILayerDoesNotFollowEscapingSymlinks},
		{"absolute-symlink", TestExtractOCILayerClampsAbsoluteSymlinksToRoot},
		{"internal-link", TestExtractOCILayerResolvesLinksInsideRoot},
		{"final-symlink", TestExtractOCILayerReplacesFinalSymlinkInsteadOfWritingThroughIt},
		{"whiteout", TestExtractOCILayerAppliesWhiteouts},
	}
	for _, tc := range suite {
		t.Run(tc.name, tc.fn)
	}
}

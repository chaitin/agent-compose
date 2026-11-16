package capmatrix

import "github.com/chaitin/agent-compose/pkg/agentinput"

// declaredMediaTypes is the static provider media matrix.
//
// It mirrors what each guest runner can hand to its harness: every provider
// consumes raster images, and only claude and opencode consume PDF documents
// natively. codex has an image-only CLI, dsh projects a file block to path text,
// and pi reads non-image files as UTF-8 text, which corrupts a PDF.
func declaredMediaTypes(provider string) []string {
	switch provider {
	case "codex", "pi", "dsh":
		return agentinput.ImageMediaTypes()
	case "claude", "opencode":
		return append(agentinput.ImageMediaTypes(), agentinput.MediaTypePDF)
	default:
		return nil
	}
}

// declaredMediaOrdering is how each runner consumes a turn that mixes text and
// media. claude builds an ordered content array and the dsh profile builds
// ordered content blocks; codex, opencode, and pi attach all media to one turn
// and join the text, so they aggregate.
func declaredMediaOrdering(provider string) MediaOrdering {
	switch provider {
	case "claude", "dsh":
		return MediaOrderingInterleaved
	default:
		return MediaOrderingAggregate
	}
}

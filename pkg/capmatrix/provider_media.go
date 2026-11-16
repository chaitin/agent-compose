package capmatrix

import "github.com/chaitin/agent-compose/pkg/agentinput"

// declaredMediaTypes is the static provider media matrix.
//
// It mirrors what each guest runner can hand to its harness: every provider
// consumes raster images, and only opencode consumes PDF documents through a
// verified path. claude's document block hangs against the facade, dsh projects
// a file block to path text, and pi reads non-image files as UTF-8 text, which
// corrupts a PDF.
func declaredMediaTypes(provider string) []string {
	switch provider {
	case "codex", "claude", "pi", "dsh":
		return agentinput.ImageMediaTypes()
	case "opencode":
		// opencode reads a PDF through its own text-extraction path, so it is
		// the only provider whose document support has been verified end to end.
		// claude's document block hangs against the facade and codex, pi, and
		// dsh have no native document modality.
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
	// The dsh runner writes one text projection and appends the attachments to
	// that single block, so its runtime behavior is aggregate even though the
	// DSH content model could interleave. Declaring interleaved would make a
	// client promise an order the model does not receive.
	switch provider {
	case "claude":
		return MediaOrderingInterleaved
	default:
		return MediaOrderingAggregate
	}
}

// NativeMediaTypes returns the media types provider hands to its harness
// natively. Any other media type is materialized as a file the agent reads with
// its own tools instead of being attached to the model request.
func NativeMediaTypes(provider string) []string {
	return declaredMediaTypes(provider)
}

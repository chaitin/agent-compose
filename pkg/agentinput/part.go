// Package agentinput owns the ordered multimodal input of one agent turn: its
// value object, the compatibility merge with the legacy prompt, admission
// rules, and the guest-side manifest shape.
//
// The package is deliberately free of I/O and of provider-specific behavior.
// Materializing bytes into a sandbox belongs to pkg/execution; translating a
// turn into a provider CLI invocation belongs to the guest runtime.
package agentinput

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalid reports input that a caller must correct.
var ErrInvalid = errors.New("invalid agent input")

// Source identifies how a media part carries its bytes.
type Source int

const (
	// SourceUnspecified is an invalid media part.
	SourceUnspecified Source = iota
	// SourceInline carries bytes uploaded with the request.
	SourceInline
	// SourceWorkspacePath references a file already inside the sandbox
	// workspace and copies nothing.
	SourceWorkspacePath
)

// Part is one ordered piece of a human turn. Exactly one of Text and Media is
// set.
type Part struct {
	Text  string
	Media *Media
}

// Media is one non-text part. Exactly one source is set.
type Media struct {
	// Type is the canonical lowercase IANA media type, for example
	// "image/png". It is required.
	Type string
	// Filename is an optional display name. It is never interpreted as a path.
	Filename string
	// Source selects which of Data and WorkspacePath carries the content.
	Source Source
	// Data holds inline bytes when Source is SourceInline.
	Data []byte
	// WorkspacePath holds a workspace-relative path when Source is
	// SourceWorkspacePath.
	WorkspacePath string
	// SHA256 optionally pins the content digest as lowercase hex.
	SHA256 string
}

// TextPart returns a text part.
func TextPart(text string) Part {
	return Part{Text: text}
}

// MediaPart returns a media part.
func MediaPart(media Media) Part {
	return Part{Media: &media}
}

// Normalize merges the legacy prompt with the ordered parts into the turn the
// guest runtime receives.
//
// A non-empty prompt is prepended as the first text part rather than rejected.
// That is the compatibility rule: a caller that already sends prompt keeps
// working untouched, and a caller that adds input media does not have to move
// or repeat its text. Adjacent text parts are joined with a blank line so the
// manifest stays small and the model sees one block per run of text.
//
// The returned warnings are user-visible and explain the normalization when
// both prompt and input were supplied.
func Normalize(prompt string, parts []Part) ([]Part, []string, error) {
	normalized := make([]Part, 0, len(parts)+1)
	var warnings []string
	if strings.TrimSpace(prompt) != "" {
		normalized = append(normalized, TextPart(prompt))
		if len(parts) > 0 {
			warnings = append(warnings, "prompt was prepended as the first text part because input is also set")
		}
	}
	normalized = append(normalized, cloneParts(parts)...)
	if len(normalized) == 0 {
		return nil, warnings, fmt.Errorf("%w: a turn requires a prompt or input", ErrInvalid)
	}
	merged := make([]Part, 0, len(normalized))
	hasText := false
	for index, part := range normalized {
		switch {
		case part.Media != nil && part.Text != "":
			return nil, warnings, fmt.Errorf("%w: part %d sets both text and media", ErrInvalid, index)
		case part.Media != nil:
			merged = append(merged, part)
		case strings.TrimSpace(part.Text) != "":
			hasText = true
			if last := len(merged) - 1; last >= 0 && merged[last].Media == nil {
				merged[last].Text += "\n\n" + part.Text
				continue
			}
			merged = append(merged, part)
		default:
			return nil, warnings, fmt.Errorf("%w: part %d is empty", ErrInvalid, index)
		}
	}
	if !hasText {
		return nil, warnings, fmt.Errorf("%w: a turn requires at least one non-empty text part", ErrInvalid)
	}
	return merged, warnings, nil
}

// Text returns the turn's text projection: text parts joined with a blank
// line, in order. It is what RunDetail.prompt keeps reporting.
func Text(parts []Part) string {
	var texts []string
	for _, part := range parts {
		if part.Media == nil && strings.TrimSpace(part.Text) != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

func cloneParts(parts []Part) []Part {
	out := make([]Part, len(parts))
	for index, part := range parts {
		out[index] = part
		if part.Media != nil {
			media := *part.Media
			media.Data = append([]byte(nil), part.Media.Data...)
			out[index].Media = &media
		}
	}
	return out
}

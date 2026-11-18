// Package agentinput owns the ordered text/file input of one agent turn: its
// value object, the compatibility merge with the legacy prompt, and admission
// rules.
//
// Files are provider-neutral. The daemon materializes them inside the sandbox
// workspace and appends their paths to the prompt, so a harness reads them with
// its own tools. This package never decides how a provider consumes a file.
package agentinput

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalid reports input that a caller must correct.
var ErrInvalid = errors.New("invalid agent input")

// Source identifies how a file carries its bytes.
type Source int

const (
	// SourceUnspecified is an invalid file.
	SourceUnspecified Source = iota
	// SourceInline carries bytes uploaded with the request.
	SourceInline
	// SourceWorkspacePath references a file already inside the sandbox
	// workspace and copies nothing.
	SourceWorkspacePath
)

// Part is one ordered piece of a human turn. Exactly one of Text and File is
// set.
type Part struct {
	Text string
	File *File
}

// File is one input file. Exactly one source is set.
type File struct {
	// Name is the display name and the basis of the materialized file name.
	Name string
	// MediaType is optional and informational. It never gates acceptance.
	MediaType string
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

// FilePart returns a file part.
func FilePart(file File) Part {
	return Part{File: &file}
}

// Normalize merges the legacy prompt with the ordered parts into the turn.
//
// A non-empty prompt is prepended as the first text part rather than rejected:
// an existing caller keeps sending prompt and a new caller adds files without
// repeating or moving its text. Adjacent text parts are joined with a blank
// line so the prompt stays compact.
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
		case part.File != nil && part.Text != "":
			return nil, warnings, fmt.Errorf("%w: part %d sets both text and file", ErrInvalid, index)
		case part.File != nil:
			merged = append(merged, part)
		case strings.TrimSpace(part.Text) != "":
			hasText = true
			if last := len(merged) - 1; last >= 0 && merged[last].File == nil {
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
// line, in order. File references are appended by the execution layer.
func Text(parts []Part) string {
	var texts []string
	for _, part := range parts {
		if part.File == nil && strings.TrimSpace(part.Text) != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

func cloneParts(parts []Part) []Part {
	out := make([]Part, len(parts))
	for index, part := range parts {
		out[index] = part
		if part.File != nil {
			file := *part.File
			file.Data = append([]byte(nil), part.File.Data...)
			out[index].File = &file
		}
	}
	return out
}

package agentinput

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeMergesPromptAsLeadingText(t *testing.T) {
	parts, warnings, err := Normalize("compare these", []Part{FilePart(File{Name: "a.csv", Source: SourceInline, Data: []byte("a,b\n")})})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	if parts[0].File != nil || parts[0].Text != "compare these" {
		t.Errorf("first part = %+v, want the prompt as text", parts[0])
	}
	if parts[1].File == nil {
		t.Errorf("second part = %+v, want the file part", parts[1])
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "prepended") {
		t.Errorf("warnings = %v, want one prepend warning", warnings)
	}
}

func TestNormalizePromptOnlyIsUnchanged(t *testing.T) {
	parts, warnings, err := Normalize("just text", nil)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(parts) != 1 || parts[0].Text != "just text" || parts[0].File != nil {
		t.Fatalf("parts = %+v, want one text part", parts)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

func TestNormalizeInputOnly(t *testing.T) {
	parts, _, err := Normalize("", []Part{TextPart("look"), FilePart(File{Name: "a.csv", Source: SourceInline, Data: []byte("x")})})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
}

func TestNormalizeJoinsAdjacentText(t *testing.T) {
	parts, _, err := Normalize("first", []Part{TextPart("second"), TextPart("third")})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(parts))
	}
	if parts[0].Text != "first\n\nsecond\n\nthird" {
		t.Errorf("text = %q", parts[0].Text)
	}
}

func TestNormalizeRejectsInvalidTurns(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		parts  []Part
	}{
		{name: "empty", prompt: "", parts: nil},
		{name: "whitespace prompt", prompt: " \n\t", parts: nil},
		{name: "file only", prompt: "", parts: []Part{FilePart(File{Name: "a.csv", Source: SourceInline, Data: []byte("x")})}},
		{name: "empty text part", prompt: "", parts: []Part{TextPart(""), FilePart(File{Name: "a.csv", Source: SourceInline, Data: []byte("x")})}},
		{name: "text and file on one part", prompt: "", parts: []Part{{Text: "x", File: &File{Name: "a.csv"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Normalize(tc.prompt, tc.parts)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestNormalizeDoesNotMutateCallerParts(t *testing.T) {
	input := []Part{TextPart("second")}
	parts, _, err := Normalize("first", input)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	parts[0].Text = "changed"
	if input[0].Text != "second" {
		t.Errorf("caller part mutated: %q", input[0].Text)
	}
}

func TestTextProjectionJoinsTextParts(t *testing.T) {
	parts := []Part{TextPart("a"), FilePart(File{Name: "x"}), TextPart("b")}
	if got := Text(parts); got != "a\n\nb" {
		t.Errorf("Text = %q, want %q", got, "a\n\nb")
	}
}

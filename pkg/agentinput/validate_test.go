package agentinput

import (
	"errors"
	"strings"
	"testing"
)

func imageOnlyPolicy() Policy {
	return Policy{Limits: DefaultLimits(), MediaTypes: ImageMediaTypes()}
}

func TestValidateAdmitsInlineRasterImages(t *testing.T) {
	cases := []struct {
		mediaType string
		data      []byte
	}{
		{MediaTypePNG, pngBytes()},
		{MediaTypeJPEG, jpegBytes()},
		{MediaTypeGIF, gifBytes()},
		{MediaTypeWebP, webpBytes()},
	}
	for _, tc := range cases {
		t.Run(tc.mediaType, func(t *testing.T) {
			parts := []Part{TextPart("look"), MediaPart(Media{Type: tc.mediaType, Source: SourceInline, Data: tc.data})}
			if err := Validate(parts, imageOnlyPolicy()); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestValidateAdmitsPDFUnderAnyPolicyAsGenericOrNative(t *testing.T) {
	parts := []Part{TextPart("read"), MediaPart(Media{Type: MediaTypePDF, Source: SourceInline, Data: pdfBytes()})}
	// A provider without native PDF support still accepts the file: execution
	// materializes it and hands the agent a path reference.
	if err := Validate(parts, imageOnlyPolicy()); err != nil {
		t.Fatalf("image policy: %v", err)
	}
	documentPolicy := Policy{Limits: DefaultLimits(), MediaTypes: append(ImageMediaTypes(), MediaTypePDF)}
	if err := Validate(parts, documentPolicy); err != nil {
		t.Fatalf("document policy: %v", err)
	}
}

func TestValidateRejectsMismatchedMagicBytes(t *testing.T) {
	parts := []Part{TextPart("look"), MediaPart(Media{Type: MediaTypePNG, Source: SourceInline, Data: jpegBytes()})}
	err := Validate(parts, imageOnlyPolicy())
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "declares") {
		t.Fatalf("err = %v, want a declared/content mismatch", err)
	}
}

func TestValidateRejectsInvalidLimitsAndShapes(t *testing.T) {
	limits := DefaultLimits()
	cases := []struct {
		name   string
		parts  []Part
		policy Policy
	}{
		{
			name:   "no parts",
			parts:  nil,
			policy: imageOnlyPolicy(),
		},
		{
			name:   "too many parts",
			parts:  []Part{TextPart("a"), TextPart("b"), TextPart("c")},
			policy: Policy{Limits: Limits{MaxParts: 2, MaxPartBytes: limits.MaxPartBytes, MaxTurnBytes: limits.MaxTurnBytes}, MediaTypes: ImageMediaTypes()},
		},
		{
			name:   "part over limit",
			parts:  []Part{TextPart("a"), MediaPart(Media{Type: MediaTypePNG, Source: SourceInline, Data: pngBytes()})},
			policy: Policy{Limits: Limits{MaxParts: 10, MaxPartBytes: 4, MaxTurnBytes: limits.MaxTurnBytes}, MediaTypes: ImageMediaTypes()},
		},
		{
			name:   "empty inline bytes",
			parts:  []Part{TextPart("a"), MediaPart(Media{Type: MediaTypePNG, Source: SourceInline})},
			policy: imageOnlyPolicy(),
		},
		{
			name:   "no source",
			parts:  []Part{TextPart("a"), MediaPart(Media{Type: MediaTypePNG})},
			policy: imageOnlyPolicy(),
		},
		{
			name:   "uppercase media type",
			parts:  []Part{TextPart("a"), MediaPart(Media{Type: "Image/PNG", Source: SourceInline, Data: pngBytes()})},
			policy: imageOnlyPolicy(),
		},
		{
			name:   "media type with parameters",
			parts:  []Part{TextPart("a"), MediaPart(Media{Type: "image/png; charset=binary", Source: SourceInline, Data: pngBytes()})},
			policy: imageOnlyPolicy(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(tc.parts, tc.policy); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestValidateAcceptsGenericFileTypesAsReferences(t *testing.T) {
	cases := []struct {
		mediaType string
		data      []byte
	}{
		{"text/csv", []byte("a,b\n1,2\n")},
		{"application/zip", []byte("PK\x03\x04")},
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", []byte("PK\x03\x04")},
		{"image/svg+xml", []byte("<svg/>")},
	}
	for _, tc := range cases {
		t.Run(tc.mediaType, func(t *testing.T) {
			parts := []Part{TextPart("read"), MediaPart(Media{Type: tc.mediaType, Source: SourceInline, Data: tc.data})}
			if err := Validate(parts, imageOnlyPolicy()); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestValidateChecksDigest(t *testing.T) {
	data := pngBytes()
	parts := []Part{TextPart("a"), MediaPart(Media{Type: MediaTypePNG, Source: SourceInline, Data: data, SHA256: sha256Hex(data)})}
	if err := Validate(parts, imageOnlyPolicy()); err != nil {
		t.Fatalf("matching digest: %v", err)
	}
	parts[1].Media.SHA256 = sha256Hex(jpegBytes())
	if err := Validate(parts, imageOnlyPolicy()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched digest err = %v, want ErrInvalid", err)
	}
	parts[1].Media.SHA256 = "not-a-digest"
	if err := Validate(parts, imageOnlyPolicy()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed digest err = %v, want ErrInvalid", err)
	}
}

func TestValidateWorkspacePaths(t *testing.T) {
	cases := []struct {
		path    string
		wantErr bool
	}{
		{path: "reports/2026-Q3.pdf"},
		{path: "a/../b"},
		{path: "a//b"},
		{path: "", wantErr: true},
		{path: ".", wantErr: true},
		{path: "/etc/passwd", wantErr: true},
		{path: "../secret", wantErr: true},
		{path: "a/../../secret", wantErr: true},
		{path: "a/..", wantErr: true},
		{path: "a\x00b", wantErr: true},
	}
	policy := Policy{Limits: DefaultLimits(), MediaTypes: append(ImageMediaTypes(), MediaTypePDF)}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			parts := []Part{TextPart("read"), MediaPart(Media{Type: MediaTypePDF, Source: SourceWorkspacePath, WorkspacePath: tc.path})}
			err := Validate(parts, policy)
			if tc.wantErr && !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
		})
	}
}

func TestNormalizeWorkspacePathCleans(t *testing.T) {
	cleaned, err := NormalizeWorkspacePath("a/./b//c")
	if err != nil {
		t.Fatalf("NormalizeWorkspacePath: %v", err)
	}
	if cleaned != "a/b/c" {
		t.Errorf("cleaned = %q, want %q", cleaned, "a/b/c")
	}
}

func TestValidateTurnBytes(t *testing.T) {
	policy := Policy{
		Limits:     Limits{MaxParts: 10, MaxPartBytes: 1 << 20, MaxTurnBytes: 10},
		MediaTypes: ImageMediaTypes(),
	}
	parts := []Part{
		TextPart("a"),
		MediaPart(Media{Type: MediaTypePNG, Source: SourceInline, Data: pngBytes()}),
	}
	if err := Validate(parts, policy); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestFileExtensionMatchesMediaType(t *testing.T) {
	cases := map[string]string{
		MediaTypePNG:    ".png",
		MediaTypeJPEG:   ".jpg",
		MediaTypeGIF:    ".gif",
		MediaTypeWebP:   ".webp",
		MediaTypePDF:    ".pdf",
		"image/svg+xml": "",
	}
	for mediaType, want := range cases {
		if got := FileExtension(mediaType); got != want {
			t.Errorf("FileExtension(%q) = %q, want %q", mediaType, got, want)
		}
	}
}

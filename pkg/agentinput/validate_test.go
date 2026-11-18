package agentinput

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

func defaultLimits() Limits {
	return DefaultLimits()
}

func TestValidateAcceptsArbitraryFileTypes(t *testing.T) {
	cases := []struct {
		name      string
		filename  string
		mediaType string
		data      []byte
	}{
		{"csv", "data.csv", "text/csv", []byte("a,b\n1,2\n")},
		{"zip", "bundle.zip", "application/zip", []byte("PK\x03\x04")},
		{"docx", "report.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", []byte("PK\x03\x04")},
		{"svg", "icon.svg", "image/svg+xml", []byte("<svg/>")},
		{"binary", "blob.bin", "", []byte{0x00, 0x01, 0x02}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parts := []Part{TextPart("read"), FilePart(File{Name: tc.filename, MediaType: tc.mediaType, Source: SourceInline, Data: tc.data})}
			if err := Validate(parts, defaultLimits()); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestValidateRejectsInvalidFiles(t *testing.T) {
	limits := defaultLimits()
	tooLarge := make([]byte, limits.MaxFileBytes+1)
	cases := []struct {
		name  string
		file  File
		limit Limits
	}{
		{name: "missing name", file: File{Source: SourceInline, Data: []byte("x")}},
		{name: "no source", file: File{Name: "a.csv"}},
		{name: "empty bytes", file: File{Name: "a.csv", Source: SourceInline}},
		{name: "bad media type", file: File{Name: "a.csv", MediaType: "CSV", Source: SourceInline, Data: []byte("x")}},
		{name: "over file limit", file: File{Name: "big.bin", Source: SourceInline, Data: tooLarge}},
		{name: "sha256 malformed", file: File{Name: "a.csv", Source: SourceInline, Data: []byte("x"), SHA256: "nope"}},
		{name: "workspace escape", file: File{Name: "a.csv", Source: SourceWorkspacePath, WorkspacePath: "../etc/passwd"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parts := []Part{TextPart("read"), FilePart(tc.file)}
			limit := tc.limit
			if limit == (Limits{}) {
				limit = limits
			}
			if err := Validate(parts, limit); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestValidateChecksDigest(t *testing.T) {
	data := []byte("hello")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	parts := []Part{TextPart("read"), FilePart(File{Name: "a.txt", Source: SourceInline, Data: data, SHA256: digest})}
	if err := Validate(parts, defaultLimits()); err != nil {
		t.Fatalf("matching digest: %v", err)
	}
	parts[1].File.SHA256 = hex.EncodeToString(sum[:])[:64]
	if err := Validate(parts, defaultLimits()); err != nil {
		t.Fatalf("matching digest copy: %v", err)
	}
	parts[1].File.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := Validate(parts, defaultLimits()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched digest err = %v, want ErrInvalid", err)
	}
}

func TestValidateTurnBytes(t *testing.T) {
	limits := Limits{MaxFileBytes: 100, MaxTurnBytes: 10, MaxFiles: 10}
	parts := []Part{
		TextPart("a"),
		FilePart(File{Name: "a.bin", Source: SourceInline, Data: []byte("123456")}),
		FilePart(File{Name: "b.bin", Source: SourceInline, Data: []byte("123456")}),
	}
	if err := Validate(parts, limits); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want a turn-size error", err)
	}
}

// max_files counts files, not text parts: a prompt and interleaved text must
// not consume the per-turn file budget.
func TestValidateCountsFilesNotTextParts(t *testing.T) {
	limits := Limits{MaxFileBytes: 100, MaxTurnBytes: 100, MaxFiles: 2}
	parts := []Part{TextPart("read")}
	for i := 0; i < 2; i++ {
		parts = append(parts,
			TextPart("next"),
			FilePart(File{Name: "a.bin", Source: SourceInline, Data: []byte("1")}),
		)
	}
	if err := Validate(parts, limits); err != nil {
		t.Fatalf("two files with text parts: %v", err)
	}
	parts = append(parts, FilePart(File{Name: "b.bin", Source: SourceInline, Data: []byte("1")}))
	if err := Validate(parts, limits); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want a file-count error", err)
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

func TestSafeFileName(t *testing.T) {
	cases := []struct {
		name      string
		mediaType string
		want      string
	}{
		{"report.docx", "", "report.docx"},
		{"../evil/../x.csv", "", "x.csv"},
		{"data", "text/csv", "data.csv"},
		{"a b?.bin", "", "a_b_.bin"},
		{"", "application/zip", "file.zip"},
	}
	for _, tc := range cases {
		if got := SafeFileName(tc.name, tc.mediaType); got != tc.want {
			t.Errorf("SafeFileName(%q, %q) = %q, want %q", tc.name, tc.mediaType, got, tc.want)
		}
	}
}

package archive

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type zipEntry struct {
	name    string
	content string
	mode    os.FileMode
}

func writeTestZip(t *testing.T, entries ...zipEntry) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "archive.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, entry := range entries {
		var (
			target io.Writer
			err    error
		)
		if entry.mode == 0 {
			target, err = writer.Create(entry.name)
		} else {
			header := &zip.FileHeader{Name: entry.name}
			header.SetMode(entry.mode)
			target, err = writer.CreateHeader(header)
		}
		if err != nil {
			t.Fatalf("create zip entry %s: %v", entry.name, err)
		}
		if _, err := target.Write([]byte(entry.content)); err != nil {
			t.Fatalf("write zip entry %s: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

func TestExtractZipRejectsEscapingEntries(t *testing.T) {
	for _, name := range []string{"../escape.txt", `..\escape.txt`, "/absolute.txt", "nested/../../escape.txt"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			archivePath := writeTestZip(t, zipEntry{name: name, content: "escape"})
			err := ExtractZip(archivePath, filepath.Join(root, "out"), ExtractPolicy{MaxExpandedBytes: 1 << 20})
			if err == nil || !strings.Contains(err.Error(), "escapes destination") {
				t.Fatalf("ExtractZip error = %v, want escape rejection", err)
			}
			if _, statErr := os.Stat(filepath.Join(root, "escape.txt")); !os.IsNotExist(statErr) {
				t.Fatalf("extraction wrote outside the destination: %v", statErr)
			}
		})
	}
}

func TestExtractZipRejectsSymlinkEntries(t *testing.T) {
	archivePath := writeTestZip(t, zipEntry{name: "link", content: "/etc/passwd", mode: os.ModeSymlink | 0o777})
	err := ExtractZip(archivePath, filepath.Join(t.TempDir(), "out"), ExtractPolicy{MaxExpandedBytes: 1 << 20})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ExtractZip error = %v, want symlink rejection", err)
	}
}

func TestExtractZipEnforcesExpandedLimitAndRemovesPartialFiles(t *testing.T) {
	archivePath := writeTestZip(t,
		zipEntry{name: "one.txt", content: "123456"},
		zipEntry{name: "two.txt", content: "abcdef"},
	)
	destination := filepath.Join(t.TempDir(), "out")
	err := ExtractZip(archivePath, destination, ExtractPolicy{MaxExpandedBytes: 10})
	if err == nil || !strings.Contains(err.Error(), "expanded size exceeds") {
		t.Fatalf("ExtractZip error = %v, want expanded size limit", err)
	}
	if _, statErr := os.Stat(filepath.Join(destination, "two.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("over-limit file was retained: %v", statErr)
	}
}

func TestExtractZipAllowsEmptyFiles(t *testing.T) {
	archivePath := writeTestZip(t, zipEntry{name: ".gitkeep"})
	destination := filepath.Join(t.TempDir(), "out")
	if err := ExtractZip(archivePath, destination, ExtractPolicy{MaxExpandedBytes: 10}); err != nil {
		t.Fatalf("ExtractZip empty archive: %v", err)
	}
	info, err := os.Stat(filepath.Join(destination, ".gitkeep"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("empty file size = %d, want 0", info.Size())
	}
}

func TestExtractZipSanitizesEntryModes(t *testing.T) {
	archivePath := writeTestZip(t,
		zipEntry{name: "bin/", mode: os.ModeDir | os.ModeSetgid | os.ModeSticky | 0o777},
		zipEntry{name: "bin/run.sh", content: "#!/bin/sh\n", mode: os.ModeSetuid | os.ModeSetgid | os.ModeSticky | 0o755},
		zipEntry{name: "notes.txt", content: "notes", mode: 0o666},
	)
	destination := filepath.Join(t.TempDir(), "out")
	if err := ExtractZip(archivePath, destination, ExtractPolicy{MaxExpandedBytes: 1 << 20}); err != nil {
		t.Fatalf("ExtractZip: %v", err)
	}
	script, err := os.Stat(filepath.Join(destination, "bin", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if script.Mode().Perm() != 0o755 {
		t.Fatalf("script mode = %v, want 0755 with special bits cleared", script.Mode().Perm())
	}
	if script.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Fatalf("script kept special mode bits: %v", script.Mode())
	}
	notes, err := os.Stat(filepath.Join(destination, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if notes.Mode().Perm() != 0o644 {
		t.Fatalf("notes mode = %v, want 0644 with group and other writes removed", notes.Mode().Perm())
	}
}

func TestExtractZipAppliesCompressionRatioGuard(t *testing.T) {
	archivePath := writeTestZip(t, zipEntry{name: "bundle.min.js", content: strings.Repeat("a", 200*1024)})
	cases := []struct {
		name      string
		policy    ExtractPolicy
		wantError bool
	}{
		{
			name:      "expansion above the floor trips the ratio guard",
			policy:    ExtractPolicy{MaxExpandedBytes: 1 << 30, MaxCompressionRatio: 10, CompressionRatioFloorBytes: 100 * 1024},
			wantError: true,
		},
		{
			name:   "small expansion stays below the floor and is allowed",
			policy: ExtractPolicy{MaxExpandedBytes: 1 << 30, MaxCompressionRatio: 10, CompressionRatioFloorBytes: 1 << 30},
		},
		{
			name:   "disabled guard allows any ratio",
			policy: ExtractPolicy{MaxExpandedBytes: 1 << 30},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := ExtractZip(archivePath, filepath.Join(t.TempDir(), "out"), test.policy)
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "compression ratio limit") {
					t.Fatalf("ExtractZip error = %v, want compression ratio rejection", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExtractZip error = %v", err)
			}
		})
	}
}

func TestExtractZipEnforcesMaxEntries(t *testing.T) {
	archivePath := writeTestZip(t, zipEntry{name: "one.txt", content: "1"}, zipEntry{name: "two.txt", content: "2"})
	err := ExtractZip(archivePath, filepath.Join(t.TempDir(), "out"), ExtractPolicy{MaxExpandedBytes: 1 << 20, MaxEntries: 1})
	if err == nil || !strings.Contains(err.Error(), "max 1") {
		t.Fatalf("ExtractZip error = %v, want entry cap", err)
	}
}

func TestExtractZipRejectsDeclaredOversizeEntryBeforeReading(t *testing.T) {
	// The declared size is checked up front, so an archive that claims a huge
	// expansion is rejected without producing any content.
	archivePath := writeDeclaredSizeZip(t, 1<<30, []byte("small"))
	destination := filepath.Join(t.TempDir(), "out")
	err := ExtractZip(archivePath, destination, ExtractPolicy{MaxExpandedBytes: 1024})
	if err == nil || !strings.Contains(err.Error(), "expanded size exceeds") {
		t.Fatalf("ExtractZip error = %v, want declared size rejection", err)
	}
	if entries, readErr := os.ReadDir(destination); readErr == nil && len(entries) != 0 {
		t.Fatalf("destination has %d entries, want none", len(entries))
	}
}

// writeDeclaredSizeZip writes a deflate entry whose local and central headers
// declare a different uncompressed size than the data they carry.
func writeDeclaredSizeZip(t *testing.T, declared uint64, payload []byte) string {
	t.Helper()
	var compressed bytes.Buffer
	deflater, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deflater.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := deflater.Close(); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(t.TempDir(), "declared.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	header := &zip.FileHeader{Name: "entry.bin", Method: zip.Deflate}
	header.UncompressedSize64 = declared
	header.CompressedSize64 = uint64(compressed.Len())
	header.CRC32 = crc32.ChecksumIEEE(payload)
	entry, err := writer.CreateRaw(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(compressed.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

func TestExtractZipRequiresExpandedLimit(t *testing.T) {
	archivePath := writeTestZip(t, zipEntry{name: "one.txt", content: "1"})
	if err := ExtractZip(archivePath, filepath.Join(t.TempDir(), "out"), ExtractPolicy{}); err == nil {
		t.Fatal("expected a missing expanded byte limit to be rejected")
	}
}

func TestSafeJoinRelative(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name      string
		relative  string
		want      string
		wantError bool
	}{
		{name: "nested", relative: "a/b", want: filepath.Join(root, "a", "b")},
		{name: "current", relative: ".", want: root},
		{name: "empty", relative: "", want: root},
		{name: "windows separators", relative: `a\b`, want: filepath.Join(root, "a", "b")},
		{name: "parent", relative: "../escape", wantError: true},
		{name: "nested parent", relative: "a/../../escape", wantError: true},
		{name: "absolute", relative: "/etc/passwd", wantError: true},
		{name: "windows parent", relative: `..\escape`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := SafeJoinRelative(root, test.relative)
			if test.wantError {
				if !errors.Is(err, ErrPathEscapes) {
					t.Fatalf("SafeJoinRelative error = %v, want ErrPathEscapes", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("SafeJoinRelative error = %v", err)
			}
			if got != test.want {
				t.Fatalf("SafeJoinRelative = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSanitizeFileMode(t *testing.T) {
	for _, test := range []struct {
		mode os.FileMode
		want os.FileMode
	}{
		{mode: 0, want: 0o644},
		{mode: 0o777, want: 0o755},
		{mode: 0o755, want: 0o755},
		{mode: 0o666, want: 0o644},
		{mode: os.ModeSetuid | 0o755, want: 0o755},
	} {
		t.Run(test.mode.String(), func(t *testing.T) {
			if got := sanitizeFileMode(test.mode); got != test.want {
				t.Fatalf("sanitizeFileMode(%v) = %v, want %v", test.mode, got, test.want)
			}
		})
	}
}

//go:build linux

package driver

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type layerEntry struct {
	name     string
	typeflag byte
	linkname string
	mode     int64
	body     string
}

func writeOCILayer(t *testing.T, entries []layerEntry, compress bool) string {
	t.Helper()
	blob := filepath.Join(t.TempDir(), "layer.tar")
	file, err := os.Create(blob)
	if err != nil {
		t.Fatal(err)
	}
	var writer io.Writer = file
	var gz *gzip.Writer
	if compress {
		gz = gzip.NewWriter(file)
		writer = gz
	}
	tarWriter := tar.NewWriter(writer)
	for _, entry := range entries {
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		size := int64(len(entry.body))
		if entry.typeflag == tar.TypeDir || entry.typeflag == tar.TypeLink || entry.typeflag == tar.TypeSymlink {
			size = 0
		}
		header := &tar.Header{
			Name:     entry.name,
			Typeflag: entry.typeflag,
			Linkname: entry.linkname,
			Mode:     mode,
			Size:     size,
			Uid:      os.Getuid(),
			Gid:      os.Getgid(),
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if _, err := tarWriter.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if gz != nil {
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return blob
}

func TestExtractOCILayerMaterializesFilesAndLinks(t *testing.T) {
	root := t.TempDir()
	blob := writeOCILayer(t, []layerEntry{
		{name: "etc", typeflag: tar.TypeDir, mode: 0o755},
		{name: "etc/config", typeflag: tar.TypeReg, mode: 0o640, body: "hello"},
		{name: "etc/alias", typeflag: tar.TypeSymlink, linkname: "config"},
		{name: "etc/hard", typeflag: tar.TypeLink, linkname: "etc/config", mode: 0o640},
	}, true)

	if err := extractOCILayer(root, blob); err != nil {
		t.Fatalf("extract layer: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "etc", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello" {
		t.Fatalf("config content = %q, want hello", content)
	}
	info, err := os.Stat(filepath.Join(root, "etc", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Fatalf("config mode = %o, want 0640", perm)
	}

	link, err := os.Readlink(filepath.Join(root, "etc", "alias"))
	if err != nil {
		t.Fatal(err)
	}
	if link != "config" {
		t.Fatalf("alias link = %q, want config", link)
	}
	aliasContent, err := os.ReadFile(filepath.Join(root, "etc", "alias"))
	if err != nil {
		t.Fatal(err)
	}
	if string(aliasContent) != "hello" {
		t.Fatalf("alias content = %q, want hello", aliasContent)
	}

	original, err := os.Stat(filepath.Join(root, "etc", "config"))
	if err != nil {
		t.Fatal(err)
	}
	hard, err := os.Stat(filepath.Join(root, "etc", "hard"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(original, hard) {
		t.Fatal("hard link does not reference the original inode")
	}
}

func TestExtractOCILayerAcceptsPlainTar(t *testing.T) {
	root := t.TempDir()
	blob := writeOCILayer(t, []layerEntry{
		{name: "plain.txt", typeflag: tar.TypeReg, body: "plain"},
	}, false)

	if err := extractOCILayer(root, blob); err != nil {
		t.Fatalf("extract layer: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "plain.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "plain" {
		t.Fatalf("content = %q, want plain", content)
	}
}

func TestExtractOCILayerRejectsCorruptGzip(t *testing.T) {
	blob := filepath.Join(t.TempDir(), "corrupt.tar")
	if err := os.WriteFile(blob, []byte{0x1f, 0x8b, 0x00}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractOCILayer(t.TempDir(), blob); err == nil {
		t.Fatal("extractOCILayer accepted a corrupt gzip layer")
	}
}

func TestExtractOCILayerRejectsEscapingEntries(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(parent, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		entry layerEntry
	}{
		{name: "file parent escape", entry: layerEntry{name: "../escaped.txt", typeflag: tar.TypeReg, body: "escape"}},
		{name: "file nested escape", entry: layerEntry{name: "dir/../../escaped.txt", typeflag: tar.TypeReg, body: "escape"}},
		{name: "whiteout parent escape", entry: layerEntry{name: "../.wh.victim"}},
		{name: "nameless whiteout", entry: layerEntry{name: "dir/.wh."}},
		{name: "hardlink parent escape", entry: layerEntry{name: "link", typeflag: tar.TypeLink, linkname: "../victim"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blob := writeOCILayer(t, []layerEntry{tc.entry}, false)
			if err := extractOCILayer(root, blob); err == nil {
				t.Fatal("extractOCILayer accepted an escaping entry")
			}
			if _, err := os.Stat(filepath.Join(parent, "escaped.txt")); !os.IsNotExist(err) {
				t.Fatalf("outside file exists after extraction: %v", err)
			}
			content, err := os.ReadFile(victim)
			if err != nil {
				t.Fatalf("outside whiteout victim was removed: %v", err)
			}
			if string(content) != "keep" {
				t.Fatalf("outside victim content = %q, want keep", content)
			}
		})
	}
}

func TestExtractOCILayerDoesNotFollowEscapingSymlinks(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	outside := filepath.Join(parent, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	blob := writeOCILayer(t, []layerEntry{
		{name: "pivot", typeflag: tar.TypeSymlink, linkname: "../outside"},
		{name: "pivot/pwned", typeflag: tar.TypeReg, body: "pwned"},
	}, false)

	if err := extractOCILayer(root, blob); err == nil {
		t.Fatal("extractOCILayer wrote through a symlink that leaves the rootfs")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned")); !os.IsNotExist(err) {
		t.Fatalf("outside file exists after extraction: %v", err)
	}
}

func TestExtractOCILayerClampsAbsoluteSymlinksToRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	outside := filepath.Join(parent, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Absolute links are interpreted inside the image rootfs, exactly as after
	// the disk is mounted; they cannot name a host path.
	blob := writeOCILayer(t, []layerEntry{
		{name: "pivot", typeflag: tar.TypeSymlink, linkname: "/outside"},
		{name: "pivot/pwned", typeflag: tar.TypeReg, body: "inside"},
	}, false)
	if err := os.MkdirAll(filepath.Join(root, "outside"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := extractOCILayer(root, blob); err != nil {
		t.Fatalf("extract layer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned")); !os.IsNotExist(err) {
		t.Fatalf("absolute link escaped the rootfs: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "outside", "pwned"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "inside" {
		t.Fatalf("content = %q, want inside", content)
	}
}

func TestExtractOCILayerResolvesLinksInsideRoot(t *testing.T) {
	root := t.TempDir()
	blob := writeOCILayer(t, []layerEntry{
		{name: "dir", typeflag: tar.TypeDir, mode: 0o755},
		{name: "dir/target", typeflag: tar.TypeDir, mode: 0o755},
		{name: "relative", typeflag: tar.TypeSymlink, linkname: "dir/target"},
		{name: "relative/a", typeflag: tar.TypeReg, body: "a"},
		{name: "absolute", typeflag: tar.TypeSymlink, linkname: "/dir/target"},
		{name: "absolute/b", typeflag: tar.TypeReg, body: "b"},
	}, false)

	if err := extractOCILayer(root, blob); err != nil {
		t.Fatalf("extract layer: %v", err)
	}
	for _, name := range []string{"a", "b"} {
		content, err := os.ReadFile(filepath.Join(root, "dir", "target", name))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != name {
			t.Fatalf("%s content = %q, want %s", name, content, name)
		}
	}
}

func TestExtractOCILayerReplacesFinalSymlinkInsteadOfWritingThroughIt(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "host.txt")
	if err := os.WriteFile(outside, []byte("host"), 0o600); err != nil {
		t.Fatal(err)
	}

	blob := writeOCILayer(t, []layerEntry{
		{name: "file", typeflag: tar.TypeSymlink, linkname: outside},
		{name: "file", typeflag: tar.TypeReg, body: "sandbox"},
	}, false)
	if err := extractOCILayer(root, blob); err != nil {
		t.Fatalf("extract layer: %v", err)
	}

	hostContent, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(hostContent) != "host" {
		t.Fatalf("host file content = %q, want host", hostContent)
	}
	info, err := os.Lstat(filepath.Join(root, "file"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("layer file is still a symlink")
	}
	content, err := os.ReadFile(filepath.Join(root, "file"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "sandbox" {
		t.Fatalf("layer file content = %q, want sandbox", content)
	}
}

func TestExtractOCILayerAppliesWhiteouts(t *testing.T) {
	root := t.TempDir()
	for _, file := range []string{
		"cache/old.txt",
		"cache/nested/old.txt",
		"other/remove.txt",
		"other/keep.txt",
	} {
		target := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("lower"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	blob := writeOCILayer(t, []layerEntry{
		{name: "cache/.wh..wh..opq"},
		{name: "cache/new.txt", typeflag: tar.TypeReg, body: "upper"},
		{name: "other/.wh.remove.txt"},
	}, false)
	if err := extractOCILayer(root, blob); err != nil {
		t.Fatalf("extract layer: %v", err)
	}

	for _, removed := range []string{"cache/old.txt", "cache/nested", "other/remove.txt"} {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(removed))); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after whiteout: %v", removed, err)
		}
	}
	for _, kept := range []string{"cache/new.txt", "other/keep.txt"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(kept))); err != nil {
			t.Fatalf("%s missing after whiteout: %v", kept, err)
		}
	}
}

func TestCleanLayerPath(t *testing.T) {
	cases := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{name: "etc/config", want: "etc/config"},
		{name: "./etc/config", want: "etc/config"},
		{name: "/etc/config", want: "etc/config"},
		{name: "etc//config", want: "etc/config"},
		{name: "etc/../config", want: "config"},
		{name: ".", want: "."},
		{name: "", want: "."},
		{name: "../etc", wantErr: true},
		{name: "etc/../../config", wantErr: true},
		{name: "..", wantErr: true},
		{name: "bad\x00name", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cleanLayerPath(tc.name)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("cleanLayerPath(%q) = %q, want error", tc.name, got)
				}
				if !strings.Contains(err.Error(), "escapes rootfs") && !strings.Contains(err.Error(), "NUL") {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("cleanLayerPath(%q): %v", tc.name, err)
			}
			if got != tc.want {
				t.Fatalf("cleanLayerPath(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

package workspaces

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractWorkspaceZipCountsActualBytesAndRejectsTruncation(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "workspace.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for _, item := range []struct{ name, content string }{{"one.txt", "123456"}, {"two.txt", "abcdef"}} {
		entry, err := archive.Create(item.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(item.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "content")
	err = extractWorkspaceZipWithLimit(archivePath, destination, 10)
	if err == nil || !strings.Contains(err.Error(), "expanded size limit") {
		t.Fatalf("extract error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "two.txt")); !os.IsNotExist(err) {
		t.Fatalf("over-limit file was retained: %v", err)
	}
}

package driver

import (
	"os"
	"testing"
)

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", path, err)
	}
	if got := string(data); got != want {
		t.Fatalf("ReadFile(%s) = %q, want %q", path, got, want)
	}
}

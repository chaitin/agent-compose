package sandboxstore

import (
	"context"
	"crypto/sha256"
	"errors"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func inputTestWorkspace(t *testing.T) (*Store, *domain.Sandbox) {
	t.Helper()
	root := t.TempDir()
	config := &appconfig.Config{DataRoot: root, SandboxRoot: filepath.Join(root, "sandboxes"), RuntimeDriver: "docker", DefaultImage: "guest:latest", GuestWorkspacePath: "/workspace"}
	store, err := NewWithConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := store.CreateSandbox(context.Background(), "input", "", "docker", "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store, sandbox
}

func inputDeclaration(id, name string, data []byte) domain.RunInputFile {
	sum := sha256.Sum256(data)
	return domain.RunInputFile{ID: id, Name: name, Size: uint64(len(data)), SHA256: sum[:]}
}

func TestRunInputIntegrationWriterValidationAndNoOverwrite(t *testing.T) {
	store, sandbox := inputTestWorkspace(t)
	ctx := context.Background()
	data := []byte("complete")
	writer, err := store.OpenRunInput(ctx, sandbox, testRunInput(inputDeclaration("a", "a.txt", data), inputDeclaration("empty", "empty.txt", nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := writer.Write(ctx, "unknown", 0, data); err == nil {
		t.Fatal("undeclared file accepted")
	}
	if err := writer.Write(ctx, "a", 1, data[:2]); err == nil {
		t.Fatal("missing bytes accepted")
	}
	if err := writer.Write(ctx, "a", 0, data[:4]); err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(ctx, "a", 0, data[:4]); err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(ctx, "a", 0, []byte("xxxx")); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate conflict=%v", err)
	}
	if err := writer.Write(ctx, "a", 2, data[:4]); err == nil {
		t.Fatal("overlap accepted")
	}
	if err := writer.Seal(ctx); err == nil {
		t.Fatal("partial file sealed")
	}
	if err := writer.Write(ctx, "a", 4, data[4:]); err != nil {
		t.Fatal(err)
	}
	if err := writer.Seal(ctx); err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(ctx, "a", 0, nil); err == nil {
		t.Fatal("sealed file write accepted")
	}
	existing := filepath.Join(sandbox.Summary.WorkspacePath, "a.txt")
	if err := os.WriteFile(existing, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writer.Publish(ctx, 0, "a.txt"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwriting publish=%v", err)
	}
	if err := writer.Publish(ctx, 0, "a (1).txt"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Publish(ctx, 1, "empty.txt"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Delivered(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(existing)
	if err != nil || string(got) != "original" {
		t.Fatalf("original=%q %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(sandbox.Summary.WorkspacePath, "a (1).txt"))
	if err != nil || string(got) != string(data) {
		t.Fatalf("delivered=%q %v", got, err)
	}
	info, err := os.Stat(filepath.Join(sandbox.Summary.WorkspacePath, "empty.txt"))
	if err != nil || info.Size() != 0 || info.Mode().Perm() != 0o644 {
		t.Fatalf("zero file=%v err=%v", info, err)
	}
	matches, err := filepath.Glob(filepath.Join(sandbox.Summary.WorkspacePath, ".ac-upload-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files=%v %v", matches, err)
	}
}

func TestRunInputIntegrationWorkspaceIsolationAndCleanup(t *testing.T) {
	for _, scenario := range []string{"symlink", "mount", "k8s", "external", "volume", "ancestor-volume"} {
		t.Run(scenario, func(t *testing.T) {
			store, sandbox := inputTestWorkspace(t)
			switch scenario {
			case "symlink":
				if err := os.RemoveAll(sandbox.Summary.WorkspacePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), sandbox.Summary.WorkspacePath); err != nil {
					t.Fatal(err)
				}
			case "mount":
				sandbox.Workspace = &domain.SandboxWorkspace{ConfigJSON: `{"mode":"mount"}`}
			case "k8s":
				sandbox.Summary.Driver = "k8s"
			case "external":
				sandbox.Summary.WorkspacePath = t.TempDir()
			case "ancestor-volume":
				store.config.GuestWorkspacePath = "/work/space"
				sandbox.VolumeMounts = []domain.SandboxVolumeMount{{Target: "/work"}}
			case "volume":
				sandbox.VolumeMounts = []domain.SandboxVolumeMount{{Target: "/workspace/a.txt"}}
			}
			writer, err := store.OpenRunInput(context.Background(), sandbox, testRunInput(inputDeclaration("a", "a", nil)))
			if err == nil {
				_ = writer.Close()
				t.Fatal("unsafe workspace accepted")
			}
		})
	}
	store, sandbox := inputTestWorkspace(t)
	writer, err := store.OpenRunInput(context.Background(), sandbox, testRunInput(inputDeclaration("a", "a", []byte("right"))))
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(context.Background(), "a", 0, []byte("wrong")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Seal(context.Background()); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	names := writer.TemporaryNames()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sandbox.Summary.WorkspacePath, names[0])); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial retained=%v", err)
	}
}

func testRunInput(files ...domain.RunInputFile) domain.RunInputPreparation {
	input := domain.RunInputPreparation{Files: files, TemporaryNames: make([]string, len(files))}
	for index := range files {
		input.TemporaryNames[index] = ".ac-upload-" + uuid.NewString()
	}
	return input
}

func TestRunInputTemporaryReplacementIsNeverPublishedOrDeleted(t *testing.T) {
	store, sandbox := inputTestWorkspace(t)
	ctx := context.Background()
	input := testRunInput(inputDeclaration("a", "a.txt", nil))
	writer, err := store.OpenRunInput(ctx, sandbox, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Seal(ctx); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(sandbox.Summary.WorkspacePath, input.TemporaryNames[0])
	if err := os.Rename(temporary, temporary+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temporary, []byte("other owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writer.Publish(ctx, 0, "a.txt"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("replacement publish=%v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(temporary)
	if err != nil || string(got) != "other owner" {
		t.Fatalf("replacement=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(sandbox.Summary.WorkspacePath, "a.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement published=%v", err)
	}
}

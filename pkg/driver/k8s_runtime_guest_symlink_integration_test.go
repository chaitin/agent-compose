//go:build k8scompose

package driver

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"
)

type localGuestSymlinkExecutor struct {
	command []string
}

func (e localGuestSymlinkExecutor) Stream(options remotecommand.StreamOptions) error {
	return e.StreamWithContext(context.Background(), options)
}

func (e localGuestSymlinkExecutor) StreamWithContext(ctx context.Context, options remotecommand.StreamOptions) error {
	command := exec.CommandContext(ctx, e.command[0], e.command[1:]...)
	command.Stdin, command.Stdout, command.Stderr = options.Stdin, options.Stdout, options.Stderr
	err := command.Run()
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return k8sexec.CodeExitError{Err: err, Code: exitError.ExitCode()}
	}
	return err
}

func TestIntegrationK8sGuestSymlinkPreservesCanonicalContentAndExistingLink(t *testing.T) {
	requireGuestPublicationTools(t)
	for _, migrate := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "managed directory"}[migrate], func(t *testing.T) {
			home := t.TempDir()
			canonical := filepath.Join(home, "canonical")
			projection := filepath.Join(home, "provider", "content")
			if err := os.MkdirAll(canonical, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(canonical, "entry.txt"), []byte("canonical content"), 0o600); err != nil {
				t.Fatal(err)
			}
			if migrate {
				if err := os.MkdirAll(projection, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(projection, ".owned.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(projection, "stale.txt"), []byte("old copy"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runtime, _ := newTestK8sRuntime()
			runtime.config.GuestHomePath = home
			runtime.newExecutor = func(_ *rest.Config, _ string, u *url.URL) (remotecommand.Executor, error) {
				return localGuestSymlinkExecutor{command: u.Query()["command"]}, nil
			}
			sandbox := testSandbox(t, "guest-link")
			for attempt := range 2 {
				// Runtime initialization restores the product's fixed /root;
				// this executor instead runs inside a temporary host directory.
				runtime.config.GuestHomePath = home
				before, _ := os.Lstat(projection)
				if err := runtime.EnsureGuestSymlink(context.Background(), sandbox, VMState{}, GuestSymlinkProjection{GuestPath: projection, RelativeTarget: "../canonical", ManagedMarkers: []string{".owned.json"}}); err != nil {
					t.Fatal(err)
				}
				after, err := os.Lstat(projection)
				if err != nil || after.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("projection is not a link: %v, %v", after, err)
				}
				if attempt == 1 && !os.SameFile(before, after) {
					t.Fatal("matching link was unnecessarily replaced")
				}
				data, err := os.ReadFile(filepath.Join(projection, "entry.txt"))
				if err != nil || string(data) != "canonical content" {
					t.Fatalf("projected content = %q, %v", data, err)
				}
				entries, err := os.ReadDir(filepath.Dir(projection))
				if err != nil || len(entries) != 2 || entries[0].Name() != ".agent-compose-content" || entries[1].Name() != "content" {
					t.Fatalf("unexpected publication residue: %v, %v", entries, err)
				}
			}
		})
	}
}

func TestIntegrationK8sGuestSymlinkRejectsUnmanagedPathsWithoutDeletingThem(t *testing.T) {
	requireGuestPublicationTools(t)
	for _, kind := range []string{"directory", "file", "symlink", "symlink marker"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "projection")
			source := filepath.Join(home, "source")
			if err := os.WriteFile(source, []byte("private content"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory", "symlink marker":
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink marker" {
					if err := os.Symlink(source, filepath.Join(path, ".owned")); err != nil {
						t.Fatal(err)
					}
				}
			case "file":
				if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("source", path); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			runtime, _ := newTestK8sRuntime()
			runtime.config.GuestHomePath = home
			runtime.newExecutor = func(_ *rest.Config, _ string, u *url.URL) (remotecommand.Executor, error) {
				return localGuestSymlinkExecutor{command: u.Query()["command"]}, nil
			}
			err = runtime.EnsureGuestSymlink(context.Background(), testSandbox(t, "unmanaged-link"), VMState{}, GuestSymlinkProjection{GuestPath: path, RelativeTarget: "canonical", ManagedMarkers: []string{".owned"}})
			if err == nil || !strings.Contains(err.Error(), "conflicts") {
				t.Fatalf("unmanaged replacement error = %v", err)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("unmanaged path changed: %v", err)
			}
			data, err := os.ReadFile(source)
			if err != nil || string(data) != "private content" {
				t.Fatalf("unmanaged source changed: %q, %v", data, err)
			}
		})
	}
}

func TestK8sGuestSymlinkRejectsInvalidPathsBeforeCreatingPod(t *testing.T) {
	for _, test := range []struct{ path, target, marker string }{
		{"relative", "target", ""}, {"/outside", "target", ""}, {"/root", "target", ""},
		{"/root/link", "/root/target", ""}, {"/root/link", "../outside", ""},
		{"/root/link", "", ""}, {"/root/link", "link", ""}, {"/root/link", ".", ""},
		{"/root/link", "target", "../marker"}, {"/root/link", "target", "a\\b"},
	} {
		runtime, client := newTestK8sRuntime()
		err := runtime.EnsureGuestSymlink(context.Background(), testSandbox(t, "invalid-link"), VMState{}, GuestSymlinkProjection{GuestPath: test.path, RelativeTarget: test.target, ManagedMarkers: []string{test.marker}})
		if err == nil || len(client.Actions()) != 0 {
			t.Fatalf("invalid input %+v: error=%v, API actions=%v", test, err, client.Actions())
		}
	}
}

// These tests execute the production shell, Node, tar and flock contract using a
// fake Kubernetes transport. A Linux guest image runs them when the host lacks
// flock; they do not assert that a real cluster was exercised.
func requireGuestPublicationTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"node", "sh", "tar", "flock"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("guest publication contract needs %s: %v", tool, err)
		}
	}
}

func TestIntegrationK8sGuestSymlinkRecoversInterruptedMigrationAndRollsBackFailure(t *testing.T) {
	requireGuestPublicationTools(t)
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprint(interrupted), func(t *testing.T) {
			home := t.TempDir()
			destination := filepath.Join(home, "projection")
			control := filepath.Join(home, ".agent-compose-projection")
			if interrupted {
				writeGuestPublicationFixture(t, filepath.Join(control, "previous"), "legacy")
				if err := os.WriteFile(filepath.Join(control, ".owner"), []byte("agent-compose-link-v1:"+destination+"->canonical"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				writeGuestPublicationFixture(t, destination, "legacy")
			}
			node, err := exec.LookPath("node")
			if err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			shim := "#!/bin/sh\ncase \"$2\" in *renameSync*) exit 73;; esac\nexec " + shellQuote(node) + " \"$@\"\n"
			if err := os.WriteFile(filepath.Join(bin, "node"), []byte(shim), 0o755); err != nil {
				t.Fatal(err)
			}
			originalPath := os.Getenv("PATH")
			t.Setenv("PATH", bin+string(os.PathListSeparator)+originalPath)
			script := guestHomeParentGuardScript(home, destination) + guestHomeSymlinkScript(destination, "canonical", []string{".owned"})
			if output, err := exec.Command("sh", "-c", script).CombinedOutput(); err == nil {
				t.Fatalf("rename fault succeeded: %s", output)
			}
			assertGuestPublication(t, destination, "legacy", 0)
			t.Setenv("PATH", originalPath)
			if output, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
				t.Fatalf("retry failed: %v, %s", err, output)
			}
			if actual, err := os.Readlink(destination); err != nil || actual != "canonical" {
				t.Fatalf("retried alias = %q, %v", actual, err)
			}
			entries, err := os.ReadDir(control)
			if err != nil || len(entries) != 2 {
				t.Fatalf("recovery residue = %v, %v", entries, err)
			}
		})
	}
}

func TestIntegrationK8sGuestPublicationMissingToolsPreservesLegacyDirectory(t *testing.T) {
	requireGuestPublicationTools(t)
	for _, tool := range []string{"node", "flock", "tar"} {
		t.Run(tool, func(t *testing.T) {
			home := t.TempDir()
			destination := filepath.Join(home, "content")
			writeGuestPublicationFixture(t, destination, "legacy")
			script := guestDirectoryPublicationScript(home, destination, ".owned", "completion")
			// Fault the command lookup itself without hiding sh needed by the test.
			script = strings.Replace(script, "command -v "+tool, "command -v agent-compose-test-missing-"+tool, 1)
			output, err := exec.Command("sh", "-c", script).CombinedOutput()
			if err == nil || !strings.Contains(string(output), "requires") {
				t.Fatalf("missing tool error=%v, output=%s", err, output)
			}
			data, err := os.ReadFile(filepath.Join(destination, "entry"))
			if err != nil || string(data) != "legacy" {
				t.Fatalf("missing tool changed legacy tree: %q, %v", data, err)
			}
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 1 {
				t.Fatalf("missing tool mutated home: %v, %v", entries, err)
			}
		})
	}
}

func TestIntegrationK8sGuestSymlinkMigratesEachExplicitLegacyMarker(t *testing.T) {
	requireGuestPublicationTools(t)
	markers := []string{".agent-compose-skills.json", ".agent-compose-managed"}
	for _, marker := range markers {
		t.Run(marker, func(t *testing.T) {
			home := t.TempDir()
			destination := filepath.Join(home, "projection")
			if err := os.Mkdir(destination, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(destination, marker), []byte("legacy marker"), 0o600); err != nil {
				t.Fatal(err)
			}
			script := guestHomeParentGuardScript(home, destination) + guestHomeSymlinkScript(destination, "canonical", markers)
			if output, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
				t.Fatalf("legacy marker migration: %v, %s", err, output)
			}
			if actual, err := os.Readlink(destination); err != nil || actual != "canonical" {
				t.Fatalf("legacy alias = %q, %v", actual, err)
			}
		})
	}
}

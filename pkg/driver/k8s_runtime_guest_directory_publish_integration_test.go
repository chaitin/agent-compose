//go:build k8scompose

package driver

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

func newLocalGuestPublisher(t *testing.T, home string) *k8sRuntime {
	t.Helper()
	runtime, _ := newTestK8sRuntime()
	runtime.config.GuestHomePath = home
	runtime.newExecutor = func(_ *rest.Config, _ string, u *url.URL) (remotecommand.Executor, error) {
		return localGuestSymlinkExecutor{command: u.Query()["command"]}, nil
	}
	return runtime
}

func writeGuestPublicationFixture(t *testing.T, root, value string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"entry": value, ".owned": "managed"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertGuestPublication(t *testing.T, destination, value string, generations int) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(destination, "entry"))
	if err != nil || string(data) != value {
		t.Fatalf("published content = %q, %v; want %q", data, err, value)
	}
	control := filepath.Join(filepath.Dir(destination), ".agent-compose-"+filepath.Base(destination))
	entries, err := os.ReadDir(control)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		switch {
		case strings.HasPrefix(entry.Name(), "generation-"):
			count++
		case entry.Name() == ".owner", entry.Name() == ".lock":
		default:
			t.Fatalf("unexpected publication residue %q", entry.Name())
		}
	}
	if count != generations {
		t.Fatalf("generation count = %d, want %d", count, generations)
	}
}

func TestIntegrationK8sGuestDirectoryPublicationKeepsCompleteGenerationsAndCleansOldContent(t *testing.T) {
	requireGuestPublicationTools(t)
	for _, migrate := range []bool{false, true} {
		t.Run(fmt.Sprint(migrate), func(t *testing.T) {
			home, source := t.TempDir(), t.TempDir()
			destination := filepath.Join(home, "content")
			if migrate {
				writeGuestPublicationFixture(t, destination, "legacy")
			}
			runtime := newLocalGuestPublisher(t, home)
			for _, value := range []string{"first", "second", "third"} {
				writeGuestPublicationFixture(t, source, value)
				runtime.config.GuestHomePath = home
				if err := runtime.PublishGuestDirectory(context.Background(), testSandbox(t, "publish"), VMState{}, GuestDirectoryPublication{HostSource: source, GuestDestination: destination, ManagedMarker: ".owned"}); err != nil {
					t.Fatal(err)
				}
				assertGuestPublication(t, destination, value, 1)
				if link, err := os.Readlink(destination); err != nil || !strings.HasPrefix(link, ".agent-compose-content/generation-") {
					t.Fatalf("canonical link = %q, %v", link, err)
				}
			}
		})
	}
}

func TestIntegrationK8sGuestDirectoryPublicationRejectsValidArchivePrefixAfterProducerFailure(t *testing.T) {
	requireGuestPublicationTools(t)
	home, source := t.TempDir(), t.TempDir()
	destination := filepath.Join(home, "content")
	writeGuestPublicationFixture(t, destination, "complete old tree")
	writeGuestPublicationFixture(t, source, "partial new tree")
	// Sorted last: the tar reader sees a valid prefix containing regular files
	// before the producer rejects this unsupported entry and closes its stream.
	if err := unix.Mkfifo(filepath.Join(source, "zz-unsupported"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := newLocalGuestPublisher(t, home)
	err := runtime.PublishGuestDirectory(context.Background(), testSandbox(t, "prefix"), VMState{}, GuestDirectoryPublication{HostSource: source, GuestDestination: destination, ManagedMarker: ".owned"})
	if err == nil {
		t.Fatal("incomplete source unexpectedly published")
	}
	assertGuestPublication(t, destination, "complete old tree", 0)
	if err := os.Remove(filepath.Join(source, "zz-unsupported")); err != nil {
		t.Fatal(err)
	}
	runtime.config.GuestHomePath = home
	if err := runtime.PublishGuestDirectory(context.Background(), testSandbox(t, "prefix"), VMState{}, GuestDirectoryPublication{HostSource: source, GuestDestination: destination, ManagedMarker: ".owned"}); err != nil {
		t.Fatal(err)
	}
	assertGuestPublication(t, destination, "partial new tree", 1)
}

func TestIntegrationK8sGuestDirectoryPublicationRecoversInterruptedLegacyMigration(t *testing.T) {
	requireGuestPublicationTools(t)
	home, source := t.TempDir(), t.TempDir()
	destination := filepath.Join(home, "content")
	control := filepath.Join(home, ".agent-compose-content")
	writeGuestPublicationFixture(t, filepath.Join(control, "previous"), "recover me")
	writeGuestPublicationFixture(t, filepath.Join(control, "generation-interrupted"), "unpublished")
	if err := os.WriteFile(filepath.Join(control, ".owner"), []byte("agent-compose-directory-v1:"+destination), 0o600); err != nil {
		t.Fatal(err)
	}
	// An abandoned lock pathname carries no process ownership; flock can take it.
	if err := os.WriteFile(filepath.Join(control, ".lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeGuestPublicationFixture(t, source, "replacement")
	var archive bytes.Buffer
	if err := writeTarArchive(&archive, source); err != nil {
		t.Fatal(err)
	}
	// A syntactically complete tar lacking the random producer completion entry
	// still must fail, after recovery restores the original whole directory.
	command := exec.Command("sh", "-c", guestDirectoryPublicationScript(home, destination, ".owned", "completion-required"))
	command.Stdin = &archive
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "incomplete directory publication archive") {
		t.Fatalf("incomplete archive = %v, %s", err, output)
	}
	assertGuestPublication(t, destination, "recover me", 0)
}

func TestIntegrationK8sGuestDirectoryPublicationRollsBackRenameFailure(t *testing.T) {
	requireGuestPublicationTools(t)
	home, source := t.TempDir(), t.TempDir()
	destination := filepath.Join(home, "content")
	writeGuestPublicationFixture(t, destination, "old")
	writeGuestPublicationFixture(t, source, "new")
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	shim := "#!/bin/sh\ncase \"$2\" in *renameSync*) exit 73;; esac\nexec " + shellQuote(node) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "node"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var archive bytes.Buffer
	if err := writeTarArchiveWithCompletion(&archive, source, "completion"); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", "-c", guestDirectoryPublicationScript(home, destination, ".owned", "completion"))
	command.Stdin = &archive
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("rename fault unexpectedly succeeded: %s", output)
	}
	assertGuestPublication(t, destination, "old", 0)
}

func TestIntegrationK8sGuestPublicationRejectsRedirectedParentsAndUnownedRecovery(t *testing.T) {
	requireGuestPublicationTools(t)
	for _, kind := range []string{"parent", "control", "backup", "generation"} {
		t.Run(kind, func(t *testing.T) {
			home, outside, source := t.TempDir(), t.TempDir(), t.TempDir()
			writeGuestPublicationFixture(t, outside, "private")
			writeGuestPublicationFixture(t, source, "new")
			destination := filepath.Join(home, "nested", "content")
			control := filepath.Join(home, "nested", ".agent-compose-content")
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "parent":
				if err := os.Remove(filepath.Dir(destination)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Dir(destination)); err != nil {
					t.Fatal(err)
				}
			case "control":
				if err := os.Symlink(outside, control); err != nil {
					t.Fatal(err)
				}
			case "backup", "generation":
				if err := os.Mkdir(control, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(control, ".owner"), []byte("agent-compose-directory-v1:"+destination), 0o600); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(control, "previous")
				if kind == "generation" {
					path = filepath.Join(control, "generation-redirected")
					if err := os.Symlink(".agent-compose-content/generation-redirected", destination); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			}
			runtime := newLocalGuestPublisher(t, home)
			if err := runtime.PublishGuestDirectory(context.Background(), testSandbox(t, "redirect"), VMState{}, GuestDirectoryPublication{HostSource: source, GuestDestination: destination, ManagedMarker: ".owned"}); err == nil {
				t.Fatal("redirected publication unexpectedly succeeded")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 2 {
				t.Fatalf("outside path changed: %v, %v", entries, err)
			}
			data, err := os.ReadFile(filepath.Join(outside, "entry"))
			if err != nil || string(data) != "private" {
				t.Fatalf("outside content = %q, %v", data, err)
			}
		})
	}
}

func TestIntegrationK8sGuestDirectoryPublicationProcessLockSurvivesNoOwnerFileWindow(t *testing.T) {
	requireGuestPublicationTools(t)
	home, source := t.TempDir(), t.TempDir()
	destination := filepath.Join(home, "content")
	writeGuestPublicationFixture(t, source, "original")
	runtime := newLocalGuestPublisher(t, home)
	sandbox := testSandbox(t, "locked")
	if err := runtime.PublishGuestDirectory(context.Background(), sandbox, VMState{}, GuestDirectoryPublication{HostSource: source, GuestDestination: destination, ManagedMarker: ".owned"}); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(home, ".agent-compose-content", ".lock"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	writeGuestPublicationFixture(t, source, "next")
	runtime.config.GuestHomePath = home
	err = runtime.PublishGuestDirectory(context.Background(), sandbox, VMState{}, GuestDirectoryPublication{HostSource: source, GuestDestination: destination, ManagedMarker: ".owned"})
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("concurrent publication = %v", err)
	}
	assertGuestPublication(t, destination, "original", 1)
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	runtime.config.GuestHomePath = home
	if err := runtime.PublishGuestDirectory(context.Background(), sandbox, VMState{}, GuestDirectoryPublication{HostSource: source, GuestDestination: destination, ManagedMarker: ".owned"}); err != nil {
		t.Fatal(err)
	}
	assertGuestPublication(t, destination, "next", 1)
}

func TestK8sGuestPublicationPreservesPersistentVolumeOwnershipBeforeCreatingPod(t *testing.T) {
	for _, mount := range []string{"/root/managed", "/root", "/root/managed/nested"} {
		t.Run(mount, func(t *testing.T) {
			runtime, client := newTestK8sRuntime()
			sandbox := testSandbox(t, "volume-publication")
			sandbox.VolumeMounts = []SandboxVolumeMount{{Type: "volume", Driver: RuntimeDriverK8s, Target: mount}}
			for _, err := range []error{
				runtime.PublishGuestDirectory(context.Background(), sandbox, VMState{}, GuestDirectoryPublication{HostSource: t.TempDir(), GuestDestination: "/root/managed", ManagedMarker: ".owned"}),
				runtime.EnsureGuestSymlink(context.Background(), sandbox, VMState{}, GuestSymlinkProjection{GuestPath: "/root/managed", RelativeTarget: "target", ManagedMarkers: []string{".owned"}}),
			} {
				if err == nil || !strings.Contains(err.Error(), "persistent volume") {
					t.Fatalf("PVC overlap publication = %v", err)
				}
			}
			if len(client.Actions()) != 0 {
				t.Fatalf("PVC overlap unexpectedly created a Pod: %v", client.Actions())
			}
		})
	}
}

package driver

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	containerapi "github.com/docker/docker/api/types/container"
	mountapi "github.com/docker/docker/api/types/mount"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/nfsvolume"
)

func testNFSSource(t *testing.T, options map[string]string) nfsvolume.Source {
	t.Helper()
	source, _, err := nfsvolume.FromOptions(options)
	if err != nil {
		t.Fatalf("FromOptions: %v", err)
	}
	return source
}

func testNFSSandbox(t *testing.T, root string, readOnly bool) (*Sandbox, nfsvolume.Source) {
	t.Helper()
	source := testNFSSource(t, map[string]string{"server": "nas.example.com", "export": "/srv/share", "mount_options": "nolock"})
	session := testRuntimeMountSandbox(root)
	session.VolumeMounts = []SandboxVolumeMount{{
		ID:       "mount-nfs0001",
		Type:     "volume",
		Source:   "shared",
		Target:   "/mnt/shared",
		ReadOnly: readOnly,
		VolumeID: "volume-1",
		Driver:   "nfs",
		HostPath: source.Ref(),
	}}
	return session, source
}

// stubNFSMounts replaces the kernel mount, the mount table and the unmount so
// tests observe what ensureSandboxNFSMounts would do on a host.
func stubNFSMounts(t *testing.T, state map[string]string, mountErr error) *[]string {
	t.Helper()
	var calls []string
	originalMounter, originalState, originalUnmounter := nfsVolumeMounter, nfsVolumeMountStateOf, boxliteVolumeBridgeUnmounter
	nfsVolumeMounter = func(_ context.Context, source nfsvolume.Source, mountPoint string, readOnly bool) error {
		mode := "rw"
		if readOnly {
			mode = "ro"
		}
		calls = append(calls, "mount "+source.Device()+" "+mountPoint+" "+mode)
		if mountErr != nil {
			return mountErr
		}
		state[mountPoint] = "nfs4"
		return nil
	}
	nfsVolumeMountStateOf = func(path string) (bool, bool, error) {
		fsType, ok := state[path]
		return ok, fsType == "nfs" || fsType == "nfs4", nil
	}
	boxliteVolumeBridgeUnmounter = func(path string) error {
		calls = append(calls, "unmount "+path)
		delete(state, path)
		return nil
	}
	t.Cleanup(func() {
		nfsVolumeMounter, nfsVolumeMountStateOf, boxliteVolumeBridgeUnmounter = originalMounter, originalState, originalUnmounter
	})
	return &calls
}

func TestEnsureSandboxNFSMountsMountsOnceUnderSandboxVolumes(t *testing.T) {
	root := t.TempDir()
	session, _ := testNFSSandbox(t, root, true)
	state := map[string]string{}
	calls := stubNFSMounts(t, state, nil)

	if err := ensureSandboxNFSMounts(context.Background(), session); err != nil {
		t.Fatalf("ensureSandboxNFSMounts: %v", err)
	}
	mountPoint := filepath.Join(root, "volumes", "mount-nfs0001")
	want := []string{"mount nas.example.com:/srv/share " + mountPoint + " ro"}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %q, want %q", *calls, want)
	}
	// A second start keeps the export that is still mounted.
	if err := ensureSandboxNFSMounts(context.Background(), session); err != nil {
		t.Fatalf("second ensureSandboxNFSMounts: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls after second ensure = %q", *calls)
	}
}

func TestEnsureSandboxNFSMountsRefusesForeignMountAndRollsBack(t *testing.T) {
	root := t.TempDir()
	session, _ := testNFSSandbox(t, root, false)
	first := filepath.Join(root, "volumes", "mount-nfs0001")
	session.VolumeMounts = append(session.VolumeMounts, session.VolumeMounts[0])
	session.VolumeMounts[1].ID = "mount-nfs0002"
	session.VolumeMounts[1].Target = "/mnt/other"
	second := filepath.Join(root, "volumes", "mount-nfs0002")

	state := map[string]string{second: "ext4"}
	calls := stubNFSMounts(t, state, nil)
	err := ensureSandboxNFSMounts(context.Background(), session)
	if err == nil || !strings.Contains(err.Error(), "non-NFS mount") {
		t.Fatalf("error = %v, want non-NFS mount refusal", err)
	}
	want := []string{"mount nas.example.com:/srv/share " + first + " rw", "unmount " + first}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %q, want %q", *calls, want)
	}
	if state[second] != "ext4" {
		t.Fatal("the foreign mount was touched")
	}
}

func TestEnsureSandboxNFSMountsReportsMountFailure(t *testing.T) {
	session, _ := testNFSSandbox(t, t.TempDir(), false)
	stubNFSMounts(t, map[string]string{}, errors.New("connection refused"))
	err := ensureSandboxNFSMounts(context.Background(), session)
	if err == nil || !strings.Contains(err.Error(), `mount nfs volume "shared" (nas.example.com:/srv/share): connection refused`) {
		t.Fatalf("error = %v", err)
	}
}

func TestRuntimeMountManifestPointsNFSVolumesAtMountPoints(t *testing.T) {
	root := t.TempDir()
	session, _ := testNFSSandbox(t, root, true)
	manifest, err := prepareRuntimeMountManifest(testRuntimeMountConfig(), session, RuntimeDriverMicrosandbox)
	if err != nil {
		t.Fatalf("prepareRuntimeMountManifest: %v", err)
	}
	want := filepath.Join(root, "volumes", "mount-nfs0001")
	for _, mount := range manifest.Mounts {
		if mount.GuestPath == "/mnt/shared" {
			if mount.HostPath != want || !mount.ReadOnly || mount.Type != "bind" {
				t.Fatalf("nfs manifest entry = %+v, want ro bind from %s", mount, want)
			}
			return
		}
	}
	t.Fatalf("manifest has no /mnt/shared entry: %+v", manifest.Mounts)
}

func TestRuntimeMountManifestRejectsCorruptedNFSReference(t *testing.T) {
	session, _ := testNFSSandbox(t, t.TempDir(), false)
	session.VolumeMounts[0].HostPath = "/some/host/dir"
	_, err := prepareRuntimeMountManifest(testRuntimeMountConfig(), session, RuntimeDriverDocker)
	if err == nil || !strings.Contains(err.Error(), "not an nfs:// reference") {
		t.Fatalf("error = %v", err)
	}
}

func TestBoxliteVolumeBridgeLeavesNFSMountPointToTheVMHost(t *testing.T) {
	root := t.TempDir()
	session, _ := testNFSSandbox(t, root, false)
	originalMounter := boxliteVolumeBridgeMounter
	boxliteVolumeBridgeMounter = func(sourcePath, targetPath string, _ bool) error {
		t.Fatalf("bridge bind-mounted %s -> %s for an nfs volume", sourcePath, targetPath)
		return nil
	}
	t.Cleanup(func() { boxliteVolumeBridgeMounter = originalMounter })

	if _, err := prepareRuntimeMountManifest(testRuntimeMountConfig(), session, RuntimeDriverBoxlite); err != nil {
		t.Fatalf("prepareRuntimeMountManifest: %v", err)
	}
	commands := strings.Join(boxliteVolumeGuestSymlinkCommands(session), "\n")
	if !strings.Contains(commands, "/data/volumes/mount-nfs0001") || !strings.Contains(commands, "/mnt/shared") {
		t.Fatalf("guest symlink commands = %s", commands)
	}
}

func TestDockerRuntimeMountsNFSVolumeThroughEngine(t *testing.T) {
	root := t.TempDir()
	config := &appconfig.Config{
		SandboxRoot:        filepath.Join(root, "sandboxes"),
		GuestWorkspacePath: "/workspace",
		GuestHomePath:      "/root",
		GuestStateRoot:     "/data/state",
		GuestRuntimeRoot:   "/data/runtime",
		GuestLogRoot:       "/data/logs",
	}
	session, source := testNFSSandbox(t, filepath.Join(config.SandboxRoot, "session-1"), true)
	if _, err := prepareRuntimeMountManifest(config, session, RuntimeDriverDocker); err != nil {
		t.Fatalf("prepareRuntimeMountManifest: %v", err)
	}
	runtime := &dockerRuntime{config: config}
	mounts, err := runtime.dockerRuntimeMounts(context.Background(), nil, session)
	if err != nil {
		t.Fatalf("dockerRuntimeMounts: %v", err)
	}
	var found *mountapi.Mount
	for i := range mounts {
		if mounts[i].Target == "/mnt/shared" {
			found = &mounts[i]
		}
	}
	if found == nil {
		t.Fatalf("no /mnt/shared mount in %+v", mounts)
	}
	if found.Type != mountapi.TypeVolume || found.Source != source.DockerVolumeName() || !found.ReadOnly {
		t.Fatalf("nfs mount = %+v", found)
	}
	driver := found.VolumeOptions.DriverConfig
	if driver.Name != "local" || driver.Options["type"] != "nfs" || driver.Options["device"] != ":/srv/share" ||
		driver.Options["o"] != "addr=nas.example.com,vers=4,nolock" {
		t.Fatalf("driver config = %+v", driver)
	}

	// The container created with these mounts must be reusable, while an
	// image's anonymous volume is not an agent-compose mount.
	info := containerapi.InspectResponse{}
	for _, mount := range mounts {
		point := containerapi.MountPoint{Type: mount.Type, Destination: mount.Target, RW: !mount.ReadOnly, Source: mount.Source}
		if mount.Type == mountapi.TypeVolume {
			point.Name = mount.Source
			point.Source = "/var/lib/docker/volumes/" + mount.Source + "/_data"
		}
		info.Mounts = append(info.Mounts, point)
	}
	info.Mounts = append(info.Mounts, containerapi.MountPoint{Type: mountapi.TypeVolume, Name: "0123abcd", Destination: "/var/lib/data", RW: true})
	if !dockerContainerMountsMatch(info, mounts) {
		t.Fatal("container with the expected nfs volume was rejected")
	}
	for i := range info.Mounts {
		if info.Mounts[i].Destination == "/mnt/shared" {
			info.Mounts[i].Name = nfsvolume.DockerVolumeNamePrefix + "000000000000000000000000"
		}
	}
	if dockerContainerMountsMatch(info, mounts) {
		t.Fatal("container with a different nfs export was accepted")
	}
}

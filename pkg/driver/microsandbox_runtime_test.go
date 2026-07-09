//go:build cgo

package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/identity"
	"agent-compose/pkg/runtimecache"
)

func TestMicrosandboxExecCollectorMapsStdioStreams(t *testing.T) {
	var streamed []ExecChunk
	collector := &microsandboxExecCollector{stream: func(chunk ExecChunk) {
		streamed = append(streamed, chunk)
	}}
	collector.writeChunk(ExecChunk{Text: "out"})
	collector.writeChunk(ExecChunk{Text: "err", Stream: StdioStderr})

	if collector.stdout.String() != "out" {
		t.Fatalf("stdout = %q", collector.stdout.String())
	}
	if collector.stderr.String() != "err" {
		t.Fatalf("stderr = %q", collector.stderr.String())
	}
	if collector.output.String() != "outerr" {
		t.Fatalf("output = %q", collector.output.String())
	}
	want := []ExecChunk{{Text: "out"}, {Text: "err", Stream: StdioStderr}}
	if len(streamed) != len(want) {
		t.Fatalf("streamed chunks = %#v", streamed)
	}
	for i := range want {
		if streamed[i] != want[i] {
			t.Fatalf("streamed[%d] = %#v, want %#v", i, streamed[i], want[i])
		}
	}
}

func TestMicrosandboxResolveLibkrunfwPrefersVersionedRealFile(t *testing.T) {
	libDir := t.TempDir()
	versioned := filepath.Join(libDir, "libkrunfw.so.5.5.0")
	if err := os.WriteFile(versioned, []byte("krun"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("libkrunfw.so.5.5.0", filepath.Join(libDir, "libkrunfw.so.5")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("libkrunfw.so.5", filepath.Join(libDir, "libkrunfw.so")); err != nil {
		t.Fatal(err)
	}

	runtime := &microsandboxRuntime{config: &appconfig.Config{
		MicrosandboxLibPath: filepath.Join(libDir, "libmicrosandbox_go_ffi.so"),
	}}
	got, err := runtime.resolveLibkrunfwPath()
	if err != nil {
		t.Fatalf("resolveLibkrunfwPath: %v", err)
	}
	if got != versioned {
		t.Fatalf("resolveLibkrunfwPath() = %q, want %q", got, versioned)
	}
}

func TestMicrosandboxResolveLibkrunfwUsesNumericVersionOrder(t *testing.T) {
	libDir := t.TempDir()
	for _, name := range []string{
		"libkrunfw.so.5.2.1",
		"libkrunfw.so.5.10.0",
		"libkrunfw.so.5.99.0.bak",
	} {
		if err := os.WriteFile(filepath.Join(libDir, name), []byte("krun"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	runtime := &microsandboxRuntime{config: &appconfig.Config{
		MicrosandboxLibPath: filepath.Join(libDir, "libmicrosandbox_go_ffi.so"),
	}}
	want := filepath.Join(libDir, "libkrunfw.so.5.10.0")
	got, err := runtime.resolveLibkrunfwPath()
	if err != nil {
		t.Fatalf("resolveLibkrunfwPath: %v", err)
	}
	if got != want {
		t.Fatalf("resolveLibkrunfwPath() = %q, want %q", got, want)
	}
}

func TestMicrosandboxResolveLibkrunfwHandlesGlobMetaInDirectory(t *testing.T) {
	root := t.TempDir()
	libDir := filepath.Join(root, "lib[meta]")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	versioned := filepath.Join(libDir, "libkrunfw.so.5.6.0")
	if err := os.WriteFile(versioned, []byte("krun"), 0o644); err != nil {
		t.Fatal(err)
	}

	runtime := &microsandboxRuntime{config: &appconfig.Config{
		MicrosandboxLibPath: filepath.Join(libDir, "libmicrosandbox_go_ffi.so"),
	}}
	got, err := runtime.resolveLibkrunfwPath()
	if err != nil {
		t.Fatalf("resolveLibkrunfwPath: %v", err)
	}
	if got != versioned {
		t.Fatalf("resolveLibkrunfwPath() = %q, want %q", got, versioned)
	}
}

func TestMicrosandboxPrepareEnvironmentPreservesDockerDisks(t *testing.T) {
	config := testMicrosandboxConfig(t)
	runtime := &microsandboxRuntime{config: config}
	disk := writeMicrosandboxFile(t, config.MicrosandboxHome, "docker-disks", "old-session.raw")
	ignored := writeMicrosandboxFile(t, config.MicrosandboxHome, "docker-disks", "note.txt")
	subdir := filepath.Join(config.MicrosandboxHome, "docker-disks", "nested")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir docker-disks subdir: %v", err)
	}

	if err := runtime.prepareEnvironment(); err != nil {
		t.Fatalf("prepareEnvironment: %v", err)
	}
	for _, path := range []string{disk, ignored, subdir} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("path %s missing after prepareEnvironment: %v", path, err)
		}
	}
}

func TestMicrosandboxRemoveDockerDiskOnlyCurrentSession(t *testing.T) {
	config := testMicrosandboxConfig(t)
	runtime := &microsandboxRuntime{config: config}
	current := writeMicrosandboxFile(t, config.MicrosandboxHome, "docker-disks", "current.raw")
	other := writeMicrosandboxFile(t, config.MicrosandboxHome, "docker-disks", "other.raw")

	runtime.removeDockerDisk("current")

	if _, err := os.Stat(current); !os.IsNotExist(err) {
		t.Fatalf("current disk exists after removeDockerDisk, err=%v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("other disk missing after removeDockerDisk: %v", err)
	}
}

func TestMicrosandboxDockerDiskPathUsesIdentityHash(t *testing.T) {
	config := testMicrosandboxConfig(t)
	runtime := &microsandboxRuntime{config: config}
	sessionID := identity.NewRandomID(identity.ResourceSandbox)
	hash, err := identity.Hash(sessionID)
	if err != nil {
		t.Fatalf("hash session id: %v", err)
	}

	got := runtime.dockerDiskPath(sessionID)
	want := filepath.Join(config.MicrosandboxHome, "docker-disks", hash+".raw")
	if got != want {
		t.Fatalf("dockerDiskPath = %q, want %q", got, want)
	}
	if strings.ContainsAny(filepath.Base(got), ",:;") {
		t.Fatalf("docker disk basename = %q, want no runtime-forbidden characters", filepath.Base(got))
	}
}

func TestMicrosandboxBindMountSetsConfiguredQuota(t *testing.T) {
	runtime := &microsandboxRuntime{config: &appconfig.Config{MicrosandboxBindQuotaGB: 60}}

	mount := runtime.microsandboxBindMount("/host/session", false)

	if mount.QuotaMiB != 60*1024 {
		t.Fatalf("QuotaMiB = %d, want %d", mount.QuotaMiB, 60*1024)
	}
	if mount.Readonly {
		t.Fatalf("Readonly = true, want false")
	}
}

func TestMicrosandboxBindMountPreservesReadonly(t *testing.T) {
	runtime := &microsandboxRuntime{config: &appconfig.Config{MicrosandboxBindQuotaGB: 11}}

	mount := runtime.microsandboxBindMount("/host/session", true)

	if mount.QuotaMiB != 11*1024 {
		t.Fatalf("QuotaMiB = %d, want %d", mount.QuotaMiB, 11*1024)
	}
	if !mount.Readonly {
		t.Fatalf("Readonly = false, want true")
	}
}

func TestMicrosandboxBindMountFallsBackToBoxDiskSize(t *testing.T) {
	runtime := &microsandboxRuntime{config: &appconfig.Config{BoxDiskSizeGB: 42}}

	mount := runtime.microsandboxBindMount("/host/session", false)

	if mount.QuotaMiB != 42*1024 {
		t.Fatalf("QuotaMiB = %d, want %d", mount.QuotaMiB, 42*1024)
	}
}

func TestListMicrosandboxSessionEphemeralCaches(t *testing.T) {
	home := t.TempDir()
	diskPath := writeMicrosandboxFile(t, home, "docker-disks", "running.raw")
	orphanDisk := writeMicrosandboxFile(t, home, "docker-disks", "orphan.raw")
	_ = writeMicrosandboxFile(t, home, "docker-disks", "ignored.txt")
	sandboxPath := writeMicrosandboxFile(t, home, "sandboxes", "stopped-sandbox", "state.json")
	result, err := listMicrosandboxSessionEphemeralCaches(context.Background(), home, microsandboxCacheReferenceState{
		ActiveSessions: map[string]runtimecache.Reference{
			"running": {Name: "running-session"},
		},
		ReferencedSandboxes: map[string]runtimecache.Reference{
			"stopped-sandbox": {Name: "stopped session"},
		},
	})
	if err != nil {
		t.Fatalf("listMicrosandboxSessionEphemeralCaches: %v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("item count = %d, want 3 (%#v)", len(result.Items), result.Items)
	}
	byPath := map[string]runtimecache.Item{}
	for _, item := range result.Items {
		byPath[item.Path] = item
		if item.Domain != runtimecache.DomainSessionEphemeralState {
			t.Fatalf("domain = %q, want %q", item.Domain, runtimecache.DomainSessionEphemeralState)
		}
		if item.Driver != runtimecache.DriverMicrosandbox {
			t.Fatalf("driver = %q, want %q", item.Driver, runtimecache.DriverMicrosandbox)
		}
		if item.CacheID == "" {
			t.Fatalf("cache id is empty for %#v", item)
		}
	}
	if got := byPath[diskPath]; got.Status != runtimecache.StatusActive || got.Removable {
		t.Fatalf("running disk status/removable = %s/%v, want active/false (%#v)", got.Status, got.Removable, got)
	}
	if got := byPath[orphanDisk]; got.Status != runtimecache.StatusOrphaned || !got.Removable {
		t.Fatalf("orphan disk status/removable = %s/%v, want orphaned/true (%#v)", got.Status, got.Removable, got)
	}
	if got := byPath[filepath.Dir(sandboxPath)]; got.Status != runtimecache.StatusReferenced || got.Removable {
		t.Fatalf("sandbox state status/removable = %s/%v, want referenced/false (%#v)", got.Status, got.Removable, got)
	}
}

func TestMicrosandboxSessionEphemeralPruneDryRunAndForce(t *testing.T) {
	home := t.TempDir()
	orphan := writeMicrosandboxFile(t, home, "docker-disks", "orphan.raw")
	active := writeMicrosandboxFile(t, home, "docker-disks", "active.raw")
	refs := microsandboxCacheReferenceState{
		ActiveSessions: map[string]runtimecache.Reference{"active": {Name: "active session"}},
	}

	dryRun, err := pruneMicrosandboxSessionEphemeralCaches(context.Background(), home, refs, runtimecache.PruneRequest{
		Filter: runtimecache.Filter{Driver: runtimecache.DriverMicrosandbox, Domain: runtimecache.DomainSessionEphemeralState},
	})
	if err != nil {
		t.Fatalf("pruneMicrosandboxSessionEphemeralCaches dry-run: %v", err)
	}
	if !dryRun.DryRun || len(dryRun.Removed) != 0 {
		t.Fatalf("dry-run result = %#v, want no removal", dryRun)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("orphan disk missing after dry-run: %v", err)
	}

	forced, err := pruneMicrosandboxSessionEphemeralCaches(context.Background(), home, refs, runtimecache.PruneRequest{
		Filter: runtimecache.Filter{Driver: runtimecache.DriverMicrosandbox, Domain: runtimecache.DomainSessionEphemeralState},
		Force:  true,
	})
	if err != nil {
		t.Fatalf("pruneMicrosandboxSessionEphemeralCaches force: %v", err)
	}
	if len(forced.Removed) != 1 {
		t.Fatalf("removed = %#v, want one orphan removal", forced.Removed)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan disk exists after force prune, err=%v", err)
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("active disk missing after force prune: %v", err)
	}
	if len(forced.Skipped) != 1 || forced.Skipped[0].Status != runtimecache.StatusActive {
		t.Fatalf("skipped = %#v, want active item skipped", forced.Skipped)
	}
}

func TestMicrosandboxSessionEphemeralReferencedRequiresIncludeReferenced(t *testing.T) {
	home := t.TempDir()
	statePath := filepath.Dir(writeMicrosandboxFile(t, home, "sandboxes", "stopped-sandbox", "state.json"))
	refs := microsandboxCacheReferenceState{
		ReferencedSandboxes: map[string]runtimecache.Reference{"stopped-sandbox": {Name: "stopped session"}},
	}
	list, err := listMicrosandboxSessionEphemeralCaches(context.Background(), home, refs)
	if err != nil {
		t.Fatalf("listMicrosandboxSessionEphemeralCaches: %v", err)
	}
	cacheID := list.Items[0].CacheID

	dryRun, err := removeMicrosandboxSessionEphemeralCache(context.Background(), home, refs, runtimecache.RemoveRequest{CacheID: cacheID, Force: true})
	if err != nil {
		t.Fatalf("removeMicrosandboxSessionEphemeralCache without include referenced: %v", err)
	}
	if len(dryRun.Removed) != 0 || len(dryRun.Skipped) != 1 {
		t.Fatalf("remove result = %#v, want referenced skipped", dryRun)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("referenced sandbox state missing after skipped remove: %v", err)
	}

	forced, err := pruneMicrosandboxSessionEphemeralCaches(context.Background(), home, refs, runtimecache.PruneRequest{
		Filter:            runtimecache.Filter{CacheID: cacheID},
		IncludeReferenced: true,
		Force:             true,
	})
	if err != nil {
		t.Fatalf("pruneMicrosandboxSessionEphemeralCaches include referenced: %v", err)
	}
	if len(forced.Removed) != 1 {
		t.Fatalf("removed = %#v, want referenced removal", forced.Removed)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("referenced sandbox state exists after include-referenced prune, err=%v", err)
	}
}

func TestMicrosandboxSessionEphemeralUnknownAndSymlinkEscapeNotRemoved(t *testing.T) {
	home := t.TempDir()
	unknown := writeMicrosandboxFile(t, home, "docker-disks", "unknown.raw")
	refs := microsandboxCacheReferenceState{Unknown: true}
	result, err := pruneMicrosandboxSessionEphemeralCaches(context.Background(), home, refs, runtimecache.PruneRequest{
		Filter: runtimecache.Filter{Driver: runtimecache.DriverMicrosandbox, Domain: runtimecache.DomainSessionEphemeralState},
		Force:  true,
	})
	if err != nil {
		t.Fatalf("pruneMicrosandboxSessionEphemeralCaches unknown: %v", err)
	}
	if len(result.Removed) != 0 || len(result.Skipped) != 1 || result.Skipped[0].Status != runtimecache.StatusUnknown {
		t.Fatalf("unknown prune result = %#v, want unknown skipped", result)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatalf("unknown disk missing after prune: %v", err)
	}

	home = t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.raw")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatalf("write outside target: %v", err)
	}
	diskRoot := filepath.Join(home, "docker-disks")
	if err := os.MkdirAll(diskRoot, 0o755); err != nil {
		t.Fatalf("mkdir disk root: %v", err)
	}
	linkPath := filepath.Join(diskRoot, "escape.raw")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Fatalf("symlink outside target: %v", err)
	}
	list, err := listMicrosandboxSessionEphemeralCaches(context.Background(), home, microsandboxCacheReferenceState{})
	if err != nil {
		t.Fatalf("listMicrosandboxSessionEphemeralCaches symlink: %v", err)
	}
	result, err = removeMicrosandboxSessionEphemeralCache(context.Background(), home, microsandboxCacheReferenceState{}, runtimecache.RemoveRequest{
		CacheID: list.Items[0].CacheID,
		Force:   true,
	})
	if err != nil {
		t.Fatalf("removeMicrosandboxSessionEphemeralCache symlink: %v", err)
	}
	if len(result.Removed) != 0 || len(result.Skipped) != 1 || len(result.Warnings) == 0 {
		t.Fatalf("symlink remove result = %#v, want skipped warning", result)
	}
	if _, err := os.Lstat(linkPath); err != nil {
		t.Fatalf("symlink was removed: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside target was removed: %v", err)
	}
}

func testMicrosandboxConfig(t *testing.T) *appconfig.Config {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", filepath.Join(root, "user-home"))
	bin := writeMicrosandboxFile(t, root, "bin", "msb")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatalf("chmod msb: %v", err)
	}
	lib := writeMicrosandboxFile(t, root, "lib", "libmicrosandbox_go_ffi.so")
	_ = writeMicrosandboxFile(t, root, "lib", "libkrunfw.so.5.2.1")
	return &appconfig.Config{
		MicrosandboxHome:    home,
		MicrosandboxMSBPath: bin,
		MicrosandboxLibPath: lib,
		BoxDiskSizeGB:       1,
	}
}

func writeMicrosandboxFile(t *testing.T, root string, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{root}, parts...)...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

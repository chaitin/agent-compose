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

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
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

func TestMicrosandboxExecStreamReturnsAfterExitWithoutDone(t *testing.T) {
	events := []*microsandbox.ExecEvent{
		{Kind: microsandbox.ExecEventStarted},
		{Kind: microsandbox.ExecEventStdout, Data: []byte("finished\n")},
		{Kind: microsandbox.ExecEventExited, ExitCode: 7},
	}
	var closeCalls int
	recv := func(ctx context.Context) (*microsandbox.ExecEvent, error) {
		if len(events) > 0 {
			event := events[0]
			events = events[1:]
			return event, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	closeHandle := func() error {
		closeCalls++
		return nil
	}
	collector := &microsandboxExecCollector{filter: newExecOutputFilter()}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	grace := 25 * time.Millisecond
	startedAt := time.Now()

	result, err := consumeMicrosandboxExecStream(ctx, recv, closeHandle, collector, grace)
	if err != nil {
		t.Fatalf("consumeMicrosandboxExecStream returned error: %v", err)
	}
	if result.ExitCode != 7 || result.Success {
		t.Fatalf("result = %#v, want exit code 7 and failure", result)
	}
	if result.Output != "finished\n" {
		t.Fatalf("output = %q, want %q", result.Output, "finished\n")
	}
	if closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", closeCalls)
	}
	if elapsed := time.Since(startedAt); elapsed < grace {
		t.Fatalf("returned before drain grace period: %s", elapsed)
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
	currentID := identity.Prefix + identity.NewRandomID(identity.ResourceSandbox)
	otherID := identity.NewRandomID(identity.ResourceSandbox)
	current := writeMicrosandboxFile(t, config.MicrosandboxHome, "docker-disks", microsandboxDockerDiskName(currentID)+".raw")
	legacyCurrent := writeMicrosandboxFile(t, config.MicrosandboxHome, "docker-disks", currentID+".raw")
	other := writeMicrosandboxFile(t, config.MicrosandboxHome, "docker-disks", microsandboxDockerDiskName(otherID)+".raw")

	runtime.removeDockerDisk(currentID)

	if _, err := os.Stat(current); !os.IsNotExist(err) {
		t.Fatalf("current disk exists after removeDockerDisk, err=%v", err)
	}
	if _, err := os.Stat(legacyCurrent); !os.IsNotExist(err) {
		t.Fatalf("legacy current disk exists after removeDockerDisk, err=%v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("other disk missing after removeDockerDisk: %v", err)
	}
}

func TestMicrosandboxEnsureDockerDiskMigratesLegacyPath(t *testing.T) {
	config := testMicrosandboxConfig(t)
	runtime := &microsandboxRuntime{config: config}
	sandboxID := identity.Prefix + identity.NewRandomID(identity.ResourceSandbox)
	legacy := writeMicrosandboxFile(t, config.MicrosandboxHome, "docker-disks", sandboxID+".raw")

	got, err := runtime.ensureDockerDisk(sandboxID)
	if err != nil {
		t.Fatalf("ensureDockerDisk: %v", err)
	}
	want := runtime.dockerDiskPath(sandboxID)
	if got != want {
		t.Fatalf("ensureDockerDisk path = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("migrated docker disk missing: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy docker disk still exists after migration, err=%v", err)
	}
}

func TestMicrosandboxDockerDiskPathUsesIdentityHash(t *testing.T) {
	config := testMicrosandboxConfig(t)
	runtime := &microsandboxRuntime{config: config}
	sandboxID := identity.NewRandomID(identity.ResourceSandbox)
	hash, err := identity.Hash(sandboxID)
	if err != nil {
		t.Fatalf("hash sandbox id: %v", err)
	}

	got := runtime.dockerDiskPath(sandboxID)
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

func TestListMicrosandboxSandboxEphemeralCaches(t *testing.T) {
	home := t.TempDir()
	runningID := identity.NewRandomID(identity.ResourceSandbox)
	diskPath := writeMicrosandboxFile(t, home, "docker-disks", microsandboxDockerDiskName(runningID)+".raw")
	orphanDisk := writeMicrosandboxFile(t, home, "docker-disks", "orphan.raw")
	_ = writeMicrosandboxFile(t, home, "docker-disks", "ignored.txt")
	sandboxPath := writeMicrosandboxFile(t, home, "sandboxes", "stopped-sandbox", "state.json")
	result, err := listMicrosandboxSandboxEphemeralCaches(context.Background(), home, microsandboxCacheReferenceState{
		ActiveSandboxes: map[string]runtimecache.Reference{
			runningID: {Name: "running sandbox"},
		},
		ReferencedSandboxes: map[string]runtimecache.Reference{
			"stopped-sandbox": {Name: "stopped sandbox"},
		},
	})
	if err != nil {
		t.Fatalf("listMicrosandboxSandboxEphemeralCaches: %v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("item count = %d, want 3 (%#v)", len(result.Items), result.Items)
	}
	byPath := map[string]runtimecache.Item{}
	for _, item := range result.Items {
		byPath[item.Path] = item
		if item.Domain != runtimecache.DomainSandboxEphemeralState {
			t.Fatalf("domain = %q, want %q", item.Domain, runtimecache.DomainSandboxEphemeralState)
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
	if got := byPath[diskPath]; got.SandboxID != runningID || len(got.References) != 1 || got.References[0].ID != runningID {
		t.Fatalf("running disk reference = sandboxID %q refs %#v, want original sandbox id %q", got.SandboxID, got.References, runningID)
	}
	if got := byPath[orphanDisk]; got.Status != runtimecache.StatusOrphaned || !got.Removable {
		t.Fatalf("orphan disk status/removable = %s/%v, want orphaned/true (%#v)", got.Status, got.Removable, got)
	}
	if got := byPath[filepath.Dir(sandboxPath)]; got.Status != runtimecache.StatusReferenced || got.Removable {
		t.Fatalf("sandbox state status/removable = %s/%v, want referenced/false (%#v)", got.Status, got.Removable, got)
	}
}

func TestMicrosandboxSandboxEphemeralPruneDryRunAndForce(t *testing.T) {
	home := t.TempDir()
	orphan := writeMicrosandboxFile(t, home, "docker-disks", "orphan.raw")
	active := writeMicrosandboxFile(t, home, "docker-disks", "active.raw")
	refs := microsandboxCacheReferenceState{
		ActiveSandboxes: map[string]runtimecache.Reference{"active": {Name: "active sandbox"}},
	}

	dryRun, err := pruneMicrosandboxSandboxEphemeralCaches(context.Background(), home, refs, runtimecache.PruneRequest{
		Filter: runtimecache.Filter{Driver: runtimecache.DriverMicrosandbox, Domain: runtimecache.DomainSandboxEphemeralState},
	})
	if err != nil {
		t.Fatalf("pruneMicrosandboxSandboxEphemeralCaches dry-run: %v", err)
	}
	if !dryRun.DryRun || len(dryRun.Removed) != 0 {
		t.Fatalf("dry-run result = %#v, want no removal", dryRun)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("orphan disk missing after dry-run: %v", err)
	}

	forced, err := pruneMicrosandboxSandboxEphemeralCaches(context.Background(), home, refs, runtimecache.PruneRequest{
		Filter: runtimecache.Filter{Driver: runtimecache.DriverMicrosandbox, Domain: runtimecache.DomainSandboxEphemeralState},
		Force:  true,
	})
	if err != nil {
		t.Fatalf("pruneMicrosandboxSandboxEphemeralCaches force: %v", err)
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

func TestMicrosandboxSandboxEphemeralReferencedRequiresIncludeReferenced(t *testing.T) {
	home := t.TempDir()
	statePath := filepath.Dir(writeMicrosandboxFile(t, home, "sandboxes", "stopped-sandbox", "state.json"))
	refs := microsandboxCacheReferenceState{
		ReferencedSandboxes: map[string]runtimecache.Reference{"stopped-sandbox": {Name: "stopped sandbox"}},
	}
	list, err := listMicrosandboxSandboxEphemeralCaches(context.Background(), home, refs)
	if err != nil {
		t.Fatalf("listMicrosandboxSandboxEphemeralCaches: %v", err)
	}
	cacheID := list.Items[0].CacheID

	dryRun, err := removeMicrosandboxSandboxEphemeralCache(context.Background(), home, refs, runtimecache.RemoveRequest{CacheID: cacheID, Force: true})
	if err != nil {
		t.Fatalf("removeMicrosandboxSandboxEphemeralCache without include referenced: %v", err)
	}
	if len(dryRun.Removed) != 0 || len(dryRun.Skipped) != 1 {
		t.Fatalf("remove result = %#v, want referenced skipped", dryRun)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("referenced sandbox state missing after skipped remove: %v", err)
	}

	forced, err := pruneMicrosandboxSandboxEphemeralCaches(context.Background(), home, refs, runtimecache.PruneRequest{
		Filter:            runtimecache.Filter{CacheID: cacheID},
		IncludeReferenced: true,
		Force:             true,
	})
	if err != nil {
		t.Fatalf("pruneMicrosandboxSandboxEphemeralCaches include referenced: %v", err)
	}
	if len(forced.Removed) != 1 {
		t.Fatalf("removed = %#v, want referenced removal", forced.Removed)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("referenced sandbox state exists after include-referenced prune, err=%v", err)
	}
}

func TestMicrosandboxSandboxEphemeralUnknownAndSymlinkEscapeNotRemoved(t *testing.T) {
	home := t.TempDir()
	unknown := writeMicrosandboxFile(t, home, "docker-disks", "unknown.raw")
	refs := microsandboxCacheReferenceState{Unknown: true}
	result, err := pruneMicrosandboxSandboxEphemeralCaches(context.Background(), home, refs, runtimecache.PruneRequest{
		Filter: runtimecache.Filter{Driver: runtimecache.DriverMicrosandbox, Domain: runtimecache.DomainSandboxEphemeralState},
		Force:  true,
	})
	if err != nil {
		t.Fatalf("pruneMicrosandboxSandboxEphemeralCaches unknown: %v", err)
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
	list, err := listMicrosandboxSandboxEphemeralCaches(context.Background(), home, microsandboxCacheReferenceState{})
	if err != nil {
		t.Fatalf("listMicrosandboxSandboxEphemeralCaches symlink: %v", err)
	}
	result, err = removeMicrosandboxSandboxEphemeralCache(context.Background(), home, microsandboxCacheReferenceState{}, runtimecache.RemoveRequest{
		CacheID: list.Items[0].CacheID,
		Force:   true,
	})
	if err != nil {
		t.Fatalf("removeMicrosandboxSandboxEphemeralCache symlink: %v", err)
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

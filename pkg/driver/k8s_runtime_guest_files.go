//go:build k8scompose

package driver

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ReadGuestFile and ReadGuestDir let the daemon pull data a guest process
// wrote back out of a sandbox Pod without any shared filesystem (see
// docs/design/k8s_pod_runtime_driver_design.md §2.1: the k8s driver mounts
// nothing). Both are plain SandboxRuntime.Exec calls under the hood - `cat`
// for one file, `tar` for a directory (the same primitive `kubectl cp` is
// built on) - so they only need whatever's already in the guest image (a
// POSIX shell, cat, tar), no image changes and no extra plumbing beyond
// Exec, which this driver already implements.
//
// Neither method is part of the SandboxRuntime interface: docker/boxlite/
// microsandbox have a real shared mount and have no need for them, so
// callers reach these through a type assertion (see
// pkg/agentcompose/adapters.GuestFileReader/GuestDirReader), the same
// pattern already used for Stats/IsSandboxAlive.

func (r *k8sRuntime) ReadGuestFile(ctx context.Context, sandbox *Sandbox, vmState VMState, guestPath string) ([]byte, error) {
	guestPath = strings.TrimSpace(guestPath)
	if guestPath == "" {
		return nil, fmt.Errorf("read guest file: guest path is required")
	}
	stdout, stderr, exitCode, err := r.execRaw(ctx, k8sExecRequest{
		Sandbox: sandbox,
		VMState: vmState,
		Spec:    ExecSpec{Command: "cat", Args: []string{guestPath}},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("read guest file %s: %w", guestPath, err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("read guest file %s: exit code %d: %s", guestPath, exitCode, strings.TrimSpace(string(stderr)))
	}
	return stdout, nil
}

func (r *k8sRuntime) ReadGuestDir(ctx context.Context, sandbox *Sandbox, vmState VMState, guestDir, hostDestDir string) error {
	guestDir = strings.TrimSpace(guestDir)
	if guestDir == "" {
		return fmt.Errorf("read guest dir: guest path is required")
	}
	if strings.TrimSpace(hostDestDir) == "" {
		return fmt.Errorf("read guest dir %s: host destination is required", guestDir)
	}
	stdout, stderr, exitCode, err := r.execRaw(ctx, k8sExecRequest{
		Sandbox: sandbox,
		VMState: vmState,
		Spec:    ExecSpec{Command: "tar", Args: []string{"cf", "-", "-C", guestDir, "."}},
	}, nil)
	if err != nil {
		return fmt.Errorf("read guest dir %s: %w", guestDir, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("read guest dir %s: exit code %d: %s", guestDir, exitCode, strings.TrimSpace(string(stderr)))
	}
	if err := os.MkdirAll(hostDestDir, 0o755); err != nil {
		return fmt.Errorf("create host destination %s: %w", hostDestDir, err)
	}
	if err := k8sExtractTarArchive(bytes.NewReader(stdout), hostDestDir); err != nil {
		return fmt.Errorf("extract guest dir %s into %s: %w", guestDir, hostDestDir, err)
	}
	return nil
}

// WriteGuestFile pushes content into a sandbox Pod at a guest-absolute
// path, for the daemon-writes/guest-reads direction (prompts, skills,
// generated MCP/provider config - see
// docs/design/k8s_pod_runtime_driver_design.md §2.1 and §6) that docker/
// boxlite get for free from their shared mount and the k8s driver doesn't.
//
// Content is streamed on stdin through the k8s exec subresource. This remains
// an internal k8s transfer primitive rather than adding a field used by only
// one backend to the driver-wide ExecSpec.
//
// Calls EnsureSandbox itself rather than assuming the Pod already exists:
// pkg/runs/sandbox_preparation.go calls PrepareSandboxAgentEnvironment
// (which is what ends up calling this, for prompts/skills/MCP config)
// *before* startProjectRunSandboxRuntime's driver.StartSandboxVM - the
// step that actually creates the Pod for every other driver too. That
// ordering is fine for docker/boxlite (their writes just land on the host
// filesystem, which the container mounts whenever it starts), but this
// push needs a running Pod to Exec into right now, not later - confirmed
// by a live E2E run against k3d, where the first push of a run reliably
// hit "pod is not running" without this. EnsureSandbox's own find-or-create
// logic makes this safe to call defensively: idempotent, so it's a no-op
// once the real StartSandboxVM call reaches this same sandbox later.
func (r *k8sRuntime) WriteGuestFile(ctx context.Context, sandbox *Sandbox, vmState VMState, guestPath string, content []byte) error {
	guestPath = strings.TrimSpace(guestPath)
	if guestPath == "" {
		return fmt.Errorf("write guest file: guest path is required")
	}
	if !filepath.IsAbs(guestPath) {
		return fmt.Errorf("write guest file: guest path %s must be absolute", guestPath)
	}
	if _, err := r.EnsureSandbox(ctx, sandbox, vmState, ProxyState{}); err != nil {
		return fmt.Errorf("write guest file %s: ensure sandbox: %w", guestPath, err)
	}
	// content == nil is callers' (see execution.GuestFileWriterFunc, and
	// e.g. mcp_config.go's clear-on-remove path) way of saying "this file
	// should no longer exist" - a plain "cat > path" would instead leave a
	// 0-byte file behind, which is not the same thing to a guest-side
	// reader: an empty file still exists and still parses as invalid JSON/
	// TOML/whatever, where a missing file reads as "nothing configured".
	// docker/boxlite get this for free from os.Remove on their shared
	// mount (see mcp_config.go); rm -f is the k8s equivalent.
	script := fmt.Sprintf("rm -f %s", shellQuote(guestPath))
	if content != nil {
		script = fmt.Sprintf(
			"mkdir -p %s && cat > %s",
			shellQuote(filepath.Dir(guestPath)),
			shellQuote(guestPath),
		)
	}
	result, err := r.execWithInput(ctx, k8sExecRequest{
		Sandbox: sandbox,
		VMState: vmState,
		Spec:    ExecSpec{Command: "sh", Args: []string{"-c", script}},
	}, bytes.NewReader(content), nil)
	if err != nil {
		return fmt.Errorf("write guest file %s: %w", guestPath, err)
	}
	if !result.Success {
		return fmt.Errorf("write guest file %s: exit code %d: %s", guestPath, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

// WriteGuestDir pushes a local host directory tree into a sandbox Pod at a
// guest-absolute path (for example a resolved workspace or skill directory).
// The tar archive is produced and consumed as a stream so its size does not
// become daemon heap usage.
func (r *k8sRuntime) WriteGuestDir(ctx context.Context, sandbox *Sandbox, vmState VMState, hostSrcDir, guestDir string) error {
	hostSrcDir = strings.TrimSpace(hostSrcDir)
	guestDir = strings.TrimSpace(guestDir)
	if hostSrcDir == "" {
		return fmt.Errorf("write guest dir: host source directory is required")
	}
	if guestDir == "" {
		return fmt.Errorf("write guest dir: guest path is required")
	}
	if !filepath.IsAbs(guestDir) {
		return fmt.Errorf("write guest dir: guest path %s must be absolute", guestDir)
	}
	if filepath.Clean(guestDir) == string(filepath.Separator) {
		return fmt.Errorf("write guest dir: refusing to replace guest root")
	}
	if overlap := k8sGuestDirVolumeMountOverlapKind(sandbox, guestDir); overlap != k8sGuestDirVolumeMountOverlapNone {
		// guestDir is, contains, or is contained by a PVC-backed named
		// volume mount. The script below does `rm -rf` on guestDir before
		// restoring it from the daemon's host-side snapshot; running that
		// against a mounted volume would destroy whatever the guest itself
		// already persisted there across Pod recreations - exactly the
		// durable state a PVC mount is for. Skip the push and leave the
		// volume's own content as the source of truth.
		//
		// An exact match is the only overlap k8sValidateVolumeMountTarget
		// lets a freshly created Pod have, and it's the expected shape for
		// "PVC mounted at the whole workspace/home" - every prepare call for
		// such a sandbox hits it, so it's not logged to avoid turning that
		// into WARN spam that would bury a genuinely rare partial overlap.
		// Partial overlap only reaches here as a runtime safety net for Pods
		// this call didn't create itself (EnsureSandbox reuses an existing
		// Pod via findPod without re-validating its mounts), so a Pod with a
		// pre-validation or out-of-band partial-overlap mount still can't
		// have its PVC data wiped - it just stops receiving syncs, which is
		// worth flagging since it means the workspace/home sync silently
		// stopped working for that Pod.
		if overlap == k8sGuestDirVolumeMountOverlapPartial {
			slog.Warn("skipping guest dir push because it partially overlaps a k8s PVC mount",
				"sandbox_id", sandbox.Summary.ID, "guest_dir", guestDir)
		}
		return nil
	}
	info, err := os.Stat(hostSrcDir)
	if err != nil {
		return fmt.Errorf("write guest dir %s: inspect host source %s: %w", guestDir, hostSrcDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("write guest dir %s: host source %s is not a directory", guestDir, hostSrcDir)
	}
	if _, err := r.EnsureSandbox(ctx, sandbox, vmState, ProxyState{}); err != nil {
		return fmt.Errorf("write guest dir %s: ensure sandbox: %w", guestDir, err)
	}
	clearScript, homeEntries, err := r.k8sGuestDirClearScript(hostSrcDir, guestDir)
	if err != nil {
		return fmt.Errorf("write guest dir %s: %w", guestDir, err)
	}
	archiveReader, archiveWriter := io.Pipe()
	archiveErr := make(chan error, 1)
	go func() {
		archiveErr <- archiveGuestDir(ctx, archiveWriter, hostSrcDir)
		close(archiveErr)
	}()
	script := fmt.Sprintf(
		"%s && mkdir -p %s && tar xf - -C %s",
		clearScript,
		shellQuote(guestDir),
		shellQuote(guestDir),
	)
	result, execErr := r.execWithInput(ctx, k8sExecRequest{
		Sandbox: sandbox,
		VMState: vmState,
		Spec:    ExecSpec{Command: "sh", Args: []string{"-c", script}},
	}, archiveReader, nil)
	_ = archiveReader.CloseWithError(execErr)
	packErr := <-archiveErr
	if packErr != nil && (!errors.Is(packErr, io.ErrClosedPipe) || (execErr == nil && result.Success)) {
		return fmt.Errorf("archive %s: %w", hostSrcDir, packErr)
	}
	if execErr != nil {
		return fmt.Errorf("write guest dir %s: %w", guestDir, execErr)
	}
	if !result.Success {
		return fmt.Errorf("write guest dir %s: exit code %d: %s", guestDir, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	if homeEntries != nil {
		// Record what this push actually restored, so the next push knows
		// to also clear any of these that have since disappeared from the
		// host snapshot (see k8sGuestDirClearScript) - only once the push
		// has actually succeeded, so a failed push doesn't advance the
		// manifest past entries the guest never actually received.
		if err := k8sWriteHomePushManifest(k8sHomePushManifestPath(hostSrcDir), homeEntries); err != nil {
			return fmt.Errorf("write guest dir %s: record home push manifest: %w", guestDir, err)
		}
	}
	return nil
}

// k8sGuestDirClearScript builds the "clear the guest destination before
// restoring it" step of a WriteGuestDir push. homeEntries is non-nil only
// for the GuestHomePath case (see k8sWriteHomePushManifest below); callers
// use it to record what this push restored, once it succeeds.
//
// For every guestDir except GuestHomePath, a plain "rm -rf guestDir" is
// correct: the guest image ships nothing there worth preserving (workspace
// is created empty - see guest-images/Dockerfile.agent-compose-guest).
//
// GuestHomePath is different: the guest image bakes default content
// directly into it (.codex, .claude, .gitconfig, .claude.json, .dsh, plus
// PATH entries like .local/bin, .cargo/bin - same Dockerfile). The daemon's
// host-side home snapshot (hostSrcDir) only ever contains what the daemon
// itself has written there, so a wholesale "rm -rf" of the whole guest home
// before restoring from that snapshot would delete everything the image
// shipped that the daemon never touched. Instead, only specific top-level
// entries are cleared in the guest, mirroring what docker/boxlite achieve
// by bind-mounting just those specific sub-paths (see runtimeMountEntries)
// rather than the whole home directory.
//
// Which entries: the union of the current snapshot's top-level entries and
// whatever the *previous* push actually restored (k8sReadHomePushManifest).
// The current snapshot alone isn't enough - an entry the daemon pushed in
// an earlier run and has since stopped writing (a rotated credential, a
// removed MCP/provider config directory) would otherwise never be cleared
// from the guest side of a Pod that gets reused across runs, even though it
// no longer exists on the host at all.
func (r *k8sRuntime) k8sGuestDirClearScript(hostSrcDir, guestDir string) (script string, homeEntries []string, err error) {
	if filepath.Clean(guestDir) != filepath.Clean(r.config.GuestHomePath) {
		return "rm -rf " + shellQuote(guestDir), nil, nil
	}
	dirEntries, err := os.ReadDir(hostSrcDir)
	if err != nil {
		return "", nil, fmt.Errorf("list host home snapshot %s: %w", hostSrcDir, err)
	}
	current := make([]string, 0, len(dirEntries))
	for _, entry := range dirEntries {
		current = append(current, entry.Name())
	}
	previous := k8sReadHomePushManifest(k8sHomePushManifestPath(hostSrcDir))
	clear := slices.Concat(current, previous)
	slices.Sort(clear)
	clear = slices.Compact(clear)
	if len(clear) == 0 {
		return "true", current, nil
	}
	var b strings.Builder
	b.WriteString("rm -rf")
	for _, name := range clear {
		b.WriteString(" " + shellQuote(filepath.Join(guestDir, name)))
	}
	return b.String(), current, nil
}

// k8sHomePushManifestPath is where the previous WriteGuestDir(home) push's
// top-level entry names are recorded, so the next push can still clear an
// entry that has since disappeared from the host snapshot (see
// k8sGuestDirClearScript). A sibling file next to hostSrcDir itself, so it
// never collides with anything the daemon writes under it.
func k8sHomePushManifestPath(hostSrcDir string) string {
	return hostSrcDir + ".push-manifest.json"
}

// k8sReadHomePushManifest returns the entry names recorded by the previous
// push, or nil if there isn't one - including if the manifest is missing,
// unreadable, or corrupt. A tracking file agent-compose itself owns failing
// to parse isn't worth failing the whole guest sync over; the cost of
// treating it as absent is only that this push behaves like the very first
// one (no stale-entry cleanup this time), not a wipe of anything real.
//
// Entries this daemon itself wrote are always plain os.ReadDir names (see
// k8sGuestDirClearScript) - never empty, ".", "..", or containing a path
// separator. k8sGuestDirClearScript folds every entry straight into
// filepath.Join(guestDir, name) and then an rm -rf, so a manifest that's
// well-formed JSON but has been tampered with or corrupted on disk (e.g.
// an entry of "../../etc") could otherwise walk that rm -rf outside
// guestDir entirely. Filtering here is defense in depth, not a response to
// a reachable attack: nothing this driver considers untrusted (the guest
// Pod has no shared filesystem to write this file through) can reach it.
func k8sReadHomePushManifest(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var entries []string
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil
	}
	valid := entries[:0]
	for _, entry := range entries {
		if entry == "" || entry == "." || entry == ".." || strings.ContainsAny(entry, `/\`) {
			continue
		}
		valid = append(valid, entry)
	}
	return valid
}

// k8sWriteHomePushManifest records the top-level entry names a home push
// just restored, for k8sReadHomePushManifest to pick up on the next push.
func k8sWriteHomePushManifest(path string, entries []string) error {
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// k8sGuestDirVolumeMountOverlap classifies how guestDir relates to a k8s
// named-volume (PVC) mount target: no relation, an exact match, or a
// partial overlap (one is a strict ancestor of the other).
type k8sGuestDirVolumeMountOverlap int

const (
	k8sGuestDirVolumeMountOverlapNone k8sGuestDirVolumeMountOverlap = iota
	k8sGuestDirVolumeMountOverlapExact
	k8sGuestDirVolumeMountOverlapPartial
)

// k8sGuestDirVolumeMountOverlapKind reports how guestDir relates to the
// target of one of the sandbox's k8s named-volume (PVC) mounts. A
// destructive `rm -rf` push (WriteGuestDir) into a path that exactly
// matches or overlaps such a target would delete the mounted volume's
// actual persistent content, not just a stale copy.
//
// podVolumeSpecs (k8sValidateVolumeMountTarget) already rejects a mount
// target that partially overlaps GuestWorkspacePath/GuestHomePath at
// Pod-creation time, so a freshly created Pod can only ever produce an
// exact match here. The partial-overlap check stays in this function too
// as a runtime safety net: EnsureSandbox reuses an existing Pod via findPod
// without re-validating its mounts, so a Pod whose mounts were never
// checked by k8sValidateVolumeMountTarget - created before that validation
// existed, or out-of-band - could still have a partially overlapping
// mount. For that Pod, skipping the whole push (rather than trying to
// exclude just the mounted sub-path from the rm -rf/tar-restore, which has
// no clean implementation) trades "daemon's workspace/home snapshot stops
// reaching the guest" for "PVC data survives".
func k8sGuestDirVolumeMountOverlapKind(sandbox *Sandbox, guestDir string) k8sGuestDirVolumeMountOverlap {
	if sandbox == nil {
		return k8sGuestDirVolumeMountOverlapNone
	}
	guestDir = filepath.Clean(guestDir)
	for _, mount := range sandbox.VolumeMounts {
		if strings.ToLower(strings.TrimSpace(mount.Type)) != "volume" || strings.TrimSpace(mount.Driver) != RuntimeDriverK8s {
			continue
		}
		target := strings.TrimSpace(mount.Target)
		if target == "" {
			continue
		}
		target = filepath.Clean(target)
		if target == guestDir {
			return k8sGuestDirVolumeMountOverlapExact
		}
		if k8sPathIsWithin(target, guestDir) || k8sPathIsWithin(guestDir, target) {
			return k8sGuestDirVolumeMountOverlapPartial
		}
	}
	return k8sGuestDirVolumeMountOverlapNone
}

// k8sPathIsWithin reports whether the cleaned absolute path is equal to, or
// a descendant of, root. Plain string concatenation (root+separator) mishandles
// root == "/", where the join would otherwise require a "//" prefix.
func k8sPathIsWithin(path, root string) bool {
	if path == root || root == string(filepath.Separator) {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// buildTarArchive packs hostSrcDir's contents (relative paths, no leading
// directory entry for hostSrcDir itself) into a tar byte stream for
// WriteGuestDir.
func buildTarArchive(hostSrcDir string) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeTarArchive(&buf, hostSrcDir); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func archiveGuestDir(ctx context.Context, destination *io.PipeWriter, hostSrcDir string) error {
	err := writeTarArchive(destination, hostSrcDir)
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	return errors.Join(err, destination.CloseWithError(err))
}

func writeTarArchive(destination io.Writer, hostSrcDir string) error {
	return writeTarArchiveWithCompletion(destination, hostSrcDir, "")
}

// A publication completion entry is written only after the entire source tree
// was read successfully. A valid tar prefix from a failed producer must never
// be mistaken for a complete directory by the guest publisher.
func writeTarArchiveWithCompletion(destination io.Writer, hostSrcDir, completion string) error {
	writer := tar.NewWriter(destination)
	walkErr := filepath.WalkDir(hostSrcDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, relErr := filepath.Rel(hostSrcDir, path)
		if relErr != nil {
			return relErr
		}
		if relPath == "." {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		linkTarget := ""
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, infoErr = os.Readlink(path)
			if infoErr != nil {
				return infoErr
			}
		}
		header, headerErr := tar.FileInfoHeader(info, linkTarget)
		if headerErr != nil {
			return headerErr
		}
		header.Name = filepath.ToSlash(relPath)
		if entry.IsDir() {
			header.Name += "/"
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("archive path %s has unsupported file type %s", path, info.Mode().Type())
		}
		file, openErr := os.Open(path) //nolint:gosec // hostSrcDir is a resolved skill/config directory the daemon itself manages, not attacker-controlled input
		if openErr != nil {
			return openErr
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if walkErr != nil {
		_ = writer.Close()
		return walkErr
	}
	if completion != "" {
		if err := writer.WriteHeader(&tar.Header{Name: completion, Mode: 0o600, Typeflag: tar.TypeReg}); err != nil {
			_ = writer.Close()
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return nil
}

// k8sExtractTarArchive extracts a tar stream into destDir, rejecting any entry
// whose path would resolve outside destDir (a defensively-applied guard
// against a malicious/corrupt archive, same class of check `tar`/`kubectl
// cp` itself applies - not expected to trigger against a well-formed
// archive from our own ReadGuestDir tar call).
func k8sExtractTarArchive(r io.Reader, destDir string) error {
	reader := tar.NewReader(r)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		target := filepath.Join(destDir, filepath.Clean(filepath.FromSlash(header.Name)))
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) && target != filepath.Clean(destDir) {
			return fmt.Errorf("tar entry %q escapes destination directory", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create directory %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create parent directory for %s: %w", target, err)
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode&0o777)) //nolint:gosec // tar mode bits, masked to drop setuid/setgid/sticky
			if err != nil {
				return fmt.Errorf("create file %s: %w", target, err)
			}
			_, copyErr := io.Copy(file, reader) //nolint:gosec // bounded by the guest's own tar output, not an untrusted external source
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("write file %s: %w", target, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close file %s: %w", target, closeErr)
			}
		default:
			// Symlinks, devices, etc. are not expected from our own
			// ReadGuestDir tar call; skip rather than fail the whole pull.
			continue
		}
	}
}

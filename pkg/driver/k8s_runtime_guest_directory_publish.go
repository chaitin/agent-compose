//go:build k8scompose

package driver

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PublishGuestDirectory publishes private, writable content via a relative
// symlink. Node, already required by the agent runtime using this capability,
// provides rename-over-symlink without the destination-following behavior of mv.
// Only a caller-marked legacy directory may be migrated. That first migration
// has a short rename/link window; later publications atomically replace a link.
func (r *k8sRuntime) PublishGuestDirectory(ctx context.Context, sandbox *Sandbox, vmState VMState, publication GuestDirectoryPublication) error {
	hostSrcDir, guestDir, managedMarker := publication.HostSource, publication.GuestDestination, publication.ManagedMarker
	home := r.config.GuestHomePath
	if err := validateGuestHomeSymlink(home, guestDir, ".publication-target", managedMarker); err != nil {
		return fmt.Errorf("publish guest directory: %w", err)
	}
	if k8sGuestDirVolumeMountOverlapKind(sandbox, guestDir) != k8sGuestDirVolumeMountOverlapNone {
		return fmt.Errorf("publish guest directory %s: destination overlaps a persistent volume", guestDir)
	}
	info, err := os.Stat(hostSrcDir)
	if err != nil {
		return fmt.Errorf("inspect guest directory publication source: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("guest directory publication source must be a directory")
	}
	if _, err := r.EnsureSandbox(ctx, sandbox, vmState, ProxyState{}); err != nil {
		return fmt.Errorf("publish guest directory: ensure sandbox: %w", err)
	}
	reader, writer := io.Pipe()
	completion := ".agent-compose-complete-" + rand.Text()
	archiveErr := make(chan error, 1)
	go func() {
		packErr := writeTarArchiveWithCompletion(writer, hostSrcDir, completion)
		if ctx.Err() != nil {
			packErr = errors.Join(packErr, ctx.Err())
		}
		archiveErr <- errors.Join(packErr, writer.CloseWithError(packErr))
		close(archiveErr)
	}()
	result, execErr := r.execWithInput(ctx, k8sExecRequest{
		Sandbox: sandbox,
		VMState: vmState,
		Spec:    ExecSpec{Command: "sh", Args: []string{"-c", guestDirectoryPublicationScript(home, guestDir, managedMarker, completion)}, Cwd: "/"},
	}, reader, nil)
	_ = reader.CloseWithError(execErr)
	packErr := <-archiveErr
	if packErr != nil && (!errors.Is(packErr, io.ErrClosedPipe) || (execErr == nil && result.Success)) {
		return fmt.Errorf("archive guest directory publication: %w", packErr)
	}
	if execErr != nil {
		return fmt.Errorf("publish guest directory %s: %w", guestDir, execErr)
	}
	if !result.Success {
		return fmt.Errorf("publish guest directory %s: exit code %d: %s", guestDir, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

func guestDirectoryPublicationScript(home, destination, marker, completion string) string {
	control := filepath.Join(filepath.Dir(destination), ".agent-compose-"+filepath.Base(destination))
	relativeControl := filepath.Base(control)
	legacyOwned := "false"
	if marker != "" {
		legacyMarker := shellQuote(filepath.Join(destination, marker))
		legacyOwned = "test -d \"$destination\" && test ! -L " + legacyMarker + " && test -f " + legacyMarker
	}
	return guestHomeParentGuardScript(home, destination) + fmt.Sprintf(`
command -v flock >/dev/null || { echo 'guest directory publication requires flock' >&2; exit 1; }
command -v tar >/dev/null || { echo 'guest directory publication requires tar' >&2; exit 1; }
destination=%s
control=%s
relative_control=%s
owner=%s
mkdir -p %s
if test -e "$control" || test -L "$control"; then
  test -d "$control" && test ! -L "$control" && test -f "$control/.owner" && test ! -L "$control/.owner" &&
    test "$(cat "$control/.owner")" = "$owner" || { echo 'unmanaged directory publication path' >&2; exit 1; }
else
  mkdir "$control"
  printf '%%s' "$owner" > "$control/.owner"
fi
test ! -L "$control/.lock" || { echo 'unmanaged publication lock' >&2; exit 1; }
if test -e "$control/.lock"; then test -f "$control/.lock" || { echo 'invalid publication lock' >&2; exit 1; }; fi
if test -e "$control/previous" || test -L "$control/previous"; then
  test -d "$control/previous" && test ! -L "$control/previous" || { echo 'invalid publication recovery directory' >&2; exit 1; }
fi
exec 9>>"$control/.lock"
flock -n 9 || { echo 'directory publication is already running' >&2; exit 1; }
stage=
cleanup() {
  if test -d "$control/previous" && test ! -e "$destination" && test ! -L "$destination"; then
    mv "$control/previous" "$destination"
  fi
  if test -n "$stage"; then
    active=$(readlink "$destination" 2>/dev/null || true)
    test "$active" = "$relative_control/$(basename "$stage")" || rm -rf "$stage"
  fi
  rm -f "$control/next"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
if test -d "$control/previous" && test ! -e "$destination" && test ! -L "$destination"; then
  mv "$control/previous" "$destination"
fi
current=
if test -L "$destination"; then
  current=$(readlink "$destination")
  case "$current" in "$relative_control"/generation-*) ;; *) echo 'unmanaged publication link' >&2; exit 1;; esac
  name=${current#"$relative_control"/}
  case "$name" in */*|*\\*) echo 'invalid publication generation' >&2; exit 1;; esac
  test ! -L "$control/$name" || { echo 'invalid publication generation link' >&2; exit 1; }
elif test -e "$destination" && ! ( %s ); then
  echo 'unmanaged publication destination' >&2; exit 1
fi
# A prior process may have stopped after publishing the link but before cleanup.
# Only this marked control directory's own generations can be reclaimed.
for old in "$control"/generation-*; do
  test -e "$old" || continue
  test "$relative_control/$(basename "$old")" = "$current" || rm -rf "$old"
done
if test -L "$destination"; then rm -rf "$control/previous"; fi
rm -f "$control/next"
stage=$(mktemp -d "$control/generation-XXXXXX")
tar xf - -C "$stage"
test -f "$stage/%s" && test ! -L "$stage/%s" || { echo 'incomplete directory publication archive' >&2; exit 1; }
rm "$stage/%s"
ln -s "$relative_control/$(basename "$stage")" "$control/next"
if test -e "$destination" && test ! -L "$destination"; then
  mv "$destination" "$control/previous"
fi
node -e 'require("node:fs").renameSync(process.argv[1], process.argv[2])' "$control/next" "$destination"
if test -n "$current"; then rm -rf "$control/${current#"$relative_control"/}"; fi
rm -rf "$control/previous"
`, shellQuote(destination), shellQuote(control), shellQuote(relativeControl),
		shellQuote("agent-compose-directory-v1:"+destination), shellQuote(filepath.Dir(destination)), legacyOwned, completion, completion, completion)
}

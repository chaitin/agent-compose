//go:build k8scompose

package driver

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// EnsureGuestSymlink projects an already delivered directory inside the guest
// home. An existing matching link is left untouched. The caller may identify a
// previously managed directory by a marker filename to migrate that directory;
// unrelated files, directories, and links are never replaced.
func (r *k8sRuntime) EnsureGuestSymlink(ctx context.Context, sandbox *Sandbox, vmState VMState, projection GuestSymlinkProjection) error {
	guestPath, relativeTarget := projection.GuestPath, projection.RelativeTarget
	home := r.config.GuestHomePath
	if err := validateGuestHomeSymlink(home, guestPath, relativeTarget, ""); err != nil {
		return err
	}
	for _, marker := range projection.ManagedMarkers {
		if err := validateGuestHomeSymlink(home, guestPath, relativeTarget, marker); err != nil {
			return err
		}
	}
	if k8sGuestDirVolumeMountOverlapKind(sandbox, guestPath) != k8sGuestDirVolumeMountOverlapNone {
		return fmt.Errorf("ensure guest symlink %s: destination overlaps a persistent volume", guestPath)
	}
	if _, err := r.EnsureSandbox(ctx, sandbox, vmState, ProxyState{}); err != nil {
		return fmt.Errorf("ensure guest symlink %s: ensure sandbox: %w", guestPath, err)
	}
	result, err := r.execWithInput(ctx, k8sExecRequest{
		Sandbox: sandbox,
		VMState: vmState,
		Spec: ExecSpec{
			Command: "sh", Args: []string{"-c", guestHomeParentGuardScript(home, guestPath, filepath.Join(filepath.Dir(guestPath), relativeTarget)) + guestHomeSymlinkScript(guestPath, relativeTarget, projection.ManagedMarkers)}, Cwd: "/",
		},
	}, nil, nil)
	if err != nil {
		return fmt.Errorf("ensure guest symlink %s: %w", guestPath, err)
	}
	if !result.Success {
		return fmt.Errorf("ensure guest symlink %s: exit code %d: %s", guestPath, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

func validateGuestHomeSymlink(home, path, target, marker string) error {
	if !filepath.IsAbs(home) || !filepath.IsAbs(path) || filepath.Clean(path) == filepath.Clean(home) || !k8sPathIsWithin(filepath.Clean(path), filepath.Clean(home)) {
		return fmt.Errorf("guest symlink path must be inside the guest home")
	}
	if strings.TrimSpace(target) == "" || filepath.IsAbs(target) || !k8sPathIsWithin(filepath.Clean(filepath.Join(filepath.Dir(path), target)), filepath.Clean(home)) {
		return fmt.Errorf("guest symlink target must be relative and stay inside the guest home")
	}
	if k8sPathIsWithin(filepath.Clean(path), filepath.Clean(filepath.Join(filepath.Dir(path), target))) {
		return fmt.Errorf("guest symlink target must not contain the link")
	}
	if marker != "" && (marker == "." || marker == ".." || filepath.Base(marker) != marker || strings.ContainsAny(marker, `/\\`)) {
		return fmt.Errorf("guest symlink managed marker must be a filename")
	}
	return nil
}

func guestHomeSymlinkScript(path, target string, markers []string) string {
	managed := "false"
	for _, marker := range markers {
		if marker == "" {
			continue
		}
		markerPath := shellQuote(filepath.Join(path, marker))
		managed += " || ( test -d " + shellQuote(path) + " && test ! -L " + markerPath + " && test -f " + markerPath + " )"
	}
	control := filepath.Join(filepath.Dir(path), ".agent-compose-"+filepath.Base(path))
	return fmt.Sprintf(`set -eu
command -v flock >/dev/null || { echo 'guest symlink publication requires flock' >&2; exit 1; }
destination=%s
target=%s
control=%s
owner=%s
mkdir -p %s
if test -e "$control" || test -L "$control"; then
  test -d "$control" && test ! -L "$control" && test -f "$control/.owner" && test ! -L "$control/.owner" &&
    test "$(cat "$control/.owner")" = "$owner" || { echo 'unmanaged symlink publication path' >&2; exit 1; }
else
  mkdir "$control"
  printf '%%s' "$owner" > "$control/.owner"
fi
test ! -L "$control/.lock" || { echo 'unmanaged symlink publication lock' >&2; exit 1; }
if test -e "$control/.lock"; then test -f "$control/.lock" || { echo 'invalid publication lock' >&2; exit 1; }; fi
if test -e "$control/previous" || test -L "$control/previous"; then
  test -d "$control/previous" && test ! -L "$control/previous" || { echo 'invalid publication recovery directory' >&2; exit 1; }
fi
exec 9>>"$control/.lock"
flock -n 9 || { echo 'symlink publication is already running' >&2; exit 1; }
cleanup() {
  if test -d "$control/previous" && test ! -e "$destination" && test ! -L "$destination"; then
    mv "$control/previous" "$destination"
  fi
  rm -f "$control/next"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
if test -d "$control/previous" && test ! -e "$destination" && test ! -L "$destination"; then
  mv "$control/previous" "$destination"
fi
if test -L "$destination"; then
  if test "$(readlink "$destination")" = "$target"; then
    rm -rf "$control/previous"
    exit 0
  fi
  echo 'guest symlink conflicts with an existing link' >&2
  exit 1
fi
if test -e "$destination" && ! ( %s ); then
  echo 'guest symlink conflicts with an unmanaged path' >&2
  exit 1
fi
rm -f "$control/next"
ln -s "$target" "$control/next"
if test -e "$destination"; then
  mv "$destination" "$control/previous"
fi
node -e 'require("node:fs").renameSync(process.argv[1], process.argv[2])' "$control/next" "$destination"
rm -rf "$control/previous"
`, shellQuote(path), shellQuote(target), shellQuote(control), shellQuote("agent-compose-link-v1:"+path+"->"+target), shellQuote(filepath.Dir(path)), managed)
}

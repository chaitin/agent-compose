# Driver-Specific Runtime Mount Manifest

Chinese version: [../zh-CN/design/runtime_mount_manifest_driver_specific_design.md](../zh-CN/design/runtime_mount_manifest_driver_specific_design.md)

This document describes current mount manifest behavior for the three runtime
drivers. The core rule is: keep one logical runtime mount list, then apply it
with driver-specific mechanics. Docker can use fine-grained directory and file
binds. BoxLite and Microsandbox use directory sources only and expose compatible
guest paths through bootstrap.

## Background

Early manifests contained both directory sources and file sources:

- Directory sources: `workspace`, `state`, `runtime`, `logs`, `home/.codex`,
  `home/.claude`, and similar paths.
- File sources: `home/.claude.json`, `home/.gitconfig`.

BoxLite reports an error for file sources:

```text
[internal] boxlite async operation: configuration error: Volume host path is not a directory: /data/sandboxes/<sandbox_id>/home/.claude.json
```

The implementation therefore applies one logical list by driver:

- `docker`: turn logical entries into fine-grained directory and file binds.
- `boxlite`: mount only `<sandbox> -> /data`, then expose logical entries in
  guest bootstrap.
- `microsandbox`: mount only `<sandbox> -> /data`, then expose logical entries
  in guest bootstrap.

The manifest is always written to:

```text
<sandbox>/vm/mount-manifest.json
```

The manifest contains a `driver` field. Runtime consumers validate that the
manifest driver matches the current runtime driver.

## Manifest Model

The persisted manifest is driver-specific applied mount data:

```json
{
  "version": 1,
  "driver": "boxlite",
  "mounts": [
    {
      "hostPath": "/abs/path/to/sandbox",
      "guestPath": "/data",
      "type": "bind",
      "readOnly": false
    }
  ]
}
```

`loadRuntimeMountManifest` validates:

- `version` is the currently supported version.
- `driver` is a valid runtime driver.
- If the caller passes an expected driver, manifest driver must match it.
- Mount `type` must be `bind`.
- `hostPath` and `guestPath` must be absolute paths.

`loadDirectoryRuntimeMountManifest` adds the requirement that every `hostPath`
is a directory. BoxLite and Microsandbox use this loader.

## Logical Runtime Mount List

The logical list is the source of truth for all drivers:

| Sandbox source | Guest path | Type |
| --- | --- | --- |
| `workspace` | `/workspace` | dir |
| `state` | `/data/state` | dir |
| `runtime` | `/data/runtime` | dir |
| `logs` | `/data/logs` | dir |
| `home/.codex` | `/root/.codex` | dir |
| `home/.claude` | `/root/.claude` | dir |
| `home/.opencode` | `/root/.opencode` | dir |
| `home/.claude.json` | `/root/.claude.json` | file |
| `home/.gitconfig` | `/root/.gitconfig` | file |
| `home/.gemini` | `/root/.gemini` | dir |
| `home/.config/claude` | `/root/.config/claude` | dir |
| `home/.config/Claude` | `/root/.config/Claude` | dir |
| `home/.config/gemini` | `/root/.config/gemini` | dir |
| `home/.config/opencode` | `/root/.config/opencode` | dir |
| `home/.local/share/gemini` | `/root/.local/share/gemini` | dir |

Paths under `/root` that are not listed here are not guaranteed to persist for
directory-only runtimes.

## Docker Layout

Docker manifest keeps fine-grained sources derived from the logical list:

| Host path | Guest path |
| --- | --- |
| `<sandbox>/workspace` | `/workspace` |
| `<sandbox>/state` | `/data/state` |
| `<sandbox>/runtime` | `/data/runtime` |
| `<sandbox>/logs` | `/data/logs` |
| `<sandbox>/home/.codex` | `/root/.codex` |
| `<sandbox>/home/.claude` | `/root/.claude` |
| `<sandbox>/home/.opencode` | `/root/.opencode` |
| `<sandbox>/home/.claude.json` | `/root/.claude.json` |
| `<sandbox>/home/.gitconfig` | `/root/.gitconfig` |
| `<sandbox>/home/.gemini` | `/root/.gemini` |
| `<sandbox>/home/.config/claude` | `/root/.config/claude` |
| `<sandbox>/home/.config/Claude` | `/root/.config/Claude` |
| `<sandbox>/home/.config/gemini` | `/root/.config/gemini` |
| `<sandbox>/home/.config/opencode` | `/root/.config/opencode` |
| `<sandbox>/home/.local/share/gemini` | `/root/.local/share/gemini` |

Docker runtime applies `DOCKER_HOST_SANDBOX_ROOT` rebase to each source. File
entries such as `.claude.json` and `.gitconfig` remain file bind sources.

## BoxLite Layout

BoxLite manifest contains one directory source only:

| Host path | Guest path |
| --- | --- |
| `<sandbox>` | `/data` |

The BoxLite consumer reads this manifest with the directory-only loader before
passing sources to `boxlite_options_add_volume`.

Guest bootstrap keeps `/root` as the image's real directory. It creates
`/workspace -> /data/workspace` and creates symlinks only for declared home
entries, for example:

```text
/root/.codex -> /data/home/.codex
/root/.gitconfig -> /data/home/.gitconfig
```

Default `/data/state`, `/data/runtime`, and `/data/logs` are already inside the
sandbox mount and do not need symlinks.

## Microsandbox Layout

Microsandbox manifest is the same as BoxLite and contains one directory source
only:

| Host path | Guest path |
| --- | --- |
| `<sandbox>` | `/data` |

The Microsandbox consumer reads this manifest with the directory-only loader
before constructing `microsandbox.Mount.Bind`. Guest bootstrap uses the same
logical-entry symlink behavior as BoxLite.

## BoxLite / Microsandbox Host Layout

Under BoxLite and Microsandbox, a fresh sandbox host layout includes the logical
sources:

```text
<sandbox>/
  workspace/
  state/
  runtime/
  logs/
  home/
    .codex/
    .claude/
    .opencode/
    .claude.json
    .gitconfig
    .gemini/
    .config/
      claude/
      Claude/
      gemini/
      opencode/
    .local/
      share/
        gemini/
  vm/
    mount-manifest.json
```

The directory mount `<sandbox> -> /data` overrides the final visible content of
the guest image's native `/data`. `/workspace` is recreated as a symlink.
`/root` stays a real image directory, and only declared home entries under
`/root` are symlinked into `/data/home`. This avoids requiring guest
`mount --bind` privileges and avoids replacing the entire home directory with
`/root -> /data/home`.

## Directory-Only Bootstrap

BoxLite and Microsandbox execute the same bootstrap command after the sandbox or
box is started or reconnected. The command runs with cwd `/`, before Jupyter
readiness checks, and before each `Exec` / `ExecStream` user command.

Bootstrap verifies that `/data/workspace` and `/data/home` exist, recreates
`/workspace -> /data/workspace`, ensures `/root` is a real directory, and then
creates or repairs declared home-entry symlinks. It refuses to replace unknown
non-symlink targets under `/root`, refuses mounted `/root` targets, does not run
`mount --bind /data/home /root`, and does not create an overall
`/root -> /data/home` symlink.

Bootstrap stdout/stderr is kept out of user command streams. If bootstrap fails,
the driver returns a diagnostic error with driver, sandbox, runtime id,
exit-code, stdout, and stderr context where available, and the original user
command is not executed.

## Driver Switch Behavior

Before start/resume, the manifest is always rewritten according to the currently
resolved driver. If the same sandbox first generated a Docker manifest and is
later started with BoxLite or Microsandbox, the final manifest becomes the
directory-only layout and does not reuse old Docker file source mounts.

## Runtime Image Source Order

The mount manifest describes only how sandbox data directories are mounted. It
does not describe the guest rootfs source. BoxLite and Microsandbox rootfs/image
resolution follows a Docker-first strategy:

- BoxLite: if `BOX_ROOTFS_PATH` is non-empty, use that directory directly.
  Otherwise, when Docker daemon is available, first materialize OCI layout from
  the local Docker image. When Docker daemon is unavailable or the Docker image
  is missing, use OCI cache; cache miss pulls through go-containerregistry, then
  materializes to `image-cache/<image-id>/oci` beside `IMAGE_CACHE_ROOT` and
  passes that path to BoxLite.
- Microsandbox: when Docker daemon is available, first materialize rootfs from
  the local Docker image. When Docker daemon is unavailable or the Docker image
  is missing, use OCI cache; cache miss pulls through go-containerregistry, then
  extracts to `image-cache/<image-id>/rootfs` beside `IMAGE_CACHE_ROOT` and
  passes that absolute path to Microsandbox. Absolute rootfs path uses
  `PullPolicyNever`.
- Docker runtime still uses only Docker daemon image store and does not consume
  OCI cache directly.

This strategy does not change the BoxLite/Microsandbox directory-only mount
manifest or guest environment contract.

## Test Coverage

The test suite covers:

- Docker manifest includes file sources such as `.claude.json` and
  `.gitconfig`.
- Docker mount rebase covers file sources.
- BoxLite/Microsandbox manifests do not contain file sources.
- BoxLite/Microsandbox manifests contain only `<sandbox> -> /data`.
- All host sources in BoxLite/Microsandbox manifests are directories.
- Directory-only loader rejects file sources.
- Docker and directory-only bootstrap are derived from the same logical mount
  list.
- Directory-only bootstrap keeps `/root` as a real directory and exposes only
  declared home entries as symlinks.
- Driver switching rewrites the manifest.

## Runtime Smoke Tests

Real runtime startup smoke tests are explicit opt-in. Default `go test` does not
start a sandbox.

Enable with:

```bash
task test:runtime-smoke
```

Use `SMOKE_RUNTIME_DRIVERS` to choose drivers:

```bash
SMOKE_RUNTIME_DRIVERS=boxlite task test:runtime-smoke
SMOKE_RUNTIME_DRIVERS=microsandbox task test:runtime-smoke
SMOKE_RUNTIME_DRIVERS=boxlite,microsandbox task test:runtime-smoke
```

Smoke tests create and start the real runtime and validate startup markers:

- BoxLite/Microsandbox manifests can be consumed by the directory-only loader.
- Manifest does not contain independent file sources for `/root/.claude.json` or
  `/root/.gitconfig`.
- `<sandbox>` is mounted at `/data`.
- Guest `/root` is a real directory, not an overall symlink to `/data/home`.
- Guest declared home entries such as `/root/.claude.json`, `/root/.gitconfig`,
  and `/root/.codex` resolve to `/data/home/...`.
- Guest writes to `/data/state` and declared home entries persist to host
  `<sandbox>/state` and `<sandbox>/home`.
- When `SMOKE_OCI_IMAGE_REF` is set, BoxLite uses OCI cache materialized layout
  and Microsandbox uses OCI cache rootfs. The test forces Docker daemon to be
  unavailable to avoid fallback to local Docker materialization.

Optional image overrides:

- `SMOKE_DEFAULT_IMAGE`
- `SMOKE_DOCKER_DEFAULT_IMAGE`
- `SMOKE_MICROSANDBOX_DEFAULT_IMAGE`
- `SMOKE_BOX_ROOTFS_PATH`
- `SMOKE_OCI_IMAGE_REF`

`SMOKE_OCI_IMAGE_REF` must point to a bootable agent-compose guest image. It
must include at least the shell and Jupyter startup dependencies required by the
smoke test, and an environment where guest bootstrap can write
`/data/state/runtime-mount-smoke.txt` and a declared home entry. When unset, OCI
image smoke is skipped; directory-only mount smoke still follows the original
logic.

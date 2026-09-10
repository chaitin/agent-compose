# Custom Guest Image ABI

This document defines the current minimum contract between `agent-compose` and
an OCI guest image. It also explains how to build, select, and validate a custom
image without copying every tool from the published
`chaitin/agent-compose-guest` image on Docker Hub.

The contract is capability-based. A sandbox image used only for direct command
execution needs much less software than an image that runs Codex, Claude,
Gemini, OpenCode, Pi, JupyterLab, and every supported notebook cell type.

Normative terms such as **MUST**, **SHOULD**, and **MAY** describe compatibility
requirements in this document.

## 1. Compatibility Model

There is currently no guest ABI version label and no startup handshake that
proves that an arbitrary guest image matches a daemon release. The daemon
selects an image, mounts sandbox state, starts the image through a runtime
driver, and invokes commands by path and protocol convention.

Consequently:

- A custom image **MUST** be tested with every `agent-compose` release and
  runtime driver used in production.
- An image containing `agent-compose-runtime` **SHOULD** build that runtime from
  the same Git tag or commit as the daemon. The runtime CLI and its stdout
  protocols are an internal release boundary and are not guaranteed to remain
  compatible across arbitrary releases.
- The safest customization is to extend an immutable published guest tag that
  matches the daemon tag.
- A multi-architecture tag **MUST** contain the architecture used by the runtime
  host. The published matrix is `linux/amd64` and `linux/arm64`.

This contract covers the image filesystem and process environment. KVM access,
Docker reachability, image pulling, daemon privileges, network policy, model
credentials, and external service health are deployment concerns, not guest
image ABI properties.

## 2. Contract Layers

Only the baseline layer is unconditional. Add each feature layer that the
project actually uses.

| Layer | Required when | Minimum image content |
| --- | --- | --- |
| Sandbox baseline | Always | Linux OCI image, root home convention, shell and bootstrap utilities, writable mount targets |
| Runtime CLI | Agent prompts, scheduler command/shell execution, or prompt attach | Node.js and a release-matched `agent-compose-runtime` executable |
| Provider | An agent selects that provider | The selected provider executable and runtime dependencies |
| Jupyter | `jupyter.enabled` or `run --jupyter` is used | Python 3 and importable `jupyterlab`; additional kernels are optional |
| Notebook cell | That cell language is executed | `bash`, `python3`, or `node`, depending on cell type |
| Runtime SDK | Workspace code installs/uses the SDK offline | Optional SDK tarball at the conventional path |

An image can therefore be compatible for direct `exec` operations while being
intentionally incompatible with agent prompts or Jupyter. Missing optional
layers fail when that capability is invoked; the daemon does not preflight all
tools during project apply.

## 3. Baseline ABI

### 3.1 Image and user

A guest image **MUST**:

- be a Linux OCI/Docker image for the target architecture;
- run as root by default (`USER root` or no non-root `USER` override);
- define `HOME=/root` and provide a real, writable `/root` directory;
- provide a writable root filesystem during sandbox startup; and
- not depend on an image `ENTRYPOINT` to initialize required files.

Non-root guest images are not currently supported. The daemon fixes its guest
home target at `/root`, mounts or symlinks provider state below `/root`, and
executes bootstrap operations that replace selected paths. There is no public
`GUEST_HOME` override.

Docker and BoxLite replace the image entrypoint and command when starting a
sandbox. Microsandbox also performs explicit post-start bootstrap. `CMD`,
`ENTRYPOINT`, `EXPOSE`, health checks, and image labels are therefore not ABI
requirements. They may still be useful when running the image by hand, but
required initialization **MUST** be baked into the image filesystem.

### 3.2 Commands required by the control plane

For all three runtime drivers, the image **MUST** provide `sh` with `-lc`
support. A cross-driver image **MUST** also provide these commands in the fixed
runtime `PATH`:

```text
mkdir  test  rm  ln  readlink  mountpoint  tail  sleep
```

These are ordinary shell/core utilities. In Debian-derived images they are
provided by packages such as `coreutils` and `util-linux`. Compatibility also
depends on the forms used by the daemon: `tail` must accept `-f`, `sleep` must
accept `infinity`, `mountpoint` must accept `-q`, and `readlink` must work on
symbolic links.

Why they are needed:

- Docker starts a non-Jupyter sandbox with `sh -lc 'tail -f /dev/null'`.
- BoxLite starts it with `sh -lc 'sleep infinity'`.
- BoxLite and Microsandbox run a `sh -lc` bootstrap that prepares `/workspace`
  and persistent home-entry symlinks and uses `mountpoint` and `readlink`.

`bash` is not needed merely to keep a basic Docker sandbox alive, but it
**MUST** be installed when using shell cells, runtime `shell` requests, the
runtime SDK `shell` API, or cross-driver Jupyter. A general-purpose custom guest
should install it.

For Kubernetes agent skills, the image **MUST** additionally provide `node`,
`sh`, `tar`, and `flock` (from `util-linux` on Debian), plus the ordinary file
utilities `cat`, `mktemp`, `mv`, `basename`, `mkdir`, `rm`, `ln`, and `readlink`.
The content comparison uses only Node's built-in `fs`, `path`, and `crypto`
modules. Delivery stages one private, writable generation, verifies a random
final archive entry, and atomically renames a canonical symlink; `flock`
serializes publication with an OS-managed process lock. A missing required tool
fails before replacing the previous directory. Kubernetes transport tests using
fake API clients and a real Linux shell validate this contract; they do not
substitute for a live-cluster deployment test.

### 3.3 Fixed command search path

For managed agent and runtime execution, `agent-compose` injects this `PATH`:

```text
/root/.local/bin:/usr/local/go/bin:/root/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
```

Required executables **MUST** be installed in one of these directories, or be
addressed through a supported explicit environment variable. Do not rely on a
custom image `PATH` entry outside this list. If `agent-compose-runtime` uses the
standard `#!/usr/bin/env node` launcher, `/usr/bin/env` and `node` must also be
available through this path.

### 3.4 Filesystem and mount targets

With default daemon configuration, the guest-visible contract is:

| Guest path | Ownership and purpose | Persistence contract |
| --- | --- | --- |
| `/workspace` | Agent cwd and project workspace | Persisted per sandbox |
| `/data/state` | Prompts, schemas, cells, provider session state, artifacts | Persisted per sandbox |
| `/data/runtime` | Runtime resources, cache, MPI, and extensions | Persisted per sandbox |
| `/data/logs` | Jupyter and runtime logs | Persisted per sandbox |
| `/root` | Image-provided real home directory | The directory itself is image-owned |
| `/root/.codex` | Codex config and state | Persisted |
| `/root/.agents` | Projected agent skills | Persisted/projected |
| `/root/.claude` | Claude config and state | Persisted |
| `/root/.opencode` | OpenCode state | Persisted |
| `/root/.pi` | Pi configuration and state | Persisted |
| `/root/.gemini` | Gemini state | Persisted |
| `/root/.claude.json` | Claude root config | Persisted file |
| `/root/.gitconfig` | Git config | Persisted file |
| Selected `/root/.config/...` and `/root/.local/share/gemini` paths | Provider state | Persisted |

The daemon prepares host-side mount sources. The image **SHOULD** pre-create
`/workspace`, `/data/state`, `/data/runtime`, and `/data/logs` as directories,
but it **MUST NOT** store irreplaceable image content in those paths:

- Docker bind-mounts the logical paths individually.
- BoxLite and Microsandbox mount the whole sandbox at `/data`, hiding any
  image-native `/data` content, then replace `/workspace` with a symlink to
  `/data/workspace`.
- Only the declared home entries are persisted. Other `/root` paths remain in
  the image writable layer and are not part of the portable persistence ABI.

Additional Compose volumes may target other absolute guest paths. Those paths
are project-specific and are not part of the baseline guest ABI.

The daemon supports `GUEST_WORKSPACE`, `GUEST_STATE_ROOT`,
`GUEST_RUNTIME_ROOT`, and `GUEST_LOG_ROOT` overrides. This document defines the
default, cross-driver ABI. A deployment that changes these paths creates a
custom ABI and **MUST** validate it with every selected driver. `/root` remains
fixed.

## 4. Runtime CLI ABI

Agent prompts and managed scheduler commands do not invoke provider tools
directly. The daemon invokes `agent-compose-runtime` through `sh -lc`.

An image supporting these capabilities **MUST** provide:

- Node.js satisfying the `engines.node` range in the matching
  `runtime/javascript/package.json` (currently Node.js 20 or newer);
- an executable named `agent-compose-runtime` in the fixed `PATH`;
- the `prompt` and `exec` subcommands used by normal runs; and
- the `stream` subcommand when interactive prompt attach is required.

Prompt-mode `stream` sessions support the `codex`, `claude`, `opencode`, and `pi` providers.
Other providers are rejected before the guest runtime interaction is opened.

The daemon passes explicit workspace, state, and home paths. It also injects:

| Variable | Default/value |
| --- | --- |
| `HOME` | Inherited from the image; must be `/root` |
| `WORKSPACE` | `/workspace` |
| `STATE_ROOT` | `/data/state` |
| `RUNTIME_ROOT` | `/data/runtime` |
| `SANDBOX_ID` | Current sandbox ID |
| `VERSION` | Current daemon version |
| `AGENT_COMPOSE_RUNTIME_BASE_URL` | Set when the runtime facade is configured |

The `prompt` and `exec` stdout payloads, stream separation, artifact files, and
interactive NDJSON frames are protocol, not just CLI presentation. A custom
replacement runtime **MUST** implement the matching release protocol. Reusing
the repository runtime is strongly recommended; see
[agent-compose and runtime call contract](https://github.com/chaitin/agent-compose/blob/main/docs/design/agent-compose-runtime_contract.md).

### 4.1 Graceful-stop capability

When graceful sandbox stop is requested, the daemon signals only the tracked
outer `agent-compose-runtime` process. It does not discover, retain, or signal
the runtime's child-process tree. A release-matched runtime owns cancellation,
child cleanup, partial-result persistence, and its eventual process exit.

The daemon injects these private variables into each managed runtime execution:

| Variable | Value |
| --- | --- |
| `AGENT_COMPOSE_INTERNAL_EXECUTION_ID` | A unique execution UUID |
| `AGENT_COMPOSE_INTERNAL_EXECUTION_READY_FILE` | `/tmp/agent-compose-runtime-ready/<execution-id>` |

After installing its `SIGTERM` handler, the runtime **MUST** write its decimal
PID to the ready file with mode `0600`. It **SHOULD** remove the file when the
handler is disposed. The matching repository runtime implements this protocol.

The daemon opens a separate driver control execution, reads the exact ready
file, verifies the PID's execution ID, executable, and command line through
`/proc`, and sends `SIGTERM` only to that PID. A guest that supports this
capability **MUST** mount a readable procfs and provide `sh`, `cat`, `tr`,
`grep`, `readlink`, and a shell `kill` implementation. The runtime executable
must currently resolve to `node` or `nodejs`, and its command line must identify
`agent-compose-runtime`.

An older runtime that ignores these variables remains compatible for normal
execution. Its ready file never appears, so graceful stop waits for the grace
period and then safely escalates to sandbox stop. Incomplete or stale readiness
also eventually times out, while a missing control utility is treated as a
signaling failure. Both outcomes escalate safely; neither causes the daemon to
scan or manage arbitrary guest processes.

## 5. Optional Capability Requirements

### 5.1 Agent providers

Install only the providers selected by the project. The runtime adapters and
the provider executables should come from the same repository tag's official
guest Dockerfile unless a combination has been independently tested.

| Provider | Executable/runtime requirement |
| --- | --- |
| Codex | `@openai/codex-sdk` in the runtime package plus an executable selected by `CODEX_BIN`, `AGENT_COMPOSE_CODEX_BIN`, `/usr/bin/codex`, `/usr/local/bin/codex`, or `codex` in `PATH` |
| Claude | `@anthropic-ai/claude-agent-sdk` plus Claude Code; use `CLAUDE_CODE_EXECUTABLE`/`CLAUDE_CODE_PATH`, `/usr/bin/claude`, or the SDK-supported default |
| Gemini | A `gemini` executable in `PATH` |
| OpenCode | An `opencode` executable in `PATH` |
| Pi | A `pi` executable in `PATH`; projects using MCP also require the pinned `pi-mcp-adapter` extension at `/usr/local/share/agent-compose/pi-mcp-adapter/index.ts` |

Provider credentials and endpoint variables are injected at execution time.
They **MUST NOT** be embedded in the image.

### 5.2 Jupyter

When Jupyter is enabled, the image **MUST** provide:

- `python3`;
- an importable `jupyterlab` Python module;
- `sh`, and `/bin/bash` for the directory-only runtime launch path;
- `nohup` for background launch; and
- a writable workspace and log root.

The daemon launches `python3 -m jupyterlab` itself with the configured port,
root directory, base URL, and token. The image does not need `EXPOSE 8888`, an
image startup command, a baked token, or a Jupyter password. The default guest
port is `8888`, but it is configurable.

`bash_kernel` and the repository JavaScript kernelspec are conveniences of the
published image. They are required only if users need those kernels.

### 5.3 Notebook cells and scheduler commands

Direct cell execution uses these image commands:

| Cell/request | Required command |
| --- | --- |
| Shell cell or runtime shell request | `bash` |
| Python cell | `python3` |
| JavaScript cell | `node` |
| Runtime exec request | The command named by the request |

Likewise, MCP server commands and arbitrary `agent-compose exec` commands must
be installed by the custom image or supplied in the workspace. They are not
part of the baseline ABI.

### 5.4 Offline runtime SDK

The published guest includes:

```text
/opt/agent-compose/npm/agent-compose-runtime-sdk.tgz
```

This tarball is **not** required by the control plane or runtime CLI. Include a
release-matched tarball only when workspace Node.js code needs to install
`@chaitin-ai/agent-compose-runtime-sdk` without registry access.

## 6. What Is Not Required

The published default guest is a broad runtime environment, but it intentionally
does not include the Go toolchain or the `protoc-gen-go` and
`protoc-gen-go-grpc` code generators. It retains the standalone `grpcurl`
utility for gRPC diagnostics; although `grpcurl` is written in Go, the Go
toolchain is not present in the final image. The baseline ABI does not require a
particular Linux distribution, `apt`, Go, a C/C++ compiler, the protobuf Go code
generators, Git, curl, `tini`, every provider CLI, Jupyter, extra kernels, or the
offline runtime SDK.

Some of these become workload requirements. For example, Git is usually useful
to coding agents, CA certificates are needed for normal TLS access, and build
tools may be needed by a repository. Git workspace provisioning itself is
performed by the control plane before first runtime start and does not make Git
a baseline image requirement.

A project that compiles or tests Go code **MUST** select a development or custom
guest image that installs Go. The repository's
`guest-images/Dockerfile.devbox-archlinux` remains an x86_64-oriented
development image example with Go and other build tools; it is not the default
guest image. Production deployments should build and publish an immutable,
architecture-appropriate development image and select it explicitly in the
agent spec.

### 6.1 Optional Arch Linux Guest

The repository also provides
`guest-images/Dockerfile.agent-compose-guest-archlinux`. It uses Arch Linux and
`pacman`, while retaining the default guest's provider CLIs, Jupyter support,
JavaScript runtime, offline runtime SDK bundle, root user, directories,
entrypoint, and startup command. Like the default guest, its final image does
not contain the Go toolchain; Go is used only in an isolated builder stage to
produce `grpcurl`. It installs Node.js 22 LTS and only the compiler packages
needed by native npm dependencies rather than the full Arch `base-devel` group.

Build it locally from the repository root:

```bash
task image:agent-compose-guest-archlinux
```

The resulting tag is `agent-compose-guest:archlinux`. The task explicitly
targets `linux/amd64`, so it also works through emulation on an Arm development
host. By default, builds use the upstream Arch mirrorlist and the official Go,
PyPI, and npm registries. Override the tag with `IMAGE_TAG`, and use
`ARCHLINUX_TAG` or `ARCHLINUX_MIRROR` when a deployment needs a pinned base tag
or another package mirror:

```bash
IMAGE_TAG=registry.example.com/team/agent-compose-guest:vX.Y.Z-archlinux \
ARCHLINUX_TAG=<pinned-tag> \
task image:agent-compose-guest-archlinux
```

Image CI builds this variant on pull requests and publishes it as
`chaitin/agent-compose-guest` on Docker Hub on pushes to `main` and any Git tag.
Its branch, Git tag, and SHA tags use an `-archlinux` suffix, while `v*` release
tags also update the stable `archlinux` tag. For example, release `vX.Y.Z`
publishes both `vX.Y.Z-archlinux` and `archlinux`. Its manifest contains only
`linux/amd64`. The upstream
`library/archlinux` image used by the build is x86_64-only; a team that supplies
its own compatible Arch Linux base for another architecture must build and
validate that variant independently. The Arch Linux guest is not the deployment
default, so select it explicitly in the agent spec and run the validation in
Section 10 before using it.

### 6.2 Repository Image Build Inputs

Repository image tasks pass standard ecosystem variables under their original
names. Taskfile only orchestrates the build; it does not define `BUILD_*`
aliases. Empty inputs are omitted so Dockerfiles retain their public upstream
defaults.

| Variable | Scope | Tasks |
| --- | --- | --- |
| `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, `NO_PROXY` | Docker build network access | daemon, both guest variants, native runtime artifact export |
| `REGISTRY_MIRROR` | Docker base-image references only | daemon, both guest variants, native runtime artifact export |
| `GOPROXY` | Go module downloads | daemon and both guest variants |
| `GITHUB_MIRROR` | BoxLite and Microsandbox GitHub release downloads | daemon native runtime artifact export |
| `NPM_CONFIG_REGISTRY` | npm package downloads | both guest variants |
| `PIP_INDEX_URL`, `PIP_TRUSTED_HOST` | Python package downloads | both guest variants |
| `ARCHLINUX_MIRROR` | pacman packages | Arch Linux guest only |

Lowercase `http_proxy`, `https_proxy`, `all_proxy`, and `no_proxy` inputs are
accepted and normalized to the corresponding Docker proxy build arguments.
The remaining build controls are:

| Variable | Scope |
| --- | --- |
| `DOCKER_DEFAULT_PLATFORM` | Docker target platform; the daemon task derives it from `GOARCH`, while the Arch Linux guest defaults to `linux/amd64` |
| `IMAGE_NAME`, `IMAGE_TAG` | Local daemon and guest image tags |
| `NO_CACHE=1` | Passes Docker's `--no-cache` flag |
| `GO_VERSION`, `GRPCURL_VERSION`, `NODE_MAJOR` | Default guest toolchain inputs; `NODE_MAJOR` applies only to the Debian guest |
| `ARCHLINUX_TAG` | Arch Linux guest base-image tag |
| `CODEX_VERSION`, `CLAUDE_CODE_VERSION`, `GEMINI_CLI_VERSION`, `OPENCODE_VERSION`, `PI_AGENT_VERSION`, `PI_MCP_ADAPTER_VERSION` | Guest provider package versions |

`REGISTRY_MIRROR`, `GITHUB_MIRROR`, and `ARCHLINUX_MIRROR` have no equivalent
cross-tool standard environment variable, so they remain narrowly scoped
project inputs. `REGISTRY_MIRROR` is not a dockerd mirror configuration; it only
rewrites repository Dockerfile base-image references.

For example:

```bash
task image:agent-compose-guest \
  HTTPS_PROXY=http://proxy.example.com:8080 \
  REGISTRY_MIRROR=mirror.example.com \
  GOPROXY=https://goproxy.example.com,direct \
  NPM_CONFIG_REGISTRY=https://npm.example.com \
  PIP_INDEX_URL=https://pypi.example.com/simple
```

Passing variables after the task name and exporting the same environment
variables are equivalent. Do not put credentials in committed files or build
logs. The daemon image task derives `DOCKER_DEFAULT_PLATFORM` from `GOARCH`,
keeping the binary and BoxLite/Microsandbox artifacts on the same architecture.

## 7. Recommended Build: Extend the Published Guest

This is the smallest maintenance surface and preserves all supported
capabilities:

```dockerfile
ARG AGENT_COMPOSE_VERSION=vX.Y.Z
FROM chaitin/agent-compose-guest:${AGENT_COMPOSE_VERSION}

# Add only project-specific tools. Keep the default root user and paths.
RUN apt-get update \
    && apt-get install -y --no-install-recommends jq rsync \
    && rm -rf /var/lib/apt/lists/*

COPY ./company-ca.crt /usr/local/share/ca-certificates/company-ca.crt
RUN update-ca-certificates
```

Replace `vX.Y.Z` with an immutable tag or digest matching the daemon release.
Build and publish a multi-architecture image when both architectures are used:

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg AGENT_COMPOSE_VERSION=vX.Y.Z \
  -f Dockerfile.guest \
  -t registry.example.com/team/agent-compose-guest:vX.Y.Z-custom.1 \
  --push .
```

Do not place secrets, model API keys, runtime tokens, or mutable project state
in image layers.

## 8. From-Scratch Example

The following example builds a Codex-only guest from the current repository
checkout. It intentionally omits Jupyter, Python, Go, compilers, other provider
CLIs, and the offline SDK tarball. The Docker build context **MUST** be the
repository root, and `CODEX_VERSION` should match the value in the same
checkout's published guest Dockerfile.

```dockerfile
FROM node:22-bookworm-slim

ARG CODEX_VERSION

RUN test -n "${CODEX_VERSION}" \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
         bash ca-certificates coreutils git util-linux \
    && npm install -g "@openai/codex@${CODEX_VERSION}" \
    && rm -rf /var/lib/apt/lists/* /root/.npm

COPY runtime/javascript /tmp/agent-compose-runtime

RUN cd /tmp/agent-compose-runtime \
    && npm ci \
    && runtime_tarball="$(npm pack --silent | tail -n 1)" \
    && npm install -g "./${runtime_tarball}" \
    && rm -rf /tmp/agent-compose-runtime /root/.npm

RUN mkdir -p \
      /root/.agents /root/.claude /root/.codex /root/.gemini /root/.opencode /root/.pi \
      /workspace /data/state /data/runtime /data/logs

ENV HOME=/root
USER root

# Helpful for direct `docker run`; agent-compose drivers replace startup
# commands with their own lifecycle command.
CMD ["sleep", "infinity"]
```

Build it from the matching checkout:

```bash
docker build \
  --build-arg CODEX_VERSION=<version-from-guest-Dockerfile> \
  -f Dockerfile.guest \
  -t registry.example.com/team/agent-compose-guest:custom .
```

This pattern deliberately copies runtime source instead of mixing an arbitrary
npm runtime version with the daemon. Add the optional capability dependencies
from Section 5 as needed.

## 9. Selecting the Image

Set the image on an agent:

```yaml
agents:
  reviewer:
    provider: codex
    image: registry.example.com/team/agent-compose-guest:vX.Y.Z-custom.1
    driver:
      docker: {}
```

The same OCI image reference can be selected with BoxLite or Microsandbox on a
prepared Linux/KVM deployment. A Docker-only test does not establish
cross-driver compatibility.

To make the image the deployment default, set `DEFAULT_IMAGE` or the applicable
driver-specific default in daemon configuration. An explicit agent image takes
precedence over the default.

## 10. Validation

### 10.1 Static contract probe

Adjust the optional checks to match the intended capability set:

```bash
image=registry.example.com/team/agent-compose-guest:custom

docker run --rm --entrypoint sh "$image" -lc '
  set -eu
  test "$(id -u)" = 0
  test "${HOME}" = /root
  test -d /root
  for command in sh mkdir test rm ln readlink mountpoint tail sleep; do
    command -v "$command" >/dev/null
  done
  for path in /workspace /data/state /data/runtime /data/logs; do
    test -d "$path"
  done

  # Runtime/Codex capability checks:
  command -v node >/dev/null
  command -v agent-compose-runtime >/dev/null
  command -v codex >/dev/null
  agent-compose-runtime --help >/dev/null
'
```

For Jupyter, also run:

```bash
docker run --rm --entrypoint sh "$image" -lc '
  command -v bash >/dev/null
  command -v nohup >/dev/null
  python3 -c "import jupyterlab"
'
```

### 10.2 Repository lifecycle tests

The Docker image lifecycle smoke verifies creation, direct exec, workspace
persistence, stop/resume, and removal with the real daemon image:

```bash
task image:agent-compose

AGENT_COMPOSE_E2E_DAEMON_IMAGE=agent-compose:latest \
AGENT_COMPOSE_E2E_GUEST_IMAGE=registry.example.com/team/agent-compose-guest:custom \
task test:e2e:image-docker
```

That smoke does not call a model provider. It proves the baseline Docker
lifecycle, not the runtime/provider layer.

If Jupyter is included, run:

```bash
AGENT_COMPOSE_E2E_DOCKER_JUPYTER_IMAGE=registry.example.com/team/agent-compose-guest:custom \
task test:e2e:docker-jupyter
```

For BoxLite or Microsandbox, run `task test:runtime-smoke` only on a prepared
Linux/KVM host, then perform a real project run with the custom image. Provider
acceptance should include at least one real prompt for every installed provider
and should verify resume/session state if that behavior is relied upon.

## 11. Upgrade Checklist

For every daemon upgrade:

1. Rebase the custom image on the matching published guest tag, or rebuild
   `runtime/javascript` from the matching source tag.
2. Reconcile provider CLI versions with
   the selected guest Dockerfile.
3. Re-run the static probe and Docker lifecycle tests.
4. Re-run Jupyter, provider, and selected-driver acceptance tests for enabled
   capabilities.
5. Publish an immutable tag or digest and update project/deployment references
   deliberately; do not silently move a production compatibility tag.

The implementation sources of truth are:

- [`guest-images/Dockerfile.agent-compose-guest`](https://github.com/chaitin/agent-compose/blob/main/guest-images/Dockerfile.agent-compose-guest)
- [`guest-images/Dockerfile.agent-compose-guest-archlinux`](https://github.com/chaitin/agent-compose/blob/main/guest-images/Dockerfile.agent-compose-guest-archlinux)
- [`pkg/driver/runtime_mount_manifest.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/driver/runtime_mount_manifest.go)
- [`pkg/driver/directory_only_guest_bootstrap.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/driver/directory_only_guest_bootstrap.go)
- [`pkg/driver/docker_runtime.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/driver/docker_runtime.go)
- [`pkg/driver/jupyter_guest.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/driver/jupyter_guest.go)
- [`pkg/agentcompose/adapters/agent_runner.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/agentcompose/adapters/agent_runner.go)
- [`pkg/execution/command_runtime.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/execution/command_runtime.go)
- [`runtime/javascript`](https://github.com/chaitin/agent-compose/tree/main/runtime/javascript)

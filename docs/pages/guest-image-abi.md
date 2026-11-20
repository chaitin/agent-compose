# Custom Guest Image ABI

This document defines the current minimum contract between `agent-compose` and
an OCI guest image. It also explains how to build, select, and validate a custom
image without copying every tool from the published
`chaitin/agent-compose-guest` image on Docker Hub.

The contract is capability-based. A sandbox image used only for direct command
execution needs much less software than an image that runs Codex, Claude,
OpenCode, Pi, DSH, JupyterLab, and every supported notebook cell type.

Normative terms such as **MUST**, **SHOULD**, and **MAY** describe compatibility
requirements in this document.

## 1. Compatibility Model

A guest image now declares the contract it was built for. Two artifacts carry
that declaration:

- the OCI label `io.agent-compose.guest-abi`, readable from the image metadata
  without starting it; and
- the manifest at `/etc/agent-compose/guest-abi.json`, carried in the image
  filesystem and readable from inside a running sandbox.

Both carry the same number, the **guest ABI**. This daemon release speaks one
guest ABI value (`CurrentGuestAbi` in `pkg/guestabi`). A guest image that
declares a different value is not a match for this daemon release.

The guest ABI is deliberately independent of provider versions. Bumping Codex,
Claude, OpenCode, Pi, or DSH changes what the image contains, not the contract
between the daemon and the guest, so `guestAbi` stays the same and a rebuilt
provider-only image can run under a daemon that is already deployed. Raising
`guestAbi` means the daemon/guest contract itself changed, which is a daemon
release rather than a guest rebuild.

The daemon still selects an image, mounts sandbox state, starts the image
through a runtime driver, and invokes commands by path and protocol convention.
The declaration changes what the daemon can know before that happens, not how it
starts a sandbox.

Consequently:

- A custom image **MUST** be tested with every `agent-compose` release and
  runtime driver used in production. A declaration is not a proof of contents:
  there is no startup handshake that inspects an arbitrary image, and the daemon
  does not refuse an image whose declaration is missing, unreadable, or for a
  different ABI. It falls back to its own compiled provider declaration instead.
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

### 1.1 The Guest ABI Artifact

`guest-images/providers.lock.json` is the single authoring point for what a
repository guest image contains. It records `guestAbi`, `nodeMajor`, and a
`packages` map; each entry names the npm package, the pinned version, the Docker
build arg that carries it, and optionally its executable, feature list,
companion package, or Node.js requirement. `scripts/provider-lock.sh` derives
everything else from that lock:

- `pairs` prints the `BUILDARG=version` lines;
- `guest-abi` renders the manifest, `sync` writes it to
  `guest-images/guest-abi.json`, and `guest-abi-version` prints the ABI number;
- `check` fails when the lock, either guest Dockerfile's `ARG` defaults, the
  build script's forwarding, or the generated manifest disagree; and
- `bump K=V [K=V ...]` rewrites one provider version in the lock, both
  Dockerfiles, and the manifest. It does not validate the new version.

`guest-images/guest-abi.json` is generated from the lock and committed. Only
packages that expose an agent executable appear under `providers`; a companion
package is a build input, so it is deliberately absent.
The daemon requires the manifest to describe every provider it knows (`codex`,
`claude`, `opencode`, `pi`, `dsh`); a manifest that omits one is rejected in
favor of the compiled declaration (`DeclaredProvidersFromManifest` in
`pkg/capmatrix/provider_matrix.go`):

```json
{
  "guestAbi": 1,
  "providers": {
    "codex": {
      "version": "x.y.z",
      "features": ["structured_output", "session_resume", "streaming"]
    }
  }
}
```

Both guest Dockerfiles bake that manifest and publish the ABI:

```dockerfile
ARG GUEST_ABI=1
LABEL io.agent-compose.guest-abi="${GUEST_ABI}"
COPY guest-images/guest-abi.json /etc/agent-compose/guest-abi.json
```

`GUEST_ABI` defaults to `1` in the Dockerfile and is overridden to the lock's
value by `scripts/build-agent-compose-guest.sh`. The manifest therefore travels
with the filesystem, and the label lets a registry or an operator see the
contract version without starting the image.

On the daemon side, `pkg/guestabi` owns the contract (`CurrentGuestAbi`,
`Parse`, `LoadFile`, `Validate`, `VerifyCompatible`, `ErrUnsupportedGuestAbi`).
The optional `GUEST_ABI_MANIFEST` environment variable names a manifest file for
the daemon; it is documented as a commented example in `.env.example`. When it is
set and valid, the provider capability matrix reported by
`EngineService.GetCapabilities` is built from that manifest instead of the
daemon's compiled declaration. When it is unset, unreadable, malformed, or
declares a different ABI, the daemon logs a warning and falls back to its
compiled declaration; it does not reject the image. Reading that file is the
only guest-related I/O in that path, so building the snapshot still performs no
Docker, KVM, or Kubernetes contact.

### 1.2 Reading the Guest ABI from a Running Sandbox

The runtime built from this repository can describe its own image:

```bash
agent-compose-runtime abi
agent-compose-runtime abi --json
agent-compose-runtime abi --manifest /etc/agent-compose/guest-abi.json
```

`abi` reads `/etc/agent-compose/guest-abi.json` by default; `--manifest`
overrides the path and `--json` prints one line of JSON. The report contains:

| Field | Meaning |
| --- | --- |
| `guestAbi` | The contract version the image declares; `0` when no usable manifest was found |
| `providers` | Provider name to `{version, features}` for every provider the image declares |
| `manifestPath` | The manifest path that was read |
| `runtimeVersion` | The version of the installed `agent-compose-runtime` package |

A missing, unreadable, malformed, or wrongly shaped manifest is reported as
`guestAbi: 0` with an empty `providers` map, and the command still exits `0`.
`0` is the documented "unknown or legacy guest" value: it means the image does
not describe itself, not that the image is broken. Only a broken runtime
installation (an unreadable runtime package manifest) makes the command fail.

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
`ENTRYPOINT`, `EXPOSE`, and health checks are therefore not ABI requirements.
They may still be useful when running the image by hand, but required
initialization **MUST** be baked into the image filesystem. The one label that
carries contract meaning is `io.agent-compose.guest-abi` (Section 1.1); the
daemon reads the baked `/etc/agent-compose/guest-abi.json` manifest, not the
label.

### 3.2 Commands required by the control plane

For all four runtime drivers, the image **MUST** provide `sh` with `-lc`
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
| `/etc/agent-compose/guest-abi.json` | Guest ABI manifest baked into the published image | Image-owned; not persisted; absent from images built without it |
| `/root` | Image-provided real home directory | The directory itself is image-owned |
| `/root/.codex` | Codex config and state | Persisted |
| `/root/.agents` | Projected agent skills | Persisted/projected |
| `/root/.claude` | Claude config and state | Persisted |
| `/root/.opencode` | OpenCode state | Persisted |
| `/root/.pi` | Pi configuration and state | Persisted |
| `/root/.dsh` | DSH profile and state | Persisted |
| `/root/.local/share/opencode` | OpenCode session data it resumes from | Persisted |
| `/root/.claude.json` | Claude root config | Persisted file |
| `/root/.gitconfig` | Git config | Persisted file |
| Selected `/root/.config/...` paths | Provider state | Persisted |

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

Prompt-mode `stream` sessions support the `codex`, `claude`, `opencode`, `pi`, and `dsh` providers.
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
| `AGENT_COMPOSE_RESOLVED_MODEL` | Model reference resolved for the current managed agent invocation |

Pi and DSH runtimes use `AGENT_COMPOSE_RESOLVED_MODEL` without interpreting its
slashes. During staged upgrades, a new runtime still accepts the legacy
connection/model argument when an older daemon omits this variable. That
tolerance runs one way only: the daemon does **not** preserve DSH's legacy
argument prefix for older guests, so an older DSH guest driven by a newer daemon
receives the model verbatim and will truncate it at the first slash. Upgrade the
guest image before or together with the daemon. This fallback is deprecated and
will be removed once those older daemon versions are no longer supported; new
integrations should consume the resolved value. Continue to use release-matched
images for the rest of the runtime protocol.

The `prompt` and `exec` stdout payloads, stream separation, artifact files, and
interactive NDJSON frames are protocol, not just CLI presentation. A custom
replacement runtime **MUST** implement the matching release protocol. Reusing
the repository runtime is strongly recommended.

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

### 4.2 LLM facade variables

When the daemon manages an agent's LLM connection, it publishes the facade in the
sandbox environment, so an image that reads these variables at container start
finds them. Every value is a facade route and a facade token; the upstream
credential never reaches the guest.

| Variable | Value |
| --- | --- |
| `AGENT_COMPOSE_SANDBOX_TOKEN` | Facade token for the managed invocation |
| `LLM_API_ENDPOINT` | Facade endpoint of the resolved connection |
| `LLM_API_KEY` | Same facade token, for images that read the generic contract |
| `LLM_API_PROTOCOL` | Upstream protocol the facade speaks for this connection |
| `LLM_MODEL` | Model reference the facade token authorizes |
| `AGENT_COMPOSE_RESOLVED_MODEL` | The same model in the namespace the agent addresses models by |

The selected agent's dialect then adds its own names. Codex, Claude, opencode,
and pi each address one provider family, so each publishes that family's whole
set: `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `CODEX_MODEL`, and `OPENAI_MODEL` for
the OpenAI family, or `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`,
`ANTHROPIC_BASE_URL`, `ANTHROPIC_MODEL`, and `CLAUDE_MODEL` for the Anthropic
family. A dialect that replaced only part of the set would leave the rest at the
startup compatibility facade's value, pairing this run's token with the model that
facade resolved rather than the one this run did. opencode adds `OPENCODE_CONFIG`
and `OPENCODE_MODEL`, pi adds `PI_CODING_AGENT_DIR`, and dsh publishes no
provider-specific name at all, which leaves the startup compatibility facade's set
intact.

Older images that predate the generic `LLM_*` contract are served by the
provider-specific names. `ANTHROPIC_MODEL` and `CLAUDE_MODEL` are one pair;
`CODEX_MODEL` and `OPENAI_MODEL` are another. The daemon publishes **both pairs**,
each from an enabled provider plus the catalog default, the declared upstream, or
a provider credential in the sandbox or global environment. Which family an image
reads is a property of its entrypoint, not of the agent the daemon selected: a
sandbox whose agent is codex can run an image whose entrypoint reads
`ANTHROPIC_API_KEY`, and publishing only the selected agent's family leaves that
image without the variables it needs to start. A family is skipped only when it
has no enabled provider or no model to publish.

A model name in any of these variables is the model the delivered token was
issued for, but the token authorizes the connection it names rather than that one
model: a request for a model it does not record is forwarded there, and **that
connection decides which models it serves**. An image **SHOULD** therefore send
the model it was given, or the resolved reference, rather than a model name built
into the image — a name the upstream does not serve fails there, with an error the
image has to interpret, instead of being caught at the facade.

The two pairs are not equivalent to the guest. The family the selected agent
addresses is also written by that agent's own configuration, which replaces the
startup compatibility facade's names for it and points them at the model this run
resolved to. The other family has no such owner: it is published so an image whose
entrypoint reads only that family still starts, and its token records no model,
because the daemon does not configure that image and cannot predict which model
its entrypoint will ask for. Either way, a request that names a model the token
does not record is forwarded to the connection the token names.

## 5. Optional Capability Requirements

### 5.1 Agent providers

Install only the providers selected by the project. The runtime adapters and
the provider executables should come from the same repository tag's official
guest Dockerfile unless a combination has been independently tested.

| Provider | Executable/runtime requirement |
| --- | --- |
| Codex | `@openai/codex-sdk` in the runtime package plus an executable selected by `CODEX_BIN`, `AGENT_COMPOSE_CODEX_BIN`, `/usr/bin/codex`, `/usr/local/bin/codex`, or `codex` in `PATH` |
| Claude | `@anthropic-ai/claude-agent-sdk` plus Claude Code; use `CLAUDE_CODE_EXECUTABLE`/`CLAUDE_CODE_PATH`, `/usr/bin/claude`, or the SDK-supported default |
| OpenCode | An `opencode` executable in `PATH` |
| Pi | A `pi` executable in `PATH`; Pi 1.x includes built-in MCP support, so no adapter extension is required |
| DSH | A `dsh` executable in `PATH` (the published guest installs `@deepseek-ai/dsh` globally); `dsh --profile agent-compose` also requires the profile assets under `/root/.dsh/profiles/agent-compose` |

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
| `IMAGE_TAG` | Local image tag for the daemon task and both guest tasks |
| `NO_CACHE=1` | Passes Docker's `--no-cache` flag |
| `GO_VERSION`, `GRPCURL_VERSION`, `NODE_MAJOR` | Default guest toolchain inputs; `NODE_MAJOR` applies only to the Debian guest |
| `ARCHLINUX_TAG` | Arch Linux guest base-image tag |
| `GUEST_ABI` | Guest ABI contract version declared by the label and baked manifest; defaults to the lock's `guestAbi` |
| `CODEX_VERSION`, `CLAUDE_CODE_VERSION`, `OPENCODE_VERSION`, `PI_AGENT_VERSION`, `DSH_VERSION` | Guest provider package versions; each defaults to the matching pin in `guest-images/providers.lock.json` |

`guest-images/providers.lock.json` is the single authoring point for provider
versions. `scripts/build-agent-compose-guest.sh` reads the lock for every
provider version and for `GUEST_ABI`, so leaving one of these variables unset
still produces a pinned, reproducible build; setting it explicitly overrides the
lock for a one-off local build. `scripts/provider-lock.sh check` fails when the
lock, either guest Dockerfile's `ARG` defaults, the build script's forwarding,
or the generated `guest-images/guest-abi.json` disagree.

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
repository root, and `CODEX_VERSION` **SHOULD** match the pin in the same
checkout's `guest-images/providers.lock.json`, which `scripts/provider-lock.sh`
keeps consistent with the guest Dockerfiles' `ARG` defaults.

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
      /root/.agents /root/.claude /root/.codex /root/.opencode /root/.pi /root/.dsh \
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
  --build-arg CODEX_VERSION=<codex-version-from-providers.lock.json> \
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

Daemon releases and guest image builds are separate tracks. `guestAbi` decides
whether artifacts from the two tracks may be combined, and
`scripts/provider-lock.sh check` keeps the repository's own guest build
consistent. Only two things still couple the tracks: a deliberate increase of
`guestAbi`, and the DSH profile in Section 11.1.

For a daemon upgrade:

1. Note the guest ABI the new daemon speaks (`CurrentGuestAbi` in
   `pkg/guestabi`; `scripts/provider-lock.sh guest-abi-version` prints the value
   this checkout builds). It changes only when the daemon/guest contract
   changed.
2. If it changed, rebuild and republish the custom guest image at the new value
   (Section 11.2) and update project/deployment image references. If it did not
   change, an existing provider-only image keeps running; the daemon upgrade
   alone does not require rebuilding it.
3. Whenever the daemon release changes the runtime protocol, rebase the custom
   image on the matching published guest tag, or rebuild `runtime/javascript`
   from the matching source tag. The runtime CLI and its stdout protocols are
   still a release boundary (Section 1).
4. Re-run the static probe, the Docker lifecycle test, and the Jupyter,
   provider, and selected-driver acceptance tests for the capabilities that are
   enabled.
5. Publish an immutable tag or digest and update project/deployment references
   deliberately; do not silently move a production compatibility tag.

### 11.1 The DSH profile comes from the daemon, and is frozen per sandbox

The `dsh` provider is the one provider whose behavior depends on a daemon
artifact rather than only on the image: the daemon embeds the `agent-compose`
profile and copies it into a sandbox's `home/.dsh` the first time that sandbox
home is initialized. Those two must therefore match.

- The daemon seeds `home/.dsh` only when the target does not already exist
  (`initializeSandboxHomeDefaults` in
  [`pkg/driver/runtime_mount_manifest.go`](https://github.com/chaitin/agent-compose/blob/main/pkg/driver/runtime_mount_manifest.go)
  skips existing entries), and the home directory persists per sandbox.
- A sandbox therefore runs the profile of the daemon that **first seeded its
  home**, not of the daemon that is deployed now. Upgrading the daemon does not
  refresh an existing sandbox.
- The k8s driver pushes the same host-side home snapshot into the guest on
  demand, so this applies to every driver.

Consequences:

- Install or upgrade the daemon and the guest image **in the same batch**. A
  guest image built against the older DSH API cannot activate the profile a
  newer daemon embeds, and vice versa.
- Sandboxes created before the upgrade keep the old profile. Recreate
  long-lived sandboxes after a daemon upgrade rather than expecting them to pick
  up the new profile.
- When the profile cannot activate, the runtime now reports an actionable
  error and stops the run instead of waiting indefinitely.

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

### 11.2 Upgrading a Guest Image Without a Daemon Release

A provider-only rebuild is safe to publish and deploy under a daemon that is
already deployed, provided the rebuild keeps the same `guestAbi`:

- The provider versions in `guest-images/providers.lock.json` change, and
  `scripts/provider-lock.sh bump` rewrites the lock, both guest Dockerfiles, and
  `guest-images/guest-abi.json` in one step.
- `guestAbi` does not change, because neither the daemon/guest contract nor the
  runtime CLI protocol changed. `scripts/tests/test-provider-lock.sh` guards
  that property: it bumps a provider in a copy of the lock and fails if the
  rendered `guestAbi` moves.
- The rebuilt image still needs the guest image acceptance tests for the
  providers it changes. The number alone does not prove that a provider CLI
  still behaves the way the runtime adapter expects, which is why the daily
  check only reports and never edits a pin.

This holds when the only input that changed is a provider pin. The same image
build also packages `runtime/javascript` from its checkout, and the runtime CLI
protocol is a release boundary that `guestAbi` does not cover (Section 1). If a
rebuild also picks up runtime source changes, keep that image matched to the
daemon release as usual.

Anything that raises `guestAbi` is not a guest-only change. Raising it means the
daemon/guest contract changed, so a daemon release with a matching
`CurrentGuestAbi` has to ship before an image at the new value can be adopted.
There is no broader version matrix: a manifest is compatible when its `guestAbi`
equals the daemon's `CurrentGuestAbi`, and that single comparison is the whole
rule (`VerifyCompatible` in `pkg/guestabi/compat.go`).

A companion package can record that a later release of its provider makes it
redundant, so "drop this when we upgrade" is enforced by the upgrade instead of
remembered by a person. `pi-mcp-adapter` carried:

```json
"retiredBy": {
  "package": "pi",
  "version": "0.99.0",
  "reason": "Pi 0.99 and later ship a built-in MCP extension, so the adapter only exists to add MCP to older Pi releases"
}
```

`scripts/provider-lock.sh check` then fails while a retired package is still
pinned next to a version at or above `retiredBy.version`, and the message names
the package and the reason. The fix is part of the upgrade: remove the package
from the lock, both guest Dockerfiles, and the build script. The gate fired when
`pi` moved to 1.3.0, which is how the adapter was removed; the lock no longer
pins a companion today, and the mechanism remains for the next one.

[`.github/workflows/guest-image-release.yml`](https://github.com/chaitin/agent-compose/blob/main/.github/workflows/guest-image-release.yml)
is the independent release path. On manual `workflow_dispatch` it verifies the
lock, builds a loadable `linux/amd64` image, runs
`scripts/verify-guest-image-abi.sh`, probes the baseline guest ABI, prints the
in-image `agent-compose-runtime abi --json` report, and only then optionally
publishes a multi-architecture image. Its tags are:

- an immutable `guest-abi<N>-<YYYYMMDD>-<shortsha>`, plus an `-archlinux` suffix
  for the optional Arch Linux variant; and
- a moving `guest-abi<N>` alias, but only when `promote_alias` is set.

It deliberately never moves `latest`: deployments default to
`chaitin/agent-compose-guest:latest` through `DEFAULT_IMAGE`, and silently
repointing that tag would swap the guest under every existing installation.

Verify a published image against the checkout that built it before selecting it:

```bash
scripts/verify-guest-image-abi.sh \
  registry.example.com/team/agent-compose-guest:guest-abi1-20260101-abc1234
```

The script compares the image's `io.agent-compose.guest-abi` label, its baked
manifest, and every agent provider version against
`guest-images/providers.lock.json`, prints the providers it found, and exits
non-zero on any disagreement. Run it against a digest or an immutable tag, not
against a moving alias, and use the checkout that built the image because the
provider versions are compared against that checkout's lock.

### 11.3 Daily Upstream Dependency Check

[`.github/workflows/upstream-check.yml`](https://github.com/chaitin/agent-compose/blob/main/.github/workflows/upstream-check.yml)
runs `scripts/check-upstreams.sh` daily at `17 1 * * *` UTC and on manual
`workflow_dispatch`. It compares two kinds of pinned dependency and reports the
result where a team already looks:

- every package pinned in `guest-images/providers.lock.json` is compared with the
  version npm currently publishes; and
- every native runtime driver pinned in `drivers.lock.json` is compared with the
  latest stable GitHub release of its repository. The lock currently covers
  BoxLite and Microsandbox. Docker and Kubernetes are deliberately not tracked:
  they are host-provided deployment dependencies rather than artifacts this
  repository builds against a pinned SDK, so there is no version here to chase.

The scan refuses to run when a driver lock entry disagrees with the `Dockerfile`
ARG that actually builds it, or with the Go SDK module it must move in lockstep
with. The committed contract test also checks the committed pins, so a change
that moves one without the other fails CI.

- when the `DINGTALK_DEV_TARGETS_JSON` secret is configured, one summary is
  pushed to the development DingTalk group through `scripts/notify-dingtalk.sh`.
  It is written for that group, so it is Simplified Chinese and lists only what
  needs attention: which pinned providers or drivers have a newer release with
  their pinned and latest versions, one line for each package retired by another
  provider's release, and a line when an npm or GitHub release query failed. This
  is a channel of its own: release announcements use the separate
  `DINGTALK_TARGETS_JSON` secret consumed by the release workflow. That webhook is
  the notification path: there is no tracking issue to watch. If the secret is
  unset, the run still succeeds but emits a warning and says so in the job
  summary, because a silent run and an up-to-date run would otherwise look
  identical; and
- the same summary is written to the job summary, and is the body of the draft
  bump pull request.

The workflow never edits a provider or driver pin on its own. A package whose
lock entry records a `retiredBy` release is reported together with that plan, and
is left out of the bump list: the intended action is to delete it in the upgrade
that retires it, not to follow its own releases. A `workflow_dispatch` input
`bump` additionally opens a draft pull request that applies `provider-lock.sh
bump` to the outdated provider pins and then runs `scripts/provider-lock.sh
check`. Driver pins are only reported: their Dockerfile artifacts, lock entry, and
Go SDK module must move together, so a person upgrades them by hand. The draft is
deliberate: the provider bump is mechanical, while deciding whether a provider
release is safe is not. A failing check does not block the draft — it is recorded
in the pull request body, and CI enforces it once the draft is marked ready. Two
optional repository variables refine the scan: `PROVIDER_UPGRADE_MENTIONS` is
appended to the pushed summary for @-mentions, and `NPM_CONFIG_REGISTRY` is used
for the npm queries.

GitHub's scheduler is best-effort, so treat the job summary as the durable record
and the DingTalk push as the notification:

- a `cron` entry can be delayed, and scheduled workflows run only on the default
  branch;
- scheduled workflows are disabled after 60 days of repository inactivity; and
- scheduled workflows do not run in forks.

Run the same scan on demand with:

```bash
scripts/check-upstreams.sh
```

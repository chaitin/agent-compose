<p align="center">
  <img src="images/agent-compose-logo.png" alt="Agent-compose" height="60">
</p>
<p align="center">
  <a href="https://github.com/chaitin/agent-compose/actions/workflows/ci.yml">
    <img src="https://github.com/chaitin/agent-compose/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI">
  </a>
  <a href="https://github.com/chaitin/agent-compose/actions/workflows/images.yml">
    <img src="https://github.com/chaitin/agent-compose/actions/workflows/images.yml/badge.svg?branch=main" alt="Images & Release">
  </a>
  <a href="LICENSE.txt">
    <img src="https://img.shields.io/badge/License-AGPL%20v3-blue.svg" alt="License: AGPL v3">
  </a>
</p>
<p align="center">
  <a href="https://agent-compose.ai">Website</a> ·
  <a href="https://demo.agent-compose.ai">Demo</a> ·
  <a href="README.zh-CN.md">中文文档</a>
</p>

**agent-compose is a daemon + CLI control plane that runs AI coding agents in isolated sandboxes.** You describe your agents in an `agent-compose.yml` file, and a long-lived daemon builds, runs, schedules, and proxies an isolated runtime for each one.

## What is agent-compose?

If you know Docker Compose, the mental model is familiar: instead of declaring
containers, you declare **agents**. Each agent picks a provider CLI — `codex`,
`claude` (Claude Code), `gemini`, `opencode`, or `pi` — and the daemon gives it its own
isolated sandbox with a workspace, then runs it on a prompt, a shell command, a
schedule, or an event.

You manage the whole lifecycle with a Compose-style CLI (`up`, `run`, `ps`,
`logs`, `down`), and everything is driven by one declarative file.

Concretely, agent-compose provides:

- A **declarative compose model** (`agent-compose.yml`) with `${ENV}` interpolation.
- **Multi-provider guest agents**: Codex, Claude Code, Gemini, OpenCode, and Pi CLIs.
- **Three runtime drivers**: `docker` (default), `boxlite` (microVM), and `microsandbox`.
- A **scheduler** with `cron`, `interval`, `timeout`, and `event` triggers — or full inline JavaScript scheduler scripts.
- **Event triggers and webhooks** for event-driven agent runs.
- **Workspaces** provisioned from a local directory or a Git repository.
- **MCP servers, reusable skills, and named volumes** per agent.

## How it works

The **daemon** is the single source of truth: it owns persistence, scheduler
execution, runtime lifecycle, and control-plane APIs. The
**CLI** is a thin client — it reads your local `agent-compose.yml`, validates it,
and calls the daemon. The compose file describes *projects and agents*, not
already-running sandboxes. The **web UI** is a separate service
([agent-compose-ui](https://github.com/chaitin/agent-compose-ui)) and is not
hosted by the daemon.

For the full architecture, see [docs/design/agent-compose_design.md](docs/design/agent-compose_design.md).

## Quick start

### Option A — Run a server (recommended)

The one-line installer sets up the agent-compose daemon with Docker Compose on
Linux amd64/arm64:

```bash
curl -fsSL https://github.com/chaitin/agent-compose/releases/download/installer-latest/install.sh | bash
```

The bootstrap selects the Linux amd64/arm64 installer and opens its bilingual
TUI. The default installation directory is `/opt/agent-compose`; use `sudo`
when the current user cannot write there. For automation, run
`install --yes` and pass options explicitly.

The TUI resolves the selected Release and shows its exact backend, frontend,
and guest image defaults. Changing the application version refreshes those
defaults; entering a complete image reference overrides only that image.

The base stack starts the daemon. The frontend is defined under the `with-ui`
profile, which the installer leaves off unless you answer yes to **Install web
UI** (or pass `--with-ui`). Enabling it there persists `COMPOSE_PROFILES` in
`.env`, so later `docker compose up -d` runs keep the frontend. To turn it on
afterwards, from the installation directory printed by the installer:

```bash
cd <directory printed by the installer>
docker compose --profile with-ui up -d
```

The installer also pre-pulls the sandbox guest image so the first agent run
does not stall on a large download; pass `--skip-guest-pull` (or answer no in
the TUI) to defer it.

On first run the installer generates an `admin` password and prints it once;
use it at the URL the installer prints when the UI is enabled. See
[deploy/README.md](deploy/README.md) for install, upgrade, uninstall, data
preservation, and mirror/private-registry options.

### Option B — Pull published images (without the installer)

Published daemon, guest, and UI images are available from [Docker
Hub](https://hub.docker.com/u/chaitin):

```bash
docker pull chaitin/agent-compose:latest
docker pull chaitin/agent-compose-guest:latest
docker pull chaitin/agent-compose-ui:latest
```

The daemon and standard guest images publish `linux/amd64` and `linux/arm64`
manifests. To deploy with Compose, copy `.env.example` to `.env`, fill in the
required settings, and use these image references:

```dotenv
AGENT_COMPOSE_IMAGE=chaitin/agent-compose:latest
DEFAULT_IMAGE=chaitin/agent-compose-guest:latest
AGENT_COMPOSE_FRONTEND_IMAGE=chaitin/agent-compose-ui:latest
```

Then run `docker compose pull` and `docker compose up -d`, adding
`--profile with-ui` when the frontend is needed.

### Option C — Build from source (for the CLI workflow)

```bash
task build                       # builds ./build/agent-compose
export PATH="$PWD/build:$PATH"   # so `agent-compose` is on your PATH
agent-compose daemon
```

The host build is platform-specific: macOS produces a Docker-only native
binary, while Linux produces a full binary with Docker, BoxLite, and
Microsandbox compiled in. A Linux build prepares both native runtime artifact
sets through Docker when matching local artifacts are not already available.
These native binaries are development and CI verification artifacts, not
GitHub Release downloads.

The daemon listens on a local Unix socket by default. To expose a local HTTP
endpoint instead:

```bash
HTTP_LISTEN=127.0.0.1:7410 agent-compose daemon
agent-compose --host http://127.0.0.1:7410 status
```

### Run your first agent

With a local daemon running (Option C), create an `agent-compose.yml`:

```yaml
name: demo

agents:
  reviewer:
    provider: codex
    image: chaitin/agent-compose-guest:latest
    driver:
      docker: {}
```

Then drive the lifecycle:

```bash
agent-compose up                                  # apply the project to the daemon
agent-compose ps                                  # list project sandboxes
agent-compose run reviewer --prompt "Review this change"
agent-compose logs --agent reviewer
agent-compose down                                # stop sandboxes, disable schedulers
```

More runnable examples (cron, timeout, scheduler scripts) live in
[examples/agent-compose/](examples/agent-compose/).

## The compose file

**Top-level fields:** `name`, `env_file`, `variables`, `workspaces`, `agents`, `mcp_servers`, `volumes`.

**Common agent fields:** `provider`, `model`, `system_prompt`, `image`,
`driver`, `env` (scalars or `{ value, secret }`), `workspace`, `scheduler`,
`mcp_servers`, `skills`, and `volumes`.

Provision an agent's workspace from a local path (`provider: file`) or a Git
repository (`provider: git`):

```yaml
agents:
  reviewer:
    workspace:
      provider: git
      url: https://github.com/example/repo.git
      ref: main
      target: .
```

Scheduler scripts may be inline JavaScript or a flat source mapping using
`provider: file`, `provider: http`, or `provider: git`. HTTP(S) script sources
must resolve to public addresses and are fetched without environment proxies;
this prevents SSRF through proxy-side DNS resolution. `config` and `up` fetch
mapped sources locally and send an inline snapshot to the daemon. Use
either `scheduler.script` or `scheduler.triggers` in one scheduler.

For example, load a scheduler script over HTTP:

```yaml
agents:
  reviewer:
    scheduler:
      enabled: true
      script:
        provider: http
        url: https://example.com/scheduler.js
```

Add scheduled or event-driven runs. Use either `scheduler.triggers` **or** an
inline `scheduler.script`, not both in the same scheduler:

```yaml
agents:
  reviewer:
    scheduler:
      enabled: true
      triggers:
        - name: hourly-review
          cron: "0 * * * *"
          prompt: "Review the current project state and summarize changes."
```

See the [command line manual](docs/pages/command-line-manual.md) for the full field reference.

See the [Connect transport support matrix](docs/pages/connect-transport-matrix.md) for unary, streaming, and bidirectional attach deployment requirements.

## CLI overview

| Command | Purpose |
| --- | --- |
| `agent-compose daemon` | Start the HTTP/Connect daemon. |
| `agent-compose up` | Read `agent-compose.yml` and apply the project. |
| `agent-compose run <agent> --prompt/--command` | Run a prompt or shell command as an agent. |
| `agent-compose exec <sandbox>` | Execute a command or prompt in a running sandbox. |
| `agent-compose ps` / `stats` | List project sandboxes / show sandbox resource stats. |
| `agent-compose logs` | Print project run logs; a project, agent, run, or sandbox ID can be passed without its resource type. |
| `agent-compose scheduler ls\|invoke\|runs\|logs\|trigger\|inspect\|prune` | Invoke schedulers, list triggers and runs, read logs, manually run triggers, inspect resources, or prune terminal trigger-run history. |
| `agent-compose sandbox ls\|stop\|resume\|rm\|prune` | Manage project sandboxes. |
| `agent-compose image ls\|pull\|build\|rm\|inspect` | Manage daemon images and build agent images; top-level shortcuts remain available. |
| `agent-compose volume ls\|create\|inspect\|rm\|prune` | Manage daemon volumes. |
| `agent-compose cache ls\|inspect\|prune\|rm` | Inspect and clean daemon runtime caches. |
| `agent-compose down` | Disable managed schedulers and stop sandboxes. |
| `agent-compose status` | Check daemon status. |

Useful global flags: `--file, -f` (choose a compose file), `--project-name, -p` (select a deployed project by name),
`--json` (stable JSON for scripts), `--host` / `AGENT_COMPOSE_HOST` (connect to a
TCP daemon), and `AGENT_COMPOSE_SOCKET` (Unix socket path). Full reference:
[docs/pages/command-line-manual.md](docs/pages/command-line-manual.md).

## Runtime drivers

- **`k8s`**: runs each guest as a Kubernetes Pod; the daemon must run inside the target cluster and be installed with the Helm chart.

- **`docker`** (default): runs guests in Docker containers; requires a working Docker daemon.
- **`boxlite`**: runs guests as microVMs using BoxLite runtime artifacts.
- **`microsandbox`**: runs guests using the Microsandbox VM runtime.

The three names describe product-supported drivers; a particular artifact may
compile a subset:

| Artifact | Compiled drivers |
| --- | --- |
| macOS native binary | `docker` |
| Linux native binary | `docker`, `boxlite`, `microsandbox` |
| Published Linux daemon image (`amd64` and `arm64`) | `docker`, `boxlite`, `microsandbox`, `k8s` |

Inspect an artifact with `agent-compose --json version` or `/api/version`.
The `compiled_drivers` field reports build capability only—it does not probe the
Docker daemon, KVM, or native runtime artifacts, and it is not a runtime health
or availability list. The full Linux image still defaults to `docker` and works
in Docker mode on macOS Docker Desktop; KVM is needed only when actually using
BoxLite or Microsandbox.

Image handling is selected by `IMAGE_STORE_MODE` (`auto` / `docker` / `oci`,
where `oci` uses a daemonless image cache). New sandboxes use the image set by
`DEFAULT_IMAGE`; the bundled `.env.example` and installer set this to
`chaitin/agent-compose-guest:latest`, which ships the agent runtime and
provider CLIs.

## Agent providers

Each agent sets a `provider`, which selects the CLI it runs inside the sandbox:

| Provider | Runs |
| --- | --- |
| `codex` | Codex CLI |
| `claude` | Claude Code CLI |
| `gemini` | Gemini CLI |
| `opencode` | OpenCode CLI |
| `pi` | Pi coding agent CLI |
| `dsh` | DeepSeek Harness CLI |

Set the variables for the backend family your agents use. **OpenAI-family**
(Codex, plus the daemon's own `LLMService` and scheduler LLM calls):

```env
LLM_API_ENDPOINT=https://api.openai.com
LLM_API_PROTOCOL=responses    # or chat_completions for DeepSeek / vLLM / Ollama
LLM_API_KEY=sk-...
LLM_MODEL=gpt-...
```

**Anthropic-family** (Claude):

```env
ANTHROPIC_BASE_URL=https://api.anthropic.com
ANTHROPIC_API_KEY=sk-ant-...
ANTHROPIC_MODEL=claude-...
```

Set `LLM_API_PROTOCOL=chat_completions` to target any OpenAI-compatible endpoint
(DeepSeek, vLLM, Ollama).

**Gemini** is never handed an LLM key (`GEMINI_API_KEY` /
`GOOGLE_API_KEY` are filtered out of the guest) and authenticates through the
Gemini CLI's own login, persisted under the sandbox home (`~/.gemini`).

See [`.env.example`](.env.example) for the full list (timeouts, endpoint aliases,
`OPENAI_API_KEY` / `ANTHROPIC_AUTH_TOKEN`).

## Deployment & configuration

For an in-cluster Kubernetes daemon, install the Helm chart and choose the
target context and namespace at install time:

```bash
helm install agent-compose ./charts/agent-compose \
  --kube-context prod-cluster \
  --namespace team-a \
  --create-namespace
```

The release namespace becomes the default sandbox namespace. The chart renders
the Service DNS callback URL and RBAC subject from the selected namespace; see
[`charts/agent-compose/README.md`](charts/agent-compose/README.md) for image,
PVC, and upgrade options.

For a server deployment with published images:

```bash
cp .env.example .env
openssl rand -base64 24   # use for AUTH_PASSWORD
openssl rand -hex 32      # use for AUTH_SECRET
docker compose pull && docker compose up -d
docker compose --profile with-ui up -d   # also start the web UI
```

The base `docker-compose.yml` mounts the Docker socket but requests neither
privileged mode nor `/dev/kvm`, so it is the standard Docker-only topology. On a
Linux host where BoxLite or Microsandbox will be used, add the explicit KVM
overlay:

```bash
docker compose -f docker-compose.yml -f docker-compose.kvm.yml up -d
```

The daemon also mounts the Linux host's `/etc/localtime` read-only. Cron
triggers without an explicit `timezone` therefore follow the host timezone.
Set `TZ` in `.env` only to override it, and restart the daemon after changing
the timezone.

The installer checks for `/dev/kvm` on a new installation and persists either
the base file or the base-plus-KVM file set as `COMPOSE_FILE` in `.env`. This is
deployment selection, not proof that KVM or either native runtime is healthy.
See [deploy/README.md](deploy/README.md) for manual selection and upgrade
behavior.

**[`.env.example`](.env.example) is the authoritative, fully commented
configuration reference.** At minimum, review these before exposing a deployment:

- `AUTH_PASSWORD`, `AUTH_SECRET` — UI server login secrets (replace the examples).
- `AGENT_COMPOSE_AUTH_TOKEN` — optional shared Bearer token for daemon HTTP(S) control-plane access.
- `AGENT_COMPOSE_HTTP_PORT` — host port for the web UI / reverse proxy (`with-ui`).
- `RUNTIME_DRIVER` — default runtime driver.

## Web UI

The web UI lives in a separate repository,
[agent-compose-ui](https://github.com/chaitin/agent-compose-ui). The daemon does not host the UI or browser
login flows; the UI image runs nginx in front of a Go UI server that owns
auth/OAuth and proxies API routes to the daemon.

## Security

The default configuration targets local development. Harden it before exposing a
deployment to a network:

- Expose browser access through the agent-compose-ui server, not the daemon directly.
- Set a stable, high-entropy `AUTH_SECRET`, and terminate HTTPS in production.
- Keep the daemon TCP API (`HTTP_LISTEN`) behind container networking, a reverse proxy, or a VPN.
- When daemon token authentication is enabled, use HTTPS or another protected tunnel across machines; plain HTTP does not prevent token capture and replay.
- Treat Git credentials, uploaded workspaces, environment variables, and LLM API keys as secrets.

See [SECURITY.md](SECURITY.md) for vulnerability reporting and hardening notes.

## Build & test

```bash
task lint
task build
task test          # includes deterministic installer/Compose/release checks
```

Build guest and daemon images with `task image:agent-compose-guest` and
`task image:agent-compose`. `task build:agent-compose` builds the native host
profile: Docker-only on Darwin and the full Docker, BoxLite, and Microsandbox
profile on Linux. Use `task build:agent-compose:darwin` or
`task build:agent-compose:linux` to select one explicitly. The old
`build:agent-compose:boxlite` task is a deprecated alias for the Linux full
profile. It is not a separate BoxLite-only build. `compiled_drivers` can verify
the resulting build capability, but not runtime availability.

Image builds use standard ecosystem variables directly. With no overrides they
use the public upstream defaults. Restricted networks can pass the same names
after `task` or export them in the environment, for example:

```bash
task image:agent-compose-guest \
  HTTPS_PROXY=http://proxy.example.com:8080 \
  NO_PROXY=localhost,127.0.0.1 \
  REGISTRY_MIRROR=mirror.example.com \
  GOPROXY=https://goproxy.example.com,direct \
  NPM_CONFIG_REGISTRY=https://npm.example.com \
  PIP_INDEX_URL=https://pypi.example.com/simple
```

`REGISTRY_MIRROR` changes Docker base-image references only. `GOPROXY`,
`GITHUB_MIRROR`, `NPM_CONFIG_REGISTRY`, `PIP_INDEX_URL`, and
`ARCHLINUX_MIRROR` control their corresponding dependency ecosystems. There
are no `BUILD_*` aliases for these variables. See the
[guest image ABI](docs/pages/guest-image-abi.md#62-repository-image-build-inputs)
for the complete scope and task matrix.

Stable deployment and full-image Docker smoke entry points are:

```bash
task test:deploy
task image:agent-compose
task image:agent-compose-guest
task test:e2e:image-docker
```

The image smoke runs the full three-driver Linux image through its Docker path
without privilege or KVM. Real BoxLite/Microsandbox smoke remains an explicit
Linux/KVM operation via `task test:runtime-smoke`.

Native daemon binaries are retained only for local and CI verification. The
architecture-specific Go installer is published separately under the fixed
`installer-latest` prerelease and consumes the deployment bundle from normal
application releases. The supported deployment remains the published
multi-architecture images plus that installer. The JavaScript runtime
components live under `runtime/`.

## Documentation

- [Documentation homepage](https://chaitin.github.io/agent-compose/)
- [Command line manual](docs/pages/command-line-manual.md)
- [agent-compose.yml manual](docs/pages/agent-compose-yaml-manual.md)

## One-time V2 storage migration

`agent-compose-v2-storage-migrator` is a transitional, one-time tool for data
roots created by the legacy storage layout. It is intentionally not included
in daemon images or the default `task build`.

The migrator applies to data roots created by agent-compose ≤ v2607.10.0.
Starting from v2608.1.0, the daemon uses V2 storage natively and upgrades
databases with a valid, versioned migration prefix automatically when they
contain only project-managed agents and schedulers. Use the migrator for
legacy or unversioned roots, including standalone or mixed agent/loader
layouts.

### Stop running sandboxes

Do not use `agent-compose down` for migration cutover. In legacy releases,
`down` also marks the project removed, disables its schedulers, and removes
its volume associations. Without the original compose file, the migrated
project cannot be restored safely from those changes.

Keep the old daemon running and stop every running sandbox by its full ID:

```bash
agent-compose stop <sandbox-id> [<sandbox-id>...]
```

Using a full ID does not require a compose file or project selection and also
works for legacy standalone sandboxes. The dry run below reports all sandbox
IDs whose persisted metadata is still `running`.

Dry-run only inspects the source and uses a temporary database copy. It does
not modify either data root, create the target, copy sandbox workspaces, or
create an in-place backup.

Immediately after the last `stop` command, stop the old daemon and keep it
stopped. A scheduler may create another sandbox in the short interval before
daemon shutdown, so the post-shutdown dry run is the authority: if it reports
any running IDs, restart the old daemon, stop every reported ID, stop the
daemon again, and repeat the dry run. Do not use `docker stop` for sandboxes;
the old daemon must persist their stopped state.

### Run a published binary

Download `SHASUMS256.txt` and the matching manually published Linux release
asset:

- `agent-compose-v2-storage-migrator-linux-amd64`
- `agent-compose-v2-storage-migrator-linux-arm64`

Verify it, make it executable, and select it. For example, on amd64:

```bash
grep '  agent-compose-v2-storage-migrator-linux-amd64$' SHASUMS256.txt | sha256sum -c -
chmod +x agent-compose-v2-storage-migrator-linux-amd64
MIGRATOR=./agent-compose-v2-storage-migrator-linux-amd64
```

### Perform the migration

Set `DATA_ROOT` to the deployment's data directory. Set `RUNTIME_ROOT` to the
path that the new daemon will see (`/data` for the standard container
deployment, or the same path as `DATA_ROOT` for a native daemon). You may run
the read-only dry run while the old daemon is still available to discover all
running sandbox IDs, then stop those IDs with the old CLI as described above.
After stopping the old daemon, run the same dry run again before migration:

```bash
DATA_ROOT=/opt/agent-compose/data
RUNTIME_ROOT=/data
"$MIGRATOR" --source "$DATA_ROOT" --target "$DATA_ROOT" \
  --runtime-root "$RUNTIME_ROOT" --dry-run
```

The post-shutdown dry run must succeed. Back up the complete data root, then
run the migration without `--dry-run`:

```bash
"$MIGRATOR" --source "$DATA_ROOT" --target "$DATA_ROOT" \
  --runtime-root "$RUNTIME_ROOT" --json
```

Keep the old daemon stopped until migration and validation finish. When a
separate rollback copy is required, pass different `--source` and `--target`
directories and set `--runtime-root` to the target path visible to the new
daemon.

## Contributing

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

agent-compose is licensed under the [GNU Affero General Public License v3.0](LICENSE.txt).

## Community and Support

Join the community to discuss agent-compose usage, deployment, and development with other developers.

<table>
  <tr>
    <td align="center"><img src="https://github.com/user-attachments/assets/fcdbb42b-2e06-409e-b116-60544461fbc1" width="160" /><br/>WeChat Group</td>
  </tr>
</table>

<p align="center">
  <img src="agent-composelogo.png" alt="Agent-compose" width="240">
  <div align="center">
  <a href="https://github.com/chaitin/agent-compose/actions/workflows/ci.yml">
    <img src="https://github.com/chaitin/agent-compose/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI">
  </a>
  <a href="https://github.com/chaitin/agent-compose/actions/workflows/images.yml">
    <img src="https://github.com/chaitin/agent-compose/actions/workflows/images.yml/badge.svg?branch=main" alt="Images & Release">
  </a>
  <a href="LICENSE.txt">
    <img src="https://img.shields.io/badge/License-AGPL%20v3-blue.svg" alt="License: AGPL v3">
  </a>
</div>
</p>

**agent-compose is a daemon + CLI control plane that runs AI coding agents in isolated sandboxes.** You describe your agents in an `agent-compose.yml` file, and a long-lived daemon builds, runs, schedules, and proxies an isolated runtime for each one.

> Public preview. APIs, runtime packaging, and deployment defaults may still change. It is suitable for experimentation, local development, and preview deployments — not yet a stable production platform.

📖 中文文档：[docs/zh-CN/README.md](docs/zh-CN/README.md)

## What is agent-compose?

If you know Docker Compose, the mental model is familiar: instead of declaring
containers, you declare **agents**. Each agent picks a provider CLI — `codex`,
`claude` (Claude Code), `gemini`, or `opencode` — and the daemon gives it its own
isolated sandbox with a workspace, then runs it on a prompt, a shell command, a
schedule, or an event. Provider API keys stay on the daemon and are never exposed
inside the guest.

You manage the whole lifecycle with a Compose-style CLI (`up`, `run`, `ps`,
`logs`, `down`), and everything is driven by one declarative file.

Concretely, agent-compose provides:

- A **declarative compose model** (`agent-compose.yml`) with `${ENV}` interpolation.
- **Multi-provider guest agents**: Codex, Claude Code, Gemini, and OpenCode CLIs.
- **Three runtime drivers**: `docker` (default), `boxlite` (microVM), and `microsandbox`.
- A **scheduler** with `cron`, `interval`, `timeout`, and `event` triggers — or full inline JavaScript scheduler scripts.
- **Event triggers and webhooks** for event-driven agent runs.
- **Workspaces** provisioned from a local directory or a Git repository.
- A **Runtime LLM Facade** that brokers LLM credentials so provider keys never enter guest containers.
- **MCP servers, reusable skills, and named volumes** per agent.
- A **Jupyter proxy** for notebook-style guest runtimes.
- **v2 Connect APIs**; the separate web UI repository tracks generated TypeScript clients.

## How it works

The **daemon** is the single source of truth: it owns persistence, scheduler
execution, runtime lifecycle, the Connect/HTTP APIs, and Jupyter proxying. The
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
curl -fsSL https://github.com/chaitin/agent-compose/releases/latest/download/install.sh | bash
```

The base stack starts the daemon. The frontend is defined under the `with-ui`
profile; enable it from the installation directory printed by the installer:

```bash
cd <directory printed by the installer>
docker compose --profile with-ui up -d
```

On first run the installer generates an `admin` password and prints it once;
use it at the printed URL after enabling the UI profile. See
[deploy/README.md](deploy/README.md) for options such as `--dir`, `--port`,
`--upgrade`, and pulling from a mirror/private registry.

### Option B — Build from source (for the CLI workflow)

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

With a local daemon running (Option B), create an `agent-compose.yml`:

```yaml
name: demo

agents:
  reviewer:
    provider: codex
    image: ghcr.io/chaitin/agent-compose-guest:latest
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

**Top-level fields:** `name`, `variables`, `agents`, `mcp_servers`, `volumes`, `network`.

**Common agent fields:** `provider`, `model`, `system_prompt`, `image`,
`driver`, `env` (scalars or `{ value, secret }`), `workspace`, `scheduler`,
`mcp_servers`, `skills`, and `volumes`.

Provision an agent's workspace from a local path (`provider: local`) or a Git
repository (`provider: git`):

```yaml
agents:
  reviewer:
    workspace:
      provider: git
      url: https://github.com/example/repo.git
      branch: main
```

Scheduler scripts may be inline JavaScript or an explicit `{ url: ... }`
source using a local path, `file://`, `http://`, or `https://`. `config` and
`up` fetch URL sources locally and send an inline snapshot to the daemon. Use
either `scheduler.script` or `scheduler.triggers` in one scheduler.

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

See the [command line manual](docs/command-line-manual.md) for the full field reference.

## CLI overview

| Command | Purpose |
| --- | --- |
| `agent-compose daemon` | Start the HTTP/Connect daemon. |
| `agent-compose up` | Read `agent-compose.yml` and apply the project. |
| `agent-compose run <agent> --prompt/--command` | Run a prompt or shell command as an agent. |
| `agent-compose exec <sandbox>` | Execute a command or prompt in a running sandbox. |
| `agent-compose ps` / `stats` | List project sandboxes / show sandbox resource stats. |
| `agent-compose logs` | Print project run logs; a project, agent, run, or sandbox ID can be passed without its resource type. |
| `agent-compose scheduler ls\|trigger\|inspect` | List, run, or inspect scheduler triggers. |
| `agent-compose sandbox ls\|stop\|resume\|rm\|prune` | Manage project sandboxes. |
| `agent-compose images\|pull\|build\|rmi\|inspect` | Manage daemon images and build agent images. |
| `agent-compose volume ls\|create\|inspect\|rm\|prune` | Manage daemon volumes. |
| `agent-compose cache ls\|inspect\|prune\|rm` | Inspect and clean daemon runtime caches. |
| `agent-compose down` | Disable managed schedulers and stop sandboxes. |
| `agent-compose status` | Check daemon status. |

Useful global flags: `--file, -f` (choose a compose file), `--project-name`,
`--json` (stable JSON for scripts), `--host` / `AGENT_COMPOSE_HOST` (connect to a
TCP daemon), and `AGENT_COMPOSE_SOCKET` (Unix socket path). Full reference:
[docs/command-line-manual.md](docs/command-line-manual.md).

## Runtime drivers

- **`docker`** (default): runs guests in Docker containers; requires a working Docker daemon.
- **`boxlite`**: runs guests as microVMs using BoxLite runtime artifacts.
- **`microsandbox`**: runs guests using the Microsandbox VM runtime.

The three names describe product-supported drivers; a particular artifact may
compile a subset:

| Artifact | Compiled drivers |
| --- | --- |
| macOS native binary | `docker` |
| Linux native binary | `docker`, `boxlite`, `microsandbox` |
| Published Linux daemon image (`amd64` and `arm64`) | `docker`, `boxlite`, `microsandbox` |

Inspect an artifact with `agent-compose --json version` or `/api/version`.
The `compiled_drivers` field reports build capability only—it does not probe the
Docker daemon, KVM, or native runtime artifacts, and it is not a runtime health
or availability list. The full Linux image still defaults to `docker` and works
in Docker mode on macOS Docker Desktop; KVM is needed only when actually using
BoxLite or Microsandbox.

Image handling is selected by `IMAGE_STORE_MODE` (`auto` / `docker` / `oci`,
where `oci` uses a daemonless image cache). New sandboxes use the image set by
`DEFAULT_IMAGE`; the bundled `.env.example` and installer set this to
`ghcr.io/chaitin/agent-compose-guest:latest`, which ships the agent runtime and
provider CLIs.

## Agent providers

Each agent sets a `provider`, which selects the CLI it runs inside the sandbox:

| Provider | Runs |
| --- | --- |
| `codex` | Codex CLI |
| `claude` | Claude Code CLI |
| `gemini` | Gemini CLI |
| `opencode` | OpenCode CLI |

You configure LLM credentials once, on the daemon (in `.env`) — not per guest.
For Codex, Claude, and OpenCode, the daemon's **Runtime LLM Facade** hands each
sandbox a scoped token instead of your real API key, so provider keys never enter
the guest. When that token pins an upstream provider, the model in each runtime
request is forwarded to that provider and does not need to be listed in
agent-compose first; an unsupported model returns the upstream provider's error.
Compatibility tokens without a provider keep the existing configured
model/provider resolution behavior.

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

**Per-provider notes.** OpenCode picks its upstream family from the agent's
`model` (`provider/model`, e.g. `anthropic/…` or `openai/…`) and gets a facade
token for it; only OpenCode's own native provider uses OpenCode's login instead.
**Gemini is the exception** — it is never handed an LLM key (`GEMINI_API_KEY` /
`GOOGLE_API_KEY` are filtered out of the guest) and authenticates through the
Gemini CLI's own login, persisted under the sandbox home (`~/.gemini`).

See [`.env.example`](.env.example) for the full list (timeouts, endpoint aliases,
`OPENAI_API_KEY` / `ANTHROPIC_AUTH_TOKEN`) and the
[Runtime LLM Facade design](docs/zh-CN/design/agent-compose-runtime-llm-facade.md)
for how brokering works.

## Deployment & configuration

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

The installer checks for `/dev/kvm` on a new installation and persists either
the base file or the base-plus-KVM file set as `COMPOSE_FILE` in `.env`. This is
deployment selection, not proof that KVM or either native runtime is healthy.
See [deploy/README.md](deploy/README.md) for manual selection and upgrade
behavior.

**[`.env.example`](.env.example) is the authoritative, fully commented
configuration reference.** At minimum, review these before exposing a deployment:

- `AUTH_PASSWORD`, `AUTH_SECRET` — UI server login secrets (replace the examples).
- `AGENT_COMPOSE_HTTP_PORT` — host port for the web UI / reverse proxy (`with-ui`).
- `AGENT_COMPOSE_RUNTIME_BASE_URL` — guest-reachable daemon URL used for the LLM facade.
- `RUNTIME_DRIVER` — default runtime driver.

## Web UI

The web UI lives in a separate repository,
[agent-compose-ui](https://github.com/chaitin/agent-compose-ui). It directly tracks
the generated `agentcompose/v2` and `health/v1` TypeScript clients built from this
repository's `proto/`; the generated files are reviewed together with protocol
changes. The daemon does not host the UI or browser
login flows; the UI image runs nginx in front of a Go UI server that owns
auth/OAuth and proxies API and Jupyter routes to the daemon.

## Security

The default configuration targets local development. Harden it before exposing a
deployment to a network:

- Expose browser access through the agent-compose-ui server, not the daemon directly.
- Set a stable, high-entropy `AUTH_SECRET`, and terminate HTTPS in production.
- Keep the daemon TCP API (`HTTP_LISTEN`) behind container networking, a reverse proxy, or a VPN.
- Do not expose guest Jupyter ports directly — reach them through the agent-compose proxy.
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

Native binaries are retained only for local and CI verification. GitHub Release
continues to ship installer assets; the supported deployment payload is the
published multi-architecture image plus that installer, not standalone macOS or
Linux binaries. The JavaScript runtime components live under `runtime/`.

## Documentation

- [Documentation index](docs/README.md)
- [Command line manual](docs/command-line-manual.md)
- [Chinese documentation (中文文档)](docs/zh-CN/README.md)

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

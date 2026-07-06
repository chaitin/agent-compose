# agent-compose

[![CI](https://github.com/chaitin/agent-compose/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/chaitin/agent-compose/actions/workflows/ci.yml)
[![Images & Release](https://github.com/chaitin/agent-compose/actions/workflows/images.yml/badge.svg?branch=main)](https://github.com/chaitin/agent-compose/actions/workflows/images.yml)

agent-compose is an experimental control plane for running isolated agent
sessions. It provides a daemon, CLI, Connect APIs, runtime drivers, workspace
provisioning, scheduler automation, event history, and a Jupyter proxy for
notebook-style guest runtimes.

agent-compose is a public preview project: APIs, runtime packaging, deployment
defaults, and operational guidance may still change.

Chinese documentation is available at [docs/zh-CN/README.md](docs/zh-CN/README.md).

## What It Does

- Runs a long-lived daemon that owns state, scheduler execution, runtime
  lifecycle, Connect APIs, and Jupyter proxying.
- Provides a CLI for `up`, `run`, `logs`, `ps`, `down`, and image operations.
- Supports project definitions in `agent-compose.yml`.
- Starts isolated guest runtimes with Docker, BoxLite, or Microsandbox.
- Provisions workspaces from local directories or Git repositories.
- Exposes v1 session-oriented APIs and v2 project/run/image APIs.
- Includes JavaScript runtime components under `runtime/`.

The web UI lives in a separate repository,
[agent-compose-ui](https://github.com/chaitin/agent-compose-ui).

## Maturity

agent-compose is currently suitable for experimentation, local development, and
preview deployments. It is not yet a stable production platform.

Before using it with untrusted workloads, review the runtime driver behavior,
network access, authentication settings, workspace upload limits, and Jupyter
proxy assumptions.

## Repository Layout

```text
cmd/agent-compose/             daemon and CLI entrypoint
pkg/agentcompose/app/          service graph, route registration, background managers
pkg/agentcompose/api/          Connect handlers and API/protobuf conversion helpers
pkg/agentcompose/adapters/     daemon runtime/session/loader/capability adapters
pkg/agentcompose/proxy/        Jupyter, workspace, and runtime LLM HTTP proxy routes
pkg/model/                     domain records, validation, stable IDs, JSON helpers
pkg/storage/                   session and config persistence helpers
pkg/loaders/                   loader engine, scheduling, command, and payload helpers
pkg/projects/                  project normalization and managed-resource builders
pkg/runs/                      project run coordinator and run/session helpers
pkg/sessions/                  session stream broker and runtime state helpers
pkg/execution/                 cell, agent execution, artifact, and driver conversion helpers
pkg/llms/                      daemon LLM client and runtime facade helpers
pkg/events/                    event/webhook helpers
pkg/images/                    daemon image store service helpers
pkg/driver/                    Docker, BoxLite, and Microsandbox runtime drivers
pkg/config/                    environment configuration
pkg/imagecache/                OCI image cache helpers
proto/                         Connect API definitions and generated Go code
proto-client/                  npm package config for the generated TypeScript client
runtime/                       guest runtime SDKs and JavaScript scheduler runtime
guest-images/                  guest image Dockerfiles
loader-script/                 scheduler script examples and API notes
docs/design/                   design notes
```

## Requirements

- Go toolchain compatible with the version declared in `go.mod`
- Node.js and npm
- Task, for the documented `task ...` commands
- Docker, when using Docker runtime or building Docker images
- Runtime-specific dependencies for BoxLite or Microsandbox when using those
  drivers directly

## Quick Start

Build the CLI and daemon:

```bash
task build
```

Start the daemon:

```bash
agent-compose daemon
```

By default, the daemon listens on a local Unix socket. To expose an HTTP endpoint
for local development:

```bash
HTTP_LISTEN=127.0.0.1:7410 agent-compose daemon
```

Check daemon status:

```bash
agent-compose status
agent-compose --host http://127.0.0.1:7410 status
```

Create an `agent-compose.yml`:

```yaml
name: demo
agents:
  reviewer:
    provider: codex
    model: gpt-test
    image: debian:bookworm-slim
    scheduler:
      triggers:
        - name: hourly
          cron: "0 * * * *"
          prompt: "Review the current workspace state."
```

Apply and run it:

```bash
agent-compose up
agent-compose ps
agent-compose run reviewer --prompt "Review this change"
agent-compose logs --agent reviewer
agent-compose down
```

## CLI

The main commands are:

- `agent-compose daemon`: start the HTTP/Connect daemon.
- `agent-compose up`: read `agent-compose.yml` and apply the project to the daemon.
- `agent-compose run <agent> <trigger-name>`: run a configured trigger by name.
- `agent-compose run <agent> --prompt "..."` / `--command "..."`: run ad hoc prompt or shell command work.
- `agent-compose logs`: inspect project run logs.
- `agent-compose ps`: list project agents, recent runs, and active sessions.
- `agent-compose down`: disable managed schedulers and stop running sessions.
- `agent-compose images`, `pull`, `rmi`, `image inspect`: manage daemon-side images.

Useful flags and environment variables:

- `--file, -f`: choose a compose file.
- `--project-name`: override the compose project name.
- `--json`: emit stable JSON for scripts.
- `--host` or `AGENT_COMPOSE_HOST`: connect to a TCP daemon.
- `AGENT_COMPOSE_SOCKET`: choose the local Unix socket path.

## Compose File

Top-level fields:

- `name`: project name. If omitted, the compose file directory name is used.
- `variables`: project variables with `${ENV_NAME}` interpolation.
- `workspace`: default project workspace.
- `agents`: agent definitions keyed by agent name.
- `network.mode`: currently supports `default`.

Common agent fields:

- `provider`, `model`, `system_prompt`: guest agent settings (`provider` selects
  the guest CLI runner; `model` is passed to provider runtimes that support
  explicit model selection). Supported guest providers are `codex`, `claude`,
  `gemini`, and `opencode`. Daemon-side LLM calls
  (`LLMService`, `scheduler.llm`) use `LLM_MODEL` instead.
- `image`: guest image reference.
- `driver`: runtime driver override. Supported drivers are `boxlite`, `docker`,
  and `microsandbox`.
- `env`: agent environment variables. Values may be scalars or
  `{ value, secret }` objects.
- `workspace`: agent workspace override.
- `scheduler.enabled`: defaults to `true`.
- `scheduler.triggers`: supports `cron`, `interval`, `timeout`, and `event`
  triggers.
- `scheduler.script`: inline JavaScript scheduler runtime code. Use either
  `scheduler.script` or `scheduler.triggers`, not both in the same scheduler.

Workspace providers:

```yaml
workspace:
  provider: git
  url: https://github.com/example/repo.git
  branch: main

agents:
  reviewer:
    workspace:
      provider: local
      path: .
```

## Runtime Drivers

agent-compose supports three runtime drivers:

- `docker`: the default driver. It uses Docker containers and requires a
  working Docker daemon.
- `boxlite`: uses BoxLite runtime artifacts and guest images prepared by this
  repository.
- `microsandbox`: uses Microsandbox runtime artifacts.

Image handling is selected by `IMAGE_STORE_MODE`:

- `auto`: use Docker image store when Docker is available, otherwise use the OCI
  cache.
- `docker`: require Docker image store.
- `oci`: use daemonless OCI image cache.

The default guest image is `debian:bookworm-slim` unless overridden by
`DEFAULT_IMAGE`, `DOCKER_DEFAULT_IMAGE`, or `MICROSANDBOX_DEFAULT_IMAGE`.

## Frontend

The web UI lives in a separate repository,
[agent-compose-ui](https://github.com/chaitin/agent-compose-ui). It consumes the
generated API client from the published
[`@chaitin-ai/agent-compose-client`](https://www.npmjs.com/package/@chaitin-ai/agent-compose-client)
package, which is built from this repository's `proto/` via `proto-client/`.

The daemon does not host the Web UI or browser login flows. The frontend
repository builds an image (`ghcr.io/chaitin/agent-compose-ui`) that runs nginx
in front of the agent-compose-ui server: nginx serves the built UI, and the UI
server handles browser auth/OAuth before proxying API and Jupyter routes to the
daemon. The root
`docker-compose.yml` references that published image and is the default
deployment entrypoint.

For a server deployment that uses published container images:

```bash
cp .env.example .env
openssl rand -base64 24 # use this value for AUTH_PASSWORD
openssl rand -hex 32    # use this value for AUTH_SECRET
docker compose pull
docker compose up -d
# To also pull and start the web UI:
docker compose --profile with-ui pull
docker compose --profile with-ui up -d
```

Edit `.env` before the first start. At minimum, replace `AUTH_PASSWORD` and
`AUTH_SECRET`; enable the `with-ui` profile when you want the web UI, and set
`AGENT_COMPOSE_HTTP_PORT` if port `80` is not suitable. Override
`AGENT_COMPOSE_IMAGE`, `AGENT_COMPOSE_FRONTEND_IMAGE`, or `DEFAULT_IMAGE` to
pin release tags or use a mirror/private registry. The frontend is released
independently by `agent-compose-ui`; `AGENT_COMPOSE_FRONTEND_IMAGE` is used only
when the `with-ui` profile is enabled.

For local development, Docker Compose automatically loads
`docker-compose.override.yml`, which builds the backend image from the local
Dockerfile while keeping the same service topology. Use
`docker compose up -d --build` when you want to rebuild the local backend image,
or `docker compose --profile with-ui up -d --build` when you also want the web
UI.

Upgrade notes for the UI server split:

- Upgrade `agent-compose` and `agent-compose-ui` together. The daemon no longer
  serves browser auth/OAuth routes; the UI image now includes the Go UI server
  that owns those routes and proxies daemon API/Jupyter traffic.
- Move browser login settings (`AUTH_USERNAME`, `AUTH_PASSWORD`,
  `AUTH_SECRET`, `AUTH_SESSION_TTL`, `OAUTH_*`) to the UI service environment.
  Docker Compose already passes `.env` to `agent-compose-frontend` when the
  `with-ui` profile is enabled.
- Do not expose the daemon TCP API as the browser entrypoint. If a remote daemon
  HTTP endpoint must be protected independently of the UI, configure daemon-side
  `HTTP_BASIC_AUTH`; browser cookie/OAuth settings are not consumed by the
  daemon anymore.
- After changing runtime-reachable URLs or capability proxy settings such as
  `AGENT_COMPOSE_RUNTIME_BASE_URL`, `CAP_GRPC_LISTEN`, or `CAP_GRPC_TARGET`,
  restart the daemon and create new agent sessions so guest containers receive
  the updated facade and capability environment.

## Configuration

Copy `.env.example` to `.env`, edit the values for your environment, then run
`docker compose up -d`. Add `--profile with-ui` to also start the web UI.

Important variables include:

- `AUTH_USERNAME`, `AUTH_PASSWORD`, `AUTH_SECRET`, `AUTH_SESSION_TTL`: UI server
  password login settings. Replace the example password and secret before
  exposing a deployment.
- `AGENT_COMPOSE_HTTP_PORT`: host port for the web UI and reverse proxy when
  the `with-ui` profile is enabled.
- `AGENT_COMPOSE_IMAGE`, `AGENT_COMPOSE_FRONTEND_IMAGE`: Docker Compose service
  images; the frontend image is used only with the `with-ui` profile.
- `DEFAULT_IMAGE`, `DOCKER_DEFAULT_IMAGE`, `MICROSANDBOX_DEFAULT_IMAGE`: guest
  image defaults.
- `RUNTIME_DRIVER`: default runtime driver.
- `OAUTH_*`: UI server OAuth login settings.
- `LLM_API_ENDPOINT`, `LLM_API_PROTOCOL`, `LLM_API_KEY`, `OPENAI_API_KEY`,
  `LLM_MODEL`, `LLM_TIMEOUT`: daemon-side OpenAI-family LLM settings for
  `LLMService`, `scheduler.llm`, and runtime agent LLM facade bootstrap. These
  values are not injected as provider keys into guest agent runtimes. Set
  `LLM_API_PROTOCOL=chat_completions` for OpenAI-compatible chat completions
  backends (aliases: `chat`, `chat_completion`).
- `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_ENDPOINT`, `ANTHROPIC_API_KEY`,
  `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_MODEL`, `CLAUDE_MODEL`: daemon-side
  Anthropic-family LLM facade bootstrap settings.
- `AGENT_COMPOSE_RUNTIME_BASE_URL`: optional guest-reachable daemon base URL
  used when generating runtime LLM facade configuration. Docker Compose
  defaults this to `http://agent-compose:7410`; host-based Docker setups should
  set it to a concrete host IP/name and port.
- `DOCKER_HOST_SESSION_ROOT`: host path for session data bind-mounted into guest
  containers. Docker Compose defaults this to `./data/agent-compose/sessions`.
- `CAP_GRPC_LISTEN`, `CAP_GRPC_TARGET`: required only when agents need to call
  OctoBus gRPC capabilities. `CAP_GRPC_LISTEN` starts the agent-compose
  capability proxy; `CAP_GRPC_TARGET` is the guest-reachable address injected
  into new sessions. After changing either value, restart the daemon and create
  a new session.
- `IMAGE_STORE_MODE`, `IMAGE_CACHE_ROOT`, `IMAGE_REGISTRY`,
  `IMAGE_INSECURE_REGISTRIES`: image store and OCI cache settings.
- `BOXLITE_HOME`, `BOXLITE_RUNTIME_DIR`, `BOX_ROOTFS_PATH`, `BOX_CACHE_TTL`:
  BoxLite settings.
- `BOX_DISK_SIZE_GB`: shared guest disk size for VM-type drivers (the boxlite
  box disk and the microsandbox docker disk). Default 6 GiB.
- `DOCKER_HOME`: Docker runtime state directory.
- `MICROSANDBOX_HOME`, `MICROSANDBOX_MSB_PATH`, `MICROSANDBOX_LIB_PATH`,
  `MICROSANDBOX_INSECURE_REGISTRIES`: Microsandbox settings.
- `GUEST_WORKSPACE`, `GUEST_STATE_ROOT`, `GUEST_RUNTIME_ROOT`,
  `GUEST_LOG_ROOT`, `JUPYTER_GUEST_PORT`: guest paths and Jupyter port.
- `WEBHOOK_BODY_LIMIT_BYTES`, `WORKSPACE_UPLOAD_LIMIT_BYTES`: request limits.

### Agent providers

Guest agent sessions run provider CLIs through the `agent-compose-runtime` CLI,
provided by the `@chaitin-ai/agent-compose-runtime` npm package. Codex and Claude calls use the Runtime LLM Facade:
provider keys stay in the daemon-side LLM provider configuration, while guest
runtimes receive session-scoped facade tokens and facade base URLs. LLM provider
key names such as `LLM_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`,
`ANTHROPIC_AUTH_TOKEN`, `GOOGLE_API_KEY`, and `GEMINI_API_KEY` are filtered from
user-supplied runtime environment variables. Compatibility aliases such as
`LLM_API_KEY` and `LLM_API_ENDPOINT` may still appear in the runtime, but they
are daemon-managed facade values, not upstream provider credentials. Gemini and
OpenCode still use their provider CLIs directly; OpenCode credentials depend on
the selected OpenCode model provider.

| Provider | Typical env vars | Notes |
| --- | --- | --- |
| `codex` | daemon LLM provider config; runtime receives `AGENT_COMPOSE_SESSION_TOKEN`, `LLM_API_KEY`, `LLM_API_ENDPOINT`, `OPENAI_BASE_URL`, and facade-token API key aliases | Uses Codex CLI/SDK in the guest image |
| `claude` | daemon Anthropic-family provider config; runtime receives `AGENT_COMPOSE_SESSION_TOKEN`, `LLM_API_KEY`, `LLM_API_ENDPOINT`, `ANTHROPIC_BASE_URL`, and facade-token API key aliases | Uses Claude Code CLI in the guest image |
| `gemini` | not yet routed through the LLM facade | Uses Gemini CLI in the guest image |
| `opencode` | Provider-specific keys for the selected OpenCode model, for example `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` | Uses OpenCode CLI in the guest image |

After changing guest runtime code or provider support, rebuild the guest image:

```bash
task image:agent-compose-guest
```

Create a new session (or resume one) so the updated image and environment
variables are picked up.

> **Upgrade note (breaking for some Docker setups):** Because provider keys are
> no longer passed through to guest runtimes, Codex/Claude now reach their LLM
> upstream through the daemon facade and need a guest-reachable daemon URL. The
> bundled `docker-compose.yml` sets
> `AGENT_COMPOSE_RUNTIME_BASE_URL=http://agent-compose:7410` for you. If you run
> the daemon directly on a host with the Docker driver and an
> `HTTP_LISTEN=127.0.0.1:...` bind, the container cannot reach that loopback
> address, so facade config is skipped and agent runs will have no working LLM
> credentials. Set `AGENT_COMPOSE_RUNTIME_BASE_URL` to a concrete
> host-reachable IP/name and port (for example `http://host.docker.internal:7410`).

The Runtime LLM Facade design is documented in
[`docs/zh-CN/design/agent-compose-runtime-llm-facade.md`](docs/zh-CN/design/agent-compose-runtime-llm-facade.md).

### Chat Completions LLM Protocol

Set `LLM_API_PROTOCOL=chat_completions` to use an OpenAI-compatible Chat
Completions backend for daemon-side unary text generation. This path is used by
`LLMService.Generate` and loader `scheduler.llm` calls.

```env
LLM_API_PROTOCOL=chat_completions
LLM_API_ENDPOINT=https://api.example.com
LLM_API_KEY=...
LLM_MODEL=your-model
```

Compatible backends include DeepSeek, local OpenAI-compatible proxies
(vLLM/Ollama), and similar Chat Completions endpoints.

This does not create a workspace-capable agent session and does not grant file,
command, or MCP tool access.

With `outputSchema`, `chat_completions` uses prompt guidance and
`response_format: json_object` (not Responses API strict JSON Schema).

## Security Notes

The default configuration is designed for local development. Review and harden
settings before exposing the deployment to a network.

- Expose browser access through the agent-compose-ui server, not directly
  through the daemon.
- Set a stable, high-entropy `AUTH_SECRET` when enabling UI server
  authentication.
- Use HTTPS termination in production deployments.
- `HTTP_LISTEN=0.0.0.0:7410` is an internal daemon API. Without daemon-side
  `HTTP_BASIC_AUTH`, startup emits a warning but still proceeds; keep it behind
  container networking, reverse proxies, VPNs, or equivalent controls.
- Jupyter runs inside guest runtimes and is expected to be reached through the
  agent-compose proxy. Do not expose guest Jupyter ports directly.
- Runtime drivers may allow network access from guest workloads. Check driver
  behavior before running untrusted code.
- Treat Git credentials, uploaded workspaces, environment variables, and LLM API
  keys as secrets.

See [SECURITY.md](SECURITY.md) for vulnerability reporting and hardening notes.

## Build And Test

```bash
task lint
task build
task test
```

Useful subcommands:

```bash
task test:unit
task test:integration
task test:e2e
task image:agent-compose-guest
task image:agent-compose
```

Runtime SDK:

```bash
cd runtime/agent-compose-runtime-sdk
npm ci
npm test
```

BoxLite-enabled binary builds are optional and require BoxLite runtime artifacts:

```bash
task build:agent-compose:boxlite
```

Scheduler runtime:

```bash
cd runtime/javascript
npm ci
npm run test:unit
```

## API Compatibility

The daemon exposes both v1 and v2 Connect APIs.

- v1 is session-oriented and remains available for existing UI and clients.
- v2 is the preferred path for newer CLI and project/run/image workflows.

Protocol definitions live under `proto/`.

## Related Documentation

- [Documentation index](docs/README.md)
- [Chinese documentation](docs/zh-CN/README.md)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

agent-compose is licensed under the [GNU Affero General Public License v3.0](LICENSE.txt).

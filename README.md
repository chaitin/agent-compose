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
pkg/agentcompose/              daemon service graph, route registration, handler forwarding
pkg/sessions/                  session lifecycle, stream broker, RPC bridge, Jupyter proxy support
pkg/projects/                  project apply/down, run lifecycle, exec target resolution
pkg/loaders/                   loader service, scheduler engine, webhook queue consumers
pkg/agents/                    agent definitions, agent sessions, execution config helpers
pkg/llm/                       daemon LLM client/service and runtime LLM facade
pkg/events/                    webhook/event HTTP handlers and event dispatcher
pkg/images/                    Docker/OCI/auto image backends and image service
pkg/storage/                   SQLite stores, schemas, migrations, persisted records
pkg/driver/                    Docker, BoxLite, and Microsandbox runtime drivers
pkg/auth/                      authentication middleware and login flows
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
- `agent-compose run <agent>`: start a project agent run.
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

The daemon does not host the Web UI. The frontend repository builds an nginx
image (`ghcr.io/chaitin/agent-compose-ui`) that serves the built UI and
reverse-proxies API and Jupyter routes to the daemon. The `docker-compose.yml`
and `docker-compose.deploy.yml` here reference that published image.

For a deployment that uses published container images:

```bash
export AUTH_PASSWORD="$(openssl rand -base64 24)"
export AUTH_SECRET="$(openssl rand -hex 32)"
docker compose -f docker-compose.deploy.yml pull
docker compose -f docker-compose.deploy.yml up -d
```

Override `AGENT_COMPOSE_IMAGE`, `AGENT_COMPOSE_FRONTEND_IMAGE`, or
`DEFAULT_IMAGE` to pin release tags or use locally built images. The frontend is
released independently by `agent-compose-ui`; installer manifests declare
`AGENT_COMPOSE_FRONTEND_VERSION`, which defaults to `latest` unless explicitly
pinned. The deployment compose serves the frontend on port `80` by default; set
`AGENT_COMPOSE_HTTP_PORT` to use another host port. The default
`docker-compose.yml` remains oriented toward local development builds. Set
`AUTH_USERNAME` and `AUTH_PASSWORD` before exposing the deployment to a network.

## Configuration

Copy `.env.example` to `.env` for local experiments.

Important variables include:

- `DATA_ROOT`: daemon data root. Session data lives under `<DATA_ROOT>/sessions`.
- `HTTP_LISTEN`: optional TCP listen address. Keep it on loopback for local
  unauthenticated development.
- `AGENT_COMPOSE_SOCKET`, `AGENT_COMPOSE_HOST`: daemon connection settings.
- `AUTH_USERNAME`, `AUTH_PASSWORD`, `AUTH_SECRET`, `AUTH_SESSION_TTL`: password
  login settings.
- `OAUTH_*`: OAuth login settings.
- `HTTP_BASIC_AUTH`: base64-encoded `username:password` for additional HTTP
  Basic authentication.
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
- `CAP_GRPC_LISTEN`, `CAP_GRPC_TARGET`: required only when agents need to call
  OctoBus gRPC capabilities. `CAP_GRPC_LISTEN` starts the agent-compose
  capability proxy; `CAP_GRPC_TARGET` is the guest-reachable address injected
  into new sessions. After changing either value, restart the daemon and create
  a new session.
- `RUNTIME_DRIVER`: default runtime driver.
- `DEFAULT_IMAGE`, `DOCKER_DEFAULT_IMAGE`, `MICROSANDBOX_DEFAULT_IMAGE`: guest
  image defaults.
- `AGENT_COMPOSE_IMAGE`, `AGENT_COMPOSE_FRONTEND_IMAGE`: Docker Compose service
  images.
- `IMAGE_STORE_MODE`, `IMAGE_CACHE_ROOT`, `IMAGE_REGISTRY`,
  `IMAGE_INSECURE_REGISTRIES`: image store and OCI cache settings.
- `BOXLITE_HOME`, `BOXLITE_RUNTIME_DIR`, `BOX_ROOTFS_PATH`, `BOX_DISK_SIZE_GB`,
  `BOX_CACHE_TTL`: BoxLite settings.
- `DOCKER_HOME`, `DOCKER_HOST_SESSION_ROOT`: Docker runtime settings.
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
> bundled `docker-compose.yml` / `docker-compose.deploy.yml` set
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
settings before exposing the daemon to a network.

- Do not expose an unauthenticated daemon on a non-loopback address.
- Set a stable, high-entropy `AUTH_SECRET` when enabling authentication.
- Use HTTPS termination in production deployments.
- `HTTP_LISTEN=0.0.0.0:7410` is only appropriate behind authentication and
  network controls.
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

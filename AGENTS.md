# AGENTS

## Overview

This repo contains the agent-compose session control plane. It creates, resumes, stops, and proxies isolated notebook runtimes, and exposes agent, loader, LLM, configuration, and workspace APIs.

Main entrypoints:
- `cmd/agent-compose/main.go`: starts the HTTP/Connect service, registers agent-compose routes, and handles graceful shutdown.
- `pkg/agentcompose/app/`: service graph setup, route registration, and background manager startup.
- `pkg/agentcompose/api/`: Connect handlers and protobuf/domain mapping.
- `pkg/agentcompose/adapters/`: daemon-only runtime, session, loader, capability, and LLM adapters.
- `pkg/agentcompose/proxy/`: HTTP proxy routes for Jupyter, workspaces, and runtime LLM facade.
- Owner packages under `pkg/` contain shared domain code: `model`, `storage`, `loaders`, `projects`, `runs`, `sessions`, `execution`, `llms`, `events`, `images`, `dashboard`, and `capabilities`. Event bus code lives in `loaders`; small dependency-free storage helpers (e.g. stored-time decoding) live in `pkg/storage/storeutil`.
- `proto/agentcompose/v1/`: agent-compose Connect API definitions and generated Go code.
- `proto/agentcompose/v2/`: agent-compose v2 Connect API definitions and generated Go code.
- `proto/health/v1/`: health Connect API definitions and generated Go code.
- `proto-client/`: npm package config that publishes the generated TypeScript client (`@chaitin-ai/agent-compose-client`). The web UI lives in the separate `agent-compose-ui` repository and consumes this package.

## Runtime Layout

`cmd/agent-compose/main.go` currently:
- creates a root signal context
- loads `.env`
- initializes Echo and structured logging
- serves `/api/version`
- registers API, Connect, and Jupyter proxy routes
- optionally enables global BasicAuth from base64-decoded `HTTP_BASIC_AUTH`
- calls `agentcompose.Setup(di)` to register Connect handlers and background managers
- gracefully shuts down Echo on process exit

`pkg/agentcompose/app.Setup(di)` owns the agent-compose service graph and background runtime components.

## Core Services

The active Connect services are:
- `SessionService`
- `KernelService`
- `AgentService`
- `LLMService`
- `ConfigService`
- `LoaderService`

Jupyter proxying is handled by HTTP routes in `pkg/agentcompose/proxy` under `/agent-compose/session/<session_id>`.

## Runtime Drivers

Supported runtime drivers:

- `docker`
- `boxlite`
- `microsandbox`

The default runtime driver is `docker`.

Important defaults:
- `DATA_ROOT`: `./data/`
- `SESSION_ROOT`: `<data-root>/sessions`
- `HTTP_LISTEN`: `127.0.0.1:7410`
- `DEFAULT_IMAGE`: `debian:bookworm-slim`
- `JUPYTER_PROXY_BASE`: `/jupyter`

Daemon LLM client (`LLMService`, `scheduler.llm`, SDK `runtime.llm`):
- `LLM_API_ENDPOINT`, `LLM_API_KEY`, `OPENAI_API_KEY`, `LLM_MODEL`, `LLM_TIMEOUT`
- `LLM_API_PROTOCOL`: `responses` (default) or `chat_completions` for OpenAI-compatible backends (aliases: `chat`, `chat_completion`)
- `chat_completions` structured output uses prompt guidance + `json_object`; use `responses` for strict JSON Schema enforcement
- Runtime LLM Facade bootstrap also supports `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_ENDPOINT`, `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_MODEL`, `CLAUDE_MODEL`, and optional guest-reachable `AGENT_COMPOSE_RUNTIME_BASE_URL`
- Guest agent providers remain `codex`, `claude`, and `gemini` CLI runners in guest containers

## Persistence

Session metadata, notebook cells, event history, runtime state, and proxy state are stored under `SESSION_ROOT`. Session file storage helpers live in `pkg/storage/sessionstore`.

Global environment variables, workspace configs, loader definitions, loader triggers, loader runs, and loader events are stored in `DATA_ROOT/data.db`. SQLite config-store helpers live in `pkg/storage/configstore`.

## Docker Deployment

Current Docker build behavior:
- `Dockerfile` builds the `cmd/agent-compose` binary
- `guest-images/Dockerfile.agent-compose-guest` builds the guest image used by BoxLite
- `build_docker.sh` defaults to `IMAGE_NAME=agent-compose:latest` and `DOCKERFILE=Dockerfile`

Current compose behavior:
- `docker-compose.yml` deploys the `agent-compose` service and the published `agent-compose-frontend` nginx image
- the agent-compose service listens on `7410`
- data is mounted from `./data/agent-compose`
- the user-created `.env` is mounted read-only at `/app/.env` for daemon configuration
- the Docker socket and `/dev/kvm` are exposed for runtime support

Compose and environment variable conventions:
- Keep `docker-compose.yml` deployable on its own with published images. A remote deployment should only need `docker-compose.yml` plus a user-created `.env`.
- Use `docker-compose.override.yml` for local development behavior such as `build:`, locally built image tags, local-only build args, or other settings that should not affect remote deployments.
- Do not add new application defaults directly to `docker-compose.yml` when the daemon image or application config can provide the default. Prefer image `ENV` defaults or application defaults, and document them as comments in `.env.example` when they help operators.
- Expose only deployment-specific knobs in `.env.example`, grouped by purpose. Use commented examples for optional or advanced settings.
- When adding or changing environment variables, decide whether they are deploy-time, image-default, application-default, or local-development-only before editing compose files.
- Keep secrets and required deployment credentials in `.env.example` empty unless a safe example value exists, and document that operators must set them before exposing a deployment.

## Quality Gates

Testing standards and coverage requirements are defined in `TESTING.md`.

Task runner:
- `Taskfile.yml`

Primary commands:
```bash
task lint
task build
task test
```

The lint scope is project code only:
```bash
golangci-lint fmt --diff ./cmd/... ./pkg/... ./proto/health/v1 ./proto/health/v1/healthv1connect ./proto/agentcompose/v1 ./proto/agentcompose/v1/agentcomposev1connect ./proto/agentcompose/v2 ./proto/agentcompose/v2/agentcomposev2connect
golangci-lint run --allow-parallel-runners ./cmd/... ./pkg/... ./proto/health/v1 ./proto/health/v1/healthv1connect ./proto/agentcompose/v1 ./proto/agentcompose/v1/agentcomposev1connect ./proto/agentcompose/v2 ./proto/agentcompose/v2/agentcomposev2connect
```

## Operational Commands

Build everything:
```bash
task build
```

Run the service locally:
```bash
go run ./cmd/agent-compose daemon
```

Build images:
```bash
task image:agent-compose-guest
task image:agent-compose
```

Run compose:
```bash
docker compose up -d agent-compose
```

# AGENTS

## Overview

This repo contains the agent-compose session control plane. It creates, resumes, stops, and proxies isolated notebook runtimes, and exposes agent, loader, LLM, configuration, and workspace APIs.

Main entrypoints:
- `cmd/agent-compose/main.go`: starts the HTTP/Connect service, registers agent-compose routes, and handles graceful shutdown.
- `pkg/agentcompose/`: session lifecycle, runtime drivers, Jupyter proxying, loader scheduling, config persistence, LLM client, and service setup.
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

`pkg/agentcompose.Setup(di)` owns the agent-compose service graph and background runtime components.

## Core Services

The active Connect services are:
- `SessionService`
- `KernelService`
- `AgentService`
- `LLMService`
- `ConfigService`
- `LoaderService`

Jupyter proxying is handled by HTTP routes in `pkg/agentcompose/proxy.go` under `/agent-compose/session/<session_id>`.

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

Session metadata, notebook cells, event history, runtime state, and proxy state are stored under `SESSION_ROOT`.

Global environment variables, workspace configs, loader definitions, loader triggers, loader runs, and loader events are stored in `DATA_ROOT/data.db`.

Generated local artifacts and caches are ignored by git:
- `build/`, `dist/`, `.cache/`, `coverage/`
- `runtime/javascript/dist/`
- `runtime/agent-compose-runtime-sdk/dist/`
- `proto-client/src/` and `proto-client/dist/` when generated locally

## Docker Deployment

Current Docker build behavior:
- `Dockerfile` builds the `cmd/agent-compose` binary
- `guest-images/Dockerfile.agent-compose-guest` builds the guest image used by BoxLite
- `build_docker.sh` defaults to `IMAGE_NAME=agent-compose:latest` and `DOCKERFILE=Dockerfile`

Current compose behavior:
- `docker-compose.yml` deploys the `agent-compose` service and the published `agent-compose-frontend` nginx image
- the agent-compose service listens on `7410`
- data is mounted from `./data/agent-compose`
- Docker socket and `/dev/kvm` are exposed for runtime support

## Quality Gates

Testing standards and coverage requirements are defined in `TESTING.md`.

Task runner:
- `Taskfile.yml`

Primary commands:
```bash
task lint
task test
task build
```

Focused gates:
```bash
task test:unit
task test:integration
task test:e2e
task build:runtime
task build:runtime-sdk
task build:proto-client
```

`task test` is the release-blocking coverage gate. It writes coverage artifacts to `.cache/coverage` by default and enforces the baselines documented in `TESTING.md`.

The lint scope is project code only:
```bash
golangci-lint fmt --no-config --diff ./cmd/... ./pkg/... ./proto/health/v1 ./proto/health/v1/healthv1connect ./proto/agentcompose/v1 ./proto/agentcompose/v1/agentcomposev1connect ./proto/agentcompose/v2 ./proto/agentcompose/v2/agentcomposev2connect
golangci-lint run --no-config --allow-parallel-runners ./cmd/... ./pkg/... ./proto/health/v1 ./proto/health/v1/healthv1connect ./proto/agentcompose/v1 ./proto/agentcompose/v1/agentcomposev1connect ./proto/agentcompose/v2 ./proto/agentcompose/v2/agentcomposev2connect
```

Full local gate:
```bash
task all
```

`task all` also builds Docker images, so it requires Docker and the runtime image build prerequisites.

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

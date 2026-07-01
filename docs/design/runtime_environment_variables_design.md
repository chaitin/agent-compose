# Runtime Environment Variables

Chinese version: [../zh-CN/design/runtime_environment_variables_design.md](../zh-CN/design/runtime_environment_variables_design.md)

This document describes the runtime environment variables that agent-compose
currently injects into and reads from guests, containers, and sandboxes.

## Design Principles

The runtime mount manifest already maps session subdirectories to conventional
guest paths. Therefore guest code no longer needs an agent-compose-specific
session root, and agent-compose does not need to override `HOME`.

Current principles:

- Workspace is represented by `WORKSPACE`.
- State/runtime are represented by `STATE_ROOT` and `RUNTIME_ROOT`.
- `HOME` is inherited from the image default. The current guest image convention
  is `/root`.
- Artifact dir is scoped to a single command request / CLI input and is not
  injected as a global env var.

## Guest Runtime Variables

agent-compose injects these guest runtime variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `WORKSPACE` | `/workspace` | Workspace location |
| `STATE_ROOT` | `/data/state` | Cell artifacts, agent prompt/schema/provider state |
| `RUNTIME_ROOT` | `/data/runtime` | Runtime JS resources/cache/MPI and related data |
| `SESSION_ID` | current session id | Logging, debugging, tool context |
| `VERSION` | current agent-compose version | Debugging and compatibility checks |
| `JUPYTER_TOKEN` | current proxy token | Jupyter startup and proxy access |

Historical variables no longer injected or read by agent-compose:

| Variable | Current handling |
| --- | --- |
| `HOME` | agent-compose does not explicitly inject it; the image default is inherited |
| Independent home override variable | Removed |
| `SESSION_WORKSPACE` | Replaced by `WORKSPACE` |
| `SESSION_ROOT` | Removed or deprecated; there is no guest-side session root semantic anymore |
| `ARTIFACT_DIR` | Not used as a global env var |

## Home Convention

The guest image provides the default user and default home:

```text
HOME=/root
```

Go config `GuestHomePath` is fixed to `/root` and is used only as a manifest
target path. `NewConfig` does not read `GUEST_HOME`.

Home persistence is handled by the mount manifest:

| Host path | Docker guest path | BoxLite/Microsandbox guest path |
| --- | --- | --- |
| `<session>/home/.codex` | `/root/.codex` | Exposed through `<session> -> /data` and `/root -> /data/home` |
| `<session>/home/.claude` | `/root/.claude` | Exposed through `<session> -> /data` and `/root -> /data/home` |
| `<session>/home/.claude.json` | `/root/.claude.json` | Exposed through `<session> -> /data` and `/root -> /data/home` |
| `<session>/home/.gitconfig` | `/root/.gitconfig` | Exposed through `<session> -> /data` and `/root -> /data/home` |

Docker also fine-grain mounts home subpaths such as `.gemini/`, `.config/*`,
and `.local/share/gemini/`. BoxLite and Microsandbox mount only the whole
`<session>` directory.

## Host Configuration Variables

Host/control-plane configuration still supports:

| Variable | Default |
| --- | --- |
| `GUEST_WORKSPACE` | `/workspace` |
| `GUEST_STATE_ROOT` | `/data/state` |
| `GUEST_RUNTIME_ROOT` | `/data/runtime` |
| `GUEST_LOG_ROOT` | `/data/logs` |

`GUEST_HOME` is no longer used as a public configuration input. If non-root
guest images are supported in the future, default home should be determined
through image metadata or runtime driver capability, rather than reintroducing
agent-compose `HOME` override behavior.

## Runtime JS / SDK Defaults

`agent-compose-runtime exec` path defaults:

- `cwd` default order:
  1. request `cwd`
  2. CLI/default `workspace`
  3. `process.env.WORKSPACE`
  4. `/workspace`

- `home` default order:
  1. request `home`
  2. CLI/default `home`
  3. `process.env.HOME`
  4. `/root`

- `stateRoot` default order:
  1. request `stateRoot`
  2. CLI/default `stateRoot`
  3. `process.env.STATE_ROOT`
  4. `/data/state`

- `runtimeRoot` default order:
  1. request `runtimeRoot`
  2. `process.env.RUNTIME_ROOT`
  3. Derived from `stateRoot`, or fallback `/data/runtime`

Runtime JS injects these variables when starting child processes:

- `WORKSPACE`
- `STATE_ROOT`
- `RUNTIME_ROOT`

Runtime JS does not inject:

- `HOME`
- `SESSION_WORKSPACE`
- `ARTIFACT_DIR`

Child processes inherit the runtime process's native `HOME`.

## Runtime Context And Redaction

v2 run and service invocation APIs use `RuntimeContext` as the generic envelope
for source, request ids, trace ids, metadata, env overrides, identity metadata,
and capability scope. Runtime context is business-neutral: agent-compose stores
and forwards keys without interpreting product-specific concepts such as tenant,
channel, or enterprise user identity.

Security boundary:

- Runtime context may be persisted with the run record so it can be injected into
  the runtime and used for audit/debugging.
- API responses must redact sensitive values before returning runtime context to
  clients.
- Sensitive keys include provider credential names such as `OPENAI_API_KEY`,
  `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `GOOGLE_API_KEY`, and
  `GEMINI_API_KEY`, plus generic key names containing `token`, `secret`,
  `password`, `api_key`, `apikey`, `auth`, or `credential`.
- Redaction applies to runtime context `metadata`, `env`, `identity_context`, and
  `capability_scope.metadata`.
- Redaction does not remove non-sensitive routing/debug values such as source,
  trace id, external run id, capset ids, or ordinary metadata.

The runtime SDK reads context from `AGENT_COMPOSE_RUNTIME_CONTEXT_JSON` when the
host injects it. Older runtime env fallback variables may be used only for
compatibility and should not carry provider credentials.

## Artifact Directory

Artifact dir is a command/request-scoped path:

- Host-side cell and loader command artifacts live under
  `<session>/state/cells/...`.
- The corresponding guest-side path is `/data/state/cells/...`.
- Runtime JS can still receive artifact dir through request or CLI argument.
- `ARTIFACT_DIR` is not exposed as a global env var.

## Current Invariants

- agent-compose does not explicitly set `HOME` when starting a guest/container/
  sandbox.
- Tools inside the guest see the image default `HOME=/root`.
- Workspace variable is unified as `WORKSPACE`.
- Runtime state variables are unified as `STATE_ROOT` and `RUNTIME_ROOT`.
- `HOME`, `SESSION_WORKSPACE`, guest-side `SESSION_ROOT`, and global
  `ARTIFACT_DIR` are no longer part of the current runtime contract.
- Home persistence paths in the manifest land under `/root/...`.

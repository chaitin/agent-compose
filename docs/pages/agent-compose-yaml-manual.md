# `agent-compose.yml` Manual

This manual documents every field currently accepted by the `agent-compose.yml` / `agent-compose.yaml` implementation, including defaults, validation rules, and authoring forms. The parser is strict: unknown fields, duplicate fields, and values of the wrong YAML type are errors.

> Important: project-level workspaces use the plural top-level key `workspaces`. The old top-level key `workspace` is no longer supported and is rejected as an unknown field. The singular key remains valid at `agents.<agent>.workspace`, where it selects a project workspace or defines an inline one.

Validate a file before applying it:

```bash
agent-compose config --quiet
agent-compose -f ./path/to/agent-compose.yml config
```

The first command validates without printing the normalized document. The second prints the normalized configuration and redacts values marked as secret. By default, the CLI looks for `agent-compose.yml` and then `agent-compose.yaml` in the current directory. Use `-f/--file` when both exist or when the file is elsewhere.

## Complete structure at a glance

This example shows where the available fields belong. Real projects should keep only the sections they need.

```yaml
name: review-pipeline

env_file:
  - .env
  - .env.local

variables:
  DISPLAY_NAME: review-pipeline
  CONTROL_TOKEN:
    value: ${CONTROL_TOKEN}
    secret: true

workspaces:
  source:
    provider: file
    path: .
  upstream:
    provider: git
    url: https://github.com/example/project.git
    ref: main
    target: .

mcp_servers:
  local-tools:
    type: local
    command: npx
    args: ["-y", "@example/mcp-server"]
    env:
      API_TOKEN:
        value: ${MCP_API_TOKEN}
        secret: true
  issue-tracker:
    type: remote
    transport: http
    url: ${ISSUE_TRACKER_MCP_URL}
    headers:
      Authorization:
        value: Bearer ${ISSUE_TRACKER_TOKEN}
        secret: true

octobus_servers:
  internal:
    url: https://octobus.internal.example
    token: ${OCTOBUS_INTERNAL_TOKEN}
  public:
    url: https://octobus.example
    token: ${OCTOBUS_PUBLIC_TOKEN}

volumes:
  cache:
    name: review-cache
    driver: local
    external: false
    labels:
      purpose: agent-cache
    options: {}

agents:
  reviewer:
    enabled: true
    provider: codex
    model: ${REVIEW_MODEL}
    system_prompt: |
      Review changes carefully and report concrete evidence.
    image: ghcr.io/chaitin/agent-compose-guest:latest
    build:
      context: .
      dockerfile: guest-images/Dockerfile.agent-compose-guest
      target: runtime
      args:
        CHANNEL: stable
      platforms: [linux/amd64]
      tags: [review-agent:latest]
      no_cache: false
      pull: true
    driver:
      docker: {}
    env:
      LOG_LEVEL: info
      SERVICE_TOKEN:
        value: ${SERVICE_TOKEN}
        secret: true
    mcp_servers:
      - local-tools
      - name: audit-api
        type: remote
        transport: sse
        url: https://mcp.example.com/sse
        headers:
          Authorization:
            value: Bearer ${AUDIT_TOKEN}
            secret: true
    capset_ids:
      - engineering
      - internal/code-review
    skills:
      - ./skills/review
      - name: release-check
        provider: git
        url: https://github.com/example/agent-skills.git
        path: skills/release-check
        ref: main
    volumes:
      - cache:/cache
      - type: bind
        source: ./reports
        target: /workspace/reports
        read_only: false
    workspace:
      name: source
    scheduler:
      enabled: true
      sandbox_policy: sticky
      triggers:
        - name: hourly-review
          cron: "0 * * * *"
          prompt: Review the current workspace.
          sandbox_policy: new
    jupyter:
      enabled: false
      guest_port: 8888

```

## General authoring rules

### Strict parsing

- Unknown fields are rejected rather than silently ignored.
- A field repeated in the same mapping is rejected as a duplicate.
- Boolean fields must be YAML booleans, list fields must be sequences, and object fields must be mappings.
- Project names, agent names, project workspace keys, MCP names, volume keys, and final skill names must match `^[a-z][a-z0-9_-]*$`.
- Relative paths use field-specific base directories described below.

### Environment sources and precedence

`env_file` controls which dotenv files are available to `${NAME}` interpolation. Values are loaded in this order:

1. Files are loaded in listed order; a later file overrides an earlier file.
2. The environment of the `agent-compose` CLI process overrides all dotenv values.

If `env_file` is omitted, the CLI first looks for `.env` beside the compose file. If that file does not exist, it looks for `.env` in the current working directory. An explicit `env_file: []` disables automatic dotenv loading. A configured file that is missing or unreadable, or an empty path in the list, is an error.

Only the simple `${NAME}` syntax is supported. Shell forms such as `${NAME:-default}`, command substitution, and recursive expansion are not supported. A referenced variable that is unavailable causes validation to fail.

Interpolation is implemented in these locations:

- `variables.*.value`
- `agents.*.model`
- `agents.*.env.*.value`
- Project and inline-agent MCP `url`, `env.*.value`, and `headers.*.value`
- Project OctoBus `url` and `token`
- Skill `name`, `source`, `url`, `path`, `ref`, and `username`

Other strings are not interpolated, including `name`, `provider`, `image`, `system_prompt`, workspace fields, build fields, and scheduler fields.

### Environment value shape

`variables`, agent `env`, MCP `env`, and MCP `headers` share the same value syntax:

```yaml
PLAIN_VALUE: hello
SECRET_VALUE:
  value: ${SECRET_VALUE}
  secret: true
```

| Field | Type | Default | Purpose |
| --- | --- | --- | --- |
| `value` | string | `""` | The value. `${NAME}` is expanded only in the supported locations listed above. |
| `secret` | bool | `false` | Marks sensitive output. Normalized configuration displays `********`, while runtime consumers receive the real value. |

`secret` is redaction metadata; it does not read the environment by itself. Use `${NAME}` in `value` when the value should come from deployment configuration.

## Top-level fields

| Field | Type | Required | Purpose |
| --- | --- | --- | --- |
| `name` | string | Conditionally | Project identifier. If omitted, the compose directory name is used; the final value must be a stable identifier. |
| `env_file` | string or string[] | No | Dotenv files used for interpolation. Relative paths are resolved from the compose directory. |
| `variables` | map | No | Project-level named values and secret metadata stored in the normalized project specification. |
| `workspaces` | map | No | Reusable project workspace definitions. Only the plural top-level form is valid. |
| `mcp_servers` | map | No | Named MCP servers that agents may reference. |
| `octobus_servers` | map | No | Named project-scoped OctoBus servers selected by qualified `capset_ids`. |
| `volumes` | map | No | Persistent volumes managed or referenced by the project. |
| `agents` | map | No | Agent definitions keyed by agent name. |

### `name`

```yaml
name: code-review
```

The value must begin with a lowercase letter and may then contain lowercase letters, digits, `_`, and `-`. `review-v2` is valid; `Review`, `2-review`, and names containing spaces are not. Project identity also incorporates the normalized source path, allowing projects with the same name from different compose paths to remain distinct.

### `env_file`

Use a scalar for one file:

```yaml
env_file: .env.production
```

Use a list for multiple files:

```yaml
env_file:
  - .env
  - .env.production
```

### `variables`

```yaml
variables:
  REGION: cn-hangzhou
  RELEASE_TOKEN:
    value: ${RELEASE_TOKEN}
    secret: true
```

Project variables are retained as project configuration values with redaction semantics. They are not automatically inherited by agent `env`, and they are not a source for other `${NAME}` expressions. Declare a value again under an agent's `env` when it must enter that agent's sandbox.

## `workspaces`: project workspaces

The top-level key must be plural:

```yaml
workspaces:
  source:
    provider: file
    path: .
```

The old singular top-level form is invalid:

```yaml
# Invalid: strict parsing rejects top-level workspace.
workspace:
  provider: file
  path: .
```

Each `workspaces.<key>` accepts:

| Field | Type | Applicability | Purpose |
| --- | --- | --- | --- |
| `name` | string | Compatibility field | The map key is the effective project workspace name. Normally omit this redundant field. |
| `provider` | string | Required | `file` or `git`. |
| `url` | string | Required for `git` | Git clone URL. It is forbidden for `file`. |
| `ref` | string | Optional for `git` | Git branch, tag, or commit. |
| `path` | string | Required for `file` | Source path relative to the compose directory; it cannot escape the project root. Git workspaces do not support a repository subpath. |
| `target` | string | Optional | Destination below the sandbox workspace root. Defaults to `.`. |
| `username` | string | Optional for `git` | Git HTTP username. |
| `password` | string | Optional for `git` | Git password as an exact environment reference such as `${NAME}`. |
| `token` | string | Optional for `git` | Git token as an exact environment reference such as `${NAME}`. |

A local workspace is copied into an isolated snapshot for each project run. Agent changes to that snapshot do not modify the source directory.

```yaml
workspaces:
  source:
    provider: file
    path: ./src
  release-branch:
    provider: git
    url: https://github.com/example/service.git
    ref: release
    target: .
```

Workspace selection follows these rules:

- Top-level `workspaces` entries are named definitions only; they are never assigned to an agent automatically.
- When an agent omits `workspace`, the run has no configured workspace, regardless of how many project workspaces exist.
- To use a project workspace, the agent must explicitly select it with `workspace.name`, or define an inline workspace.
- An explicit empty `workspace: {}` is invalid; omit the key to configure no workspace.

## `mcp_servers`: project MCP servers

Project MCP servers are a named map. Names must use the stable identifier format. The two server types are `local` and `remote`.

### Local MCP

```yaml
mcp_servers:
  filesystem:
    type: local
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/workspace"]
    env:
      NODE_ENV: production
```

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `type` | string | Yes | Must be `local`. |
| `command` | string | Yes | Command that starts the MCP server inside the sandbox. |
| `args` | string[] | No | Command arguments. Empty and duplicate entries are removed during normalization. |
| `env` | map | No | Process environment with value objects and `${NAME}` interpolation. |
| `transport` | string | Forbidden | A local MCP does not accept a non-empty transport. |
| `url` | string | Forbidden | A local MCP does not accept a URL. |
| `headers` | map | Forbidden | A local MCP does not accept HTTP headers. |

### Remote MCP

```yaml
mcp_servers:
  docs:
    type: remote
    transport: sse
    url: https://mcp.example.com/sse
    headers:
      Authorization:
        value: Bearer ${MCP_TOKEN}
        secret: true
```

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `type` | string | Yes | Must be `remote`. |
| `transport` | string | Yes | `sse` or `http`. |
| `url` | string | Yes | Remote endpoint; supports `${NAME}` interpolation. |
| `headers` | map | No | Request headers with interpolation and secret metadata. |
| `command` | string | Forbidden | A remote MCP does not execute a local command. |
| `args` | string[] | Forbidden | A remote MCP does not accept command arguments. |
| `env` | map | Forbidden | A remote MCP does not accept process environment values. |

Project MCP definitions are not injected into every agent automatically. Each agent selects or defines the servers it needs under its own `mcp_servers` field.

## `octobus_servers`: project OctoBus servers

Projects may declare multiple named OctoBus servers:

```yaml
octobus_servers:
  internal:
    url: https://octobus.internal.example
    token: ${OCTOBUS_INTERNAL_TOKEN}
  public:
    url: https://octobus.example
    token: ${OCTOBUS_PUBLIC_TOKEN}
```

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `url` | string | Yes | Absolute `http` or `https` URL for the OctoBus server. User information and URL fragments are rejected. Supports `${NAME}` interpolation. |
| `token` | string | No | Bearer token used by the daemon when connecting to this server. Supports `${NAME}` interpolation; an empty value configures no authorization token. |

The map key is the server name and must use the stable identifier format. Agents select these servers by qualifying an existing `capset_ids` entry as `<server>/<capset>`, for example `internal/code-review`. Defining a server does not grant access to it by itself.

OctoBus tokens are inherently sensitive. Keep them in environment or dotenv configuration and reference them with `${NAME}`. The daemon retains the resolved token so it can proxy requests, but redacts it from normalized user-facing output and never injects it into the sandbox. Avoid committing literal tokens to compose files.

Project re-apply follows the same managed agent configuration model as MCP servers. A running sandbox keeps the `capset_ids` authorization set captured when it was created, while subsequent calls resolve a referenced server from the current managed agent definition. Updating a server URL or token therefore takes effect without rebuilding the sandbox. Newly added capsets are available only to new sandboxes; if a server used by an existing authorized capset can no longer be resolved, that call fails instead of falling back to another server.

## `volumes`: project volumes

```yaml
volumes:
  cache: {}
  shared-data:
    name: existing-data
    driver: local
    external: true
    labels:
      owner: platform
    options:
      tier: fast
```

| Field | Type | Default | Purpose |
| --- | --- | --- | --- |
| `name` | string | Derived from project and key | Explicit underlying volume name. |
| `driver` | string | `local` | Volume driver. Only `local` is currently supported. |
| `external` | bool | `false` | References an existing volume instead of creating a project-owned volume. |
| `labels` | map[string]string | Empty | Volume labels. Keys and values are trimmed. |
| `options` | map[string]string | Empty | Options passed to the local volume driver. |

The volume map key must be a stable identifier. Agents mount project volumes through `agents.<name>.volumes`.

## `agents.<name>`

`agents` is a mapping keyed by agent name:

```yaml
agents:
  reviewer:
    provider: codex
    image: ghcr.io/chaitin/agent-compose-guest:latest
```

| Field | Type | Default | Purpose |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | Whether the Agent is enabled. A disabled definition remains stored but cannot run normally, and its scheduler is not enabled. |
| `display_name` | string | Empty | Human-readable agent label. |
| `description` | string | Empty | Human-readable explanation of the agent's role. |
| `provider` | string | `codex` | Agent provider: `codex`, `claude`, `gemini`, `opencode`, or `pi`. Compatibility aliases are normalized at persistence boundaries. |
| `model` | string | Provider/daemon default | Model name. Pi requires `<llm-provider-id>/<model-name>`. Supports `${NAME}` interpolation. |
| `system_prompt` | string | Empty | Additional system instructions; YAML block scalars are recommended for multiline text. |
| `image` | string | Daemon default image | Guest image reference and an output tag when `build` is used. |
| `build` | string/object | None | Image build configuration used by `agent-compose build`. |
| `driver` | object | Docker | Runtime driver. Exactly one runtime key is allowed. |
| `env` | map | Empty | Environment variables injected into the sandbox. |
| `mcp_servers` | scalar/object/list | Empty | References project MCP servers or declares agent-private servers. |
| `capset_ids` | string[] | Empty | OctoBus capability set declarations allowed for this agent's sandboxes; entries may select a project server as `<server>/<capset>`. |
| `skills` | list | Empty | Skill sources projected into the agent runtime. |
| `volumes` | list | Empty | Volume and bind mounts. |
| `workspace` | object | None | Explicitly selects a project `workspaces` entry or defines an inline workspace. |
| `scheduler` | object | None | Automatic trigger configuration. |
| `jupyter` | object | Disabled | Default Jupyter behavior for agent runs. |

### `enabled`, `provider`, `model`, and `system_prompt`

```yaml
agents:
  reviewer:
    enabled: true
    provider: claude
    model: ${CLAUDE_MODEL}
    system_prompt: |
      Focus on correctness, security, and regression risk.
```

Canonical providers are `codex`, `claude`, `gemini`, `opencode`, and `pi`. Compatibility normalization also accepts `claude-code` / `claude_code`, `gemini-cli` / `gemini_cli`, `open-code` / `open_code`, and `pi-agent` / `pi_agent`; new files should use canonical names.

Pi is a multi-model agent, so its model must identify both the configured LLM provider and model, for example:

```yaml
agents:
  reviewer:
    provider: pi
    model: openai/gpt-5.4
```

The part before the first slash is an LLM provider ID configured in agent-compose; the remainder is that provider's model name. Pi model traffic is routed through the sandbox runtime LLM facade, so upstream credentials remain on the daemon.

### `image`

```yaml
agents:
  reviewer:
    image: ghcr.io/chaitin/agent-compose-guest:latest
```

At runtime, the selected driver must be able to obtain this image. When `build` is also configured, `image` becomes one of the build output tags. `agent-compose build` fails if neither `image` nor `build.tags` provides a tag.

GitHub CI publishes these images to GHCR:

| Image | Purpose | Dockerfile | Platforms |
| --- | --- | --- | --- |
| `ghcr.io/chaitin/agent-compose` | Control-plane daemon | `Dockerfile` | `linux/amd64`, `linux/arm64` |
| `ghcr.io/chaitin/agent-compose-guest` | Sandbox guest runtime | `guest-images/Dockerfile.agent-compose-guest` | `linux/amd64`, `linux/arm64` |
| `ghcr.io/chaitin/agent-compose-guest-archlinux` | Optional Arch Linux sandbox guest runtime | `guest-images/Dockerfile.agent-compose-guest-archlinux` | `linux/amd64` |

Use either guest image for an agent's `image`. The daemon image deploys the control plane and is not a guest image. Neither guest is tied to one driver: CI does not publish separate BoxLite-only, Microsandbox-only, or other per-driver guest images.

### `build`

The scalar shorthand sets only the context:

```yaml
build: ./guest
```

The complete form is:

```yaml
build:
  context: ./guest
  dockerfile: Dockerfile
  target: runtime
  args:
    VERSION: "1.2.3"
  platforms:
    - linux/amd64
  tags:
    - example/guest:latest
  no_cache: false
  pull: true
```

| Field | Type | Default | Purpose |
| --- | --- | --- | --- |
| `context` | string | `.` | Build context. A relative path is resolved from the compose directory. |
| `dockerfile` | string | `Dockerfile` | Dockerfile path interpreted by the image build backend. |
| `target` | string | Empty | Multi-stage build target. |
| `args` | map[string]string | Empty | Build arguments. Trimmed argument names must not be empty. |
| `platforms` | string[] | Empty | Target platform in `os/arch` form. At most one platform is currently supported. |
| `tags` | string[] | Empty | Output tags merged with agent `image` and CLI `--tag` values. |
| `no_cache` | bool | `false` | Disables the build cache. |
| `pull` | bool | `false` | Requests newer base images during the build. |

`agent-compose build` currently targets the Docker daemon image store. Matching CLI flags override or extend YAML values.

### `driver`

Omitting `driver` is equivalent to:

```yaml
driver:
  docker: {}
```

Exactly one runtime may be selected:

```yaml
driver:
  docker:
    host: ""
```

```yaml
driver:
  boxlite:
    kernel: ""
    rootfs: ""
```

```yaml
driver:
  microsandbox:
    profile: secure
```

| Driver | Child fields | Current status |
| --- | --- | --- |
| `docker` | `host` | Stable supported driver. `host` is parsed and retained; the daemon's Docker boundary is still controlled by deployment configuration. |
| `boxlite` | `kernel`, `rootfs` | Compiled into full Linux builds; runtime initialization is lazy. Child strings are trimmed. |
| `microsandbox` | `profile` | Compiled into full Linux builds; runtime initialization is lazy. The profile string is trimmed. |
| `firecracker` | `kernel`, `rootfs` | Reserved in the parser schema. Normalization currently returns `unsupported runtime driver firecracker`, so it cannot be used. |

For completeness, this shape is recognized by the parser but is currently invalid during normalization:

```yaml
# Invalid with the current implementation.
driver:
  firecracker:
    kernel: /path/to/kernel
    rootfs: /path/to/rootfs
```

Schema support does not guarantee that a driver is compiled into the current binary. Inspect `compiled_drivers` with `agent-compose --json version`. That list does not test Docker daemon access, KVM, or runtime artifact health.

### `env`

```yaml
env:
  LOG_LEVEL: debug
  API_TOKEN:
    value: ${API_TOKEN}
    secret: true
```

These values enter the agent sandbox. Secret values are redacted from normalized display but remain available to the runtime.

### `mcp_servers`

Reference one project server:

```yaml
mcp_servers: filesystem
```

Reference several:

```yaml
mcp_servers:
  - filesystem
  - issue-tracker
```

Define an agent-private server:

```yaml
mcp_servers:
  - name: private-tools
    type: local
    command: private-mcp
    args: ["serve"]
```

An inline object accepts `name`, `type`, `transport`, `command`, `args`, `env`, `url`, and `headers`, with the same local/remote rules as project MCP servers. Inline servers require `name`; duplicate inline names in one agent are rejected. Repeated references to the same project server are deduplicated.

### `capset_ids`

```yaml
capset_ids:
  - legacy-capset
  - internal/engineering
  - public/ticketing
```

Empty and duplicate entries are removed. An unqualified value such as `legacy-capset` uses the daemon-wide OctoBus configuration, preserving the behavior of existing project files. A qualified value such as `internal/engineering` selects `octobus_servers.internal`; `internal` is used only by agent-compose to choose the upstream server, and OctoBus receives `engineering` as the capset ID. A qualified entry whose server is not declared is a validation error.

Qualified and unqualified entries may be mixed. Adding `octobus_servers` never changes the routing of unqualified entries. The complete declaration remains the sandbox authorization boundary: authorization for `internal/engineering` does not also authorize `engineering` or `public/engineering`.

The selected IDs drive capability gateway environment and guide injection. Missing daemon-wide gateway configuration for an unqualified entry, an unreachable server, or guide retrieval failure is a best-effort condition reported as a warning; sandbox creation continues. Project OctoBus URL and token values remain in the daemon and are not included in guest metadata or capability guides.

### `skills`

A resolved skill directory must contain a valid `SKILL.md`. Providers can be `file`, `http`, or `git`; final skill names must be unique within an agent. ZIP is a content format, not a provider.

Local directory:

```yaml
skills:
  - name: review
    provider: file
    path: ./skills/review
```

Git source:

```yaml
skills:
  - name: review
    provider: git
    url: https://github.com/example/skills.git
    path: review
    ref: v1.0.0
    username: ${GIT_USERNAME}
    token: ${GIT_TOKEN}
```

Remote ZIP:

```yaml
skills:
  - name: review
    provider: http
    url: https://downloads.example.com/review.zip
    format: zip
    path: review
```

| Field | Type | Purpose |
| --- | --- | --- |
| `name` | string | Skill name. If omitted, it is inferred from path/URL and must end as a stable identifier. |
| `provider` | string | Required source provider: `file`, `http`, or `git`. |
| `url` | string | Required for `http` and `git`. |
| `path` | string | Local path for `file`; content subdirectory for Git or ZIP. Relative file paths use the compose directory. |
| `ref` | string | Git branch, tag, or commit. |
| `format` | string | Optional content format. Currently only `zip` is supported; HTTP skills require it. |
| `username` | string | HTTP/Git username; interpolation is supported. |
| `password` | string | HTTP/Git password. Only an exact environment reference such as `${NAME}` is allowed. |
| `token` | string | HTTP/Git token. Only an exact environment reference such as `${NAME}` is allowed. |

`password` and `token` cannot contain plaintext. They are resolved against the daemon environment during skill resolution, avoiding expanded credentials in the project specification. Remote ZIP downloads are restricted to HTTP(S) and are subject to size, archive, and network-address safety checks.

Git refs are resolved at each business lifecycle: skills during an agent run, workspaces during sandbox provisioning, and scheduler sources during `config`/`up` before the script snapshot is stored. A moving branch can therefore resolve to different commits across those operations. Use a commit SHA in `ref` when all consumers must use the exact same revision.

### `volumes`

The short form is `source:target[:ro|rw]`:

```yaml
volumes:
  - cache:/cache
  - ./reports:/workspace/reports:ro
```

The long form is:

```yaml
volumes:
  - type: volume
    source: cache
    target: /cache
    read_only: false
  - type: bind
    source: ./reports
    target: /workspace/reports
    read_only: true
```

| Field | Type | Required | Purpose |
| --- | --- | --- | --- |
| `type` | string | No | `volume` or `bind`. If omitted, a project volume match, absolute path, or `.` prefix helps infer the type; other sources default to volume. |
| `source` | string | Yes | Project volume key/valid volume name, or host source for a bind. |
| `target` | string | Yes | Absolute path inside the guest. |
| `read_only` | bool | No | Read-only mount; defaults to `false`. |

An agent cannot mount multiple entries at the same target. Because short syntax uses `:`, use the long form when a source or target contains a colon.

### `workspace`: agent selection or inline workspace

This singular key is inside an agent and is distinct from the plural top-level `workspaces` map.

Reference a project workspace:

```yaml
workspace:
  name: source
```

Define an inline local workspace:

```yaml
workspace:
  provider: file
  path: ./src
```

Define an inline Git workspace:

```yaml
workspace:
  provider: git
  url: https://github.com/example/project.git
  ref: main
  target: .
```

If `name` is combined with any source field or `target`, the object is treated as an inline workspace rather than an inherited project workspace with overrides. To reuse a project entry, set only `name`.

### `scheduler`

A scheduler uses either declarative `triggers` or JavaScript `script`; the two forms are mutually exclusive.

| Field | Type | Default | Purpose |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | Enables this agent's scheduler. Disabling the Agent also prevents its scheduler from being enabled. |
| `sandbox_policy` | string | `new` | Scheduler default sandbox policy: `new` or `sticky`. |
| `concurrency_policy` | string | `skip` | Overlapping run policy for the entire agent scheduler: `skip` or `parallel`. |
| `triggers` | list | Empty | Declarative triggers. |
| `script` | string/object | Empty | Inline JavaScript or a flat `file`/`http`/`git` source mapping. Cannot coexist with `triggers`. |

`new` creates a new sandbox for scheduler calls. `sticky` allows the scheduler to bind and reuse a sandbox. A trigger-level `sandbox_policy` controls the generated agent call for that trigger.

`concurrency_policy` applies to the whole agent scheduler, including all declarative or script-registered triggers and manual scheduler invocations. With `skip`, a run that overlaps another run from the same scheduler is recorded as `skipped` and is not queued. With `parallel`, overlapping runs may execute concurrently. It is not a per-trigger policy.

#### Declarative triggers

Each trigger must set exactly one kind field: `cron`, `interval`, `timeout`, or `event`.

```yaml
scheduler:
  enabled: true
  sandbox_policy: sticky
  concurrency_policy: parallel
  triggers:
    - name: nightly
      cron: "0 2 * * *"
      prompt: Run the nightly review.
    - name: heartbeat
      interval: 30m
      prompt: Check service health.
    - name: startup-once
      timeout: 15s
      prompt: Perform the startup check.
      sandbox_policy: new
    - name: webhook-review
      event:
        topic: webhook.github.push
      prompt: Review the pushed changes.
```

| Field | Type | Required | Purpose |
| --- | --- | --- | --- |
| `name` | string | No | Readable stable name. Non-empty names must be unique within the scheduler. If omitted, identity also incorporates list position. |
| `cron` | string | One of four | Five-field cron expression with optional seconds and robfig/cron descriptors. Declarative cron triggers use UTC. |
| `interval` | duration | One of four | Positive period such as `30s`, `5m`, or `2h`; registration precision is at least 1 ms. |
| `timeout` | duration | One of four | Positive one-shot delay such as `15s`; registration precision is at least 1 ms. |
| `event.topic` | string | One of four | The nested `topic` is the non-empty subscribed topic, for example `webhook.github.push`. |
| `prompt` | string | No | Prompt sent to the agent. An empty prompt becomes `Run agent <name>.` |
| `sandbox_policy` | string | No | `sticky` or `new` for this generated agent call. If omitted, no call-level override is emitted. |

#### Inline script

```yaml
scheduler:
  enabled: true
  script: |
    scheduler.interval("review", async function () {
      return scheduler.agent("Review the workspace.");
    }, 60000);
```

The scheduler runtime validates the script and derives registered triggers from it.

#### External script

Local file:

```yaml
scheduler:
  enabled: true
  script:
    provider: file
    path: ./scheduler.js
```

HTTP URL:

```yaml
scheduler:
  enabled: true
  script:
    provider: http
    url: https://example.com/scheduler.js
```

External script mappings use the same source keys as skills and workspaces:

- File: `provider: file` with `path`. Relative paths use the compose directory.
- HTTP: `provider: http` with `url` and optional authentication.
- Git: `provider: git` with `url`, optional `ref`, and the required repository-internal `path`.

When a project is applied, the CLI reads the script and stores a content snapshot in the project specification; the daemon does not fetch the source again later. HTTP fetching uses a 10-second timeout, a 1 MiB limit, no more than five redirects, and UTF-8 validation. URL userinfo and HTTPS-to-HTTP redirect downgrades are rejected.

### `jupyter`

```yaml
jupyter:
  enabled: true
  guest_port: 8888
```

| Field | Type | Default | Purpose |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | Enables Jupyter when the run CLI does not explicitly override it. |
| `guest_port` | int | Daemon `JUPYTER_GUEST_PORT` | Guest listening port. `0` uses the daemon default; an explicit value must be 1–65535. |

Setting `guest_port` while leaving `enabled: false` retains the port configuration without enabling Jupyter by default. `agent-compose run --jupyter` can enable it for one run.

## Common errors and migration notes

### Using top-level `workspace`

Invalid:

```yaml
workspace:
  provider: file
  path: .
```

Valid:

```yaml
workspaces:
  source:
    provider: file
    path: .

agents:
  reviewer:
    workspace:
      name: source
```

### Combining scheduler script and triggers

The fields are mutually exclusive. Put all registration logic in `script`, or use declarative `triggers` exclusively.

### Expecting `variables` to enter a sandbox

Project variables are not inherited. Declare runtime values under the agent:

```yaml
variables:
  API_URL: https://api.example.com

agents:
  reviewer:
    env:
      API_URL: https://api.example.com
```

To reuse deployment environment values, both locations may contain `${API_URL}`, supplied through `env_file` or the CLI process environment.

### Expecting a project `workspace` to be selected automatically

Top-level `workspaces` are never selected automatically. An agent that omits `workspace` has no configured workspace, even when the project defines exactly one entry. Set `workspace.name` or define an inline workspace when the agent needs one. Use omission, not `workspace: {}`, to configure no workspace.

### Selecting an unavailable runtime driver

Schema validation does not prove that a runtime can start. BoxLite and Microsandbox are compiled only in full Linux builds and require KVM plus their runtime artifacts. Docker requires a reachable Docker daemon.

## Minimal example

```yaml
name: docker-minimal

agents:
  reviewer:
    provider: codex
    image: ghcr.io/chaitin/agent-compose-guest:latest
    driver:
      docker: {}
```

Validate, apply, and run it:

```bash
agent-compose config --quiet
agent-compose up
agent-compose run reviewer --prompt "Review this project."
```

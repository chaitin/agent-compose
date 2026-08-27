# `agent-compose.yml` Manual

This manual documents every field currently accepted by the `agent-compose.yml` / `agent-compose.yaml` implementation, including defaults, validation rules, and authoring forms. The parser is strict: unknown fields, duplicate fields, and values of the wrong YAML type are errors.

> Important: project-level workspaces use the plural top-level key `workspaces`. The old top-level key `workspace` is no longer supported and is rejected as an unknown field. The singular key remains valid at `agents.<agent>.workspace`, where it selects a project workspace or defines an inline one.

Validate a file before applying it:

```bash
agent-compose config --quiet
agent-compose -f ./path/to/agent-compose.yml config
```

The first command validates without printing the normalized document. The second prints the normalized configuration and redacts values marked as secret. By default, the CLI looks for `agent-compose.yml` and then `agent-compose.yaml` in the current directory. Use `-f/--file` when both exist or when the file is elsewhere.

## Compatibility policy

The public authoring schema has been stable since `v2608.1.0`. A configuration accepted by that release or a later stable release remains valid in subsequent stable releases. This is a backward-compatibility promise: a newer release may add optional fields that an older strict parser does not recognize.

Stable field removal or renaming, incompatible type changes, and making an optional field required are breaking changes. Changes to accepted shorthand forms, validation constraints, defaults, interpolation, or normalization semantics also require compatibility review. A security fix may intentionally reject an unsafe historical value, but must document the impact and migration.

The repository enforces this policy with a machine-readable `v2608.1.0` field contract and cumulative parse-and-normalize fixtures. `agent-compose config --quiet` remains the user-facing way to validate a project with the installed version; cross-version contract comparison is a repository CI responsibility and does not add a separate CLI command.

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
    image: chaitin/agent-compose-guest:latest
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
- Project names must match `^[a-z0-9][a-z0-9_-]*$`. Agent names, project workspace keys, MCP names, volume keys, and final skill names use the stricter `^[a-z][a-z0-9_-]*$` form.
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
- Skill `name`, `source`, `url`, `path`, `ref`, `username`, `password`, and `token`; `password` and `token` must be exact `${NAME}` references

Other strings are not interpolated, including `name`, `provider`, `image`, `system_prompt`, other workspace fields, build fields, and scheduler fields.

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
| `name` | string | Conditionally | Project identifier. If omitted, a Docker Compose-compatible name is derived from the compose directory. |
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

The value must match `^[a-z0-9][a-z0-9_-]*$`. `review-v2` and `2-review` are valid; `Review` and names containing spaces are not. When omitted, the compose directory basename is lowercased, unsupported characters are removed, and leading `_` or `-` characters are trimmed. The resulting name is the daemon-wide project identity; moving the compose file does not create another project.

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

The redacted `********` value in project API or normalized output is not the credential itself. `ApplyProject` keeps its existing complete-replacement behavior and does not interpret this marker as “preserve”; compose and CLI re-apply flows must still resolve and submit the real secret from the original environment-backed source.

`PatchProject` is the API for editing an existing project from a redacted `GetProject` result. It takes the complete desired `ProjectSpec`, the project reference, and the required current spec hash. At an existing secret's same stable location, `********` preserves the stored value. Using the marker for a new, moved, or non-secret value is rejected; a real value replaces the secret, and omitting a collection item deletes it. Patch cannot create or rename a project or change its source. A stale current hash fails with `ABORTED`. The CLI continues to use `ApplyProject`; these Patch semantics do not change CLI behavior.

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
    image: chaitin/agent-compose-guest:latest
```

| Field | Type | Default | Purpose |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | Whether the Agent is enabled. A disabled definition remains stored but cannot run normally, and its scheduler is not enabled. |
| `display_name` | string | Empty | Human-readable agent label. |
| `description` | string | Empty | Human-readable explanation of the agent's role. |
| `input_schema` | JSON Schema/source | None | Optional JSON Schema describing input accepted by the agent. May be inline or loaded from a source descriptor. |
| `output_schema` | JSON Schema/source | None | Optional JSON Schema describing output produced by the agent. May be inline or loaded from a source descriptor. |
| `provider` | string | `codex` | Agent provider: `codex`, `claude`, `gemini`, `opencode`, `pi`, or `dsh`. Compatibility aliases are normalized at persistence boundaries. |
| `model` | string | Provider/daemon default | Model name. Pi and dsh require `<llm-provider-id>/<model-name>`. Supports `${NAME}` interpolation. |
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
| `sandbox` | object | Remove stopped runtime | Sandbox lifecycle configuration. |
| `scheduler` | object | None | Automatic trigger configuration. |
| `jupyter` | object | Disabled | Default Jupyter behavior for agent runs. |

### `input_schema` and `output_schema`

Each schema is optional and independent. An agent may declare either one, both, or neither. Inline schemas use ordinary JSON Schema expressed as YAML; property-level `description` values are recommended so external platforms can present useful input and output documentation.

```yaml
agents:
  researcher:
    description: Researches a topic and returns cited findings.
    input_schema:
      type: object
      required: [query]
      properties:
        query:
          type: string
          description: Topic or question to research.
    output_schema:
      provider: file
      path: ./schemas/research-result.schema.json
```

The source form is the same flat descriptor accepted by `scheduler.script` (`file`, `http`, or `git`). Relative file paths resolve from the compose file directory. Source content is resolved, compiled as JSON Schema, and stored as a snapshot when the project is applied; it must contain a JSON object or boolean schema. References within the same schema document are supported, while external `$ref` resources are rejected so applying a stored snapshot never performs implicit filesystem or network access. A mapping whose top-level `provider` value is `file`, `http`, or `git` is interpreted as a source descriptor; other `provider` values remain available as custom inline-schema keywords.

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

Canonical providers are `codex`, `claude`, `gemini`, `opencode`, `pi`, and `dsh`. Compatibility normalization also accepts `claude-code` / `claude_code`, `gemini-cli` / `gemini_cli`, `open-code` / `open_code`, `pi-agent` / `pi_agent`, and `deepseek` / `deepseek-harness` / `deepseek_harness`; new files should use canonical names.

Pi and dsh are multi-model agents, so their model must identify both the configured LLM provider and model, for example:

```yaml
agents:
  reviewer:
    provider: pi
    model: openai/gpt-5.4
```

The part before the first slash is an LLM provider ID configured in agent-compose; the entire remainder is the literal upstream model ID and may contain additional slashes. Pi and dsh model traffic is routed through the sandbox runtime LLM facade, so upstream credentials remain on the daemon.

### Daemon `models.json`

The daemon loads `$DATA_ROOT/models.json` once during startup. A missing file is valid: catalog-owned entries are treated as absent while existing system and environment Provider defaults remain unchanged. Restart the daemon after editing the file.

```json
{
  "default": "gateway/deepseek-v4-flash",
  "providers": {
    "gateway": {
      "baseUrl": "https://gateway.example.com/api/openai",
      "protocol": "chat_completions",
      "apiKey": "${GATEWAY_API_KEY}",
      "models": [
        {
          "id": "deepseek-v4-flash",
          "maxOutputTokens": 8192
        }
      ]
    },
    "openai": {
      "baseUrl": "https://api.openai.com/v1",
      "protocol": "responses",
      "apiKey": "$OPENAI_API_KEY"
    }
  }
}
```

`default` is an optional `provider/model` reference. Each Provider requires `baseUrl` and one of `responses`, `chat_completions`, or `anthropic_messages`. `apiKey` and header values may be literals or complete `$NAME` / `${NAME}` references resolved from the daemon environment at startup. Invalid JSON, unknown fields, unsupported protocols, invalid limits, and unresolved references fail daemon startup.

The optional `models` array adds per-model metadata and behavior: `id`, `name`, `baseUrl`, `protocol`, `headers`, and the positive integer `maxOutputTokens`. A model-level `protocol` must remain in the Provider's protocol family: OpenAI Providers (`responses` or `chat_completions`) allow `responses` and `chat_completions`, while Anthropic Providers (`anthropic_messages`) allow only `anthropic_messages`. These attributes belong to the specific Provider/model deployment, so Providers that share a model ID do not overwrite one another. The array is not an allowlist. For a configured `gateway` Provider, `gateway/a-model-not-listed-here` is still forwarded as the literal upstream model ID using Provider defaults.

All compatible coding agents and `scheduler.llm` use this catalog for agent-compose Provider routing and model selection; it does not replace an agent's native model-capability catalog. A complete Agent-level `LLM_API_ENDPOINT`, `LLM_API_PROTOCOL`, and `LLM_API_KEY` configuration remains the higher-priority compatibility path. The daemon's complete `LLM_*` configuration remains the default ahead of `models.json.default`. A catalog Provider ID that conflicts with an existing non-catalog Provider causes startup to fail without overwriting the existing configuration.

### `image`

```yaml
agents:
  reviewer:
    image: chaitin/agent-compose-guest:latest
```

At runtime, the selected driver must be able to obtain this image. When `build` is also configured, `image` becomes one of the build output tags. `agent-compose build` fails if neither `image` nor `build.tags` provides a tag.

GitHub CI publishes these images to Docker Hub:

| Image | Purpose | Dockerfile | Platforms |
| --- | --- | --- | --- |
| `chaitin/agent-compose` | Control-plane daemon | `Dockerfile` | `linux/amd64`, `linux/arm64` |
| `chaitin/agent-compose-guest` | Sandbox guest runtime | `guest-images/Dockerfile.agent-compose-guest` | `linux/amd64`, `linux/arm64` |
| `chaitin/agent-compose-guest:archlinux` | Optional Arch Linux sandbox guest runtime | `guest-images/Dockerfile.agent-compose-guest-archlinux` | `linux/amd64` |

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

Empty and duplicate entries are removed. Each real capset ID must match OctoBus's `^[a-zA-Z][a-zA-Z0-9_-]{0,62}$` rule. An unqualified value such as `legacy-capset` uses the daemon-wide OctoBus configuration, preserving the behavior of existing project files. A qualified value such as `internal/engineering` selects `octobus_servers.internal`; `internal` is used only by agent-compose to choose the upstream server, and OctoBus receives `engineering` as the capset ID. A qualified entry whose server is not declared, contains more than one `/`, or has an invalid real capset ID is a validation error.

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

`password` and `token` cannot contain plaintext. During `config` or `up`, the CLI resolves their exact `${NAME}` references from the project dotenv/process environment before submitting the project to the daemon; a reference whose variable is absent is kept as-is and resolved at clone time instead of failing. User-facing normalized output and project APIs redact the resolved credentials. Remote ZIP downloads are restricted to HTTP(S) and are subject to size, archive, and network-address safety checks.

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

### `sandbox`: stopped runtime lifecycle

By default, stopping a sandbox removes its driver runtime and private writable state after confirming the stop. Sandbox metadata, logs, workspace, and declared durable mounts remain, and `resume` creates a fresh runtime. To restart the same container, box, or microVM sandbox, retain its private runtime state explicitly:

```yaml
sandbox:
  stopped_runtime_policy: retain
```

`stopped_runtime_policy` accepts only `retain` or `remove` (the default):

- `retain` keeps the stopped runtime and its private writable layer. Resume requires that same runtime; unexpected runtime loss is not silently recreated.
- `remove` explicitly deletes the stopped container, BoxLite box/private disk, or Microsandbox sandbox/private qcow2 overlay. Sandbox metadata, events, logs, workspace, and declared durable mounts remain. Resume creates a new runtime, so data stored only in the private writable layer is lost.

The effective policy is snapshotted when the sandbox is created. Editing the project changes only new sandboxes. Legacy sandboxes without a policy snapshot continue to retain their runtime. `agent-compose inspect sandbox <sandbox> --json` exposes the lifecycle record through `stopped_runtime_policy`, `stopped_runtime_state`, `stopped_runtime_last_error`, and `stopped_runtime_released_at`. The states are:

- `retained`: no intentional runtime release is in progress; a stopped sandbox is expected to resume the retained runtime.
- `release_pending`: release intent is durable, but stopping, runtime removal, or the ownership update has not been fully confirmed. The daemon retries this state after an interruption, and resume completes the pending release before creating a fresh runtime.
- `released`: runtime removal and the ownership update both completed. Resume creates a fresh runtime.

For `remove`, the daemon first persists `release_pending`, then confirms a driver stop when the lifecycle record contains a start or start attempt newer than the last confirmed stop, even if the coarse VM status is `failed` rather than `running`. Only then does it remove the runtime and mark the record `released`. This ordering prevents a partially started runtime from being skipped or destructively released without a confirmed stop. The on-disk ownership-record layout is internal recovery state and is not a stable operator-facing format.

### `scheduler`

A scheduler uses either declarative `triggers` or JavaScript `script`; the two forms are mutually exclusive.

| Field | Type | Default | Purpose |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | Enables this agent's scheduler. Disabling the Agent also prevents its scheduler from being enabled. |
| `sandbox_policy` | string | `new` | Scheduler default sandbox policy: `new` or `sticky`. |
| `concurrency_policy` | string | `skip` | Overlapping run policy for the entire agent scheduler: `skip` or `parallel`. |
| `model` | string | Agent model | Default `provider/model` used by `scheduler.llm`; a call-level `model` or `LLM_MODEL` takes precedence. |
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
      timezone: Asia/Shanghai
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
| `cron` | string | One of four | Five-field cron expression with optional seconds and robfig/cron descriptors. By default it uses the daemon's local timezone. |
| `timezone` | string | No | IANA timezone for a `cron` trigger, such as `UTC` or `Asia/Shanghai`. `Local` and an omitted value use the daemon's local timezone. It is invalid for other trigger kinds. |
| `interval` | duration | One of four | Positive period such as `30s`, `5m`, or `2h`; registration precision is at least 1 ms. |
| `timeout` | duration | One of four | Positive one-shot delay such as `15s`; registration precision is at least 1 ms. |
| `event.topic` | string | One of four | The nested `topic` is the non-empty subscribed topic, for example `webhook.github.push`. |
| `prompt` | string | No | Prompt sent to the agent. An empty prompt becomes `Run agent <name>.` |
| `sandbox_policy` | string | No | `sticky` or `new` for this generated agent call. If omitted, no call-level override is emitted. |

The daemon local timezone comes from `TZ` when it is set, otherwise from the operating system's `/etc/localtime`. The shipped Docker Compose deployment mounts the host's `/etc/localtime` read-only. Set `TZ` in `.env` only when the daemon should intentionally differ from the host. Restart the daemon after changing its timezone. Stored timestamps remain UTC.

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

### Duplicate project names during upgrade

An upgrade does not merge projects that already have the same name, because matching names and compose paths do not prove that their histories are equivalent. One project keeps the original name; active projects are preferred, then the most recently updated project, with creation time and full ID as deterministic tie-breakers. The others are renamed to the next available `<name>-N` value. Existing numeric suffixes are skipped.

Every project ID and its revisions, agents, schedulers, runs, sandboxes, and volume associations remain attached to the same project. Use `agent-compose project ls` after the upgrade to see the assigned names. To update a suffixed project later, set that assigned name in its compose file.

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
    image: chaitin/agent-compose-guest:latest
    driver:
      docker: {}
```

Validate, apply, and run it:

```bash
agent-compose config --quiet
agent-compose up
agent-compose run reviewer --prompt "Review this project."
```

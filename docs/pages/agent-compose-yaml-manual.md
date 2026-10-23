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

policies:
  locked-down:
    stopped_runtime_policy: retain
    network:
      default: deny-all
      allow:
        - host: api.github.com
          port: 443

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
      - name: review
        provider: file
        path: ./skills/review
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
    policy: locked-down           # selected from `policies`; never merged with it
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

  # The library is a catalog, not a default: this agent declares its own block
  # instead of naming an entry, and no entry is merged into it.
  spot-checker:
    provider: codex
    policy:
      network:
        default: allow-all

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
| `policies` | map | No | Named policy blocks that agents select by name instead of repeating. |
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

A first-party LLM credential declared here is not handed to the sandbox: the daemon imports it into its own LLM connections and proxies the run, so the sandbox only ever receives a run-scoped facade token. The recognized names are `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `OPENAI_API_KEY`, `CODEX_API_KEY`, `DEEPSEEK_API_KEY`, `OPENROUTER_API_KEY`, and `LLM_API_KEY` (optionally with `LLM_API_PROTOCOL` and `LLM_API_ENDPOINT`). The declaration's endpoint variables — `LLM_API_ENDPOINT`, `LLM_API_PROTOCOL`, `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_ENDPOINT`, `OPENAI_BASE_URL`, `DEEPSEEK_BASE_URL`, `OPENROUTER_BASE_URL` — belong to the absorbed declaration and do not reach the sandbox either; the sandbox is given the facade address that serves the same protocol. `AZURE_OPENAI_API_KEY` is recognized but cannot be proxied: the daemon removes it from the sandbox environment rather than forwarding a credential it cannot serve, so the agent never sees it either. Only a credential-looking name the daemon does not recognize at all (any other `*_API_KEY` or `*_AUTH_TOKEN`) is passed through to the sandbox, where anything running there can read it. Project checks report which of the three cases each declaration falls into and recommend the daemon's LLM configuration, which is where a credential is managed, rotated, and shared deliberately. Every credential the daemon keeps off the sandbox — absorbed or only recognized — is redacted in project and agent views whether or not the declaration set `secret: true`: the variable name stays visible so an operator can see what they declared, but the value is shown as `********`. Endpoint variables such as `OPENAI_BASE_URL` are addresses rather than credentials and stay visible. The declaration the daemon resolved is not affected, and the facade address and token a run actually uses live only in the run's environment and are never persisted.

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
| `provider` | string | Required | `file`, `git`, or `http`. |
| `url` | string | Required for `git` and `http` | Git clone URL, or an `http`/`https` URL to a ZIP archive. It is forbidden for `file`. |
| `ref` | string | Optional for `git` | Git branch, tag, or commit. `http` workspaces do not support a ref. |
| `path` | string | Required for `file` | Source path relative to the compose directory; it cannot escape the project root. Git workspaces do not support a repository subpath. For `http`, an optional directory inside the archive. |
| `format` | string | Required for `http` | Must be `zip`; the URL is downloaded and extracted as an isolated workspace. `path`, when set, must select an existing directory inside the archive. |
| `target` | string | Optional | Destination below the sandbox workspace root. Defaults to `.`. |
| `mode` | string | Optional | `copy` (default) creates an isolated workspace. `mount` maps a local `file` source directly into a Docker sandbox. |
| `read_only` | bool | Optional for `mount` | Defaults to `false`. Set `true` to prevent guest writes through the workspace mount; `true` is invalid with `copy`. |
| `username` | string | Optional for `git` | Git HTTP username. |
| `password` | string | Optional for `git` | Git password as an exact environment reference such as `${NAME}`. |
| `token` | string | Optional for `git` | Git token as an exact environment reference such as `${NAME}`. |

A local workspace defaults to `mode: copy`: it is copied into an isolated snapshot for each project run, and Agent changes do not modify the source directory. Omitting `mode` and explicitly setting `copy` have the same behavior.

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
  remote-release:
    provider: http
    url: https://example.com/releases/service.zip
    format: zip
    path: service
    target: .
```

### Fetch a ZIP workspace over HTTP

An `http` workspace downloads a ZIP archive when the run prepares its sandbox and extracts it into the run workspace, so every run starts from the content the URL served:

```yaml
workspaces:
  remote-release:
    provider: http
    url: https://artifacts.example.com/releases/service.zip
    format: zip
    token: ${ARTIFACT_TOKEN}
    path: service
    target: src
```

Behavior:

- Only `http` and `https` URLs are accepted, redirects are revalidated against the same rule, and a redirect may not downgrade `https` to `http`, so credentials are never resent over cleartext. The request ignores proxy environment variables so a proxy cannot resolve the target on the daemon's behalf. Internal hosts are supported: the URL may resolve to a loopback or private address, such as another Docker Compose service name, a published loopback port, or a host on the daemon's private network. Skill sources follow the same rules.
- `path` must name a directory that exists inside the archive. A `path` that names a file, or that is absent from the archive, fails the run instead of producing an empty workspace, and a `path` that escapes the archive (`../`, an absolute path) is rejected.
- Archive entries are validated before they are written: entries that escape the destination and symlink entries are rejected, and entry permissions are sanitized (executable bits are preserved, group and other write bits are cleared, and setuid, setgid, and sticky bits are dropped).
- Per-workspace limits: 256 MiB downloaded archive, 1 GiB expanded content, 100,000 entries, a 100:1 compression ratio once an archive expands beyond 64 MiB, and a 10 minute fetch timeout. Exceeding any limit fails the run.
- `username`, `password`, and `token` accept exact environment references such as `${NAME}`; a token is sent as `Authorization: Bearer`, and basic credentials are only used when no token is configured.
- `mode: mount` is not supported for `http`; the archive is always copied into the run workspace.

Workspace selection follows these rules:

- Top-level `workspaces` entries are named definitions only; they are never assigned to an agent automatically.
- When an agent omits `workspace`, the run has no configured workspace, regardless of how many project workspaces exist.
- To use a project workspace, the agent must explicitly select it with `workspace.name`, or define an inline workspace.
- An explicit empty `workspace: {}` is invalid; omit the key to configure no workspace.

### Mount a local workspace

Use `mode: mount` to reuse an existing local directory without creating a run snapshot or copying its files into each sandbox:

```yaml
workspaces:
  shared-input:
    provider: file
    path: ./reference-data
    mode: mount
    read_only: true
    target: inputs
agents:
  reviewer:
    provider: codex
    driver:
      docker: {}
    workspace:
      name: shared-input
```

The example exposes the source at `/workspace/inputs` with the default guest paths. The remaining workspace stays writable for outputs. `target: .` mounts the source at the whole workspace root; when this mount is read-only, programs must write outputs elsewhere. Targets must stay inside the workspace and cannot overlap an agent volume mount. Additional volumes declared by the guest image must also stay outside the mounted subtree; overlapping image volumes cause startup to fail, while unrelated image volumes remain supported.

Mount mode supports `provider: file` with the Docker runtime only. Git sources and BoxLite, Microsandbox, or Kubernetes runtimes are rejected; an unsupported mount does not fall back to copying. The recorded project source path must identify an existing directory or compose file. The source must already exist inside the project's source directory and be visible in the agent-compose daemon's filesystem. Paths on a remote CLI client are not uploaded automatically.

For a native agent-compose daemon, supported Docker connections use a local Unix socket and Linux containers: on Linux, the Docker Engine name must match the local hostname; on macOS, the Engine must identify itself as Docker Desktop or OrbStack. Native connections over TCP (including localhost) or SSH, forwarded sockets, and other VM integrations are unsupported. For a containerized agent-compose daemon, the connected Engine must be able to inspect that container by its hostname, and the source must lie within one of its existing bind or volume mounts. The most specific containing mount determines the host source path; an unshareable nested mount, such as tmpfs, blocks mapping through its parent. Sources that exist only in the daemon container's writable layer are rejected. `DOCKER_HOST_SANDBOX_ROOT` explicitly translates sources inside `SANDBOX_ROOT`, including a workspace located there, and says nothing about any other path. Workspace mounts, bind volumes, and `local` project volumes share this path-resolution implementation. When `DOCKER_HOST_SANDBOX_ROOT` is set, any of them outside `SANDBOX_ROOT` must map through a shared daemon container mount (or, for a native daemon, a verified local Engine) as described above, and is rejected otherwise. A `local` project volume lives under `DATA_ROOT`, beside `SANDBOX_ROOT`, so in the published Compose deployment it maps through the daemon's data mount. Without `DOCKER_HOST_SANDBOX_ROOT`, bind and project volumes retain their configured host-path behavior; workspace mount additionally requires the source-sharing guarantee described above.

The default `read_only: false` is a **live, writable mapping**: Agent writes, deletions, and renames affect the original directory, and sandboxes using that directory see the same changes. Concurrent writers must coordinate. `read_only: true` blocks guest writes through this mapping; host-side changes remain visible and the directory is not an immutable snapshot. Keep `copy` for isolated edits or a stable initial snapshot.

`read_only: true` requires Docker Engine API 1.44 or newer and recursive read-only bind support from the Engine's Linux kernel (5.12 or newer, including the VM kernel on macOS). agent-compose explicitly requires recursive read-only enforcement, including existing source submounts. An older API or a runtime unable to enforce it causes startup to fail; it does not fall back to a partially read-only mount. See Docker's [recursive mount requirements](https://docs.docker.com/engine/storage/bind-mounts/#recursive-mounts).

Stop, resume, and sandbox removal preserve the external source. Resume reuses the persisted source and delivery settings; changing the project does not convert an existing copied workspace into a mount. A missing or invalid source fails explicitly on creation or restart. Workspace-source upload/download APIs continue to manage copied workspace presets, rather than granting access to mounted project directories.

This option controls the declared workspace source. A `.codex` directory inside that source follows the workspace mode. The sandbox's separate provider home, such as `~/.codex`, holds private configuration, credentials, and history and stays isolated between sandboxes.

### Automatic content reuse

Copy mode releases its internal run input after the private sandbox workspace reaches persisted Ready. Inputs needed for a pending or failed preparation remain available for retry; copied workspace presets and externally mounted directories are preserved. File copies use filesystem cloning where supported and otherwise copy bytes, retaining independent writable files. Existing unmarked snapshots from older versions are not deleted automatically.

Agent skills also remain private and writable. Preparation compares the source and actual sandbox content, including executable permissions, and leaves unchanged files in place. Modified content is staged before publication; modifications made inside a sandbox are reconciled against the declared skills on the next preparation. A valid Claude skills alias is reused. Kubernetes compares the guest contents before transferring a single canonical tree and reconciling the alias, so an unchanged daemon-side directory does not hide a missing or modified guest copy. Provider credentials and histories are never converted into a shared skill cache.

The daemon API is an administrative trust boundary. API clients can choose project source paths, and existing bind volumes can expose host directories. The workspace's project-root check validates the source contract; it is not a per-project access-control boundary for untrusted API clients. Configure the listening address and `AGENT_COMPOSE_AUTH_TOKEN` for trusted administrators.

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

The Pi provider in the default guest supports remote MCP servers over streamable
HTTP only; it does not support the legacy SSE transport. Declare remote servers
used by Pi with `transport: http`; declaring `transport: sse` fails the Pi run
with an unsupported-transport error instead of silently treating the server as
HTTP.

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
| `driver` | string | `local` | Volume driver. `local` uses a daemon-host directory; `k8s` creates a Kubernetes PersistentVolumeClaim when the agent uses the k8s runtime. |
| `external` | bool | `false` | References an existing volume instead of creating a project-owned volume. |
| `labels` | map[string]string | Empty | Volume labels. Keys and values are trimmed. |
| `options` | map[string]string | Empty | Driver options. For `k8s`, supported keys are `size` (default `1Gi`), `storage_class`, `access_mode` (default `ReadWriteOnce`), and `namespace` (default `K8S_NAMESPACE`/`default`). PVCs use the daemon's Kubernetes cluster. |

The volume map key must be a stable identifier. Agents mount project volumes through `agents.<name>.volumes`.

## `policies`: project policies

`policies` is the project's policy library. An entry is a policy block, exactly like the one an agent can declare inline, and an agent selects it by writing the name:

```yaml
policies:
  locked-down:
    stopped_runtime_policy: retain
    network:
      default: deny-all
      allow:
        - host: api.github.com
          port: 443

agents:
  reviewer:
    provider: codex
    policy: locked-down
```

A name is resolved when the project is normalized, the same way an agent `mcp_servers` reference is resolved: every agent ends up carrying its effective policy block. `policy: locked-down` and repeating that block inline normalize to the same specification and the same spec hash, so moving a shared block into `policies` changes nothing the engine enforces.

The library is a catalog, and an agent declaration is a selection from it or a block of its own. The two never compose:

| Agent declaration | Effective policy |
| --- | --- |
| `policy: locked-down` | Exactly the `locked-down` entry. |
| `policy: {…}` inline | Exactly the inline block. |
| no `policy` key | No policy: unrestricted egress, `remove` on stop. |

One key has one shape — a name is a scalar and a block is a mapping — so an agent cannot state both, and every agent has exactly one effective policy. A library entry is not merged into an agent that declares its own block, nothing is inherited, and there is no project-wide default today. "One policy for most agents, one exception" therefore needs no composition rule: the exception declares its own block and the rest name the entry. Merging two blocks would produce a third block that appears nowhere in the file, and a reader could no longer tell what a sandbox enforces from one declaration.

- A project policy defines a block; it cannot name another policy, so there are no chains to resolve.
- Every entry is validated even when no agent selects it, so a broken definition fails the project instead of waiting for the first agent that references it.
- An undefined name is an error and never falls back to an undeclared policy, which would leave egress unrestricted.
- The library is an authoring convenience and is not part of the normalized specification: `agent-compose config` shows each agent's effective `policy` block rather than the name it selected.

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
| `provider` | string | `codex` | Agent provider: `codex`, `claude`, `opencode`, `pi`, or `dsh`. Compatibility aliases are normalized at persistence boundaries. |
| `model` | string | Daemon default model | Opaque model name. Omitting it selects the daemon's default model. Supports `${NAME}` interpolation. |
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
| `policy` | object | Remove stopped runtime | Declared runtime policies: the stopped-runtime lifecycle and the outbound network policy. |
| `sandbox` | object | — | Deprecated spelling of `policy`. Still accepted; declaring both is an error. |
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

Canonical providers are `codex`, `claude`, `opencode`, `pi`, and `dsh`. Compatibility normalization also accepts `claude-code` / `claude_code`, `open-code` / `open_code`, `pi-agent` / `pi_agent`, and `deepseek` / `deepseek-harness` / `deepseek_harness`; new files should use canonical names.

Pi, dsh, and opencode are multi-model agents, so their model is worth declaring explicitly:

```yaml
agents:
  reviewer:
    provider: pi
    model: gpt-5.4
```

The model stays opaque: the daemon never splits it in order to select an upstream. Which connection serves it is daemon configuration — the connections that declare the model, the default model's owning connection, or the only configured connection. When several connections could serve one model, the daemon prefers a connection the agent can speak natively, so the call is passed through instead of converted; connections that speak the same protocol are interchangeable and one is chosen at random, so several upstreams offering the same model never fail a run. For pi, dsh, and opencode the daemon composes the `<llm-provider-id>/<model>` reference those CLIs expect from the selected connection, so the compose file never writes one. The retired `model: <connection>/<model>` form is rejected when its prefix names a connection that serves the remainder.

An Agent-level `LLM_API_*` environment is the higher-priority compatibility path: an agent that publishes its own upstream is served by it, and the model it injects may itself be a qualified name (for example a gateway that publishes `<provider>/<model>` logical names). Such a value reaches the upstream verbatim.

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

`default` is an optional `provider/model` reference. Each Provider requires `baseUrl` and one of `responses`, `chat_completions`, or `anthropic_messages`. `apiKey` and header values may be literals or complete `$NAME` / `${NAME}` references resolved from the daemon environment at startup. Invalid JSON, unknown fields, unsupported protocols, invalid limits, and unresolved references fail daemon startup. `enabled` is optional and decides whether the connection can serve: absent keeps the rule that a declaration without an `apiKey` is stored disabled, `true` declares a connection that needs no credential (a local endpoint, for example), and `false` disables a connection that has one.

The optional `models` array adds per-model metadata and behavior: `id`, `name`, `baseUrl`, `protocol`, `headers`, and the positive integer `maxOutputTokens`, which must not exceed 2147483647 because the API reports it as a 32-bit integer. Provider-level and model-level header names must be valid HTTP field names, since the transport refuses anything else when a request is sent, and a declaration that uses one is rejected as invalid input. A model-level `protocol` must remain in the Provider's protocol family: OpenAI Providers (`responses` or `chat_completions`) allow `responses` and `chat_completions`, while Anthropic Providers (`anthropic_messages`) allow only `anthropic_messages`. These attributes belong to the specific Provider/model deployment, so Providers that share a model ID do not overwrite one another. The array is not an allowlist. For a configured `gateway` Provider, `gateway/a-model-not-listed-here` is still forwarded as the literal upstream model ID using Provider defaults.

All compatible coding agents and `scheduler.llm` use this catalog for agent-compose Provider routing and model selection; it does not replace an agent's native model-capability catalog. A first-party credential declared in project `variables` or agent `env` is imported into this same catalog as a daemon-owned connection and served through the facade, so the key stays on the daemon and the sandbox only sees a run-scoped facade token; this is the compatibility path for a complete `LLM_API_ENDPOINT`, `LLM_API_PROTOCOL`, and `LLM_API_KEY` declaration as well as for the vendor-specific names listed under `variables` and `env`. Optional daemon or Agent `LLM_API_HEADERS` is a JSON object of static extra HTTP headers on that env-backed Provider; it does not replace catalog `headers`, and the raw value is not exposed to the guest runtime. The variable is shared by env-backed OpenAI and Anthropic Providers, so each configured header must be safe to send to every configured upstream. The daemon's `LLM_*` configuration is a bootstrap fallback for the default model rather than a precedence winner; connection selection is a separate axis that does not rank configuration sources, so two connections that serve one model over the same protocol are peers regardless of where each came from. The default model is resolved in three steps: a `models.json` `default` reference wins whenever the file declares one; otherwise the reference set through `LLMService.SetDefaultModel` applies and survives restarts; only when neither exists does the environment's default model take over, and only while an enabled connection actually serves it — a flag left behind by a connection another source took over counts as no default. A model the environment flags as the default that no enabled connection serves is treated the same way: a run that declares no model is reported as having no model instead of being routed to a connection that never served it. A `models.json` `default` must name a connection that the file itself declares and enables. A reference to a connection it does not declare, to one stored disabled (no `apiKey` and no explicit `enabled`), or to one another source owns is not applied and not stored: the rest of the file is projected, the reference is cleared when it cannot resolve either, the daemon logs what it dropped and why, and startup continues — one unusable default must not keep the whole daemon, its cleanup, scheduler, and deletion recovery from starting. A stored reference that still resolves — one an operator chose through the LLM service, for example — is kept. A `models.json` that omits `default` leaves the stored reference alone, so a default chosen at runtime is not wiped by the next startup; an explicit `"default": ""` clears it, as does `LLMService.ClearDefaultModel`. The environment projection also never takes over an ID another source currently serves. An ID belongs to a source only while that source can serve it: a Provider ID stays while its connection is enabled — a `models.json` declaration without an `apiKey` is persisted disabled and says so deliberately with `"enabled": true` when it needs no credential — and a model ID stays while an enabled connection another source owns binds it. An ID nothing can serve is not an owner, so the environment takes it over instead of yielding and leaving the deployment with no usable connection; that includes an ID only the environment's own connection serves, because the file projection reclaims the identity's scope on every startup while the connection stays the environment's. Taking an ID over never strands the file: the file projection rewrites a row whatever scope it carries, so the next startup projects the declaration from the file again, and once the file can serve it the environment yields with the usual warning. A Provider or model ID the file stops declaring is released instead: its row stays — a stored default model references it, and a later file can restore the declaration — but the row is disabled, gives up the catalog scope, and therefore no longer owns the ID, which the environment may then take over. Because the file is the daemon's own declaration and the environment is the fallback, declaring such an ID in `models.json` again takes the row back, and the environment projection that follows yields with the usual warning instead of failing startup; the same applies to a Provider or model ID the environment materialized on its own. Only a connection an explicit source owns is out of the file's reach: a `models.json` Provider ID that conflicts with an LLM-service connection or a run-declared one fails startup without overwriting the existing configuration. When no default model is configured at all, the daemon logs a warning at startup and agents that declare no model keep their own authentication.

At startup, and again after every `LLMService` provider write, the daemon verifies each enabled connection: it calls the connection's `/v1/models` endpoint and then sends one minimal `ping` request — with a one-token output limit — per protocol the connection's family can serve. Each protocol is probed independently, so an endpoint that serves both `responses` and `chat_completions` is reported as supporting both instead of stopping at the first success. The probe is a diagnostic only: it never changes routing or stored configuration, it does not use `/v1/models` to decide protocol support, and its findings are logged rather than written to the database. A fresh verdict is held in memory for a short window, keyed by endpoint and credential, so repeated triggers do not repeat identical traffic; it is lost on restart and re-derived then. Results appear as `endpoint <url> supports model list: <ids>` and `endpoint <url> supports <protocol> protocol`, with a distinct warning for a protocol the probe proved absent, an unreachable endpoint, a timeout, or a rejected credential; a rejected credential leaves protocol support unknown. After a provider write the check runs synchronously, bounded by `LLM_PROBE_TIMEOUT` (default `15s`). The startup sweep runs in the background under one such deadline of its own, so it never delays startup or anything that starts after it; when the deadline ends the sweep stops and logs how many connections it could not reach, and a verdict the deadline cut short is not cached, so the next trigger probes again. Shutdown ends a sweep in progress and waits for it to stop, bounded by the shutdown budget rather than by the probe timeout. A connection that declares models is probed with one of them, on the endpoint that model actually uses: a model-level `baseUrl` is the endpoint a real request for that model reaches, so a deployment that gives every model its own endpoint is judged on the model's endpoint rather than on the connection's. A connection that declares none is probed with a model the endpoint advertises. The most recent verdict is also reported as `capabilities` on `GetProvider` and `ListProviders`: `probes` keeps a protocol the daemon proved unsupported distinct from one it could not decide, `models` lists what the endpoint advertised, and `probedModel` / `probedAt` name the model and time. `capabilities` is absent until the daemon has probed the connection, and it disappears on restart because nothing about probing is persisted. Probing is on by default, so an upgrade adds this traffic to every enabled upstream; set `LLM_UPSTREAM_PROBE=false` to disable probing entirely.

### Managing LLM providers through RPC

`agentcompose.v2.LLMService` exposes `CreateProvider`, `GetProvider`,
`ListProviders`, `UpdateProvider`, and `DeleteProvider` for upstream model
endpoints and API keys. These are separate from coding-agent `provider: codex`
or `provider: pi` settings.

For example, call `CreateProvider` using Connect JSON:

```json
{
  "provider": {
    "id": "team-gateway",
    "baseUrl": "https://gateway.example.com/v1",
    "protocol": "responses",
    "apiKey": "your-upstream-api-key"
  }
}
```

The request path is `/agentcompose.v2.LLMService/CreateProvider`, using existing
daemon API authentication. Then point an Agent at the model with its bare `model`
value (for example `model-id`); models do not need to be enumerated. The daemon
picks the connection: a model the created connection declares is served by it,
otherwise the default model's owning connection or the only configured connection
is used. Several connections serving one model are not an error — the daemon
prefers one the agent can speak natively and otherwise picks among them. Do not
write `<connection>/<model-id>`: that form is retired and rejected when its prefix
names a connection that serves the remainder. A slash-containing upstream model
id (for example `meta-llama/Llama-3.1-8B-Instruct`) is still forwarded verbatim.
Other methods share the service path prefix.

- IDs are immutable, 1–128 ASCII letters, digits, dots, underscores or hyphens,
  starting with a letter or digit. `default`, `anthropic`, and session environment
  IDs are reserved for the daemon.
- `protocol` must be `responses`, `chat_completions`, or `anthropic_messages`.
  `baseUrl` must be an absolute HTTP(S) URL without user credentials, query or fragment.
- `auth` names an explicit credential presentation that overrides the protocol
  convention: `bearer` (`Authorization: Bearer`) is the convention for the OpenAI
  protocols and `x-api-key` for `anthropic_messages`. Set `"auth": "LLM_PROVIDER_AUTH_BEARER"` when
  a gateway serves the Anthropic Messages protocol but authenticates with
  `Authorization: Bearer`; set `"auth": "LLM_PROVIDER_AUTH_X_API_KEY"` for the reverse. The override
  changes only the header, not the wire protocol. An unknown value is rejected.
  An explicit choice is preserved across protocol changes, even when it matches
  the current protocol's convention. Responses report that stored choice; an
  unspecified value means the connection follows the protocol convention.
- `apiKey` is literal, without environment interpolation. Create requires a
  nonempty key. On update, omission preserves it, a nonempty value rotates it,
  and an empty value is invalid. Responses expose only `apiKeySet`, never the key.
- `models` optionally declares the model names an operator types, each with
  `id`, `name`, `protocol`, `baseUrl`, `headers`, and a positive
  `maxOutputTokens` of at most 2147483647. A model-level `protocol` must stay in
  the provider's protocol
  family. Declared names are routing metadata, not an allowlist: a bare `model`
  value the connection does not list is still forwarded as written. `GetProvider`
  and `ListProviders` return the declared set ordered by model ID. On
  `UpdateProvider`, an omitted `models` field preserves the declared set and a
  present empty array clears it. A retained set is revalidated when `protocol`
  changes: a model pinned to the previous protocol family makes the update fail
  with `InvalidArgument` and leaves the stored protocol and models alone, so
  restate `models` to move a connection to another family. Same-name models are
  shared across connections by ID: one another source already owns keeps its owner
  and metadata, and the new connection only gains a binding of its own.
- Create defaults an empty `name` to the ID and an omitted `enabled` field to
  `true`. `anthropic_messages` providers send `anthropic-version: 2023-06-01` by
  default; other protocols send no extra headers.
- `UpdateProvider` uses the same `provider` object. Omitted `name`, `baseUrl`,
  `protocol`, `apiKey`, `auth`, and `enabled` preserve stored values. Present
  nonempty `apiKey` rotates the key; present empty is invalid. Omitted `auth`
  preserves the stored choice even when `protocol` changes. Set
  `"auth": "LLM_PROVIDER_AUTH_UNSPECIFIED"` to clear it and follow the protocol
  convention again. Older configuration without an explicit auth choice retains
  its existing behavior; set `auth` explicitly to pin the authentication method.
- CLI: `agent-compose llm provider ls|create|inspect|update|rm`. Create requires
  `--base-url`, `--protocol`, and `--api-key`. Update sends only flags that are
  set, so `update --base-url ...` does not re-enable a disabled provider.
  `--auth x-api-key|bearer` overrides the protocol default and
  `--auth protocol-default` clears a stored override; both are accepted on either
  command. The CLI does not set `models` or the default model; use the RPC or the
  UI for those.
- `GetProvider` / `DeleteProvider` take `{"id":"team-gateway"}`.
  `ListProviders` takes `offset` / `limit`, orders by ID, includes disabled entries,
  and returns `providers` and `total`.
- These methods manage only API-owned providers, without overwriting models.json
  or environment configuration. Duplicate IDs return `AlreadyExists`, missing IDs
  return `NotFound`, and ownership conflicts return `FailedPrecondition`.
  API configuration survives restart; a models.json entry with the same ID still
  causes a startup ownership conflict.
- URL/key updates affect subsequent target resolution without restarting the
  daemon; in-flight upstream requests continue. Protocol changes may require
  restarting an Agent run to refresh client protocol configuration. Deletion
  removes provider-bound facade tokens; reusing the ID cannot revive old tokens.
- Deletion does not rewrite project model references. Unknown `provider/` prefixes
  on new requests retain the existing literal-model interpretation. Disabling
  retains configuration and tokens, allowing use again after re-enabling.

These methods do not change per-model overrides through `models` beyond what
`UpdateProvider` applies, and they never edit `models.json`. Credentials use the
existing database storage contract without additional application-level
encryption.

### Choosing the default model through RPC

`GetDefaultModel`, `SetDefaultModel`, and `ClearDefaultModel` manage the model the
daemon uses for runs that declare none, which is the same reference a
`models.json` `default` writes. `SetDefaultModel` takes
`{"model":{"providerId":"team-gateway","modelId":"model-id"}}` and requires an
existing, enabled provider; the model may be a literal the connection does not
declare, because a model ID is opaque. An unknown provider returns `NotFound`, a
disabled provider returns `FailedPrecondition`, and an empty `providerId` or
`modelId` returns `InvalidArgument` without changing the stored reference. A
run-scoped connection the daemon derived from an agent declaration is refused
with `InvalidArgument`: it carries one sandbox's own credential and must not
become the daemon default.
`GetDefaultModel` returns `model`, or omits it when no default is configured.
`ClearDefaultModel` removes the reference, after which the environment's default
model applies again if it declares one.

The reference set here survives restarts. It is replaced when `models.json`
declares a `default` of its own, which the next startup applies over it and
reports with a warning when it differs from the stored reference; when
`ClearDefaultModel` removes it; or when the connection it names is deleted or
disabled, because a reference the daemon cannot serve is not a default any run
receives. Removing a `default` from `models.json` therefore does not restore an
earlier state; the last reference the daemon stored stays in effect until one of
those happens.

### `image`

```yaml
agents:
  reviewer:
    image: chaitin/agent-compose-guest:latest
```

At runtime, the selected driver must be able to obtain this image. When `build` is also configured, `image` becomes one of the build output tags. `agent-compose build` fails unless `image`, `build.tags`, or the `--tag` flag provides at least one tag.

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

```yaml
driver:
  k8s:
    context: production
    namespace: agent-compose
```

| Driver | Child fields | Current status |
| --- | --- | --- |
| `docker` | `host` | Stable supported driver. `host` is parsed and retained; the daemon's Docker boundary is still controlled by deployment configuration. |
| `boxlite` | `kernel`, `rootfs` | Compiled into full Linux builds; runtime initialization is lazy. Child strings are trimmed. |
| `microsandbox` | `profile` | Compiled into full Linux builds; runtime initialization is lazy. The profile string is trimmed. |
| `k8s` | `context`, `namespace` | Creates sandbox Pods through Kubernetes. `context` selects a kubeconfig context; when omitted, client-go uses the kubeconfig current context or in-cluster configuration. `namespace` overrides `K8S_NAMESPACE`, whose final fallback is `default`. |
| `firecracker` | `kernel`, `rootfs` | Reserved in the parser schema. Normalization currently returns `unsupported runtime driver firecracker`, so it cannot be used. |

The k8s driver builds its client from kubeconfig: it uses an explicit
`K8S_KUBECONFIG`/`KUBECONFIG` path when set, otherwise the `~/.kube/config`
loading rules, and falls back to in-cluster configuration only when neither
resolves to a configuration. Sandbox Pods reach the daemon through the URL
configured by `K8S_RUNTIME_BASE_URL`, which overrides
`AGENT_COMPOSE_RUNTIME_BASE_URL`. The supported installation entry point is the
Helm chart at `charts/agent-compose`:

```bash
helm install agent-compose ./charts/agent-compose \
  --kube-context prod-cluster \
  --namespace team-a \
  --create-namespace
```

The release namespace becomes the default `K8S_NAMESPACE`; the chart also
renders the daemon Service DNS callback URL and the ClusterRoleBinding subject
from that namespace. Sandbox Pods do not mount the daemon data PVC or use
`hostPath`; provisioned workspace and provider home configuration are
streamed into the Pod over Kubernetes Exec.

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

A recognized first-party LLM credential declared here — `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `OPENAI_API_KEY`, `CODEX_API_KEY`, `DEEPSEEK_API_KEY`, `OPENROUTER_API_KEY`, or `LLM_API_KEY` — is the exception: the daemon imports it into its own LLM connections and proxies the run, so the sandbox receives only a facade token. An absorbed declaration takes its endpoint variables with it: `LLM_API_ENDPOINT`, `LLM_API_PROTOCOL`, `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_ENDPOINT`, `OPENAI_BASE_URL`, `DEEPSEEK_BASE_URL`, and `OPENROUTER_BASE_URL` are removed from the sandbox environment, and the managed facade address is installed in their place. Credentials the daemon recognizes but cannot proxy (`AZURE_OPENAI_API_KEY`) are removed from the sandbox environment too, and are redacted in project and agent views along with the absorbed ones. Only an unrecognized credential-looking name (any other `*_API_KEY` or `*_AUTH_TOKEN`) is passed through and can be read by anything running in the sandbox; project checks report which case each declaration falls into.

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

An agent whose `provider` is `dsh` accepts a narrower set of skill names: `^[a-z0-9]+(?:-[a-z0-9]+)*$`, lowercase letters and digits joined by single hyphens. DSH silently drops a skill named otherwise, so such a name is rejected when the project is validated. The `name` in the skill's `SKILL.md` must be the same value.

An interactive session uses the skills published into its sandbox when the sandbox was last prepared. A session attached to a sandbox that is already running does not pick up skills added, removed, or changed since; they take effect once the sandbox is prepared again, for example when a new sandbox starts.

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

A `git` or `http` skill may also come from an internal host, such as an internal GitLab or an artifact server: private and loopback addresses are accepted, because the daemon resolves that host itself. Only `http` and `https` URLs are fetched over the network; any other URL is treated as a local source and must stay under an allowed source root.

`password` and `token` cannot contain plaintext. During `config` or `up`, the CLI resolves their exact `${NAME}` references from the project dotenv/process environment before submitting the project to the daemon; a reference whose variable is absent is kept as-is and resolved at clone time instead of failing. User-facing normalized output and project APIs redact the resolved credentials. Remote ZIP downloads are restricted to HTTP(S), may point at an internal host (private and loopback addresses are accepted), and are subject to size, archive, and content checks.

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

The Kubernetes (`k8s`) driver does not support local `bind` mounts. Bind sources
refer to paths on the daemon host, which are not available inside a Pod. Use a
named project volume with `driver: k8s` (backed by a PVC) instead; a bind mount
is rejected during project validation.

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

Inline workspaces accept the same `mode` and `read_only` fields as top-level definitions. To inherit a project entry, set only `name`; its delivery mode and permissions are inherited together. A name-only reference cannot override `mode` or set `read_only: true`. To use different settings, define another project workspace or provide a complete inline source. If `name` is combined with source fields or `target`, the object is an inline workspace and must include its own valid source; it does not merge fields from a project entry.

### `policy`: declared runtime policies

`policy` groups everything this agent declares about how its sandboxes behave at runtime: the stopped-runtime lifecycle and the outbound network policy. Later runtime policies belong in this same block rather than in new top-level agent keys.

An agent either declares that block inline or selects a project `policies` entry by name:

```yaml
policy: locked-down        # a project policy, defined under `policies`
```

```yaml
policy:                    # or the block itself
  stopped_runtime_policy: retain
```

The fields below describe a policy block, whichever shape declares it. A name that no project policy defines is an error; it is never treated as no policy at all.

> **`sandbox` is the deprecated spelling of `policy`.** It is still accepted so projects written before the rename keep working, and both keys accept the same fields, but new and edited projects should use `policy`. Declaring both in one agent is an error rather than a silent choice between two blocks that could disagree.

#### `stopped_runtime_policy`: stopped runtime lifecycle

By default, stopping a sandbox removes its driver runtime and private writable state after confirming the stop. Sandbox metadata, logs, workspace, and declared durable mounts remain, and `resume` creates a fresh runtime. To restart the same container, box, or microVM sandbox, retain its private runtime state explicitly:

```yaml
policy:
  stopped_runtime_policy: retain
```

`stopped_runtime_policy` accepts only `retain` or `remove` (the default):

- `retain` keeps the stopped runtime and its private writable layer. Resume requires that same runtime; unexpected runtime loss is not silently recreated.
- `remove` explicitly deletes the stopped container, BoxLite box/private disk, or Microsandbox sandbox/private qcow2 overlay. Sandbox metadata, events, logs, workspace, and declared durable mounts remain. Resume creates a new runtime, so data stored only in the private writable layer is lost.

The Kubernetes driver does not accept `retain`: Kubernetes Pods have no
stopped-but-retained state, so stopping a sandbox deletes its Pod. Use `remove`
to recreate a Pod from its image and durable mounts on resume. When subsequent
scheduler runs must reuse the same running Pod, use `sandbox_policy: sticky`;
sticky completion keeps the Pod running and is distinct from stopped-runtime
retention.

The effective policy is snapshotted when the sandbox is created. Editing the project changes only new sandboxes.

The daemon also records the outbound network policy a runtime was created under. Starting a sandbox only *ensures* a runtime, so when the driver starts an existing runtime whose egress configuration does not match the declaration now in force, the daemon reports it: a warning when the running sandbox is not decided by that declaration (its runtime keeps no egress configuration, or applies one the daemon does not control), and an informational line when it already is (the forward proxy reads the declaration on every request). Editing a declaration reaches a running sandbox only through the mechanisms listed under `policy.network`; the report never claims more than that. Legacy sandboxes without a policy snapshot continue to retain their runtime. `agent-compose inspect sandbox <sandbox> --json` exposes the lifecycle record through `stopped_runtime_policy`, `stopped_runtime_state`, `stopped_runtime_last_error`, and `stopped_runtime_released_at`. The states are:

- `retained`: no intentional runtime release is in progress; a stopped sandbox is expected to resume the retained runtime.
- `release_pending`: release intent is durable, but stopping, runtime removal, or the ownership update has not been fully confirmed. The daemon retries this state after an interruption, and resume completes the pending release before creating a fresh runtime.
- `released`: runtime removal and the ownership update both completed. Resume creates a fresh runtime.

For `remove`, the daemon first persists `release_pending`, then confirms a driver stop when the lifecycle record contains a start or start attempt newer than the last confirmed stop, even if the coarse VM status is `failed` rather than `running`. Only then does it remove the runtime and mark the record `released`. This ordering prevents a partially started runtime from being skipped or destructively released without a confirmed stop. The on-disk ownership-record layout is internal recovery state and is not a stable operator-facing format.

### `policy.network`: declared outbound network policy

`network` declares what this agent's sandboxes may reach. **It is optional, and an undeclared agent policy is not deny**: when `policy.network` is absent, the agent adds no restriction of its own. A daemon-wide policy configured with `EGRESS_GLOBAL_POLICY_FILE` still applies as an outer bound; without one, outbound access stays unrestricted exactly as before. `default: deny-all` is a policy that takes effect only because the block was declared; it is not an engine default.

| Field | Type | Default | Purpose |
| --- | --- | --- | --- |
| `default` | string | `allow-all` | Selects the mode and the action for traffic no entry matches: `deny-all` is allowlist mode, `allow-all` is denylist mode. `deny` is not an accepted spelling; use `deny-all`. A `deny` entry wins over an `allow` entry in either mode. |
| `allow` | list | Empty | Explicit allowances. They restrict only in allowlist mode (`default: deny-all`), or as exceptions when no denial matches. |
| `deny` | list | Empty | Explicit denials. They restrict in either mode and are applied before every allowance. |

Older files need a one-line migration: the removed `default: deny` spelling now fails with `network default "deny" is no longer supported; use "deny-all"`, and a `protocol` field inside an `allow` or `deny` entry fails with `protocol was removed from network entries`. A stored canonical spec that carries a `protocol` other than the field's default `any` is rejected on read with the agent and entry path, so re-apply the project after removing the field; the default value is accepted and dropped, because it leaves the entry's meaning unchanged. A client built before the field was removed is refused on the API path when it sends a protocol other than `any`, because the wire type no longer carries the field. Entries match any protocol now. The removed field never made the engine inspect L7 payloads, but it did narrow which proxy scheme the entry covered (an entry declared `https` matched only requests the forward proxy classified as https). Without the field the entry covers any scheme on the same host and port, which is why a declaration that relied on the distinction is rejected rather than migrated silently.

Each `allow` entry:

| Field | Type | Default | Purpose |
| --- | --- | --- | --- |
| `host` | string | Required | A host name, a label-wise host pattern, an IPv4 or IPv6 literal, or an IPv4 or IPv6 CIDR range. In a pattern, labels are separated by `.`, and `*` matches exactly one label: `*.example.com` matches `api.example.com` but neither `example.com` nor `a.b.example.com`. Literals and CIDRs are canonicalized; CIDR host bits are ignored (`10.2.16.21/8` becomes `10.0.0.0/8`). |
| `port` | int | One of `port`/`ports` | A single destination port, 1–65535. |
| `ports` | list of int | One of `port`/`ports` | A shorthand for several destination ports on the same host, for example `[80, 443]`. Normalization expands each port into its own canonical entry; `port` and `ports` are alternatives and must not be combined. |
| `allowed_ips` | list | Empty | Optional IP addresses or CIDR ranges this entry's `host` may resolve to. Every address the name resolves to must fall inside one of them, or the allowance does not apply. A bare address is its own single-address range (`10.0.0.5` means `10.0.0.5/32`); entries are canonicalized and sorted. This constrains a hostname; it is not a subnet destination and is not accepted on a `deny` entry. |

Each `deny` entry has `host` and `port` or `ports`, with the same host and port validation as an allowance; it has no `allowed_ips` field, because a denial refuses the destination itself. Each `ports` value is expanded into one canonical entry per port, so the spec hash and the admission report are the same as if each port had been written as its own `port` entry.

`allowed_ips` is what ties a name to an address. Without it an allowance for `api.example.com` follows that name wherever it resolves, so a poisoned or compromised answer could send the sandbox to an address the declaration never named. With it, the engine refuses the connection unless the address it is about to use is inside the declared range, and a name that resolves to both an allowed and a disallowed address is refused rather than picked opportunistically. An address the engine never forwards to at all — loopback, link-local including the metadata address `169.254.169.254`, multicast, and the control-plane ports `2379`, `2380`, `6443`, `10250`, `10255` — stays refused whatever `allowed_ips` says.

A destination that names an IPv4 or IPv6 address (`host: 10.0.0.5` or `host: 2001:db8::1`) is matched by that same address and never by a wildcard pattern: a wildcard allowance cannot reach an address the declaration did not name. A CIDR in `host` is itself a destination range: the forward proxy matches a direct address inside it, and a name whose resolved answers all fall inside it for an allowance (or any answer inside it for a denial). An exact address `host` does not automatically cover a name that resolves to it; use a CIDR or `allowed_ips` when the target is a name. `allowed_ips` remains a constraint on a hostname and is not a substitute for a subnet destination.

```yaml
policy:
  network:
    default: deny-all
    allow:
      - host: api.github.com
        ports: [80, 443]
        allowed_ips:
          - 140.82.112.0/20
      - host: "*.internal.example.com"
        port: 8080
      - host: 203.0.113.7
        port: 443
      - host: 2001:db8::1
        port: 443
      - host: 10.0.0.0/8
        port: 5432
```

```yaml
policy:
  network:
    deny:
      - host: 10.2.16.21
        ports: [80, 443]
      - host: metadata.example.com
        port: 80
```

#### Daemon-wide policy

A deployment can set `EGRESS_GLOBAL_POLICY_FILE` in the daemon environment to a YAML file with the same `default`, `allow`, and `deny` body as `policy.network` (without a wrapping `network:` key). The file must state `default` explicitly; an omitted default fails startup instead of inheriting the compose "omitted means allow-all" rule. The daemon reads it once at startup; an unreadable file, an unknown field, a missing `default`, or an invalid value fails startup instead of silently running wider. In the shipped Compose deployment, put the file under the mounted data directory and point at it, for example `EGRESS_GLOBAL_POLICY_FILE=/data/work/global-network-policy.yaml`. The daemon logs the loaded file and its SHA-256 at startup, and admission reports carry the same identity, so an operator can compare a running daemon with the file they expected; restart the daemon to load edited bytes.

```yaml
default: deny-all
allow:
  - host: "*.example.com"
    ports: [443, 80]
  - host: 10.0.0.0/8
    port: 5432
deny:
  - host: metadata.example.com
    port: 80
```

The daemon policy is an **outer bound applied to every sandbox**, including agents that declare no `policy.network`. An agent policy can narrow it but never widen it:

- daemon allowlist and agent allowlist: an agent allowance must be inside a daemon allowance; an uncovered agent allowance is refused rather than accepted as a widening.
- daemon allowlist and agent denylist: the daemon's allowances remain the set, and the agent's denials subtract from it.
- daemon denylist and agent allowlist: the agent's allowances become the set, and the daemon's denials still subtract.
- daemon denylist and agent denylist: both denial sets apply; everything else is allowed.

Denials from both layers always win, and engine-owned endpoints are still emitted first whenever the effective policy restricts. The effective policy reaches the same driver boundary and forward proxy as an agent-only declaration, and admission reports include it for agents that declared nothing. Changing the file requires a daemon restart; an agent edit still takes effect through the mechanisms described below.

The default field selects the mode:

- `default: deny-all` is **allowlist mode**: only an `allow` entry can permit a destination. A `deny` entry can carve an exception out of a broad allowance.
- `default: allow-all` is **denylist mode**: every destination is permitted unless a `deny` entry matches it. An `allow` entry under `allow-all` cannot narrow anything, because the default already permits it.

Hosts are lowercased, duplicate entries are rejected, and entries are sorted, so the normalized declaration enters canonical JSON and the spec hash independently of declaration order. Declaring `network` changes the hash; leaving it out keeps the hash byte-identical to a spec written before the field existed.

An `allow` list restricts traffic only together with `default: deny-all`. A `deny` list restricts under either default, and a denial wins over an allowance that matches the same destination. A declaration with `default: allow-all` and no `deny` entries cannot restrict traffic; its `allow` entries are still validated, stored, and hashed, but they leave outbound access unrestricted.

When the declaration restricts — a `deny` default or at least one `deny` entry — the engine automatically allows its own endpoints before every declared rule: the runtime LLM facade and the telemetry endpoint a guest exports to. These are engine-side entries, not user declarations: a project cannot declare, remove, or override them, they are as narrow as the exact endpoint the engine hands the guest, and every request they allow is recorded against an engine rule (`engine.llm-facade`, `engine.telemetry`) so the exemption is visible rather than hidden.

The declaration is validated, hashed, round-trips through the API, and compiles into the engine's single egress decision model. Whether it restricts traffic depends on the runtime driver, and the driver's capability report states the strength it actually applies instead of presenting a partial mechanism as a complete one:

- On Microsandbox the SDK network configuration applies the declaration: engine endpoints come first, then `deny` entries, then `allow` entries; traffic no rule matches falls through to the default. Its capability report carries `strength=allowlist` for allowlist mode or `strength=denylist` for denylist mode, plus the remaining limits, and it states what a real KVM run verified: the deny default and the allow rules hold (a name without an allowance fails to resolve, and an address allowance is refused on every other address and port), an `allowed_ips` constraint is not applied, an allowed HTTPS destination did not complete a TLS handshake, and the engine's forward proxy carries an HTTPS allowance when the sandbox can reach the daemon. When the proxy is also in the path, admission reports both mechanisms and states that traffic bypassing the proxy is not decided by it; when the declaration also has `allowed_ips`, it states that the SDK filter does not apply that address constraint to bypass traffic, and `EGRESS_UNENFORCEABLE_POLICY=refuse` refuses such a sandbox.
- Microsandbox's in-VM DNS proxy refuses answers that point at private addresses, which is what stops DNS rebinding. The protection applies to every sandbox, with or without a declaration, and is on by default. A deployment whose guests must resolve internal names — an intranet service on a `10/8` address, for example — sets `MICROSANDBOX_DNS_REBIND_PROTECTION=false`; the daemon logs one warning when it then starts a sandbox, and those guests can be steered at internal addresses by any name they resolve. There is no per-domain exemption: the switch is global to the deployment.
- On every driver whose sandbox can reach the daemon — Docker, Kubernetes, and BoxLite included — **the engine enforces the declaration itself, in its own forward proxy**. The sandbox is given the proxy as its egress path (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, and the lowercase forms, none of which a project can override), the proxy authenticates that sandbox, and it decides every connection: a `deny` entry is answered `403` with the rule that refused it, an `allow` entry is tunnelled in allowlist mode, and in denylist mode anything no denial matches is tunnelled under the allow default. Docker therefore carries a real strength even though the driver installs no filter of its own.
- **The proxy refuses destinations the engine never forwards to, whatever the declaration says**: loopback, link-local (including the `169.254.169.254` metadata address), unspecified, and multicast addresses, plus the control-plane ports a cluster exposes — `2379` and `2380` (etcd), `6443` (API server), `10250` and `10255` (kubelet). Each refusal is recorded under its own engine rule (`engine.blocked-address`, `engine.blocked-port`) with the role it refused. For a restricting sandbox this closes a path it would otherwise find on its own; for any sandbox it is what keeps the proxy from becoming a new route into the daemon's services or the platform hosting it.
- The proxy resolves the destination itself and dials the address it decided against, so a name cannot resolve to something else between the decision and the connection.
- **The limits are stated rather than implied.** A process that ignores the proxy environment still dials the internet directly and is not decided, until transparent capture puts every connection through the proxy. The policy schema covers host and port; it has no UDP field, and the forward proxy carries TCP traffic only. IPv4 and IPv6 literals, CIDR destinations, and IPv6-only names are supported; the sandbox runtime network must also have IPv6 connectivity for an IPv6 destination to work.
- When the sandbox has no address for the proxy (a deployment where the daemon is not reachable from inside the sandbox), the declaration cannot be applied and the sandbox is **reported as unenforced and still runs** — a restriction the platform cannot deliver must not become an availability failure. The daemon logs `declared network policy is not enforced` with the sandbox, driver, and reason, and the capability report states the same limit, so an unenforced declaration is never presented as a control. Set `EGRESS_UNENFORCEABLE_POLICY=refuse` to reject the sandbox in that case instead; the refusal happens while the sandbox is created (inside a run or a scheduler tick), so a deployment that requires the constraint to be real should treat the project as invalid rather than relying on that error.
- A declaration with `default: allow-all` and no `deny` entries (including an `allow` list without `default: deny-all`) never restricts traffic, is never given the proxy environment, and never makes a sandbox fail to start. A `deny` entry is a restriction even under `allow-all`, so it is mediated and reported accordingly.

**Admission reports this, so you do not have to guess.** `config` prints the declared policies (their allowances and denials, and any declared address ranges) to stderr while keeping the canonical spec on stdout. `up` and `apply` answer the question that matters at admission time — which mechanism will actually apply each declaration — from the daemon's own view:

```
Network policy
  daemon policy  /data/work/global-network-policy.yaml (sha256:6f1c...)
  worker (driver microsandbox)
    declared  default deny-all, 1 allowance(s), 0 denial(s)
      deny  metadata.example.com:80/* [daemon]
      allow api.github.com:443/* (addresses 203.0.113.0/24) [daemon+agent]
    enforced  forward_proxy (allowlist; mechanisms: sdk_network_policy, forward_proxy)
    limit     the engine's forward proxy decides only traffic that honors the proxy environment
    limit     sdk_network_policy applies the declared destinations but not allowed_ips; traffic that bypasses the forward proxy is not address-constrained
```

When a daemon-wide policy is configured, the first line names the file and the SHA-256 of the exact bytes loaded at startup. Every effective allow/deny entry carries its provenance: `[daemon]`, `[agent]`, or `[daemon+agent]`. `mechanisms` lists every mechanism that applies part of the declaration in driver-first order, so a Microsandbox sandbox with both its SDK filter and the proxy shows both rather than hiding one behind the other. `limit` lines state material limits that do not make the declaration unenforced, including bypass traffic and a driver's failure to apply `allowed_ips`.

A declaration the engine will not enforce is printed as `enforced  no:` with the reason and what this deployment does about it (`EGRESS_UNENFORCEABLE_POLICY=warn` or `refuse`), and the same text is written to stderr as a warning. Agents that share one gap share one warning. Agents that declared no policy and have no daemon-wide policy are not listed — undeclared is not deny, so there is nothing to report. With `EGRESS_GLOBAL_POLICY_FILE` set, every agent is listed under that effective policy, including agents that declared nothing. `agent-compose up --json` carries the same account under `network_policies`, with `sources`, `mechanisms`, `limitations`, `daemon_policy_source`, and `daemon_policy_sha256` fields.

**When a declaration takes effect.** It depends on which mechanism applies it:

- The engine's forward proxy reads the declaration **on every request**, so on a proxied sandbox an edit takes effect immediately, without recreating or restarting anything. A credential is issued once per sandbox creation; the policy behind it is re-read each time.
- A driver that applies the network configuration itself (Microsandbox) reads it when it creates the sandbox: a sandbox created after an edit uses the new declaration, while an existing sandbox that is only resumed keeps the configuration it was created with. With the default `stopped_runtime_policy: remove` the runtime is discarded on stop, so the next start creates one and picks the declaration up; a retained runtime does not.
- On a sandbox with no reachable proxy and a current restricting declaration, the unenforced report (or the `EGRESS_UNENFORCEABLE_POLICY=refuse` refusal) applies at the next start.

`stopped_runtime_policy` does not share this timing: it is snapshotted onto the sandbox when the sandbox is created, so editing it affects only new sandboxes.

### `scheduler`

A scheduler uses either declarative `triggers` or JavaScript `script`; the two forms are mutually exclusive.

| Field | Type | Default | Purpose |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | Enables this agent's scheduler. Disabling the Agent also prevents its scheduler from being enabled. |
| `sandbox_policy` | string | `new` | Scheduler default sandbox policy: `new` or `sticky`. |
| `concurrency_policy` | string | `skip` | Overlapping run policy for the entire agent scheduler: `skip` or `parallel`. |
| `run_timeout` | duration | Empty | Maximum duration of one scheduler run. Empty inherits `SCHEDULER_RUN_TIMEOUT`; accepts Go durations such as `30m` or `2h`. |
| `model` | string | Agent model | Default `provider/model` used by `scheduler.llm`; a call-level `model` or `LLM_MODEL` takes precedence. |
| `triggers` | list | Empty | Declarative triggers. |
| `script` | string/object | Empty | Inline JavaScript or a flat `file`/`http`/`git` source mapping. Cannot coexist with `triggers`. |

`new` creates a new sandbox for scheduler calls. `sticky` allows the scheduler to bind and reuse a sandbox. A trigger-level `sandbox_policy` controls the generated agent call for that trigger.

`concurrency_policy` applies to the whole agent scheduler, including all declarative or script-registered triggers and manual scheduler invocations. With `skip`, a run that overlaps another run from the same scheduler is recorded as `skipped` and is not queued. With `parallel`, overlapping runs may execute concurrently. It is not a per-trigger policy.

Script schedulers can run host calls in parallel with `scheduler.llm.async` and `scheduler.agent.async`. Two scheduler env values cap how many run at once:

| Env | Default | Purpose |
| --- | --- | --- |
| `LLM_MAX_CONCURRENCY` | `8` | Concurrent `scheduler.llm.async` calls. |
| `AGENT_MAX_CONCURRENCY` | `3` | Concurrent `scheduler.agent.async` runs. |

The agent limit is much lower because every parallel agent run creates its own sandbox, sandbox creation has no ceiling of its own, and each run writes through the SQLite pool (`SQLITE_MAX_OPEN_CONNS`, four connections by default). A value that is not a positive integer is ignored and the default applies. `scheduler.agent.async` always uses the `new` sandbox policy: parallel agents cannot share one.

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
| `include_event` | bool | No | `event` triggers only. Defaults to `true`, which appends the triggering event to the prompt; `false` sends the declared prompt alone. |
| `sandbox_policy` | string | No | `sticky` or `new` for this generated agent call. If omitted, no call-level override is emitted. |

The daemon local timezone comes from `TZ` when it is set, otherwise from the operating system's `/etc/localtime`. The shipped Docker Compose deployment mounts the host's `/etc/localtime` read-only. Set `TZ` in `.env` only when the daemon should intentionally differ from the host. Restart the daemon after changing its timezone. Stored timestamps remain UTC.

##### Event payload in the prompt

An `event` trigger appends the triggering event to its prompt, so the agent knows which object to work on. For a webhook event the payload is the delivery record, with the request body under `body`:

```text
Review the pushed changes.

<trigger-event topic="webhook.github.push">
{"body":{"ref":"refs/heads/main",...},"eventId":"...","headers":{"x-github-event":"push",...},"method":"POST","path":"/api/webhooks/...","topic":"webhook.github.push",...}
</trigger-event>
```

The block holds the event payload as compact JSON. An event delivered through the event bus and a manual `StartSchedulerRun` with the same payload produce the same block: the bus envelope (`topic`, `createdAt`, `payload`) is unwrapped and `topic` is the published topic, while a manual run's raw payload is paired with the declared `event.topic`. Only a JSON object with exactly the keys `topic`, `createdAt` (a string), and `payload` is treated as an envelope, so a manual run that replays a stored run's `payload_json` renders like the original delivery. A run without a payload, or whose payload is `null` or an empty object, sends the declared prompt unchanged.

The topic is escaped as an attribute value, and a `</trigger-event` string inside the payload is written as `<\/trigger-event`, which decodes to the same JSON value; neither can end the block early. The block content is at most 65,536 UTF-16 code units. Longer content is cut to that length, so it is no longer valid JSON, and the tag gains `truncated="true"` and `original-length="<n>"`.

Set `include_event: false` on the trigger to send the declared prompt without the block. Use an inline scheduler script when the agent needs a different rendering of the event.

#### Event delivery scope

The daemon operator, not the Project, decides which Projects an event reaches. `EVENT_DELIVERY_SCOPE` in the daemon environment accepts two values:

| Value | Behavior |
| --- | --- |
| `project` (default) | An event published from a Project reaches only `event` triggers in that same Project. |
| `daemon` | An event published from a Project reaches matching `event` triggers in every Project on the daemon. |

The scope applies to topics a scheduler script publishes with `scheduler.event.publish` (`runtime.*`, `workflow.*`, `external.*`) and to the Engine system topics raised on a Project's behalf (`agent-compose.session.*`, `agent-compose.sandbox.*`, `agent-compose.agent.completed`). Webhook topics are not published by a Project: an operator-configured webhook source admits them, and they reach matching triggers in every Project under either scope.

The webhook exception assumes only the operator can configure webhook sources. The webhook source API is protected only when `AGENT_COMPOSE_AUTH_TOKEN` is set; without it, anyone who can reach the daemon API can add a source and inject webhook events. Even with it set, webhook events are shared: every Project that subscribes to `webhook.*` or `*` receives every webhook payload. A daemon shared by mutually untrusted users must set `AGENT_COMPOSE_AUTH_TOKEN` and should not route tenant-private data through webhooks.

Neither `agent-compose.yml` nor a scheduler script can change the scope, and a topic name grants no access: under `project` scope, subscribing to `*` still receives only the Project's own events. A control plane can read the scope in effect with `SettingsService.GetEventDeliveryScope`. A daemon shared by a single user that relies on cross-Project events sets `EVENT_DELIVERY_SCOPE=daemon`.

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

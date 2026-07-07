# Resource Identity and CLI Display Design

Chinese version: [../zh-CN/design/resource_identity_cli_design.md](../zh-CN/design/resource_identity_cli_design.md)

Status: design proposal. This document describes the intended CLI-facing
resource identity model and output shape. It does not require code changes by
itself.

The examples use sanitized project-relative paths:

- `agent-compose/examples/docker-minimal/agent-compose.yml`
- `agent-compose/examples/docker-scheduler-timeout/agent-compose.yml`

## Summary

agent-compose CLI output should follow Docker/Compose style:

- Declared resources are identified by the names users wrote, or by the
  directory name that contains `agent-compose.yml` when `name` is omitted.
- Runtime resources are identified by opaque IDs. Default CLI output shows a
  short 12-character prefix; verbose and JSON output can show the full value.
- Images keep Docker/OCI identity: image ID, tags, repo digests, and
  `sha256:` values.
- The CLI should not generate readable compound IDs that concatenate resource
  type, names, and hash fragments as user-facing IDs.
- User-facing CLI, UI, and public API vocabulary should use Sandbox.

The key idea is to separate human names from opaque identity:

- For named resources, `name` is the public unique identifier in its scope.
- For opaque identity, use `id` for the complete `sha256:` value and
  `short_id` for the first 12 hash characters.
- If a named resource also has an opaque identity for storage or conflict
  resolution, it can be exposed in `--verbose` or `--json`, but the default CLI
  should prefer the name.

## Resource Classes

### Declared Resources

Declared resources come from `agent-compose.yml` or the project directory.

| Resource | Public identifier | Default display | Additional handles |
| --- | --- | --- | --- |
| Project | `name`, or config directory name when omitted | project name | `id`, `short_id` in verbose/JSON |
| Agent | `agents.<name>` within a project | agent name | `id`, `short_id` in verbose/JSON |
| Scheduler | one scheduler resource in a project | not normally displayed as a separate named resource | scheduler `id`, `short_id` in verbose/JSON |
| Trigger | declared trigger `name`, or runtime-registered trigger ID/ordinal | trigger name or trigger short ID | `id` in verbose/JSON |

Project example:

```yaml
name: docker-minimal

agents:
  reviewer:
    provider: codex
    image: agent-compose-guest:latest
    driver:
      docker: {}
```

In this example:

- Project identifier: `docker-minimal`
- Agent identifier in that project: `reviewer`
- Neither should be displayed as a generated compound ID.

### Scheduler And Trigger Scope

`agent-compose scheduler ls` is scoped to the current project. A project has one
scheduler resource for CLI purposes, and that scheduler may have many triggers.

Trigger sources:

- Declarative triggers in `agent-compose.yml`, usually with `name`.
- Loader script registrations, usually without a user-defined name. These use
  the internally registered trigger ID or a stable ordinal in compact display.

Therefore the default `scheduler ls` output should focus on triggers, not on a
scheduler ID. The scheduler ID remains useful for debugging and machine output,
so it belongs in `--verbose` and `--json`.

### Runtime Resources

Runtime resources are created by execution. They do not get generated readable
compound IDs.

| Resource | Public identifier | Default display | Additional handles |
| --- | --- | --- | --- |
| Run | opaque run `id` or `short_id` | short run ID plus project/agent/status | `id` in JSON |
| Sandbox | opaque sandbox `id` or `short_id` | short sandbox ID plus project/agent/status | `id` in JSON |
| Cache | opaque cache `id` or `short_id` | short cache ID plus type/ref/path | `id` in JSON |

Examples:

```text
Run ID:     103f88fea811
Sandbox ID: c5582b466ada
Cache ID:   8b42ac739d10
```

Do not present runtime resources as generated compound IDs that combine the
resource type, project name, agent name, and hash fragment.

### Image Resources

Images use Docker/OCI identity. The CLI should not wrap or rename image IDs.

Compact text output:

```text
IMAGE ID      REF                         STATUS
e67e6413b80b  agent-compose-guest:latest  available
```

JSON output keeps full values:

```json
{
  "image_id": "sha256:e67e6413b80b665a4ca89279a67d709e77ee50640b3d267b568d379d211c9a8b",
  "short_id": "e67e6413b80b",
  "image_ref": "agent-compose-guest:latest"
}
```

## Display Rules

Default CLI output is compact and optimized for next-step operations.

| Output mode | Rule |
| --- | --- |
| Default text | Show names for declared resources; show 12-character IDs for runtime resources. |
| `--verbose` | Add full paths, scheduler IDs, full hashes, timestamps, and debug metadata. |
| `--json` | Use consistent `id`, `name`, and `short_id` fields in the current resource object. Do not truncate `sha256:` values. |

Do not remove existing JSON fields unless a versioned API change explicitly
requires it. Generated compound IDs such as `project-<name>-<hash12>` should be
removed rather than kept as compatibility fields because they are not useful
public identifiers. Revision is an internal implementation detail, so default
text output should hide it. `--verbose` and `--json` can expose revision fields
for debugging and automation.

## CLI Resolution Rules

Each command resolves arguments in its command scope.

| Command | Scope | Preferred input |
| --- | --- | --- |
| `agent-compose ls` | global daemon | no target |
| `agent-compose up/down` | current compose project | config path or project name |
| `agent-compose inspect project` | global daemon or current config | project name; `id`/`short_id` as fallback |
| `agent-compose inspect agent` | current project | agent name; `id`/`short_id` as fallback |
| `agent-compose scheduler ls` | current project | no target, optional agent filter |
| `agent-compose scheduler inspect` | current project | agent name + trigger name/ID |
| `agent-compose run` | current project | agent name |
| `agent-compose ps/logs` | current project by default | agent name, run ID, sandbox ID |
| `agent-compose inspect run` | current project by default | run `id`/`short_id` |
| `agent-compose inspect sandbox` | current project by default | sandbox `id`/`short_id` |
| `agent-compose images/cache` | daemon-level resources | image ref/ID or cache `id`/`short_id` |

Resolution order:

1. Exact name match for named resources in scope.
2. Exact `id` match.
3. Unique short ID or ID prefix match.
4. Scoped name match when needed, such as project + agent or agent + trigger.
5. If multiple resources match, fail with an ambiguous-target error and show
   candidates.

## Compatibility Policy

The CLI should improve display and resolution without breaking existing clients.

| Existing field | Recommendation |
| --- | --- |
| `project_id` with generated compound text | Remove it. The project object should use `name`, `id`, and `short_id`; no `project-<name>-<hash12>` style ID is needed. |
| `managed_agent_id` | Do not expose it as the preferred field in new JSON output. The agent object should use `name`, `id`, and `short_id`. |
| `scheduler_id` | Keep only where another resource object needs to reference the scheduler, or in verbose/debug output. A scheduler object should use `id` and `short_id`. |
| `trigger_id` | Keep when referring to a trigger from another object. A trigger object should use `id`, `name` when available, and `short_id`. |
| `run_id` | Keep when referring to a run from another resource object. A run object should use `id` and `short_id`. |
| `sandbox_id` | Keep when referring to a sandbox from another resource object. A sandbox object should use `id` and `short_id`. |
| `cache_id` | Keep when referring to a cache from another resource object. A cache object should use `id` and `short_id`. |
| `image_id` | Keep Docker/OCI semantics unchanged. |
| `spec_hash` | Keep full `sha256:` value in JSON and verbose output. |

## Command Output Examples

The examples below show the intended output shape. Current output examples are
sanitized and simplified to focus on identity fields.

### `agent-compose ls`

`ls` is global and should follow Docker Compose by showing the config file path
by default.

Proposed shape:

```text
ID            NAME                      CONFIG FILE                                         AGENTS  SCHEDULERS
55521f60a3e9  docker-minimal            agent-compose/examples/docker-minimal/agent-compose.yml       1  0
92f42e13d913  docker-scheduler-timeout  agent-compose/examples/docker-scheduler-timeout/agent-compose.yml  1  1
```

Verbose can add opaque identities and full hashes:

```text
ID            NAME            CONFIG FILE                                      REVISION  SPEC HASH
55521f60a3e9  docker-minimal  agent-compose/examples/docker-minimal/agent-compose.yml  1  sha256:0e351a523ae793f780fc0933f3b88920501f20dfd4d855654fe711a8a3cb4edd
```

### `agent-compose up`

Default text should only show the action table. `ID` is the first column and
uses `short_id`; action stays at the end.

```text
ID            NAME            TYPE              ACTION
55521f60a3e9  docker-minimal  project           created
6a3d03099bc3  reviewer        agent             created
```

### `agent-compose inspect project docker-minimal --json`

JSON uses `id`, `name`, and `short_id` inside each resource object. It should
not add redundant `project_id`, `agent_id`, or `agent_short_id` fields when the
object type already defines the scope.

```json
{
  "project": {
    "name": "docker-minimal",
    "id": "sha256:55521f60a3e9...",
    "short_id": "55521f60a3e9",
    "source_path": "agent-compose/examples/docker-minimal/agent-compose.yml",
    "current_revision": 1,
    "spec_hash": "sha256:0e351a523ae793f780fc0933f3b88920501f20dfd4d855654fe711a8a3cb4edd"
  },
  "agents": [
    {
      "name": "reviewer",
      "id": "sha256:6a3d03099bc3...",
      "short_id": "6a3d03099bc3",
      "provider": "codex",
      "image": "agent-compose-guest:latest",
      "driver": "docker"
    }
  ],
  "schedulers": []
}
```

The `id` values above are opaque. They are not readable compound IDs.

### `agent-compose scheduler ls`

Scope: current project. Default output focuses on triggers.

Compact shape:

```text
AGENT     TRIGGER                    KIND     SOURCE       ENABLED
reviewer  run-once-after-15-seconds  timeout  declarative  true
```

If trigger IDs are available and stable:

```text
TRIGGER ID    AGENT     TRIGGER                    KIND     SOURCE       ENABLED
8f52c930d7a4  reviewer  run-once-after-15-seconds  timeout  declarative  true
```

Verbose:

```text
TRIGGER ID    AGENT     TRIGGER                    KIND     SOURCE       SCHEDULER ID   ENABLED
8f52c930d7a4  reviewer  run-once-after-15-seconds  timeout  declarative  cd228d46fd7d  true
```

### `agent-compose run reviewer --command 'echo ok' --keep-running`

Foreground output should remain command output:

```text
ok
```

Detached output should return handles for the next command:

```text
Run: 103f88fea811
Sandbox: c5582b466ada
Status: running
Logs: agent-compose logs --run 103f88fea811 --follow
```

### `agent-compose ps`

```text
SANDBOX ID    PROJECT         AGENT     STATUS   RUN ID        DRIVER  IMAGE
c5582b466ada  docker-minimal  reviewer  running  103f88fea811  docker  agent-compose-guest:latest
```

### `agent-compose logs --run 103f88fea811`

```text
reviewer-run-103f88fea811 [2026-07-07T10:15:30Z]| ok
```

For `logs --json`, keep fields separate instead of using this display prefix:
`agent_name`, `run_id`, `run_short_id`, `time`, and `content`.

### `agent-compose inspect run 103f88fea811 --json`

```json
{
  "id": "sha256:103f88fea811...",
  "short_id": "103f88fea811",
  "project_name": "docker-minimal",
  "agent_name": "reviewer",
  "status": "succeeded",
  "sandbox_id": "sha256:c5582b466ada...",
  "sandbox_short_id": "c5582b466ada",
  "exit_code": 0,
  "output": "ok\n",
  "driver": "docker",
  "image_ref": "agent-compose-guest:latest"
}
```

### `agent-compose inspect sandbox c5582b466ada --json`

```json
{
  "id": "sha256:c5582b466ada...",
  "short_id": "c5582b466ada",
  "project_name": "docker-minimal",
  "agent_name": "reviewer",
  "run_id": "sha256:103f88fea811...",
  "run_short_id": "103f88fea811",
  "status": "running",
  "driver": "docker",
  "image_ref": "agent-compose-guest:latest",
  "workspace_path": "agent-compose/data/sandboxes/c5582b466ada/workspace"
}
```

### `agent-compose images --query agent-compose-guest`

No semantic change:

```text
IMAGE ID      REF                         STATUS     SIZE
e67e6413b80b  agent-compose-guest:latest  available  3277198228
```

### `agent-compose cache ls`

```text
CACHE ID      DRIVER  TYPE       STATUS      REMOVABLE  SIZE   REF
8b42ac739d10  docker  sandbox    referenced  false      789    c5582b466ada
4a19e01f64ca  docker  rootfs      unused      true       123M   agent-compose-guest:latest
```

## Migration Notes

1. Add shared helpers for deriving `short_id` from opaque IDs and hashes.
2. Stop rendering generated compound IDs in default text output.
3. Standardize JSON output on `id`, `name`, and `short_id` for the current
   resource object.
4. Add CLI argument resolution for names, exact IDs, and unique ID
   prefixes.
5. Move scheduler IDs to verbose/JSON output and make trigger display the
   default for `scheduler ls`.
6. Standardize public CLI/UI/API vocabulary around Sandbox.
7. Keep image output aligned with Docker/OCI behavior.

## Risks

- Short ID matching must reject ambiguous prefixes.
- Named resources are unique only in their scope; project context must be part
  of agent and trigger resolution.
- Removing existing JSON fields requires a separate versioning plan. Prefer
  additive migration first.
- Runtime IDs must stay opaque and stable enough for follow-up commands.

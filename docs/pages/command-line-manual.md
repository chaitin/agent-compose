# agent-compose Command Line Manual

The `agent-compose` CLI connects to an agent-compose daemon and manages projects, agents, sandboxes, logs, and images. Its operating model is close to Docker Compose: a configuration file defines a project, the daemon owns long-lived state and runtime lifecycle, and the CLI applies changes, starts runs, and displays results.

## Core Concepts

- `project`: one `agent-compose.yml` or `agent-compose.yaml` defines one project. The directory containing that file is the project root.
- `agent`: an agent definition in a project. A project can define multiple agents.
- `sandbox`: a runtime isolation environment for one agent run context. An agent can have multiple sandboxes. The CLI uses the same sandbox concept whether the underlying runtime is Docker, BoxLite, or Microsandbox.
- `daemon`: the server process that owns project state, schedulers, sandbox lifecycle, logs, images, and APIs.

## Command Format

```bash
agent-compose [global options] <command> [command options] [arguments]
```

Global options are placed between `agent-compose` and the subcommand, and apply to project-related commands.

| Option | Description |
| --- | --- |
| `-f, --file <path>` | Path to the project config file. Both `agent-compose.yml` and `agent-compose.yaml` are supported. When this option is used, the project root is the config file directory, so you do not need to `cd` into it. |
| `--host <endpoint>` | Daemon HTTP endpoint. This can target a local daemon or a remote daemon. |
| `--project-name <name>` | Override the project name from the config file. Useful when running the same config under different environment names. |
| `--json` | Print machine-readable JSON for scripts, AI agents, and automation. |

Examples:

```bash
agent-compose -f /path/to/project/agent-compose.yml up
agent-compose -f /path/to/project/agent-compose.yaml ps --all
agent-compose --host http://10.0.0.12:7410 ls --json
```

Rules:

- Without `-f`, the CLI looks for `agent-compose.yml` or `agent-compose.yaml` in the current directory.
- With `-f`, the CLI can operate on a project from any working directory.
- `--host` only selects the daemon. Sandboxes run in the daemon environment.
- Automation should use `--json` and avoid parsing human-readable tables.

### Daemon authentication

Set `AGENT_COMPOSE_AUTH_TOKEN` in the daemon environment to require a shared
Bearer token for HTTP(S) control-plane requests. Leaving it empty keeps
authentication disabled. Trusted local Unix socket connections do not use this
authentication path.

Verify and save a token for a daemon site:

```bash
agent-compose --host https://compose.example.com auth login --token '<token>'
agent-compose --host https://compose.example.com status
```

The first command verifies the token against the daemon before saving it under
`~/.config/agent-compose/config.yml` (or the platform user configuration
directory). Later commands automatically load the token associated with the
normalized `--host` or `AGENT_COMPOSE_HOST` value. The file is written with
owner-only permissions. Use `agent-compose auth ls` to list saved sites and
`agent-compose --host <site> auth logout` to remove one.

HTTP remains supported, including loopback container port mappings, but a
Bearer token sent over plain HTTP can be observed and replayed. Use HTTPS, an
SSH tunnel, a VPN, or another protected network when the CLI and daemon are on
different machines.

Health RPCs, the runtime LLM facade, Jupyter proxy traffic, and webhook
ingestion retain their existing independent authentication or trust boundaries
and do not consume the daemon token.

The token protects the daemon control plane rather than identifying the CLI
application. Any UI server or reverse proxy that calls the same control-plane
APIs must also inject `Authorization: Bearer <token>` before daemon
authentication is enabled.

### Project environment files

A project can explicitly load one or more dotenv files. Relative paths are resolved from the directory containing the project config file:

```yaml
env_file:
  - .env
  - .env.local
```

Without `env_file`, the CLI first looks for `.env` in the project directory, then falls back to `.env` in the current working directory. An explicit `env_file` disables both automatic locations.

Later files override earlier files, and the environment inherited by the CLI overrides every env file. Project env files are only used to render `agent-compose.yml`; they do not change CLI connection settings such as `--host` or authentication.

## Common Workflows

Local development:

```bash
agent-compose up
agent-compose ps
agent-compose run reviewer --prompt "Review the current diff"
agent-compose logs reviewer --follow
agent-compose down
```

Daemon-managed project:

```bash
agent-compose -f /path/to/project/agent-compose.yml up
agent-compose -f /path/to/project/agent-compose.yml ps --all
agent-compose -f /path/to/project/agent-compose.yml logs --follow
```

Remote daemon:

```bash
agent-compose --host http://10.0.0.12:7410 project ls
agent-compose --host http://10.0.0.12:7410 -f /path/to/project/agent-compose.yml up
agent-compose --host http://10.0.0.12:7410 -f /path/to/project/agent-compose.yml logs --follow
```

## `project ls`: List Projects

List projects known to the selected daemon.

```bash
agent-compose project ls
agent-compose project ls --limit 20 --offset 40
agent-compose project ls --verbose
agent-compose project ls --json
```

Default columns:

- `PROJECT`: project name.
- `CONFIG FILE`: config file path.
- `REVISION`: current project revision. Revisions increase for each applied spec
  change; repeated applies of the current spec keep the same revision.
- `AGENTS`: agent count.
- `SCHEDULERS`: scheduler count.
- `SERVICES`: service count. The current project spec does not define a service model, so this column is shown as `-`.

`--verbose` prints additional daemon metadata, including project id, project root, spec hash, timestamps, and status summary.

| Option | Description |
| --- | --- |
| `--limit <n>` | Return at most `n` projects. Without this option, the CLI reads all pages. |
| `--offset <n>` | Start from an offset. Usually used together with `--limit`. |
| `--verbose` | Show additional columns. |

## `agent ls`: List Current Project Agents

List the agents in the current applied project. The top-level `ls` command is an alias for `agent ls`.

```bash
agent-compose agent ls
agent-compose ls
agent-compose agent ls --json
```

## `project up`: Apply a Project

Read the config file and apply the project to the daemon. This starts or updates project schedulers and daemon-managed state.

```bash
agent-compose up
agent-compose project up
agent-compose -f /path/to/project/agent-compose.yml up
```

Current `up` semantics are daemon-style: the command applies the project and returns. It does not attach project logs and does not support `-d/--detach`.

The top-level `up` command is an alias for `project up`.

## `project down`: Stop a Project

Stop the current project, including schedulers, services, and running sandboxes.

```bash
agent-compose down
agent-compose project down
agent-compose -f /path/to/project/agent-compose.yml down
```

The top-level `down` command is an alias for `project down`.

Notes:

- `down` only affects the selected project.
- When using `-f` or `--project-name`, verify that the command targets the intended project.
- If some sandboxes cannot be stopped, the command exits non-zero and reports the failed items.

## `run`: Run a Sandbox

Start a sandbox for an agent, or continue work in an existing sandbox.

```bash
agent-compose run <agent> --prompt "..."
agent-compose run <agent> --command "..."
agent-compose run <agent> --sandbox <sandbox> --prompt "..."
```

Input modes:

| Mode | Usage | Description |
| --- | --- | --- |
| prompt | `run <agent> --prompt "..."` | Send a prompt to the agent provider. |
| command | `run <agent> --command "..."` | Start or reuse the agent sandbox and execute a shell command through guest `agent-compose-runtime exec`; stdout/stderr transcript is streamed and persisted to the run record without protocol payload markers. |
| prompt REPL | `run <agent> -i --prompt` | Read prompts line by line from stdin. Each non-empty input creates one run and reuses the same sandbox. |
| command REPL | `run <agent> -i --command` | Read commands line by line from stdin. Each non-empty input creates one run and reuses the same sandbox. |
| sandbox reuse | `run <agent> --sandbox <sandbox> --prompt "..."` | Continue in a specific sandbox. |

Prompt input must use `--prompt`, and non-interactive runs must choose `--prompt` or `--command`. Positional prompt arguments are not supported.
Additional positional arguments are not supported.

| Option | Description |
| --- | --- |
| `--keep-running` | Keep the sandbox runtime after the run completes. |
| `--sandbox <sandbox>` | Reuse an existing sandbox. |
| `--rm` | Remove the sandbox after the run reaches a terminal state. |
| `--jupyter` | Enable Jupyter for this run. When unset, the agent YAML default is used; when YAML is unset, Jupyter is disabled. |
| `--jupyter-expose` | Mark the Jupyter agent-compose proxy endpoint for this run as explicitly exposed. This does not request runtime-driver host port exposure and also enables Jupyter. |
| `-d, --detach` | Submit the run to the daemon and return immediately with the run id, initial status, and a `logs --follow` command. |
| `-i, --interactive` | Enter prompt or command REPL mode. Must be combined with `--prompt` or `--command`. |

Examples:

```bash
agent-compose run reviewer --prompt "Review the staged changes"
agent-compose run builder --command "task build"
agent-compose run tester --command "task test" --keep-running
agent-compose run tester --command "task test" -d
agent-compose run reviewer -i --prompt
agent-compose run tester -i --command
agent-compose run reviewer --sandbox sandbox_123 --prompt "Continue the review"
agent-compose run reviewer --jupyter --jupyter-expose --prompt "Inspect the notebook state"
```

Rules:

- Choose only one of prompt or command.
- Do not combine `--prompt` or `--command` with additional positional arguments.
- `run -d/--detach` and `run -i/--interactive` are mutually exclusive.
- `run -i/--interactive` must select `--prompt` or `--command`; it cannot be combined with `--json`.
- Empty REPL lines do not create runs. Enter `/exit` or press Ctrl+D to exit.
- REPL mode is not TTY/PTY or running stdin passthrough. Each input is one independent `RunAgentStream` call that reuses the same sandbox.
- Detached runs can be observed with the printed `agent-compose logs --run <run-id> --follow` command, or managed later with `stop` and `logs`.
- `run -i --prompt` supports providers with reusable provider conversations: Codex, Claude/cc, and OpenCode. Gemini currently returns unsupported.
- `StopRun` requests cancellation for active in-daemon runs. Pending/running runs left behind after daemon restart are reconciled to failed with a `daemon interrupted` error.

## `scheduler`: Invoke, Inspect, and Operate Project Schedulers

```bash
agent-compose scheduler ls [agent]
agent-compose scheduler invoke <scheduler-ref> [--payload <json>]
agent-compose scheduler trigger <scheduler-ref> <trigger-ref> [--payload <json>] [--detach]
agent-compose scheduler runs [scheduler-ref] [--trigger <trigger-ref>] [--status <status>] [--limit <n>]
agent-compose scheduler logs [run-ref] [--run <run-ref>] [--scheduler <scheduler-ref>] [--trigger <trigger-ref>] [--tail <n>]
agent-compose scheduler prune [--scheduler <scheduler-ref>] [--trigger <trigger-ref>] [--status <terminal-statuses>] [--older-than <duration>] [--force]
agent-compose scheduler inspect <scheduler-or-trigger-or-run-ref> [--scheduler <scheduler-ref>]
```

- `scheduler ls` lists triggers from declarative scheduler config and triggers registered by scheduler scripts.
- `scheduler invoke` calls the default entry point of an explicitly script-based scheduler in the foreground. It does not create trigger-run history, persisted outer logs, or artifacts. The former `scheduler run` command has been removed.
- `scheduler trigger` manually executes a named trigger. `--detach` returns a persisted trigger run that can be inspected or stopped later.
- `scheduler trigger --payload '{"key":"value"}'` passes a JSON payload to the scheduler trigger handler.
- `scheduler runs` lists only outer trigger runs; inner agent runs created by `scheduler.agent()` are managed by the ordinary run commands. The default is all matching runs, while `--limit` restricts the final count. Status is one of `running`, `succeeded`, `failed`, `canceled`, or `skipped`.
- `scheduler logs` prints outer structured events for all current schedulers' trigger runs by default. `--tail N` selects the newest N matching events globally and prints them oldest-to-newest; `--tail -1` means all and `--tail 0` means none. Invocation logs and inner agent transcripts are not included.
- For `scheduler runs/logs --trigger`, names and short IDs are resolved against the current definition first. An exact trigger ID that was removed or renamed remains queryable when persisted trigger-run history exists. If that historical ID belongs to multiple schedulers, add the scheduler positional argument for `runs` or `--scheduler` for `logs`.
- `scheduler prune` removes outer trigger-run history and its directly owned loader events, event delivery/link rows, and canonical run artifacts. It matches all terminal (`succeeded`, `failed`, `canceled`, or `skipped`) trigger runs in the current project by default. Use `--scheduler`, `--trigger`, `--status`, or `--older-than` to narrow the scope. The default is a dry-run; only `--force` deletes data. Running runs, invocations, inner agent runs, topic events, sandboxes, loader state, and sticky bindings are retained. Historical trigger IDs use the same current-definition-first resolution as `runs` and `logs`.
- On daemon startup, an outer trigger run left in `running` by an interrupted daemon process is reconciled to `failed` with a daemon-interrupted loader event before it can later become eligible for pruning.
- `scheduler inspect` accepts one scheduler name/ID, trigger name/ID, or outer trigger-run ID. If a trigger reference exists in multiple schedulers, add `--scheduler <scheduler-ref>`; the old two-position-argument form is no longer supported.
- `scheduler runs` and `scheduler logs` currently collect unary cursor pages and render once. Streaming and follow behavior are intentionally deferred to a separate change.

## `ps`: List Sandboxes

List sandboxes in the current project. By default, only running sandboxes are shown. With `--all`, the command includes all statuses while remaining scoped to the current project.
The project must already exist on the daemon; after `agent-compose down`, run `agent-compose up` again before using `ps`.

```bash
agent-compose ps
agent-compose ps -a
agent-compose ps --all
agent-compose ps --status running
agent-compose ps --status exited,error
agent-compose ps --verbose
agent-compose ps --json
```

| Option | Description |
| --- | --- |
| `-a, --all` | Show current project sandboxes in all statuses. |
| `--verbose` | Show additional columns. |
| `--status <status>[,<status>...]` | Filter by sandbox status. |

Default columns:

- `SANDBOX`
- `AGENT`
- `STATUS`
- `RUN`
- `CREATED`
- `UPDATED`

`--verbose` adds project, driver, image, Jupyter, workspace, and error summary fields.

## `sandbox`: Manage Sandboxes

Use the `sandbox` command group to manage project sandboxes from a single namespace. The compatibility commands `ps`, `stop`, `resume`, and `rm` remain available.

```bash
agent-compose sandbox ls
agent-compose sandbox ls --all --json
agent-compose sandbox stop <sandbox>
agent-compose sandbox resume <sandbox>
agent-compose sandbox rm <sandbox>
agent-compose sandbox rm --force <sandbox>
agent-compose sandbox prune
agent-compose sandbox prune --older-than 7d
agent-compose sandbox prune --status error --json
agent-compose sandbox prune --agent worker --driver microsandbox --force
agent-compose sandbox prune --include-orphans
```

Subcommands:

| Command | Description |
| --- | --- |
| `sandbox ls` | Equivalent to `ps`; supports `--all/-a`, `--status`, `--verbose`, and `--json`. |
| `sandbox stop <sandbox...>` | Equivalent to `stop`; stops one or more sandboxes. |
| `sandbox resume <sandbox...>` | Equivalent to `resume`; resumes one or more stopped sandboxes. |
| `sandbox rm <sandbox...>` | Equivalent to `rm`; removes one or more sandboxes. Use `--force` only when intentionally removing running sandboxes. |
| `sandbox prune` | Dry-run cleanup for stopped or failed sandboxes in the current project. Use `--force` to remove matched sandboxes. |

`sandbox prune` options:

| Option | Description |
| --- | --- |
| `--status <status>[,<status>...]` | Override the default `stopped,failed` status filter. `running` and `pending` are rejected; use `sandbox rm --force <sandbox>` for running sandboxes. |
| `--agent <agent>` | Match only sandboxes for one agent name. |
| `--driver <docker|boxlite|microsandbox>` | Match only sandboxes using one runtime driver. |
| `--older-than <duration>` | Match sandboxes whose `updated_at`, or `created_at` when `updated_at` is missing, is older than a duration such as `7d` or `168h`. |
| `--include-orphans` | Also inventory daemon-wide managed runtime residue that has no sandbox record in any project. |
| `--force` | Actually remove matched sandboxes. Without this flag, `sandbox prune` is a dry-run. |

Rules:

- Without `--include-orphans`, `sandbox prune` only considers stopped or failed sandbox records in the current compose project and does not scan driver residue.
- With `--include-orphans`, `--driver` and `--older-than` filter both record and residue candidates; `--status` and `--agent` only filter records. A runtime resource associated with any known sandbox record is never an orphan.
- Ownership-incomplete, corrupt, path-escaping, active, or unknown-schema residue is displayed as non-removable and remains skipped even with `--force`.
- `sandbox prune` calls the daemon `SandboxService.PruneSandboxes` use case. It removes sandbox-owned runtime/data state, not shared cache artifacts; use `cache prune` or `cache rm` for cache inventory.
- If a forced prune fails to remove one matched sandbox, it continues with later matches, writes the skipped item, and exits non-zero.

`sandbox stop` preserves resumable driver state. `sandbox rm` writes a durable deletion journal under `<SANDBOX_ROOT>/.lifecycle`, rejects a running sandbox unless `--force` is supplied, and removes the driver resource, sandbox accessories, sandbox directory, and metadata in restart-safe stages. A sandbox in `DELETING` cannot be resumed or used for new exec/run work; daemon startup resumes only incomplete deletion journals and never guesses that an ordinary historical resource is orphaned.

## `stats`: Show Sandbox Resource Stats

Show resource stats snapshots for running sandboxes. Without a sandbox argument, the command shows all running sandboxes for the current compose project.
Project-wide stats require the project to already exist on the daemon; after `agent-compose down`, run `agent-compose up` again before using `stats` without a sandbox.

```bash
agent-compose stats
agent-compose stats --json
agent-compose stats <sandbox>
agent-compose stats <sandbox> --json
```

Fields include CPU percent, memory usage/limit/percent, network rx/tx, block read/write, uptime, driver, and sampled_at. Metrics unavailable from a runtime driver are shown as `-` in text tables. JSON keeps stable keys and represents those metrics with `value: null` and `status: unknown` or `status: unavailable`.

When a driver has no stable stats capability, the command returns unsupported instead of a generic execution failure.

## `stop`: Stop Sandboxes

Stop one or more sandboxes.

```bash
agent-compose stop <sandbox>
agent-compose stop <sandbox> [<sandbox N>]
```

Examples:

```bash
agent-compose stop sandbox_123
agent-compose stop sandbox_123 sandbox_456
```

## `resume`: Resume Sandboxes

Resume one or more stopped sandboxes.

```bash
agent-compose resume <sandbox>
agent-compose resume <sandbox> [<sandbox N>]
```

Examples:

```bash
agent-compose resume sandbox_123
agent-compose resume sandbox_123 sandbox_456
```

## `rm`: Remove Sandboxes

Remove one or more sandboxes.

```bash
agent-compose rm <sandbox>
agent-compose rm <sandbox> [<sandbox N>]
agent-compose rm --force <sandbox>
```

| Option | Description |
| --- | --- |
| `--force` | Force removal of a running sandbox. |

Rules:

- Removing a non-running sandbox deletes its sandbox record and runtime resources.
- Removing a running sandbox without `--force` fails with an `is running` error.
- To remove a running sandbox, explicitly use `--force`. Forced removal stops the sandbox first, then removes related resources.
- Removing a sandbox does not delete the project config.

Examples:

```bash
agent-compose rm sandbox_123
agent-compose rm sandbox_123 sandbox_456
agent-compose rm --force sandbox_789
```

## `exec`: Execute in a Sandbox

Execute a command in a running sandbox, similar to `docker compose exec`.

```bash
agent-compose exec <sandbox> -- <command> [args...]
agent-compose exec <sandbox> --command "..."
agent-compose exec <sandbox> --prompt "..."
```

| Option | Description |
| --- | --- |
| `--command "..."` | Pass a shell command as a flag. It is executed as `bash -lc "..."` in the sandbox. |
| `--prompt "..."` | Run one agent prompt in the existing sandbox and exit after the response. Add `-i` (and optionally `-t`) for a multi-turn attached session. |
| `--cwd <path>` | Set the working directory inside the sandbox. |
| `--agent <agent>` | Deprecated target selection option; use `exec <sandbox>` instead. |
| `--run <run-id>` | Deprecated target selection option; use `exec <sandbox>` instead. |

Examples:

```bash
agent-compose exec sandbox_123 -- pwd
agent-compose exec sandbox_123 -- bash -lc "task test"
agent-compose exec sandbox_123 --command "git status --short"
agent-compose exec sandbox_123 --prompt "summarize the workspace"
agent-compose exec sandbox_123 --cwd /workspace --command "pwd"
```

`exec` and `run --command` use the same guest `agent-compose-runtime exec` command output path. Text mode streams command stdout to local stdout and command stderr to local stderr after host-side marker filtering; it does not echo the host wrapper command. `--json` suppresses streaming output and prints only the final result. `exec` does not create a `ProjectRun`; use `run --command` when run audit, `logs`, or run artifacts are required.

## `logs`: Show Logs

Show logs for agents, sandboxes, or runs in the current project. By default, logs for all project agents are shown.

Current `logs` output is based on run log artifacts returned by the v2 RunService. `--follow` is served by the daemon from the log file referenced by `logs_path`; non-follow views use the run record output and artifact summary. It does not automatically read private provider log files from Codex, Claude, Gemini, or other provider CLIs.

```bash
agent-compose logs
agent-compose logs <agent>
agent-compose logs <project|agent|run|sandbox-id>
agent-compose logs --agent reviewer
agent-compose logs --run <run-id>
agent-compose logs --sandbox <sandbox>
agent-compose logs --follow
agent-compose logs -n 100
agent-compose logs -t
```

| Option | Description |
| --- | --- |
| `-n, --tail <n>` | Show only the last `n` lines of run output. Text and JSON output use the same truncation. |
| `--follow` | Follow log output. |
| `-t, --timestamp` | Prefix text log lines with a run-level timestamp. Current output does not have per-chunk timestamps; the CLI uses the best available run timestamp. |
| `--agent <agent>` | Filter by agent. |
| `--run <run-id>` | Filter by run id. |
| `--sandbox <sandbox>` | Filter by sandbox. |

Examples:

```bash
agent-compose logs
agent-compose logs reviewer
agent-compose logs --agent reviewer --tail 200
agent-compose logs --sandbox sandbox_123 --follow -t
agent-compose logs --run run_123 --json
```

## `inspect`: Inspect Resources

Inspect project resources, daemon images, or runtime cache items.
`inspect project` and `inspect agent <agent>` require the project to already exist on the daemon; after `agent-compose down`, run `agent-compose up` again before using them.

```bash
agent-compose inspect project
agent-compose inspect <project|agent|run|sandbox|image|cache-id>
agent-compose inspect agent <agent>
agent-compose inspect run <run-id>
agent-compose inspect sandbox <sandbox>
agent-compose inspect image <image>
agent-compose inspect cache <cache-id>
```

When a full ID or hexadecimal short ID is passed as the only argument, `inspect` resolves its resource type through the daemon. Names still require the explicit typed form. Ambiguous short IDs are rejected with the matching resource types.

Details:

- `inspect project` shows project spec, revision, agents, schedulers, and related metadata.
- `inspect agent <agent>` shows agent config and runtime summary.
- `inspect run <run-id>` shows one run record.
- `inspect sandbox <sandbox>` shows sandbox/runtime details.
- `inspect image <image>` shows image details.
- `inspect cache <cache-id>` shows one daemon runtime cache item, including references, blocked reasons, and warnings.

## Image Commands

Manage images known to the daemon or referenced by the current project.

```bash
agent-compose image ls
agent-compose image pull
agent-compose image pull <image>
agent-compose image build [agent...]
agent-compose image rm <image>
agent-compose image inspect <image>
```

Commands:

- `image ls`: list images.
- `image pull`: pull all agent images referenced by the current project.
- `image pull <image>`: pull a specific image. If the local OCI image backend/store already has the image, the command succeeds directly with a skipped/already exists warning and does not pull again.
- `image build [agent...]`: build images configured for all project agents, or only the named agents when names are provided.
- `image rm <image>`: remove an image metadata/store entry. For OCI storage this removes the logical metadata reference only; physical manifests/blobs are reclaimed explicitly by CacheService once unreferenced. It does not delete materialized or runtime-derived cache.
- `image inspect <image>`: inspect an image.

The following top-level commands are shortcuts for the corresponding `image` subcommands:

| Top-level shortcut | Image command |
| --- | --- |
| `images` | `image ls` |
| `pull [image]` | `image pull [image]` |
| `build [agent...]` | `image build [agent...]` |
| `rmi <image>` | `image rm <image>` |
| `inspect image <image>` | `image inspect <image>` |

Common options:

| Command | Option | Description |
| --- | --- | --- |
| `image ls` | `-a, --all` | Show all images. |
| `image ls` | `--query <text>` | Filter by image reference. |
| `image pull` | `--platform <os/arch[/variant]>` | Pull for a specific platform. |
| `image build` | `-t, --tag <name[:tag]>` | Add an output image tag. |
| `image build` | `--dockerfile <path>` | Override the configured Dockerfile. |
| `image build` | `--target <stage>` | Select a Dockerfile target stage. |
| `image build` | `--build-arg <key=value>` | Set a build-time variable; may be repeated. |
| `image build` | `--platform <os/arch[/variant]>` | Build for a specific platform. |
| `image build` | `--no-cache` | Disable the build cache. |
| `image build` | `--pull` | Always attempt to pull newer base images. |
| `image rm` | `--force` | Force image removal. |
| `image rm` | `--prune-children` | Request child-image pruning from the image backend. OCI cache currently returns a warning and does not remove blobs or runtime/materialized cache. |

## Cache Commands

List and explicitly prune daemon runtime cache inventory. The daemon is the only component that scans cache paths and performs deletion; the CLI only sends filters and displays results.

```bash
agent-compose cache ls
agent-compose cache inspect <cache-id>
agent-compose cache prune
agent-compose cache rm <cache-id>
agent-compose inspect cache <cache-id>
```

Cache domains are shown as command-level `--type` values:

- `oci`: physical manifests, blobs, and interrupted entries in the daemon OCI image store.
- `materialized`: runtime input generated from images, such as BoxLite OCI layout or Microsandbox rootfs.
- `runtime`: shared runtime-derived images under driver homes.
- `skill`: content-addressed skill artifacts and interrupted temporary/lock entries.

Protection status:

- `active`: currently used by a running/resuming runtime; never removed.
- `referenced`: has a `REQUIRED` reference, such as OCI metadata or a running/stopped sandbox dependency. It is never removable, including with `--force`. `ADVISORY` references are shown for context but do not block deletion.
- `unused`, `expired`, `orphaned`: eligible for removal when `--force` is set.
- `unknown`: reference or safety checks were incomplete; never removed.

Common options:

| Command | Option | Description |
| --- | --- | --- |
| `cache ls`, `cache prune` | `--driver <docker|boxlite|microsandbox|all>` | Filter by runtime driver. |
| `cache ls`, `cache prune` | `--type <oci|materialized|runtime|skill>` | Filter by cache type. |
| `cache ls`, `cache prune` | `--status <active|referenced|unused|expired|orphaned|unknown>` | Filter by protection status. |
| `cache prune` | `--unused`, `--orphaned`, `--expired` | Status shortcuts; mutually exclusive with each other and with `--status`. |
| `cache prune` | `--older-than <duration>` | Match caches older than a duration such as `7d` or `168h`. |
| `cache prune`, `cache rm` | `--force` | Actually remove eligible items. Without `--force`, both commands are dry-run. |

Examples:

```bash
agent-compose cache ls --type materialized
agent-compose cache inspect <cache-id>
agent-compose cache prune --driver boxlite --unused
agent-compose cache prune --type skill --orphaned --force
agent-compose cache prune --expired --force
agent-compose cache prune --older-than 7d --force
agent-compose cache rm <cache-id> --force
```

`CACHE_TTL` defaults to `168h`; `0` disables expiration classification. TTL never triggers background/startup deletion. Use `cache prune --expired --force` explicitly. `--older-than` remains an independent filter. `cache prune` and `cache rm` default to dry-run; `--force` authorizes execution but never bypasses `active`, `referenced`, or `unknown` protection. BoxLite v0.9.7 runtime image inventory is read-only because its ABI has no safe image remove/prune operation; Microsandbox shared images use the SDK inventory/remove APIs. `sandbox prune` does not delete cache artifacts.

The daemon can optionally run time-based retention cleanup. `WORKSPACE_CLEANUP_TTL` reclaims only the workspace directory of eligible stopped sandboxes, while preserving metadata, logs, and state for audit; a reclaimed sandbox cannot be resumed. `IMAGE_CACHE_CLEANUP_TTL` removes unreferenced OCI and materialized data owned by `IMAGE_CACHE_ROOT`, using last-used time when available and pull time or filesystem modification time as a fallback. Both default to `0`, which disables that cleaner. `CLEANUP_INTERVAL` defaults to `1h`. Automatic cleanup does not touch workspace sources, Docker daemon images, BoxLite home, or Microsandbox SDK caches, and it does not implement a disk-space watermark.

Compatibility:

- `agent-compose image ls` is deprecated; use `agent-compose images`.
- `agent-compose image pull <image>` is deprecated; use `agent-compose pull <image>`.
- `agent-compose image rm <image>` is deprecated; use `agent-compose rmi <image>`.
- `agent-compose image inspect <image>` is deprecated; use `agent-compose inspect image <image>`.
- The old `image` command tree still works and prints warnings to stderr, but it may be removed in a future release.

## `status`: Query Daemon Status

Check the selected daemon status and version.

```bash
agent-compose status
agent-compose --host http://127.0.0.1:7410 status
agent-compose status --json
```

Default columns:

- `STATUS`: daemon response status.
- `UPTIME`: daemon-reported timestamp rendered in the daemon timezone when available.
- `VERSION`: daemon build version.

Use `--json` to print the raw daemon status response for automation.

## Other Commands

```bash
agent-compose daemon
agent-compose status
agent-compose version
agent-compose config
agent-compose config --quiet
```

- `daemon`: start the agent-compose daemon.
- `status`: query daemon status.
- `version`: print the CLI build version.
- `config`: parse, validate, and print normalized project config.
- `config --quiet`: validate config without printing the normalized config.

## Deferred Commands

The following commands or capabilities are not published as stable CLI features yet:

- `push`: image push is deferred.
- `up -d/--detach`: current `up` already applies the project and returns; no detach flag is provided.
- Foreground `up` attach and Ctrl+C project shutdown are deferred.

## Usage Recommendations

- Use `up` to apply a project to the daemon, then use `ps` and `logs` to observe state.
- Use `-f /path/to/project/agent-compose.yml` or `-f /path/to/project/agent-compose.yaml` for cross-directory project operations.
- When operating against a remote daemon, pass `--host` explicitly and verify the target project name and config path.
- Use `--json` in scripts and automation; do not parse table layouts.

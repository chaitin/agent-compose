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

Remote daemon authentication example:

```bash
export AUTH_USERNAME=admin
export AUTH_PASSWORD=change-me
agent-compose --host http://10.0.0.12:7410 ls
```

Rules:

- Without `-f`, the CLI looks for `agent-compose.yml` or `agent-compose.yaml` in the current directory.
- With `-f`, the CLI can operate on a project from any working directory.
- `--host` only selects the daemon. Sandboxes run in the daemon environment.
- When connecting to an HTTP(S) daemon through `--host` or `AGENT_COMPOSE_HOST`, the CLI reads Basic Auth credentials from local `AUTH_USERNAME` and `AUTH_PASSWORD`; local Unix socket connections do not use this authentication path.
- Automation should use `--json` and avoid parsing human-readable tables.

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
agent-compose --host http://10.0.0.12:7410 ls
agent-compose --host http://10.0.0.12:7410 -f /path/to/project/agent-compose.yml up
agent-compose --host http://10.0.0.12:7410 -f /path/to/project/agent-compose.yml logs --follow
```

## `ls`: List Projects

List projects known to the selected daemon.

```bash
agent-compose ls
agent-compose ls --limit 20 --offset 40
agent-compose ls --verbose
agent-compose ls --json
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

## `up`: Apply a Project

Read the config file and apply the project to the daemon. This starts or updates project schedulers and daemon-managed state.

```bash
agent-compose up
agent-compose -f /path/to/project/agent-compose.yml up
```

Current `up` semantics are daemon-style: the command applies the project and returns. It does not attach project logs and does not support `-d/--detach`.

## `down`: Stop a Project

Stop the current project, including schedulers, services, and running sandboxes.

```bash
agent-compose down
agent-compose -f /path/to/project/agent-compose.yml down
```

Notes:

- `down` only affects the selected project.
- When using `-f` or `--project-name`, verify that the command targets the intended project.
- If some sandboxes cannot be stopped, the command exits non-zero and reports the failed items.

## `run`: Run a Sandbox

Start a sandbox for an agent, or continue work in an existing sandbox.

```bash
agent-compose run <agent> <trigger-name>
agent-compose run <agent> --prompt "..."
agent-compose run <agent> --command "..."
agent-compose run <agent> --sandbox <sandbox> --prompt "..."
```

Input modes:

| Mode | Usage | Description |
| --- | --- | --- |
| trigger | `run <agent> <trigger-name>` | Run a named trigger defined in the project config. |
| prompt | `run <agent> --prompt "..."` | Send a prompt to the agent provider. |
| command | `run <agent> --command "..."` | Start or reuse the agent sandbox and execute a shell command through guest `agent-compose-runtime exec`; stdout/stderr transcript is streamed and persisted to the run record without protocol payload markers. |
| prompt REPL | `run <agent> -i --prompt` | Read prompts line by line from stdin. Each non-empty input creates one run and reuses the same sandbox. |
| command REPL | `run <agent> -i --command` | Read commands line by line from stdin. Each non-empty input creates one run and reuses the same sandbox. |
| sandbox reuse | `run <agent> --sandbox <sandbox> --prompt "..."` | Continue in a specific sandbox. |

Prompt input must use `--prompt`. Positional prompt arguments are not supported.
If the agent has no configured triggers, `run <agent> <trigger-name>` fails with
a usage error; use `--prompt` or `--command` for ad hoc work.

| Option | Description |
| --- | --- |
| `--keep-running` | Keep the sandbox runtime after the run completes. |
| `--sandbox <sandbox>` | Reuse an existing sandbox. |
| `--session-id <session-id>` | Deprecated alias for `--sandbox`; prints a warning to stderr. |
| `--rm` | Remove the sandbox after the run reaches a terminal state. |
| `--jupyter` | Enable Jupyter for this run. When unset, the agent YAML default is used; when YAML is unset, Jupyter is disabled. |
| `--jupyter-expose` | Mark the Jupyter agent-compose proxy endpoint for this run as explicitly exposed. This does not request runtime-driver host port exposure and also enables Jupyter. |
| `-d, --detach` | Submit the run to the daemon and return immediately with the run id, initial status, and a `logs --follow` command. |
| `-i, --interactive` | Enter prompt or command REPL mode. Must be combined with `--prompt` or `--command`. |

Examples:

```bash
agent-compose run reviewer pr-opened
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

- Choose only one of trigger, prompt, or command.
- `run <agent> <trigger-name>` accepts exactly one trigger name positional argument.
- Do not combine `--prompt` or `--command` with additional positional arguments.
- `run -d/--detach` and `run -i/--interactive` are mutually exclusive.
- `run -i/--interactive` must select `--prompt` or `--command`; it cannot be combined with `--json`.
- Empty REPL lines do not create runs. Enter `/exit` or press Ctrl+D to exit.
- REPL mode is not TTY/PTY or running stdin passthrough. Each input is one independent `RunAgentStream` call that reuses the same sandbox.
- Detached runs can be observed with the printed `agent-compose logs --run-id <run-id> --follow` command, or managed later with `stop` and `logs`.
- `run -i --prompt` supports providers with reusable provider sessions: Codex, Claude/cc, and OpenCode. Gemini currently returns unsupported.
- `StopRun` requests cancellation for active in-daemon runs. Pending/running runs left behind after daemon restart are reconciled to failed with a `daemon interrupted` error.

## `ps`: List Sandboxes

List sandboxes in the current project. By default, only running sandboxes are shown.
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
| `-a, --all` | Show all sandboxes, including completed and errored ones. |
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
agent-compose exec <sandbox>
agent-compose exec <sandbox> <command> [args...]
agent-compose exec <sandbox> --command "..."
```

| Option | Description |
| --- | --- |
| `--command "..."` | Pass a shell command as a flag. It is executed as `bash -lc "..."` in the sandbox. |
| `--cwd <path>` | Set the working directory inside the sandbox. |
| `--session-id <sandbox>` | Deprecated alias for positional `<sandbox>`; prints a warning to stderr. |
| `--agent <agent>` | Deprecated target selection option; use `exec <sandbox>` instead. |
| `--run-id <run-id>` | Deprecated target selection option; use `exec <sandbox>` instead. |

Examples:

```bash
agent-compose exec sandbox_123
agent-compose exec sandbox_123 pwd
agent-compose exec sandbox_123 bash -lc "task test"
agent-compose exec sandbox_123 --command "git status --short"
agent-compose exec sandbox_123 --cwd /workspace --command "pwd"
```

`exec` and `run --command` use the same guest `agent-compose-runtime exec` command output path. Text mode streams command stdout to local stdout and command stderr to local stderr after host-side marker filtering; it does not echo the host wrapper command. `--json` suppresses streaming output and prints only the final result. `exec` does not create a `ProjectRun`; use `run --command` when run audit, `logs`, or run artifacts are required.

## `logs`: Show Logs

Show logs for agents, sandboxes, or runs in the current project. By default, logs for all project agents are shown.

Current `logs` output is based on run log artifacts returned by the v2 RunService. `--follow` is served by the daemon from the log file referenced by `logs_path`; non-follow views use the run record output and artifact summary. It does not automatically read private provider log files from Codex, Claude, Gemini, or other provider CLIs.

```bash
agent-compose logs
agent-compose logs <agent>
agent-compose logs --agent reviewer
agent-compose logs --run-id <run-id>
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
| `--run-id <run-id>` | Filter by run id. |
| `--sandbox <sandbox>` | Filter by sandbox. |
| `--session-id <sandbox>` | Deprecated alias for `--sandbox`; prints a warning to stderr. |

Examples:

```bash
agent-compose logs
agent-compose logs reviewer
agent-compose logs --agent reviewer --tail 200
agent-compose logs --sandbox sandbox_123 --follow -t
agent-compose logs --run-id run_123 --json
```

## `inspect`: Inspect Resources

Inspect project resources or daemon images.
`inspect project` and `inspect agent <agent>` require the project to already exist on the daemon; after `agent-compose down`, run `agent-compose up` again before using them.

```bash
agent-compose inspect project
agent-compose inspect agent <agent>
agent-compose inspect run <run-id>
agent-compose inspect sandbox <sandbox>
agent-compose inspect session <sandbox>
agent-compose inspect image <image>
```

Details:

- `inspect project` shows project spec, revision, agents, schedulers, and related metadata.
- `inspect agent <agent>` shows agent config and runtime summary.
- `inspect run <run-id>` shows one run record.
- `inspect sandbox <sandbox>` shows sandbox/runtime details.
- `inspect session <sandbox>` is a deprecated compatibility entrypoint; use `inspect sandbox`.
- `inspect image <image>` shows image details.

## Image Commands

Manage images known to the daemon or referenced by the current project.

```bash
agent-compose images
agent-compose pull
agent-compose pull <image>
agent-compose rmi <image>
agent-compose inspect image <image>
```

Commands:

- `images`: list images.
- `pull`: pull all agent images referenced by the current project.
- `pull <image>`: pull a specific image. If the local OCI image backend/store already has the image, the command succeeds directly with a skipped/already exists warning and does not pull again.
- `rmi <image>`: remove an image.
- `inspect image <image>`: inspect an image.

Common options:

| Command | Option | Description |
| --- | --- | --- |
| `images` | `-a, --all` | Show all images. |
| `images` | `--query <text>` | Filter by image reference. |
| `pull` | `--platform <os/arch[/variant]>` | Pull for a specific platform. |
| `rmi` | `--force` | Force image removal. |
| `rmi` | `--prune-children` | Remove untagged child images. |

Compatibility:

- `agent-compose image ls` is deprecated; use `agent-compose images`.
- `agent-compose image pull <image>` is deprecated; use `agent-compose pull <image>`.
- `agent-compose image rm <image>` is deprecated; use `agent-compose rmi <image>`.
- `agent-compose image inspect <image>` is deprecated; use `agent-compose inspect image <image>`.
- The old `image` command tree still works and prints warnings to stderr, but it may be removed in a future release.

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

- `build`: project image build is deferred.
- `push`: image push is deferred.
- `up -d/--detach`: current `up` already applies the project and returns; no detach flag is provided.
- Foreground `up` attach and Ctrl+C project shutdown are deferred.

## Usage Recommendations

- Use `up` to apply a project to the daemon, then use `ps` and `logs` to observe state.
- Use `-f /path/to/project/agent-compose.yml` or `-f /path/to/project/agent-compose.yaml` for cross-directory project operations.
- When operating against a remote daemon, pass `--host` explicitly and verify the target project name and config path.
- Use `--json` in scripts and automation; do not parse table layouts.

# agent-compose CLI User Guide

This guide explains how to use the `agent-compose` CLI, how to connect it to a daemon with `--host`, and the common mistakes to avoid.

## Usage Model

The recommended deployment model is:

- `agent-compose daemon` runs on a local or remote container host. It owns runtime lifecycle, project state, agent/service runs, schedulers, and persisted history.
- The `agent-compose` CLI runs on the user's machine and connects to the daemon over HTTP with `--host`.
- Compose manifests are read from the CLI machine. When `up` is executed, the normalized project spec and bundle files are submitted to the daemon.

Each command section first shows the default local form without `--host`, then shows the recommended remote-daemon form with `--host`.

## Global Flags

All subcommands support these global flags:

```bash
agent-compose [flags] [command]

-f, --file string           Path to agent-compose.yml
    --host string           Daemon HTTP endpoint
    --json                  Print machine-readable JSON
    --project-name string   Override compose project name
```

Common usage:

- `--host`: daemon HTTP endpoint, for example `http://127.0.0.1:7410` or `http://server.example.com:7410`.
- `--file` / `-f`: compose manifest path. By default, the CLI looks for `agent-compose.yml`, `agent-compose.yaml`, or `agent-compose.json` in the current directory.
- `--project-name`: overrides the manifest `name`. Useful for tests and parallel runs using the same manifest.
- `--json`: emits machine-readable output. This is useful for scripts and troubleshooting.

Common mistakes:

- Running from a directory that does not contain a compose manifest without passing `--file`.
- Forgetting `--host` when the daemon is not local to the CLI process.
- Testing with the manifest project name and accidentally updating an existing project.

## Quick Start

For `examples/agent-compose/docker-minimal/agent-compose.yml`, the manifest defines one agent named `reviewer`.

Default local form:

```bash
cd examples/agent-compose/docker-minimal

agent-compose config
agent-compose up
agent-compose ps
agent-compose run reviewer --prompt "hi"
```

Recommended remote-daemon form:

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  config

agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  up

agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  ps

agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  run reviewer --prompt "hi"
```

Important rule: in `run <agent> [prompt...]`, the first argument is the agent name, not the prompt.

Wrong:

```bash
agent-compose run "hi"
```

Correct:

```bash
agent-compose run reviewer "hi"
agent-compose run reviewer --prompt "hi"
```

## daemon

### Purpose

Starts the long-running agent-compose backend service. The daemon handles API requests, project state, scheduler execution, runtime sessions, Jupyter proxying, and persistence.

### Default Usage

```bash
agent-compose daemon
```

### Remote Access Notes

`daemon` itself does not use `--host` to connect to another daemon. `--host` is a client-side flag used by CLI commands that talk to a daemon.

Typical local development startup:

```bash
HTTP_LISTEN=127.0.0.1:7410 agent-compose daemon
```

Then connect from the CLI:

```bash
agent-compose --host http://127.0.0.1:7410 status
```

Common mistakes:

- Treating `--host` as the daemon listen address. The daemon listen address is controlled by runtime configuration such as `HTTP_LISTEN`.
- Running daemon-dependent commands before the daemon is started.

## version

### Purpose

Prints the local CLI binary version.

### Default Usage

```bash
agent-compose version
```

### Remote Access Example

`version` is local and does not contact the daemon. Check both CLI and daemon during troubleshooting:

```bash
agent-compose version
agent-compose --host http://127.0.0.1:7410 status
```

Common mistake:

- Checking only the CLI version and forgetting that the daemon may be a different version.

## status

### Purpose

Checks daemon availability and returns daemon status/version information.

### Default Usage

```bash
agent-compose status
```

### Remote Access Example

```bash
agent-compose --host http://127.0.0.1:7410 status
```

Common mistake:

- Running `agent-compose status` without `--host` when the daemon is remote.

## config

### Purpose

Loads a local compose manifest, validates it, and prints the normalized project config. Use this before `up` to confirm agent names, images, drivers, and defaults.

### Default Usage

```bash
agent-compose config
agent-compose config --quiet
```

### Remote Access Example

`config` mainly works on local files. Passing `--host` is harmless for command consistency, but it does not change local manifest parsing.

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  config
```

Common mistakes:

- Expecting `config` to apply changes to the daemon. Use `up` for that.
- Running from the wrong directory without `--file`.

## validate

### Purpose

Validates a local compose manifest. It can also print the embedded manifest JSON Schema.

### Default Usage

```bash
agent-compose validate
agent-compose validate --dry-run
agent-compose validate --schema
```

### Remote Access Example

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  validate --dry-run
```

Common mistakes:

- Treating `validate --dry-run` as deployment. It does not create or update daemon state.
- Looking through source code for the schema instead of using `validate --schema`.

## bundle

### Purpose

Validates or inspects a compose bundle directory. A bundle directory must contain `agent-compose.yml`, `agent-compose.yaml`, or `agent-compose.json`, plus any referenced files such as service entry scripts and schemas.

### Default Usage

```bash
agent-compose bundle inspect [dir]
agent-compose bundle validate [dir]
agent-compose bundle validate [dir] --dry-run
```

### Remote Access Example

```bash
agent-compose --host http://127.0.0.1:7410 \
  bundle inspect examples/service-entry

agent-compose --host http://127.0.0.1:7410 \
  bundle validate examples/service-entry --dry-run
```

Common mistakes:

- Running `agent-compose bundle inspect` from a directory that does not contain a manifest.
- Assuming `--file` defines the bundle root. For bundle commands, use the `[dir]` argument.

## up

### Purpose

Applies the local compose project to the daemon. It submits the normalized project spec and bundle files, then creates or updates daemon-side resources such as projects, agents, services, and schedulers.

### Default Usage

```bash
agent-compose up
```

### Remote Access Example

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  up
```

Use a temporary project name when testing:

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  --project-name my-docker-minimal-test \
  up
```

Common mistakes:

- Editing the manifest and running only `config`; daemon state is updated only by `up`.
- Expecting `up` to run an agent. Use `run` or `invoke` for execution.
- Forgetting `--project-name` during tests and updating an existing project.

## down

### Purpose

Stops project schedulers and running sessions. Use it to stop scheduled work and clean up test runtimes.

### Default Usage

```bash
agent-compose down
```

### Remote Access Example

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  down
```

Common mistakes:

- Assuming `down` deletes project history. It mainly disables schedulers and stops sessions.
- Running `down` against the wrong project. Use `--file` and `--project-name` to be explicit.

## ls / list

### Purpose

Lists projects already applied to the daemon. `list` is an alias for `ls`.

### Default Usage

```bash
agent-compose ls
agent-compose ls --query docker-minimal
agent-compose ls --verbose
```

### Remote Access Example

```bash
agent-compose --host http://127.0.0.1:7410 ls
agent-compose --host http://127.0.0.1:7410 ls --query docker-minimal --verbose
agent-compose --host http://127.0.0.1:7410 --json ls --query docker-minimal
```

Common mistakes:

- Using `ps` to list all projects. `ps` is scoped to one project; use `ls` for daemon-wide project listing.
- Treating `--query` as a SQL query. It filters by project name, id, or source path.

## ps

### Purpose

Shows the current project agents, schedulers, latest runs, sessions, drivers, and images.

### Default Usage

```bash
agent-compose ps
```

### Remote Access Example

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  ps

agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  --json ps
```

Common mistakes:

- Running `ps` before `up`.
- Running `exec` without checking whether the `SESSION` column has a running session.

## run

### Purpose

Manually runs one agent from the current project and sends it a prompt.

### Default Usage

```bash
agent-compose run <agent> [prompt...]
agent-compose run <agent> --prompt "..."
agent-compose run <agent> --prompt "..." --keep-running
agent-compose run <agent> --session-id <session-id> --prompt "..."
```

### Remote Access Example

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  run reviewer --prompt "hi"
```

Keep the runtime session for later `exec`:

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  run reviewer --prompt "hi" --keep-running
```

JSON output:

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  --json run reviewer --prompt "hi"
```

Common mistakes:

- Passing the prompt as the first argument. `run "hi"` treats `hi` as the agent name.
- Not knowing available agent names. Use `config`, `ps`, or `inspect project`.
- Assuming `--keep-running` means the agent keeps working forever. It keeps the runtime session after the run completes.
- Forgetting that provider credentials and network access are runtime requirements.

## invoke

### Purpose

Invokes a service entry defined in the current project. Use it for structured input/output and non-interactive workflows.

### Default Usage

```bash
agent-compose invoke <service> --input-json '{"key":"value"}'
agent-compose invoke <service> --input-file input.json
agent-compose invoke <service> --input-json '{"key":"value"}' --keep-running
agent-compose invoke <service> --session-id <session-id> --input-json '{"key":"value"}'
```

### Remote Access Example

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/service-entry/agent-compose.yml \
  invoke risk-review --input-json '{"scope":"daily"}'
```

Use JSON output for troubleshooting:

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/service-entry/agent-compose.yml \
  --json invoke risk-review --input-json '{"scope":"daily"}'
```

Common mistakes:

- Passing both `--input-json` and `--input-file`.
- Providing input that does not match the service input schema.
- Forgetting to run `up` before invoking a service.
- Passing an agent name to `invoke`; it expects a key from `services:`.

## logs

### Purpose

Prints project run logs. Logs can be filtered by agent, run id, or session id, and can follow running output.

### Default Usage

```bash
agent-compose logs
agent-compose logs --agent reviewer
agent-compose logs --run-id <run-id>
agent-compose logs --session-id <session-id>
agent-compose logs --follow
```

### Remote Access Example

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  logs --agent reviewer

agent-compose --host http://127.0.0.1:7410 \
  --file examples/service-entry/agent-compose.yml \
  logs --run-id <run-id>
```

JSON output:

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/service-entry/agent-compose.yml \
  --json logs --run-id <run-id>
```

Common mistakes:

- Combining `--json` and `--follow`; this is not supported.
- Expecting logs before any run has happened.
- Using `--agent` for service runs. Prefer `--run-id` for service run logs.

## exec

### Purpose

Executes a command inside a running project session. Use it to inspect the guest environment, workspace, and runtime variables.

### Default Usage

```bash
agent-compose exec --agent <agent> -- <command> [args...]
agent-compose exec --session-id <session-id> -- <command> [args...]
agent-compose exec --run-id <run-id> -- <command> [args...]
agent-compose exec --agent <agent> --cwd /workspace -- pwd
```

### Remote Access Example

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  exec --agent reviewer -- pwd
```

Target a specific session:

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  exec --session-id <session-id> -- env
```

Common mistakes:

- Running `exec` without a running session. Use `ps`, or create one with `run --keep-running`.
- Forgetting `--` before the guest command when command args may look like CLI flags.
- Using `--agent` when multiple sessions may match. Prefer `--session-id` for precision.

## inspect

### Purpose

Prints details for a project, agent, run, or session. The output is JSON-shaped and useful for debugging.

### Default Usage

```bash
agent-compose inspect project
agent-compose inspect agent <agent-name>
agent-compose inspect run <run-id>
agent-compose inspect session <session-id>
```

### Remote Access Example

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  inspect project

agent-compose --host http://127.0.0.1:7410 \
  --file examples/agent-compose/docker-minimal/agent-compose.yml \
  inspect agent reviewer
```

Run and session inspection:

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/service-entry/agent-compose.yml \
  inspect run <run-id>

agent-compose --host http://127.0.0.1:7410 \
  --file examples/service-entry/agent-compose.yml \
  inspect session <session-id>
```

Common mistakes:

- Running `inspect agent` without an agent name.
- Inspecting a run id under the wrong project context.
- Passing a run id to `inspect session`; it expects a session id.

## images / image ls

### Purpose

Lists images visible to the daemon host. `images` is equivalent to `image ls`.

### Default Usage

```bash
agent-compose images
agent-compose image ls
agent-compose image ls --query agent-compose-guest
agent-compose image ls --all
```

### Remote Access Example

```bash
agent-compose --host http://127.0.0.1:7410 image ls --query agent-compose-guest
agent-compose --host http://127.0.0.1:7410 --json image ls --query agent-compose-guest
```

Common mistakes:

- Expecting this to list images on the CLI machine. With `--host`, it lists images on the daemon host.
- Omitting `--query` on hosts with many images.

## image inspect

### Purpose

Shows daemon-host image details such as image id, tags, platform, size, availability, and container count.

### Default Usage

```bash
agent-compose image inspect <image>
```

### Remote Access Example

```bash
agent-compose --host http://127.0.0.1:7410 \
  image inspect agent-compose-guest:latest
```

Common mistake:

- Using an image that exists on the CLI machine but not on the daemon host.

## pull / image pull

### Purpose

Pulls an image on the daemon host. `pull` is a top-level shortcut for `image pull`.

### Default Usage

```bash
agent-compose pull <image>
agent-compose image pull <image>
agent-compose pull <image> --platform linux/arm64
```

### Remote Access Example

```bash
agent-compose --host http://127.0.0.1:7410 \
  pull ghcr.io/chaitin/agent-compose-guest:latest --platform linux/arm64
```

Common mistakes:

- Running `docker pull` on the CLI machine and expecting the remote daemon host to use it.
- Ignoring platform compatibility, especially on ARM64 hosts.

## rmi / image rm

### Purpose

Removes an image from the daemon host. `rmi` is a top-level shortcut for `image rm`.

### Default Usage

```bash
agent-compose rmi <image>
agent-compose image rm <image>
agent-compose image rm <image> --force
agent-compose image rm <image> --prune-children
```

### Remote Access Example

```bash
agent-compose --host http://127.0.0.1:7410 \
  image rm old-image:latest
```

Common mistakes:

- Removing an image still used by sessions or containers. Check `container_count` with `image inspect`.
- Using `--force` without confirming the impact.

## JSON Output

Commands where `--json` is especially useful:

```bash
agent-compose --json ls
agent-compose --json ps
agent-compose --json run reviewer --prompt "hi"
agent-compose --json invoke risk-review --input-json '{"scope":"daily"}'
agent-compose --json logs --run-id <run-id>
agent-compose --json image ls --query agent-compose-guest
```

Do not combine:

- `logs --json --follow`: this is not supported.

## Common Issues

### `project agent .../hi not found`

Cause: the prompt was passed in the `<agent>` position.

Wrong:

```bash
agent-compose run "hi"
```

Correct:

```bash
agent-compose run reviewer "hi"
agent-compose run reviewer --prompt "hi"
```

Confirm agent names:

```bash
agent-compose config
agent-compose ps
agent-compose inspect project
```

### Project Not Found Or Unexpected Project State

Check the compose file and project name:

```bash
agent-compose --file /path/to/agent-compose.yml config
agent-compose --host http://127.0.0.1:7410 ls --query <project-name>
```

If you used `--project-name` with `up`, use the same `--project-name` for `ps`, `run`, `logs`, and `down`.

### `exec` Reports No Running Session

Check `ps`:

```bash
agent-compose ps
```

If `SESSION` is empty, create a retained session:

```bash
agent-compose run reviewer --prompt "hi" --keep-running
```

Then execute:

```bash
agent-compose exec --agent reviewer -- pwd
```

### Service Succeeded But Output Location Is Unclear

Use `--json invoke` and inspect:

- `run_id`
- `session_id`
- `status`
- `output_json`
- `logs_path`
- `artifacts`

Example:

```bash
agent-compose --host http://127.0.0.1:7410 \
  --file examples/service-entry/agent-compose.yml \
  --json invoke risk-review --input-json '{"scope":"daily"}'
```

### Remote Daemon Cannot See An Image

Image commands operate on the daemon host:

```bash
agent-compose --host http://127.0.0.1:7410 image ls --query agent-compose-guest
agent-compose --host http://127.0.0.1:7410 image inspect agent-compose-guest:latest
```

Pull on the daemon host:

```bash
agent-compose --host http://127.0.0.1:7410 pull agent-compose-guest:latest
```

## Recommended Troubleshooting Order

1. Check daemon: `agent-compose --host <url> status`
2. Check local manifest: `agent-compose --file <manifest> config`
3. Check daemon project: `agent-compose --host <url> ls --query <name>`
4. Apply manifest: `agent-compose --host <url> --file <manifest> up`
5. Check project state: `agent-compose --host <url> --file <manifest> ps`
6. Run an agent or service: `run` / `invoke`
7. Inspect details: `logs` / `inspect run` / `inspect session`
8. Clean up: `down`

## Command Cheatsheet

```bash
# Daemon status
agent-compose --host http://127.0.0.1:7410 status
agent-compose version

# Local manifest
agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml config
agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml validate --dry-run
agent-compose validate --schema

# Bundle
agent-compose bundle inspect examples/service-entry
agent-compose bundle validate examples/service-entry --dry-run

# Project
agent-compose --host http://127.0.0.1:7410 --file examples/agent-compose/docker-minimal/agent-compose.yml up
agent-compose --host http://127.0.0.1:7410 ls --query docker-minimal --verbose
agent-compose --host http://127.0.0.1:7410 --file examples/agent-compose/docker-minimal/agent-compose.yml ps
agent-compose --host http://127.0.0.1:7410 --file examples/agent-compose/docker-minimal/agent-compose.yml down

# Agent
agent-compose --host http://127.0.0.1:7410 --file examples/agent-compose/docker-minimal/agent-compose.yml run reviewer --prompt "hi"
agent-compose --host http://127.0.0.1:7410 --file examples/agent-compose/docker-minimal/agent-compose.yml run reviewer --prompt "hi" --keep-running

# Service
agent-compose --host http://127.0.0.1:7410 --file examples/service-entry/agent-compose.yml invoke risk-review --input-json '{"scope":"daily"}'
agent-compose --host http://127.0.0.1:7410 --file examples/service-entry/agent-compose.yml --json invoke risk-review --input-json '{"scope":"daily"}'

# Logs / inspect
agent-compose --host http://127.0.0.1:7410 --file examples/agent-compose/docker-minimal/agent-compose.yml logs --agent reviewer
agent-compose --host http://127.0.0.1:7410 --file examples/agent-compose/docker-minimal/agent-compose.yml inspect project
agent-compose --host http://127.0.0.1:7410 --file examples/agent-compose/docker-minimal/agent-compose.yml inspect agent reviewer

# Images
agent-compose --host http://127.0.0.1:7410 image ls --query agent-compose-guest
agent-compose --host http://127.0.0.1:7410 image inspect agent-compose-guest:latest
agent-compose --host http://127.0.0.1:7410 pull agent-compose-guest:latest
```

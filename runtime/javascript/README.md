# @chaitin-ai/agent-compose-runtime

`@chaitin-ai/agent-compose-runtime` is the guest-side runtime package used by agent-compose agent sandboxes. It exposes the compatible CLI entrypoint:

```sh
agent-compose-runtime prompt \
  --provider <codex|claude|opencode|pi|dsh> \
  --message-file <path> \
  --output-schema-file <path> \
  --state-root <path> \
  --workspace <path> \
  --home <path> \
  --model <model> \
  --skill <name>
```

`--skill` may be repeated to enable multiple agent skills.

By default, successful runs write a single structured result line to stdout with the `__AGENT_RESULT__` prefix. Human-readable agent transcript output is written to stderr. This default remains compatible with the runtime SDK and existing scripts.

Setting `AGENT_COMPOSE_RUNTIME_OUTPUT=frames` switches stdout to version 1 NDJSON frames (`started`, `agent_event`, `agent_turn_completed`, `result`, or `error` on failure), using the same envelope as `stream`, so a caller receives the run's structured agent events rather than only its final result. Transcript text is then not mirrored to stderr; stderr remains available for diagnostics. Any other nonempty value is rejected before the agent starts.

Older runtimes ignore this variable and write only the `__AGENT_RESULT__` line, so a caller that sets it must accept both formats. The variable is removed from provider subprocess environments so tool scripts and nested SDK calls keep the default output contract. The `exec` command's `__COMMAND_RESULT__` format is unchanged.

`--output-schema-file` is optional. When set, the file must contain a JSON Schema object. The runtime passes it to the provider's native structured-output mechanism where supported. Codex and Claude support schema-based output; OpenCode, Pi, and DSH currently reject schema requests until a native provider mechanism is wired.

## Command and stream subcommands

The `exec` command runs one command request read from a JSON file and writes a single structured result line to stdout with the `__COMMAND_RESULT__` prefix:

```sh
agent-compose-runtime exec \
  --request-file /tmp/request.json \
  --state-root /data/state \
  --workspace /workspace \
  --home /root
```

The `stream` command runs the runtime NDJSON stream protocol on stdin/stdout and takes no options:

```sh
agent-compose-runtime stream
```

## Guest ABI self-description

The `abi` command reports the guest ABI contract version and the agent providers baked into the image:

```sh
agent-compose-runtime abi
agent-compose-runtime abi --json --manifest /etc/agent-compose/guest-abi.json
```

It reads `/etc/agent-compose/guest-abi.json` by default; `--manifest` overrides that path. `--json` prints the report as a single JSON line with `guestAbi`, `providers`, `manifestPath`, and `runtimeVersion`. A missing, unreadable, or malformed manifest is reported as `guestAbi: 0` with an empty provider map — the legacy-guest case — and the command still exits 0.

## Dynamic workflows

The `workflow` command runs a restricted JavaScript orchestration script:

```sh
agent-compose-runtime workflow \
  --script-file workflows/inspect.js \
  --args-file /tmp/workflow-args.json \
  --state-root /data/state \
  --workspace /workspace \
  --provider codex \
  --concurrency 4
```

The script begins with static metadata and can use `agent`, `parallel`,
`pipeline`, `phase`, `log`, `workflow`, `args`, `budget`, `cwd`, and a frozen
`process` object:

```js
export const meta = {
  name: "inspect",
  description: "Inspect backend and frontend modules",
}

const findings = await parallel([
  () => phase("Backend", () => agent("Inspect backend", { key: "backend" })),
  () => phase("Frontend", () => agent("Inspect frontend", { key: "frontend" })),
])

return { findings }
```

The CLI writes exactly one final `__WORKFLOW_RESULT__` line to stdout. Live
events use `__WORKFLOW_EVENT__` lines on stderr; provider transcript lines on
stderr remain unprefixed. Runs are stored below
`<state-root>/workflows/runs/<run-id>`.

Existing prompt sessions are backward compatible: when the internal
`sessionRoot` override is absent, provider state continues to use `stateRoot`
with the existing layout. Workflow child agents alone receive isolated session
roots; runtime configuration is still read from the original `stateRoot` and is
never copied or migrated.

## Agent system prompt (convention path)

When the host binds a run to an agent definition with non-empty `system_prompt`, it writes:

```text
<state-root>/agents/system-prompts/system-prompt.txt
```

The `prompt` command reads that convention path, combines it with the MPI catalog via `buildSystemContext` in `src/system-context.ts`, and passes the result to provider runners as `systemContext`. Per-turn user text stays in `--message-file` only.

## Development

```sh
npm install
npm run build
npm test
```

The TypeScript source lives in `src/`:

- `cli.ts`: commander-based CLI.
- `guest-abi.ts`: guest ABI manifest reading and self-description reports.
- `prompt.ts`: command orchestration and default path resolution.
- `system-context.ts`: agent identity + MPI composition.
- `runners/`: provider adapters for Codex, Claude, OpenCode, Pi, and DSH.
- `mpi.ts`: MPI catalog discovery and context formatting.
- `session-state.ts`: provider thread resume state persistence.
- `workflow/`: workflow parsing, execution, events, persistence, resume cache, and worktree isolation.

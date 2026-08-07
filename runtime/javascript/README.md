# @chaitin-ai/agent-compose-runtime

`@chaitin-ai/agent-compose-runtime` is the guest-side runtime package used by agent-compose agent sandboxes. It exposes the compatible CLI entrypoint:

```sh
agent-compose-runtime prompt \
  --provider <codex|claude|gemini|opencode|pi> \
  --message-file <path> \
  --output-schema-file <path> \
  --state-root <path> \
  --workspace <path> \
  --home <path>
```

Successful runs write a single structured result line to stdout with the `__AGENT_RESULT__` prefix. Human-readable agent transcript output is written to stderr.

`--output-schema-file` is optional. When set, the file must contain a JSON Schema object. The runtime passes it to the provider's native structured-output mechanism where supported. Codex and Claude support schema-based output; Gemini, OpenCode, and Pi currently reject schema requests until a native provider mechanism is wired.

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
`pipeline`, `phase`, `log`, `workflow`, `args`, and `budget`:

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

See `docs/design/agent_system_prompt_design.md` for the full host/guest contract.

## Development

```sh
npm install
npm run build
npm test
```

The TypeScript source lives in `src/`:

- `cli.ts`: commander-based CLI.
- `prompt.ts`: command orchestration and default path resolution.
- `system-context.ts`: agent identity + MPI composition.
- `runners/`: provider adapters for Codex, Claude, Gemini, OpenCode, and Pi.
- `mpi.ts`: MPI catalog discovery and context formatting.
- `session-state.ts`: provider thread resume state persistence.
- `workflow/`: workflow parsing, execution, events, persistence, resume cache, and worktree isolation.

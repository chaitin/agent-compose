# DSH agent provider design

## 1. Overview

`dsh` (DeepSeek Harness) is a Cordis-based agent runtime, added to agent-compose as a sixth provider alongside `codex`, `claude`, `gemini`, `opencode`, `pi`. Unlike the others, `dsh` is not a single CLI binary with flags — it boots a *profile*: an ordered stack of plugin-bundle patch layers. agent-compose ships its own profile (`assets/.dsh/profiles/agent-compose/`) rather than passing flags to a generic binary.

## 2. Composition model

The profile is a static overlay on top of the `@deepseek-ai/dsh-base` bundle:

- `cordis.patch.yml` — patches individual plugin rows from `dsh-base` (model/provider selection, session persistence root, skill filesystem scope, credential/settings sources) and inserts one agent-compose-owned plugin (`agent-compose-runner`, see §3.1).
- `runner.js` — the inserted plugin's implementation.
- `package.json` — declares the profile's bundle (`dsh-base`); no other dependencies are needed because everything `runner.js` imports (`dsh-llm`, `dsh-session`, `dsh-mcp-client`, …) resolves transitively through the globally-installed `@deepseek-ai/dsh` package's own `node_modules`.

This file ships as a repo asset (`assets/.dsh/...`), baked into the guest image at `/root/.dsh` — not an npm package, and not rebuilt per run. Every mutable, per-run value (model, credentials, skills, MCP servers, prompt text, session id) has to flow in through the **spawn-time environment** that `runtime/javascript/src/runners/dsh.ts` sets when it `spawn()`s `dsh --profile agent-compose` (§3.2), because the profile itself is fixed at image-build time.

## 3. Runtime driving

### 3.1 `agent-compose-runner` plugin

`runner.js` is inserted into the profile via `cordis.patch.yml`'s `insert:` list, injecting `agents`, `sessions`, and `agentDefaultModel`. It is modeled on `@deepseek-ai/dsh-headless`'s one-shot driver (create, followup, whenIdle, flush, exit) and `dsh-cc-tui`'s create-vs-resume/event-subscription pattern, but reuses neither directly: headless has no resume or event stream, and cc-tui is interactive.

### 3.2 Parameterization via spawn-time environment

`cordis.patch.yml` reads most per-run values with `!!js process.env.X` (a `new Function('ctx','expr','with(ctx){return eval(expr)}')` sandbox with no `require`), which constrains that path to environment variables — no temp-file indirection, since the eval sandbox can't `readFileSync`. `runner.js`, by contrast, is real ESM with full `fs` access, so anything it (rather than a static YAML row) consumes can go through a temp file instead — see §7 for why `DSH_SYSTEM_CONTEXT` does.

Env vars aren't unbounded: Linux caps a single `argv`/`envp` string at `MAX_ARG_STRLEN` (128 KiB). `dsh.ts` checks `DSH_MCP_SERVERS` against that limit before spawning and fails with a named, actionable error rather than letting the OS reject the `exec()` call as an opaque `E2BIG`.

### 3.3 Create vs. resume

`DSH_RESUME=1` plus `DSH_SESSION_ID` selects `agents.resume()`; otherwise `agents.create()` with a host-generated `session-<uuid>`. A resume miss is deliberately uncaught — falling back to `create()` would silently drop the caller's history, so it fails loud instead.

### 3.4 Event streaming protocol

`runner.js` subscribes to `ctx.on('session/event', ...)` and writes each event to stdout as `{"type":"session_event","sessionId":...,"event":...}\n`. `dsh.ts` parses this line-by-line, cross-checking `sessionId` when present and mapping `assistant/chunk` → transcript text, `assistant/message` → final text, `turn/end`'s `reason.kind` → `stopReason` (surfacing `reason.error` as a thrown error for `kind: "error"`).

### 3.5 Environment variable reference

| Variable | Set by | Purpose |
| --- | --- | --- |
| `DSH_MODEL` | `dsh.ts` | Model name (provider routing is resolved host-side; only the model literal crosses) |
| `DSH_REASONING_EFFORT` | `dsh.ts` | agent-compose's 5-level `effort` collapsed to DSH's 2-level `high`/`max` (§6 has no equivalent collapse — this is the reasoning-effort case) |
| `DSH_PERMISSION_MODE` | facade config + `dsh.ts` | Always `danger-full-access`; guest sandboxing is the agent-compose sandbox, not a nested DSH one (§5.3/§5.5) |
| `DSH_SESSION_ROOT`, `DSH_SESSION_ID`, `DSH_RESUME` | `dsh.ts` | Session persistence and resume (§3.3) |
| `DSH_PROMPT_FILE` | `dsh.ts` | Path to the prompt text file `runner.js` reads |
| `DSH_SYSTEM_CONTEXT_FILE` | `dsh.ts` | Path to the persona text file `runner.js` reads and injects (§7); unset when there's no system context |
| `DSH_SKILL_DIRS` | `dsh.ts` | Colon-joined resolved skill directories; consumed by the `skill-filesystem` row's `customSkillDirs` (§5.1) |
| `DSH_MCP_SERVERS` | `dsh.ts` | JSON array of per-server `dsh-mcp-client` configs; consumed by `runner.js` (§6) |
| `LLM_API_KEY`, `LLM_API_ENDPOINT` | facade config | Consumed by the `llm-deepseek` row (§4) |

`env` starts from `...process.env`, so a key this run has no value for isn't automatically absent — it's whatever the host process happened to export. Every conditional `DSH_*` var (`DSH_SYSTEM_CONTEXT_FILE`, `DSH_MCP_SERVERS`, `DSH_RESUME`, `DSH_MODEL`, `DSH_REASONING_EFFORT`, `DSH_SKILL_DIRS`) is therefore explicitly `delete`d in its false branch rather than left conditionally-set, so a host-inherited value can't leak through as this run's persona file, MCP server list, resume flag, model, effort, or skill directories. `DSH_SKILL_DIRS` is the sharpest case: an inherited value would have `dsh` load a skill directory `resolveSkillPaths()`'s symlink-escape check never saw, under `danger-full-access` permissions.

## 4. LLM facade routing

### 4.1 Facade token and wire protocol

`EnsureDshFacadeConfig` (`pkg/llms/dsh_facade.go`) issues a facade token whose wire API **follows the resolved provider**, and exports the same choice as `DSH_WIRE_API` for the profile's `llm-pi-ai` route to name its protocol. Matching the provider keeps the request on the proxy's passthrough path instead of the conversion path, where an upstream event the bridge does not model would reach the guest as assistant text. It was unconditionally chat-completions while the profile used `llm-deepseek`, whose Config has no protocol field at all (see §4.2). Model selection is `<llm-provider-id>/<model-name>` (`SplitDshModel`), the same shape Pi and OpenCode use; an agent naming no model falls back to the daemon's default catalog entry.

### 4.2 LLM adapter and route

`cordis.patch.yml` disables dsh-base's `llm-deepseek` row and configures `llm-pi-ai` instead, which dsh-base mounts dormant until a profile supplies routes.

`llm-deepseek` is DSH's native adapter and speaks only chat completions — its Config exposes `apiKeyEnv`, `baseURL`, `thinking` and `reasoningEffort`, and no protocol field — so any provider serving something else forced a conversion on every turn. `llm-pi-ai` names its wire protocol per route (`openai-completions`, `openai-responses`, `anthropic-messages`), so the guest can speak whatever the facade resolved.

The profile declares one hand-declared route, `agent-compose`: pi-ai ships nothing under that key, so the route supplies `api` (from `DSH_WIRE_API`), `baseURL`, and a `models` list, all from the spawn environment. `agent-default-model` selects that route + `DSH_MODEL`.

## 5. Security and isolation

### 5.1 Skill tenant isolation

`skill-filesystem`'s `includeDefaultRoots: false` plus `customSkillDirs` from `DSH_SKILL_DIRS` means an agent only ever sees the skill directories agent-compose resolved for it, never a shared `~/.agents/skills` tree.

### 5.2 Model/provider resolution

Resolution mirrors Pi's (`resolveDshFacadeTarget` mirrors `resolvePiFacadeTarget`'s branch structure: configured provider id → family → custom OpenAI), minus an Anthropic-family branch — `llm-deepseek` always speaks chat completions, so there is nothing to mirror there.

### 5.3 Sandbox policy / permission mode

No approval or sandbox-policy overrides exist in the patch: `dsh-base`'s own rows key off `DSH_PERMISSION_MODE`, which agent-compose always sets to `danger-full-access`.

### 5.4 Credential source

`credentials` and `settings` rows are disabled, so `$DSH_HOME/settings.yaml` can't override `llm-deepseek`'s API key or base URL at runtime, and local credential discovery is off. The run-scoped facade token (§4.1) is the only LLM credential source.

### 5.5 Guest sandboxing boundary

`danger-full-access` (§5.3) is safe because DSH's own sandbox-policy layer is not the isolation boundary — the agent-compose sandbox (container/VM) is. A nested provider-side sandbox would be redundant.

## 6. MCP support

`@deepseek-ai/dsh-mcp-client` is an upstream DSH package — DSH already implements the MCP client protocol; agent-compose only wires its own generic `mcp_servers` config into it. One `dsh-mcp-client` plugin instance handles exactly one MCP server; there is no single "MCP" plugin that takes a server list.

Because `cordis.patch.yml` is static and the server list is a dynamic 0..N value known only at run time (§2), the wiring lives in `runner.js` rather than as YAML rows: `registerMcpServers()` parses `DSH_MCP_SERVERS` (§3.5) and calls `ctx.plugin(dshMcpClient, config)` once per server before the agent's first turn. `ctx.plugin()`'s returned Fiber settles once that server's plugin has finished loading, so `await`ing all of them guarantees every server's tools are registered before `agent.followup()` fires.

`dsh.ts`'s `toDshMcpServers()` maps agent-compose's generic `RuntimeMCPServer` (`type: "local"|"remote"`) onto `dsh-mcp-client`'s shape (`transport: "stdio"|"streamable-http"`): `local` → `stdio`, `remote`+`http` → `streamable-http`. `remote`+`sse` has no `dsh-mcp-client` equivalent and is rejected fail-fast, naming the offending server. Server names are sanitized to `dsh-mcp-client`'s `[A-Za-z0-9_-]{1,32}` requirement and suffixed with a deterministic hash of the raw name, since agent-compose's own name validation doesn't guarantee that charset.

**Known limitation:** `dsh-mcp-client`'s `StdioClientTransport` construction doesn't pass a `stderr` option, so the MCP SDK defaults the spawned server's stderr to `'inherit'` — it lands directly in `dsh`'s own stderr, indistinguishable from DSH's own diagnostics. `dsh.ts`'s `child.stderr` handler treats all of `dsh`'s stderr as transcript text, so any stdio MCP server that logs to its own stderr on startup (a common convention) will have that text appear in the agent's transcript. There is no `dsh-mcp-client` config option to suppress this today; fixing it requires an upstream change.

## 7. Persona injection

`cordis.patch.yml`'s `system-prompt` row is left at its `dsh-base` default (empty persona) rather than patched to read an env var, for the same env-var-size reason as §3.2/§6: a large persona risks the 128 KiB exec() limit, and unlike `DSH_MCP_SERVERS` there's no natural size cap to check against.

Instead `dsh.ts` writes the system context to a temp file and passes its path as `DSH_SYSTEM_CONTEXT_FILE` (config field `systemContextFile`, since `runner.js` — real ESM — can read it directly, unlike the static YAML row's `!!js` sandbox). After `agents.create()`/`agents.resume()` resolves an `agent`, `runner.js` reads that file and calls:

```js
agent.ctx.systemPrompt.section({ name: PERSONA_SECTION, order: PERSONA_ORDER, text });
```

`PERSONA_SECTION` (`"deployment:persona"`) and `PERSONA_ORDER` (`0`) are exported by `@deepseek-ai/dsh-system-prompt` specifically for this: "an agent preset shadows the deployment's persona with its own — and both sides naming the same section is what makes the replacement work rather than duplicate" (the package's own doc comment). `agent.ctx` is agent-scoped, so this can't collide with the (empty, dropped-at-render) global persona section the static row would otherwise own. This must happen before `agent.followup()` fires the first turn — the same ordering constraint MCP registration has (§6) — and does, since it runs synchronously after agent creation in the same function.

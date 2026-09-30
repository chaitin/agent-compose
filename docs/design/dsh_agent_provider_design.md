# DSH agent provider design

## 1. Overview

`dsh` (DeepSeek Harness) is a Cordis-based agent runtime, added to agent-compose as a fifth provider alongside `codex`, `claude`, `opencode`, `pi`. Unlike the others, `dsh` is not a single CLI binary with flags — it boots a *profile*: an ordered stack of plugin-bundle patch layers. agent-compose ships its own profile (`assets/.dsh/profiles/agent-compose/`) rather than passing flags to a generic binary.

## 2. Composition model

The profile is a static overlay on top of the `@deepseek-ai/dsh-base` bundle:

- `cordis.patch.yml` — patches individual plugin rows from `dsh-base` (model/provider selection, session persistence root, skill filesystem scope, credential/settings sources) and inserts one agent-compose-owned plugin (`agent-compose-runner`, see §3.1).
- `runner.js` — the inserted plugin's implementation.
- `package.json` — declares the profile's bundle (`dsh-base`) and no dependencies of its own. `runner.js`'s imports (`dsh-llm`, `dsh-session`, `dsh-mcp-client`, …) resolve through the `@deepseek-ai/dsh` installation instead of a per-profile `pnpm install`: 0.1.x maintained a flat symlink fallback at `$DSH_HOME/profiles/node_modules` (one link per package in the installation's dependency closure), and 0.2.x supplies that closure to Node's ESM and CommonJS resolvers directly. A profile whose plugins import in-box packages therefore needs no `node_modules` of its own — but the imports must exist in the installed DSH's API (see §7 for the 0.2 break that a build-time profile smoke now catches).

This file ships as a repo asset (`assets/.dsh/...`) — not an npm package, and not rebuilt per run. It reaches a sandbox through the **daemon**, not the guest image: `assets/assets.go` embeds the whole `.dsh` tree into the `agent-compose` binary (`//go:embed .codex .claude .claude.json .gitconfig .dsh`), and `initializeSandboxHomeDefaults` copies it into each sandbox's `home/.dsh`, which `runtime_mount_manifest.go` then bind-mounts over `/root/.dsh`. The guest image also carries a `/root/.dsh` copy for interactive use, but that copy is shadowed at runtime and never read by a sandboxed run. Consequence for upgrades: a sandbox executes the profile of the **daemon that first seeded its `home`**, not of whichever daemon is deployed later. `initializeSandboxHomeDefaults` skips any target that already exists, and `home/.dsh` lives under the sandbox's persistent home directory, so a daemon upgrade never refreshes the profile of an already-created sandbox — only sandboxes created after the upgrade pick up the new one. The k8s driver pushes that same host-side home snapshot into the guest on demand (`pkg/driver/k8s_runtime_guest_files.go`), so the rule holds for all three drivers. This makes version skew reachable in both directions: a guest image whose `dsh` predates the profile's API, or a sandbox home still holding an older profile, leaves the runner unable to activate, and the run then waits on a child that never proceeds. `runtime/javascript/src/runners/dsh.ts` detects that signature and fails the run with an actionable message instead of hanging. Release the two images together — bumping only `DSH_VERSION` in the guest image is not enough — and rebuild long-lived sandboxes whose home predates the upgrade (see §7 and §11 of the guest image ABI page).

Every mutable, per-run value (model, credentials, skills, MCP servers, prompt text, session id) has to flow in through the **spawn-time environment** that `runtime/javascript/src/runners/dsh.ts` sets when it `spawn()`s `dsh --profile agent-compose` (§3.2), because the profile itself is fixed at daemon-build time.

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
| `DSH_MODEL` | `writeDshGuestConfig` + `dsh.ts` | Opaque model literal resolved host-side; the retired `<connection>/<model>` routing form is rejected, and `dsh.ts` forwards the resolved value untouched |
| `DSH_REASONING_EFFORT` | `dsh.ts` | agent-compose's 5-level `effort` collapsed onto the `low`/`high`/`max` the `llm-pi-ai` route declares (§6 has no equivalent collapse — this is the reasoning-effort case). No daemon-driven path sets an effort today, so the route's `'max'` fallback is what every run actually gets; it preserves the static `thinking: enabled` + `reasoningEffort: 'max'` the replaced `llm-deepseek` row carried |
| `DSH_PERMISSION_MODE` | facade config + `dsh.ts` | Always `danger-full-access`; guest sandboxing is the agent-compose sandbox, not a nested DSH one (§5.3/§5.5) |
| `DSH_SESSION_ROOT`, `DSH_SESSION_ID`, `DSH_RESUME` | `dsh.ts` | Session persistence and resume (§3.3) |
| `DSH_PROMPT_FILE` | `dsh.ts` | Path to the prompt text file `runner.js` reads |
| `DSH_SYSTEM_CONTEXT_FILE` | `dsh.ts` | Path to the persona text file `runner.js` reads and injects (§7); unset when there's no system context |
| `DSH_SKILL_DIRS` | `dsh.ts` | Colon-joined resolved skill directories; consumed by the `skill-filesystem` row's `customSkillDirs` (§5.1) |
| `DSH_MCP_SERVERS` | `dsh.ts` | JSON array of per-server `dsh-mcp-client` configs; consumed by `runner.js` (§6) |
| `DSH_WIRE_API` | facade config | The wire protocol the facade resolved for this run (`openai-completions`, `openai-responses` or `anthropic-messages`); consumed by the `llm-pi-ai` route's `api` (§4.1) |
| `LLM_API_KEY`, `LLM_API_ENDPOINT` | facade config | Consumed by the `llm-pi-ai` route's `apiKeyEnv`/`baseURL` (§4) |

`env` starts from `...process.env`, so a key this run has no value for isn't automatically absent — it's whatever the host process happened to export. Every conditional `DSH_*` var (`DSH_SYSTEM_CONTEXT_FILE`, `DSH_MCP_SERVERS`, `DSH_RESUME`, `DSH_REASONING_EFFORT`, `DSH_SKILL_DIRS`) is therefore explicitly `delete`d in its false branch rather than left conditionally-set, so a host-inherited value can't leak through as this run's persona file, MCP server list, resume flag, effort, or skill directories. `DSH_MODEL` is the deliberate exception: the inherited value is the one the daemon's facade config exported for the model it minted the token against, so `dsh.ts` overwrites it only when the invocation names a model of its own and never deletes it. `DSH_SKILL_DIRS` is the sharpest case: an inherited value would have `dsh` load a skill directory `resolveSkillPaths()`'s symlink-escape check never saw, under `danger-full-access` permissions.

## 4. LLM facade routing

### 4.1 Facade token and wire protocol

`PrepareAgentLLM` (`pkg/llms/agent_llm.go`) is the single entry point that resolves a run's managed LLM configuration and mints its facade token; the DSH-specific guest environment is then written by `writeDshGuestConfig` (`pkg/llms/dialect_writers.go`), one of the writers `writeDialectGuestConfig` dispatches to by resolved dialect kind. The token's wire API **follows the resolved provider**, and `writeDshGuestConfig` exports the same choice as `DSH_WIRE_API` (spelled by `agentProtocolSpelling`) for the profile's `llm-pi-ai` route to name its protocol. Matching the provider keeps the request on the proxy's passthrough path instead of the conversion path, where an upstream event the bridge does not model would reach the guest as assistant text. It was unconditionally chat-completions while the profile used `llm-deepseek`, whose Config has no protocol field at all (see §4.2).

Model selection is opaque: a model id is a literal the daemon never splits or matches against a connection, so an id containing slashes survives intact. The former `<llm-provider-id>/<model-name>` routing form is retired — `Catalog.Resolve` rejects it with `ErrLegacyQualifiedModel` (`pkg/llms/connection_catalog.go`). The connection is instead chosen from a connection the agent declared in its own credentials, or the daemon's configured connection, and an agent naming no model at all falls back to the daemon's default catalog entry. The facade publishes the resolved model literal as `DSH_MODEL` and `AGENT_COMPOSE_RESOLVED_MODEL`; `dsh.ts` forwards that value unchanged. A legacy compatibility shim in `runtime/javascript/src/runners/model-reference.ts` only applies when a daemon predating `AGENT_COMPOSE_RESOLVED_MODEL` sends the declared argument.

### 4.2 LLM adapter and route

`cordis.patch.yml` disables dsh-base's `llm-deepseek` row and configures `llm-pi-ai` instead, which dsh-base mounts dormant until a profile supplies routes.

`llm-deepseek` is DSH's native adapter and speaks only chat completions — its Config exposes `apiKeyEnv`, `baseURL`, `thinking` and `reasoningEffort`, and no protocol field — so any provider serving something else forced a conversion on every turn. `llm-pi-ai` names its wire protocol per route (`openai-completions`, `openai-responses`, `anthropic-messages`), so the guest can speak whatever the facade resolved.

The profile declares one hand-declared route, `agent-compose`: pi-ai ships nothing under that key, so the route supplies `api` (from `DSH_WIRE_API`), `baseURL`, and a `models` list, all from the spawn environment. `agent-default-model` selects that route + `DSH_MODEL`.

## 5. Security and isolation

### 5.1 Skill tenant isolation

`skill-filesystem`'s `includeDefaultRoots: false` plus `customSkillDirs` from `DSH_SKILL_DIRS` means an agent only ever sees the skill directories agent-compose resolved for it, never a shared `~/.agents/skills` tree.

### 5.2 Model/provider resolution

Resolution is shared rather than DSH-specific: `PrepareAgentLLM` (`pkg/llms/agent_llm.go`) picks the connection (an agent-declared credential, else the catalog selection), resolves the target through `Catalog.Resolve`, and derives the inbound protocol from `DialectFor("dsh").InboundProtocol`, so an Anthropic upstream is served at the `/llm/anthropic` facade endpoint with an `anthropic-messages` token rather than being bridged down to chat completions.

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
agent.ctx.systemPrompt.section({
  name: PERSONA_PREFIX_SECTION,
  order: agent.ctx.systemPrompt.getSectionOrder('DEPLOYMENT_PERSONA_PREFIX'),
  text,
});
```

`PERSONA_PREFIX_SECTION` (`"deployment:persona-prefix"`) is exported by `@deepseek-ai/dsh-system-prompt` for exactly this: a scoped contribution shadows the deployment's own section of the same name, which is what makes the replacement work rather than duplicate (the package's own doc comment). `agent.ctx` is agent-scoped, so this replaces the (empty, dropped-at-render) global persona section the static row would otherwise own. This must happen before `agent.followup()` fires the first turn — the same ordering constraint MCP registration has (§6) — and does, since it runs synchronously after agent creation in the same function.

The slot's spelling is a compatibility boundary. DSH 0.1.5 renamed `deployment:persona` to `deployment:persona-prefix` and replaced the exported `PERSONA_ORDER` with `SystemPrompt.getSectionOrder('DEPLOYMENT_PERSONA_PREFIX')`; 0.2.0 removed the old `PERSONA_SECTION`/`PERSONA_ORDER` exports entirely, so a `runner.js` still importing them fails at load with `The requested module '@deepseek-ai/dsh-system-prompt' does not provide an export named 'PERSONA_ORDER'`. DSH's loader reports a failed plugin import as a non-fatal `warning: N entries did not activate`, so the symptom is not a boot error: the runner never activates, `dsh` stays alive with nothing to do, and the sandbox run hangs until it is killed.

The guest Dockerfiles therefore end their install layer with two checks. `dsh --profile agent-compose --dump-config-schema` imports every composed plugin without mounting it, so a runner that no longer matches the pinned DSH API fails the image build with that exact export error instead of hanging every sandbox run. That check alone is not sufficient: it only reports whether the module *imported*, and a plugin that imports but never activates — because a service it injects is missing — renders its `Config` schema there and exits 0 with no warning, which is the same hang by a different route. So the smoke then boots the profile for real with an unreachable LLM endpoint and a scratch session root, and asserts that a session file appeared. `runner.js` creates its session only after the injected `agents`, `sessions`, and `agentDefaultModel` services resolve, so the file is a log-independent signal that the plugin actually activated. That signal alone is not enough either, for two reasons: `injectPersona` returns early unless a system context is configured, so a boot without `DSH_SYSTEM_CONTEXT_FILE` never executes the persona-prefix call this section documents; and `agents.create()` runs before `injectPersona`, so the session file is already on disk when `injectPersona` throws. The boot therefore also passes a system context and requires that the runner logged no error of its own (`^agent-compose-runner:`, written only by `apply()`'s catch — a `turn/end` error merely sets the exit code), which is what makes a broken persona path fail the build instead of leaving it green. Verified against three runner variants: the shipped runner passes, a runner importing the removed `PERSONA_ORDER` fails the build, and a runner that imports but waits on a missing service fails the build too. The boot is the one step allowed to fail, so its `|| true` lives inside its own `{ ...; }` group rather than at the end of the `&&` chain: `&&` and `||` share precedence and associate left, so a trailing `|| true` would absorb a failure from the version checks or the import gate and report it as an inactive runner. That attribution matters — the import gate now fails loudly with the real `dsh: error:` line, and only genuine non-activation prints the activate status. A guest whose `dsh` cannot activate the daemon-embedded profile at runtime is reported by `dsh.ts` rather than left waiting, since that skew is reachable during a rolling upgrade (§2). `scripts/tests/test-image-ci-contract.sh` requires the boot, the persona-path context, both assertions and their failing branches, and the grouped `|| true` in each guest Dockerfile, so dropping or weakening the guard fails CI rather than silently passing.

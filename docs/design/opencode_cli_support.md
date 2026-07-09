# OpenCode CLI Provider Support

Chinese version: [../zh-CN/design/opencode_cli_support.md](../zh-CN/design/opencode_cli_support.md)

This document records the implementation for adding `opencode` as a guest agent
provider. It follows the current runtime contract in
[agent-compose-runtime_contract.md](agent-compose-runtime_contract.md):
the Go control plane creates sandboxes and executes one unified runtime command
inside the guest, while `runtime/javascript` adapts provider-specific CLIs.

## Current Code Shape

Provider handling is split across four layers:

- Go control plane: `NormalizeAgentKind` and `NormalizeAgentDefinition` in
  `pkg/model/agent_model.go`, loader
  default-agent validation, and run/sandbox orchestration currently pass a
  provider string to the guest runtime.
- JavaScript runtime: `runtime/javascript/src/provider.ts` normalizes provider
  aliases, `runtime/javascript/src/prompt.ts` selects a runner, and
  `runtime/javascript/src/runners/` contains the provider adapters.
- Guest image: `guest-images/Dockerfile.agent-compose-guest` installs provider
  CLIs and links stable executable paths such as `/usr/bin/codex`.
- UI and docs: `frontend/src/pages/AgentsPage.svelte`, `README.md`, and design
  docs list supported providers.

The compose schema does not currently restrict provider names in `pkg/compose`;
daemon-side agent definition validation does. Proto fields are strings, so this
change should not require protobuf changes.

`model` and `system_prompt` already exist in the compose, v1, v2, and store
models. The OpenCode integration forwards them through `ExecuteAgentRequest`
and `agent-compose-runtime prompt`; existing Codex, Claude, and Gemini runners
keep their previous behavior unless they explicitly consume those fields later.

## OpenCode CLI Facts

As of the current OpenCode CLI documentation at
<https://opencode.ai/docs/cli/>, the relevant non-interactive command is:

```sh
opencode run [message..]
```

Relevant flags for the agent-compose runner are:

- `--format json`: emit raw JSON events.
- `--session <id>` and `--continue`: resume an OpenCode-native session.
- `--model <provider/model>`: select the model.
- `--agent <agent>`: select an OpenCode agent profile.
- `--dir <path>`: run in a working directory.
- `--dangerously-skip-permissions`: auto-approve permissions not explicitly
  denied.
- `--attach <url>`: attach to an already running OpenCode server. This is not
  part of the first integration because agent-compose already owns sandbox
  lifecycle and does not start a persistent OpenCode server per sandbox.

The package install command in OpenCode docs is `npm install -g opencode-ai`.

## Target Behavior

Users should be able to set:

```yaml
agents:
  reviewer:
    provider: opencode
    model: anthropic/claude-sonnet-4-5
```

or create an agent definition with provider `opencode` in the UI/API.

Agent execution should:

- call `agent-compose-runtime prompt --provider opencode ...`;
- run `opencode run` inside `/workspace`;
- persist and reuse the OpenCode provider thread id in
  `/data/state/agents/providers/opencode.json`;
- stream human-readable transcript text back through stderr, consistent with
  other runners;
- return the final `AgentResult` JSON on stdout through the existing
  `__AGENT_RESULT__` protocol;
- fail clearly when `opencode` exits non-zero or emits an error event.

## Implementation Summary

1. Add provider normalization.

   Both provider normalizers support OpenCode:

   - `runtime/javascript/src/types.ts`: extend `Provider` with `"opencode"`.
   - `runtime/javascript/src/provider.ts`: map `opencode`, `open-code`, and
     `open_code` to `opencode`.
   - `pkg/model/agent_model.go`: update `NormalizeAgentKind`.
   - `pkg/model/agent_model.go`: allow `opencode` in the provider
     whitelist.

2. Add `OpenCodeRunner`.

   `runtime/javascript/src/runners/opencode.ts` uses
   `spawn("opencode", args, ...)` rather than an SDK, because the documented
   integration surface is the CLI.

   Initial command shape:

   ```text
   opencode run <prompt> --format json --dir <workspace>
     --dangerously-skip-permissions
     [--model <model>]
     [--session <stored-thread-id>]
   ```

   Implementation details:

   - read the previous provider thread with `readStoredThread(stateRoot, "opencode")`;
   - include `OPENCODE_DISABLE_AUTOUPDATE=true` in the child environment unless
     the user already set it;
   - pass the model only when `RunnerOptions.model` exists;
   - parse line-delimited JSON events when possible, and tolerate non-JSON lines
     by writing them to the transcript;
   - extract the provider-native session id from common event fields such as `sessionID`,
     `sessionId`, or `session_id`;
   - extract final text from common message/result fields with `extractText`;
   - write provider thread state only after a non-empty provider-native session
     id is observed.

3. Wire runner selection.

   - Import/export `OpenCodeRunner` in `runtime/javascript/src/prompt.ts` and
     `runtime/javascript/src/index.ts`.
   - Update CLI help text in `runtime/javascript/src/cli.ts`.

4. Agent model and system prompt plumbing.

   OpenCode needs `model` to select the target provider/model. The host forwards
   model and system prompt explicitly instead of relying on stored metadata:

   - a small execution-config helper resolves provider, model, and
     system prompt from an agent definition when a sandbox has agent tags;
   - `ExecuteAgentRequest` includes `Model` and `SystemPrompt`;
   - managed project agent definitions set those fields in
     `runProjectAgent`;
   - v1 agent definition sandbox compatibility paths set those fields when executing via
     `SendAgentMessage` / `SendAgentMessageStream`;
   - loaders linked to an agent definition set those fields when using
     `scheduler.agent`;
   - the runtime CLI accepts `--model` and `--system-prompt-file`;
   - `PromptCommandOptions` and `RunnerOptions` include `model?: string` and
     `systemPrompt?: string`.

5. Install the CLI in the guest image.

   `guest-images/Dockerfile.agent-compose-guest`:

   - adds `opencode-ai` to the global npm install line;
   - links the executable to `/usr/bin/opencode`;
   - creates `/root/.opencode`.

6. Update UI and docs.

   - `frontend/src/pages/AgentsPage.svelte` and `frontend/src/api/agents.ts`
     accept `opencode`.
   - `README.md`, SDK docs, and runtime contract docs list the new provider.
   - OpenCode environment-variable guidance notes that the exact provider key depends
     on the selected OpenCode model provider, so document common cases rather
     than a single universal key.

7. Tests.

   Runtime JS tests:

   - provider normalization accepts `opencode`, `open-code`, `open_code`;
   - `OpenCodeRunner` builds expected args for fresh and resumed provider
     threads;
   - event parsing records transcript, final text, stderr, exit failures, and
     provider-native session id.

   Go tests:

   - `normalizeAgentKind` maps aliases;
   - agent definition validation accepts `opencode`;
   - loader/default-agent paths keep accepting normalized `opencode`;
   - service API tests cover sending an `open-code` alias through to normalized
     `opencode`.

   Image verification should include `task image:agent-compose-guest` and a
   container smoke check for `opencode --help` when image build infrastructure
   is available.

## Structured Output

The existing runtime supports structured JSON output for Codex and Claude, but
Gemini currently rejects `outputSchema`. OpenCode CLI docs list `--format json`
as raw event formatting, not strict schema enforcement. The first implementation
should therefore reject `outputSchema` for `opencode` with a clear error unless
OpenCode exposes a documented structured-output contract during implementation.

If structured output is later required, add it as a separate change with a
provider-specific contract and tests.

## Compatibility And Migration

No data migration is needed. Provider thread state is stored per provider name,
so OpenCode will use a new file:

```text
/data/state/agents/providers/opencode.json
```

Existing `codex`, `claude`, and `gemini` provider thread state continues using
the current state files and command paths.

## Remaining Follow-Up Checks

- Confirm the exact JSON event shapes emitted by the installed `opencode-ai`
  version in the guest image against a real model backend.
- Confirm whether OpenCode honors common provider API keys directly from the
  environment, or whether a default config file should be mounted under
  `/root/.opencode`.
- Decide separately whether existing Codex/Claude/Gemini runners should consume
  forwarded `model` / `system_prompt` or leave them as OpenCode-only runtime
  options.

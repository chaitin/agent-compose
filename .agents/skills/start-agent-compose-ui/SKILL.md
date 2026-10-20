---
name: start-agent-compose-ui
description: Start or refresh a singleton local agent-compose daemon and Web UI from this repository for manual or browser-visible testing. Use when the user wants a local UI, an independent runtime stack, or a repeatable end-to-end environment on the current checkout; not for ADP playground or production deployment.
---

# Start Agent Compose UI

Start one source-built daemon and one UI development stack without replacing an unrelated local stack. Re-running the skill refreshes this checkout's existing instance instead of creating another one.

## Default workflow

Run the bundled script from the repository, in a persistent PTY/foreground session:

```bash
.agents/skills/start-agent-compose-ui/scripts/run-local-ui.sh --open
```

If `task build` reports that `buf` is missing, run `task prepare` once, then rerun the script.

On the first local-UI run, the script runs `npm ci` when dependencies are missing or stale, and builds the sibling UI's Go server. Expect the initial dependency download to take longer than later reruns.

The script:

- detects `../agent-compose-ui` relative to this repository;
- when that sibling directory exists, builds its Go UI server, runs it natively, and serves its Svelte frontend with Vite so frontend edits use hot reload;
- uses `<sibling-ui-repo>/.env` for UI authentication, creating it with the fixed local-development login `admin` / `admin` when absent;
- otherwise pulls and runs the published UI image;
- builds and runs the current checkout's native daemon;
- creates Docker-backed agent sandboxes through the daemon's default Docker driver;
- verifies daemon health, UI health, login, and the UI-to-daemon proxy.

Useful options:

```text
--root DIR              reuse or choose a test root
--engine-port PORT      fixed daemon port
--ui-port PORT          fixed browser UI port
--skip-build            reuse existing daemon and local UI server binaries
--open                  open the UI in the default browser
```

## LLM key requirement

The daemon reads LLM configuration from the repository's `.env` file. Do not use a separate daemon-env file for this skill.

The script checks for a non-empty `LLM_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, or `ANTHROPIC_AUTH_TOKEN` in `<repo>/.env`. If none is present, it exits with code `3` and prints the supported variables.

When that happens:

1. Stop and ask the user to add their key to the repository `.env`.
2. Give the relevant variable names and any required endpoint/model companions.
3. Do not ask the user to paste the secret into chat, do not echo an existing value, and do not invent or commit a key.
4. After the user confirms the file is updated, rerun the same skill command.

For an OpenAI-compatible endpoint, the usual `.env` entries are:

```dotenv
LLM_API_ENDPOINT=https://api.openai.com
LLM_API_PROTOCOL=responses
LLM_API_KEY=...
LLM_MODEL=...
```

For Anthropic:

```dotenv
ANTHROPIC_BASE_URL=https://api.anthropic.com
ANTHROPIC_API_KEY=...
ANTHROPIC_MODEL=...
```

`.env` is gitignored. Do not print its secret values.

## Repeat runs and refresh

The default root is deterministic for this checkout: `/private/tmp/agent-compose-ui-<repo-hash>` on macOS or `/tmp/agent-compose-ui-<repo-hash>` on Linux. Its `state.env` records the active ports and process IDs.

On every rerun, the script:

- rebuilds the daemon and, when present, the sibling UI's Go server;
- reuses the existing data, UI database, `<sibling-ui-repo>/.env`, and ports where possible;
- stops the previous daemon, UI server, and Vite process before starting the replacements;
- leaves exactly one instance for this checkout.

Do not launch another copy from a second terminal to test backend changes. Rerun the skill; the script refreshes the current instance in place. Vite already hot-reloads frontend edits while the stack is running. A preparation lock rejects overlapping invocations while one is rebuilding.

If protobuf contracts change, regenerate the sibling UI's checked-in TypeScript clients with `npm run gen:api` before relying on the new RPC types. The refresh script does not rewrite generated clients.

## Runtime layout

- Daemon: native `build/agent-compose` process.
- UI with sibling source: native Go UI server plus Vite dev server.
- UI without sibling source: `chaitin/agent-compose-ui:latest` in Docker. Override with `AGENT_COMPOSE_UI_IMAGE`.
- UI authentication for local source runs: `<sibling-ui-repo>/.env`. If it does not exist, the script creates it with username `admin`, password `admin`, and a stable local-only session secret. Existing files are parsed as data rather than sourced as shell code.
- Agent sandboxes: Docker containers using `chaitin/agent-compose-guest:latest` unless `DEFAULT_IMAGE` selects another image.
- UI browser port: `127.0.0.1:<ui-port>`.
- Daemon port: loopback on Docker Desktop and the Docker bridge gateway on Linux, so host tools and sandboxes can reach the runtime facade without binding the unauthenticated control plane to every network interface.
- Data: `<root>/data`; UI state: `<root>/ui-data`; daemon log: `<root>/daemon.log`.

Do not default to `task image:agent-compose`; it depends on optional BoxLite and Microsandbox artifacts. The native Docker-only binary is sufficient for UI testing.

## Verification

After startup, confirm:

```bash
curl -fsS <daemon-url>/api/version
curl -fsS http://127.0.0.1:<ui-port>/
```

The script additionally logs in with the configured local UI credentials and verifies `/api/version` through the UI. Report the URL, username, local UI password, and `<sibling-ui-repo>/.env` path so the user can copy the login directly. Never print the LLM key or other daemon credentials.

Stop with `Ctrl-C`. The script stops all processes and the UI container it owns while retaining the data directory for inspection.

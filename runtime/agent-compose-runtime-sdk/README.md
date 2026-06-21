# @chaitin-ai/agent-compose-runtime-sdk

Node.js SDK for scripts running inside an agent-compose guest runtime.

The package provides both named exports and a default `runtime` object:

```js
const { runtime } = require("@chaitin-ai/agent-compose-runtime-sdk");
```

```js
import runtime, { exec, shell, agent, llm } from "@chaitin-ai/agent-compose-runtime-sdk";
```

## Installation

Install from the public npm registry:

```bash
npm install @chaitin-ai/agent-compose-runtime-sdk
```

Guest images can also install the SDK from an offline tarball:

```bash
npm install --offline /opt/agent-compose/npm/agent-compose-runtime-sdk.tgz
```

## Quick Start

```js
const { runtime } = require("@chaitin-ai/agent-compose-runtime-sdk");

async function main() {
  const result = await runtime.shell("echo hello");
  runtime.log("command completed", { success: result.success });
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
```

## Runtime Paths

`runtime.paths` exposes runtime paths resolved from environment variables:

| Field | Environment variable | Default |
| --- | --- | --- |
| `workspace` | `WORKSPACE` | `/workspace` |
| `stateRoot` | `STATE_ROOT` | `/data/state` |
| `runtimeRoot` | `RUNTIME_ROOT` | `/data/runtime` |
| `home` | `HOME` | `/root` |

## API

### `runtime.exec(command, args?, options?)`

Runs a command directly without shell expansion.

```js
const result = await runtime.exec("node", ["-e", "console.log(process.cwd())"]);
```

Options:

| Option | Description |
| --- | --- |
| `cwd` | Working directory. Defaults to `runtime.paths.workspace`. |
| `env` | Key-value pairs appended to the child process environment. |
| `timeoutMs` | Terminates the command after this number of milliseconds. |
| `maxOutputBytes` | Maximum captured bytes for `stdout`, `stderr`, and combined `output`. Defaults to 1 MiB. |
| `rejectOnFailure` | Throws `CommandError` when the process exits with a non-zero status code. |
| `streamOutput` | Streams child process `stdout` and `stderr` to the current process. Defaults to `true`. |
| `forwardOutput` | Deprecated. Use `streamOutput` instead. |

Return value:

```ts
{
  stdout: string;
  stderr: string;
  output: string;
  exitCode: number;
  success: boolean;
  stdoutTruncated: boolean;
  stderrTruncated: boolean;
  outputTruncated: boolean;
}
```

By default, `runtime.exec()` streams command logs so agent-compose can display live output while the script is still running. Pass `{ streamOutput: false }` for quiet execution.

### `runtime.shell(script, options?)`

Runs a shell script through `bash -lc`.

```js
const result = await runtime.shell("echo shell:$AGENT_COMPOSE_TEST_VALUE", {
  env: { AGENT_COMPOSE_TEST_VALUE: "works" },
});
```

Options and return values are the same as `runtime.exec()`. Use `runtime.shell()` when you need variable expansion, pipes, redirects, or compound commands.

### `runtime.agent(prompt, options?)`

Calls the agent bridge in the agent-compose runtime.

```js
const result = await runtime.agent("Inspect the workspace and summarize the project.");
```

Pass `outputSchema` when you need structured JSON. A Zod schema is recommended: the SDK converts it to JSON Schema for the runtime agent bridge and validates the parsed `json` value with the same Zod schema.

```ts
import { z } from "zod";

const RiskSummary = z.object({
  summary: z.string(),
  risk: z.enum(["low", "high"]),
});

const result = await runtime.agent("Inspect the workspace and return risk.", {
  provider: "codex",
  outputSchema: RiskSummary,
});

console.log(result.json.summary);
```

When a Zod schema is passed, the type of `result.json` is inferred from the schema:

```ts
import { z } from "zod";

const RiskSummary = z.object({
  summary: z.string(),
  risk: z.enum(["low", "high"]),
});

const result = await runtime.agent("Inspect the workspace and return risk.", {
  provider: "claude",
  outputSchema: RiskSummary,
});

if (result.json) {
  console.log(result.json.risk);
}
```

You can also pass a plain JSON Schema object and declare the return type with a generic:

```ts
type RiskSummary = {
  summary: string;
  risk: "low" | "high";
};

const result = await runtime.agent<RiskSummary>("Inspect the workspace and return risk.", {
  outputSchema: {
    type: "object",
    additionalProperties: false,
    properties: {
      summary: { type: "string" },
      risk: { type: "string", enum: ["low", "high"] },
    },
    required: ["summary", "risk"],
  },
});
```

Options:

| Option | Description |
| --- | --- |
| `provider` | Agent provider. One of `codex`, `claude`, or `gemini`. Defaults to `codex`. |
| `stateRoot` | Agent state root directory. Defaults to `runtime.paths.stateRoot`. |
| `workspace` | Workspace path. Defaults to `runtime.paths.workspace`. |
| `home` | Home directory. Defaults to `runtime.paths.home`. |
| `timeoutMs` | Terminates the agent bridge after this number of milliseconds. |
| `outputSchema` | Zod schema or JSON Schema object. When set, the returned `json` field is parsed from `finalText`. Codex and Claude support this; Gemini currently throws an unsupported error when schema-based output is unavailable. |

Return value:

```ts
{
  provider: string;
  sessionId: string;
  stopReason: string;
  finalText: string;
  json: unknown | null;
  transcript: string;
  stderr: string;
}
```

`finalText` always preserves the raw text returned by the agent bridge. When `outputSchema` is set, `finalText` must be a JSON string. The SDK runs `JSON.parse(finalText)` and stores the result in `json`. If `outputSchema` is a Zod schema, the SDK also validates the parsed result with that schema. Without `outputSchema`, `json` is `null`.

Error behavior:

| Scenario | Behavior |
| --- | --- |
| `outputSchema` is not a plain JSON object | `runtime.agent()` throws before calling the runtime. |
| A Zod schema cannot be converted to JSON Schema | `runtime.agent()` throws before calling the runtime. |
| The provider does not support schema-based output | The runtime throws a provider-specific error. The current Gemini runner reports unsupported output. |
| The provider returns `finalText` that is not valid JSON | `runtime.agent()` throws a parse error. |
| The provider returns JSON that does not satisfy the Zod schema | `runtime.agent()` throws a validation error. |

### `runtime.llm(prompt, options?)`

Calls the agent-compose LLM service. The daemon selects the HTTP protocol via
`LLM_API_PROTOCOL` (`responses` by default, or `chat_completions` for
OpenAI-compatible Chat Completions backends).

```ts
const result = await runtime.llm("Summarize the workspace risk.", {
  model: "gpt-5.4",
});

console.log(result.text);
```

`runtime.llm()` supports the same `outputSchema` mechanism as `runtime.agent()`. A Zod schema is recommended: the SDK converts it to JSON Schema for the LLM service and validates the parsed `json` value with the same Zod schema.

```ts
import { z } from "zod";

const RiskSummary = z.object({
  summary: z.string(),
  risk: z.enum(["low", "high"]),
});

const result = await runtime.llm("Inspect the workspace and return risk.", {
  model: "gpt-5.4",
  outputSchema: RiskSummary,
});

console.log(result.json.risk);
```

You can also pass a plain JSON Schema object and declare the return type with a generic:

```ts
type RiskSummary = {
  summary: string;
  risk: "low" | "high";
};

const result = await runtime.llm<RiskSummary>("Inspect the workspace and return risk.", {
  outputSchema: {
    type: "object",
    additionalProperties: false,
    properties: {
      summary: { type: "string" },
      risk: { type: "string", enum: ["low", "high"] },
    },
    required: ["summary", "risk"],
  },
});
```

Options:

| Option | Description |
| --- | --- |
| `model` | LLM model name. When omitted, agent-compose uses the server-side configuration. |
| `baseUrl` | agent-compose service URL. Defaults to `BASE_URL`, then `HTTP_URL`, then `http://127.0.0.1:7410`. |
| `timeoutMs` | Terminates the LLM service request after this number of milliseconds. |
| `outputSchema` | Zod schema or JSON Schema object. When set, the returned `json` field is parsed from `text`. |

Return value:

```ts
{
  text: string;
  model: string;
  responseId: string;
  finishReason: string;
  json: unknown | null;
}
```

`text` always preserves the raw text returned by the LLM service. When `outputSchema` is set, `text` must be a JSON string. The SDK runs `JSON.parse(text)` and stores the result in `json`. If `outputSchema` is a Zod schema, the SDK also validates the parsed result with that schema. Without `outputSchema`, `json` is `null`.

### `runtime.env.get(name)`

Reads an environment variable. Returns `undefined` when the variable is not set.

```js
const value = runtime.env.get("WORKSPACE");
```

### `runtime.env.require(name)`

Reads a required environment variable. Throws when the variable is missing or an empty string.

```js
const token = runtime.env.require("API_TOKEN");
```

### `runtime.env.all()`

Returns all defined environment variables as a plain object.

```js
const environment = runtime.env.all();
```

### `runtime.log(message, payload?)`

Writes one structured runtime log line to `stdout`.

```js
runtime.log("step completed", { step: "build", ok: true });
```

Output JSON shape:

```ts
{
  type: "agent-compose.runtime.log";
  message: string;
  payload: unknown;
  createdAt: string;
}
```

### `runtime.report.writeMarkdown(name, content, options?)`

Writes a Markdown report file and returns the created file path.

```js
const reportPath = await runtime.report.writeMarkdown("summary.md", "# Summary\n\nDone.");
```

Options:

| Option | Description |
| --- | --- |
| `dir` | Output directory. Defaults to `runtime.paths.workspace`. |

File names are normalized with `path.basename()`, so callers should pass a file name rather than a nested path.

## Errors

When `rejectOnFailure` is enabled, a non-zero exit from `runtime.exec()` or `runtime.shell()` throws `CommandError`. The error object contains:

| Field | Description |
| --- | --- |
| `command` | Executed command. |
| `args` | Arguments passed to the command. |
| `result` | Command result object, with the same shape returned by `runtime.exec()` or `runtime.shell()`. |

```js
const { runtime, CommandError } = require("@chaitin-ai/agent-compose-runtime-sdk");

try {
  await runtime.shell("exit 1", { rejectOnFailure: true });
} catch (error) {
  if (error instanceof CommandError) {
    runtime.log("command failed", { exitCode: error.result.exitCode });
  }
}
```

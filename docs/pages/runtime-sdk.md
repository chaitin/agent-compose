# Guest Runtime SDK

The `@chaitin-ai/agent-compose-runtime-sdk` package runs inside an
agent-compose guest. It is the workspace-side API; it is not the daemon
control-plane client and it cannot manage projects or sandboxes.

## Install and run

```bash
npm install @chaitin-ai/agent-compose-runtime-sdk
# Guest images may install the release-matched tarball offline:
npm install --offline /opt/agent-compose/npm/agent-compose-runtime-sdk.tgz
```

```js
import runtime from "@chaitin-ai/agent-compose-runtime-sdk";

const result = await runtime.exec("node", ["--version"], {
  streamOutput: false,
});
runtime.log("runtime ready", { success: result.success });
```

The package requires the Node.js version declared by its `engines` field. Use
the SDK from the same release as the daemon/runtime image when compatibility
matters.

## API map

The exported surfaces are `exec`, `shell`, `agent`, `llm`, `workflow`,
`workflowFile`, `env`, `paths`, `log`, `report`, and `ssh`. Command results
include exit status and bounded output; `agent` and `llm` can validate
structured output with a plain JSON Schema or Zod schema.

`ssh.prepareConfig` writes a private SSH config and optional key with
restricted file modes. Use its returned config with `ssh.command` or
`ssh.scpCommand`; do not log private key contents. The SDK's `report` helper
writes reports beneath the runtime report boundary rather than exposing an
arbitrary host path.

See the [full package API reference](https://github.com/chaitin/agent-compose/blob/main/runtime/agent-compose-runtime-sdk/README.md)
and [guest image ABI](guest-image-abi.html) for the image-side requirements.

## Complete example

```js
import { runtime } from "@chaitin-ai/agent-compose-runtime-sdk";

const check = await runtime.shell("test -d \"$WORKSPACE\" && pwd", {
  streamOutput: false,
});
if (!check.success) {
  throw new Error(check.stderr || "workspace check failed");
}
await runtime.report.writeMarkdown("summary.md", `# Check\n\n${check.stdout}`);
```

Keep provider credentials in secret-marked environment variables. Treat
workspace files, command output, and agent responses as untrusted data.

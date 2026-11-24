import fs from "node:fs/promises";
import path from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createProgram } from "../src/cli.js";
import { RESULT_PREFIX } from "../src/constants.js";
import { captureStdio, withTempSession } from "./helpers.js";

afterEach(() => {
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

describe("Codex CLI integration", () => {
  it("surfaces MCP tool calls from the real Codex SDK subprocess path", async () => {
    await withTempSession(async (root) => {
      const binDir = path.join(root, "bin");
      const fakeCodex = path.join(binDir, "codex");
      const messageFile = path.join(root, "message.txt");
      const argsFile = path.join(root, "argv.json");
      await fs.mkdir(binDir, { recursive: true });
      await fs.mkdir(path.join(root, "workspace"), { recursive: true });
      await fs.writeFile(messageFile, "run lookup", "utf8");
      await fs.writeFile(fakeCodex, `#!/usr/bin/env node
require("node:fs").writeFileSync(${JSON.stringify(argsFile)}, JSON.stringify(process.argv.slice(2)));
process.stdin.resume();
process.stdout.write(JSON.stringify({ type: "thread.started", thread_id: "codex-cli-thread" }) + "\\n");
process.stdout.write(JSON.stringify({
  type: "item.completed",
  item: {
    id: "tool-1",
    type: "mcp_tool_call",
    server: "srv",
    tool: "lookup",
    arguments: { query: "docs" },
    status: "completed",
    result: {
      content: [{ type: "text", text: "lookup result" }],
      structured_content: { ok: true }
    }
  }
}) + "\\n");
process.stdout.write(JSON.stringify({
  type: "item.completed",
  item: { id: "answer", type: "agent_message", text: "done" }
}) + "\\n");
`, "utf8");
      await fs.chmod(fakeCodex, 0o755);
      vi.stubEnv("CODEX_BIN", fakeCodex);
      vi.stubEnv("AGENT_COMPOSE_CODEX_CONTEXT_WINDOW", "128000");

      const stdio = captureStdio();
      try {
        await createProgram({ exitOverride: true }).parseAsync([
          "node",
          "cli",
          "prompt",
          "--provider",
          "codex",
          "--message-file",
          messageFile,
          "--state-root",
          path.join(root, "state"),
          "--workspace",
          path.join(root, "workspace"),
          "--home",
          path.join(root, "home"),
        ]);
      } finally {
        stdio.restore();
      }

      const args = JSON.parse(await fs.readFile(argsFile, "utf8"));
      expect(args).toContain("model_context_window=128000");
      expect(args).toContain("model_auto_compact_token_limit=96000");
      expect(stdio.stderr).toContain("[mcp:srv/lookup]");
      expect(stdio.stderr).toContain("\"query\": \"docs\"");
      expect(stdio.stderr).toContain("lookup result");
      expect(stdio.stderr).toContain("done");
      expect(stdio.stderr).not.toContain("[tool:srv/lookup]");
      expect(stdio.stdout).toContain(`${RESULT_PREFIX}{`);
      expect(stdio.stdout).toContain("\"threadId\":\"codex-cli-thread\"");
      expect(stdio.stdout).toContain("\"finalText\":\"done\"");
    });
  });
});

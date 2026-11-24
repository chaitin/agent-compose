import fs from "node:fs/promises";
import path from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createProgram } from "../src/cli.js";
import { AGENT_OUTPUT_ENV, RESULT_PREFIX } from "../src/constants.js";
import { captureStdio, withTempSession } from "./helpers.js";

afterEach(() => {
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

describe("Codex CLI integration", () => {
  it.each([
    { format: "legacy", window: "128000" },
    { format: "legacy", window: "0" },
    { format: "frames", window: "128000" },
  ])("surfaces MCP tool calls through the real Codex SDK using $format output and context window $window", async ({ format, window }) => {
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
if (process.env.AGENT_COMPOSE_RUNTIME_OUTPUT) {
  process.stderr.write("runtime protocol selection leaked into provider");
  process.exit(1);
}
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
      vi.stubEnv("AGENT_COMPOSE_CODEX_CONTEXT_WINDOW", window);
      vi.stubEnv(AGENT_OUTPUT_ENV, format === "frames" ? "frames" : "");

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
      expect(args.includes("model_context_window=128000")).toBe(window !== "0");
      expect(args.includes("model_auto_compact_token_limit=96000")).toBe(window !== "0");
      if (window === "0") {
        expect(args.some((arg: string) => /^model_(context_window|auto_compact_token_limit)=/.test(arg))).toBe(false);
      }
      if (format === "frames") {
        const frames = stdio.stdout.trim().split("\n").map((line) => JSON.parse(line));
        expect(frames.every((frame, seq) => frame.v === 1 && frame.seq === seq)).toBe(true);
        expect(frames[0]).toMatchObject({ type: "started" });
        expect(frames.filter((frame) => frame.type === "agent_event").map((frame) => frame.event.kind))
          .toEqual(expect.arrayContaining(["tool_call", "tool_result", "text_delta"]));
        expect(frames.at(-1)).toMatchObject({ type: "result", threadId: "codex-cli-thread", finalText: "done" });
        expect(stdio.stdout).not.toContain(RESULT_PREFIX);
        expect(stdio.stderr).not.toContain("lookup result");
      } else {
        expect(stdio.stderr).toContain("[mcp:srv/lookup]");
        expect(stdio.stderr).toContain('"query": "docs"');
        expect(stdio.stderr).toContain("lookup result");
        expect(stdio.stderr).toContain("done");
        expect(stdio.stderr).not.toContain("[tool:srv/lookup]");
        expect(stdio.stdout).toContain(`${RESULT_PREFIX}{`);
        expect(stdio.stdout).toContain('"threadId":"codex-cli-thread"');
        expect(stdio.stdout).toContain('"finalText":"done"');
      }
    });
  });
});

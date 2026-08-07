import fs from "node:fs/promises";
import path from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createProgram } from "../src/cli.js";
import { WORKFLOW_EVENT_PREFIX, WORKFLOW_RESULT_PREFIX } from "../src/constants.js";
import { captureStdio, withTempSession } from "./helpers.js";

const promptState = vi.hoisted(() => ({ prompts: [] as string[] }));

vi.mock("../src/prompt.js", () => ({
  runPromptCommand: vi.fn(async (options: { promptText?: string }) => {
    const prompt = options.promptText ?? "";
    promptState.prompts.push(prompt);
    return {
      provider: "codex",
      threadId: `thread-${promptState.prompts.length}`,
      stopReason: "completed",
      finalText: prompt,
      finalTextSource: "provider_message",
      transcript: prompt,
      stderr: "",
    };
  }),
}));

describe("workflow CLI E2E", () => {
  beforeEach(() => {
    promptState.prompts = [];
  });

  it("runs parallel, pipeline, nested workflow, and synthesis through the CLI protocol", async () => {
    await withTempSession(async (root) => {
      const workspace = path.join(root, "workspace");
      const library = path.join(workspace, ".agent-compose", "workflows");
      const scriptFile = path.join(root, "workflow.js");
      const argsFile = path.join(root, "args.json");
      await fs.mkdir(library, { recursive: true });
      await fs.writeFile(path.join(library, "child.js"), `export const meta = { name: "child", description: "nested" }
        return await agent("nested:" + args.area, { key: "nested" })`);
      await fs.writeFile(scriptFile, `export const meta = { name: "e2e", description: "production-like" }
        phase("Scan")
        const scans = await parallel([
          () => agent("scan:" + args.area, { key: "scan" }),
          () => workflow("child", args),
        ])
        const refined = await pipeline(scans, (previous, original, index) => agent("refine:" + index + ":" + original))
        return await agent("synthesize:" + refined.join("|"), { key: "synthesis" })`);
      await fs.writeFile(argsFile, JSON.stringify({ area: "api" }));

      const stdio = captureStdio();
      try {
        await createProgram({ exitOverride: true }).parseAsync([
          "node", "cli", "workflow",
          "--script-file", scriptFile,
          "--args-file", argsFile,
          "--state-root", path.join(root, "state"),
          "--workspace", workspace,
          "--home", path.join(root, "home"),
          "--run-id", "run_e2e",
          "--concurrency", "2",
        ]);
      } finally {
        stdio.restore();
      }

      const resultLine = stdio.stdout.trim().split("\n").find((line) => line.startsWith(WORKFLOW_RESULT_PREFIX));
      const result = JSON.parse((resultLine as string).slice(WORKFLOW_RESULT_PREFIX.length));
      expect(result).toMatchObject({
        runId: "run_e2e",
        status: "completed",
        phases: ["Scan"],
        agentCount: 5,
      });
      expect(result.result).toContain("synthesize:");
      expect(promptState.prompts).toHaveLength(5);
      expect(stdio.stderr).toContain(`${WORKFLOW_EVENT_PREFIX}{"type":"workflow_start"`);
      expect(stdio.stderr).toContain(`${WORKFLOW_EVENT_PREFIX}{"type":"workflow_complete"`);

      const replayStdio = captureStdio();
      try {
        await createProgram({ exitOverride: true }).parseAsync([
          "node", "cli", "workflow",
          "--script-file", scriptFile,
          "--args-file", argsFile,
          "--state-root", path.join(root, "state"),
          "--workspace", workspace,
          "--home", path.join(root, "home"),
          "--run-id", "run_e2e_replay",
          "--resume-run-id", "run_e2e",
          "--concurrency", "2",
        ]);
      } finally {
        replayStdio.restore();
      }
      const replayLine = replayStdio.stdout.trim().split("\n").find((line) => line.startsWith(WORKFLOW_RESULT_PREFIX));
      const replay = JSON.parse((replayLine as string).slice(WORKFLOW_RESULT_PREFIX.length));
      expect(replay.status).toBe("completed");
      expect(replay.agents.every((agent: { status: string }) => agent.status === "cached")).toBe(true);
      expect(promptState.prompts).toHaveLength(5);
      expect(replayStdio.stderr).toContain(`${WORKFLOW_EVENT_PREFIX}{"type":"agent_cached"`);
      await expect(fs.readFile(path.join(root, "state", "workflows", "runs", "run_e2e", "run.json"), "utf8"))
        .resolves.toContain('"runId": "run_e2e"');
    });
  });
});

import fs from "node:fs/promises";
import path from "node:path";
import { describe, expect, it, vi } from "vitest";
import type { AgentResult } from "../src/types.js";
import { WorkflowEventWriter } from "../src/workflow/events.js";
import { parseWorkflowScript } from "../src/workflow/parser.js";
import { WorkflowRuntime } from "../src/workflow/runtime.js";
import { WorkflowStateStore } from "../src/workflow/state.js";
import { withTempSession } from "./helpers.js";

describe("workflow runtime integration", () => {
  it("persists nested calls, settles pipeline failures, and replays cached agents", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const workspace = path.join(root, "workspace");
      const library = path.join(workspace, ".agent-compose", "workflows");
      await fs.mkdir(library, { recursive: true });
      await fs.writeFile(path.join(library, "child.js"), `export const meta = { name: "child", description: "test" }
        return await agent(args.prompt, { key: "child-agent" })`);
      const source = `export const meta = { name: "integration", description: "test" }
        const nested = await parallel([
          () => workflow("child", { prompt: "one" }),
          () => workflow("child", { prompt: "two" }),
        ])
        const piped = await pipeline(["ok", "bad"],
          async (value) => value === "bad" ? (() => { throw new Error("bad item") })() : await agent(value, { key: "pipe" }),
          async (value) => value + "-done")
        return { nested, piped }`;
      const firstPrompt = vi.fn(async ({ promptText }: { promptText?: string }): Promise<AgentResult> => result(promptText ?? ""));
      const first = await runtime(root, stateRoot, workspace, "run_first", source, firstPrompt as never);
      await expect(first.execute()).resolves.toEqual({ nested: ["one", "two"], piped: ["ok-done", null] });
      expect(new Set(first.agents.map((agent) => agent.invocationKey)).size).toBe(3);

      const nestedFiles = await fs.readdir(path.join(stateRoot, "workflows", "runs", "run_first", "nested"));
      expect(nestedFiles).toHaveLength(2);

      const replayPrompt = vi.fn(async () => result("unexpected"));
      const replay = await runtime(root, stateRoot, workspace, "run_replay", source, replayPrompt as never, first.agents);
      await expect(replay.execute()).resolves.toEqual({ nested: ["one", "two"], piped: ["ok-done", null] });
      expect(replayPrompt).not.toHaveBeenCalled();
      expect(replay.agents.every((agent) => agent.status === "cached")).toBe(true);
    });
  });
});

async function runtime(
  root: string,
  stateRoot: string,
  workspace: string,
  runId: string,
  source: string,
  runPrompt: never,
  resumeAgents = [],
) {
  const store = await WorkflowStateStore.create(stateRoot, runId);
  return new WorkflowRuntime({
    runId,
    parsed: parseWorkflowScript(source),
    args: null,
    scriptFile: path.join(root, "workflow.js"),
    stateRoot,
    workspace,
    home: path.join(root, "home"),
    provider: "codex",
    concurrency: 2,
    abortController: new AbortController(),
    store,
    events: new WorkflowEventWriter(store.eventsPath, { write: () => true } as never),
    resumeAgents,
    runPrompt,
  });
}

function result(finalText: string): AgentResult {
  return {
    provider: "codex",
    threadId: "thread",
    stopReason: "completed",
    finalText,
    finalTextSource: "provider_message",
    transcript: finalText,
    stderr: "",
  };
}

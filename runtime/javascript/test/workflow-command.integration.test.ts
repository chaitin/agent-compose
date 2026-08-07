import fs from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { runWorkflowCommand } from "../src/workflow/command.js";
import { WorkflowStateStore } from "../src/workflow/state.js";
import { withTempSession } from "./helpers.js";

describe("workflow command integration", () => {
  it("reads script and args files and persists a completed run", async () => {
    await withTempSession(async (root) => {
      const scriptFile = path.join(root, "workflow.js");
      const argsFile = path.join(root, "args.json");
      const stateRoot = path.join(root, "state");
      await fs.writeFile(scriptFile, `export const meta = { name: "echo", description: "Echo args" }
        phase("Read")
        log(args.message)
        return { message: args.message, cwd }
      `);
      await fs.writeFile(argsFile, JSON.stringify({ message: "hello" }));

      const result = await runWorkflowCommand({
        scriptFile,
        argsFile,
        stateRoot,
        workspace: path.join(root, "workspace"),
        home: path.join(root, "home"),
        runId: "run_echo",
      });

      expect(result).toMatchObject({
        runId: "run_echo",
        status: "completed",
        result: { message: "hello", cwd: path.join(root, "workspace") },
        phases: ["Read"],
        logs: ["hello"],
      });
      const snapshot = JSON.parse(await fs.readFile(path.join(stateRoot, "workflows", "runs", "run_echo", "run.json"), "utf8"));
      expect(snapshot).toMatchObject({ status: "completed", agentCount: 0 });
      const eventLines = (await fs.readFile(path.join(stateRoot, "workflows", "runs", "run_echo", "events.jsonl"), "utf8")).trim().split("\n");
      expect(eventLines.map((line) => JSON.parse(line).type)).toEqual(["workflow_start", "phase", "log", "workflow_complete"]);
    });
  });

  it("returns and persists a parse failure payload", async () => {
    await withTempSession(async (root) => {
      const scriptFile = path.join(root, "invalid.js");
      const stateRoot = path.join(root, "state");
      await fs.writeFile(scriptFile, "return 1");

      const result = await runWorkflowCommand({ scriptFile, stateRoot, runId: "run_invalid" });

      expect(result).toMatchObject({
        runId: "run_invalid",
        status: "failed",
        error: { message: expect.stringContaining("first statement") },
      });
      const snapshot = JSON.parse(await fs.readFile(path.join(stateRoot, "workflows", "runs", "run_invalid", "run.json"), "utf8"));
      expect(snapshot.status).toBe("failed");
    });
  });

  it("interprets abandoned running state as interrupted", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const store = await WorkflowStateStore.create(stateRoot, "run_interrupted");
      await store.writeRun({
        schemaVersion: 1, runId: "run_interrupted", status: "running", argsHash: "a", scriptHash: "s",
        phases: [], logs: [], agentCount: 1, startedAt: new Date(0).toISOString(),
      });
      await store.writeAgent({
        schemaVersion: 1, agentId: "a1", invocationKey: "root/agent:0", index: 1, inputHash: "h",
        label: "agent", phase: "", provider: "codex", model: "", effort: "", agentType: "", isolation: "",
        status: "running", promptPreview: "prompt", providerSessionId: "", worktreePath: "", gitStatus: "",
      });
      await expect(store.readRun()).resolves.toMatchObject({ status: "interrupted" });
      await expect(store.readAgents()).resolves.toMatchObject([{ status: "interrupted" }]);
    });
  });
});

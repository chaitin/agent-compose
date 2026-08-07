import { execFile } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";
import { describe, expect, it, vi } from "vitest";
import type { AgentResult } from "../src/types.js";
import { WorkflowEventWriter } from "../src/workflow/events.js";
import { parseWorkflowScript } from "../src/workflow/parser.js";
import { WorkflowRuntime } from "../src/workflow/runtime.js";
import { WorkflowStateStore } from "../src/workflow/state.js";
import { createManagedWorktree, isLinkedWorktree, removeManagedWorktree } from "../src/workflow/worktree.js";
import { withTempSession } from "./helpers.js";

const execFileAsync = promisify(execFile);

describe("workflow worktree integration", () => {
  it("runs an isolated agent in a detached worktree and preserves modifications", async () => {
    await withTempSession(async (root) => {
      const workspace = path.join(root, "repository");
      await fs.mkdir(workspace);
      await git(workspace, ["init"]);
      await git(workspace, ["config", "user.email", "workflow@example.test"]);
      await git(workspace, ["config", "user.name", "Workflow Test"]);
      await fs.writeFile(path.join(workspace, "README.md"), "base\n");
      await git(workspace, ["add", "README.md"]);
      await git(workspace, ["commit", "-m", "initial"]);

      const source = `export const meta = { name: "worktree", description: "test" }
        return await agent("modify", { isolation: "worktree", key: "edit" })`;
      const stateRoot = path.join(root, "state");
      const store = await WorkflowStateStore.create(stateRoot, "run_worktree");
      const events = new WorkflowEventWriter(store.eventsPath, { write: () => true } as never);
      const runPrompt = vi.fn(async ({ workspace: agentWorkspace }: { workspace?: string }): Promise<AgentResult> => {
        await fs.writeFile(path.join(agentWorkspace as string, "change.txt"), "changed\n");
        return agentResult("modified");
      });
      const runtime = new WorkflowRuntime({
        runId: "run_worktree",
        parsed: parseWorkflowScript(source),
        args: null,
        scriptFile: path.join(root, "workflow.js"),
        stateRoot,
        workspace,
        home: path.join(root, "home"),
        provider: "codex",
        abortController: new AbortController(),
        store,
        events,
        runPrompt: runPrompt as never,
      });

      await expect(runtime.execute()).resolves.toBe("modified");
      const record = runtime.agents[0];
      expect(record.worktreePath).toContain(path.join("workflows", "worktrees", "run_worktree", "a1"));
      expect(record.gitStatus).toContain("?? change.txt");
      await expect(fs.readFile(path.join(record.worktreePath, "change.txt"), "utf8")).resolves.toBe("changed\n");
      await expect(fs.access(path.join(workspace, "change.txt"))).rejects.toThrow();
    });
  });

  it("reuses an isolated agent across consecutive resume generations", async () => {
    await withTempSession(async (root) => {
      const workspace = path.join(root, "repository");
      await fs.mkdir(workspace);
      await git(workspace, ["init"]);
      await git(workspace, ["config", "user.email", "workflow@example.test"]);
      await git(workspace, ["config", "user.name", "Workflow Test"]);
      await fs.writeFile(path.join(workspace, "README.md"), "base\n");
      await git(workspace, ["add", "README.md"]);
      await git(workspace, ["commit", "-m", "initial"]);

      const source = `export const meta = { name: "resume-worktree", description: "test" }
        return await agent("modify", { isolation: "worktree", key: "edit" })`;
      const stateRoot = path.join(root, "state");
      const runPrompt = vi.fn(async ({ workspace: agentWorkspace }: { workspace?: string }): Promise<AgentResult> => {
        await fs.writeFile(path.join(agentWorkspace as string, "change.txt"), "changed\n");
        return agentResult("modified");
      });
      const createRuntime = async (runId: string, resumeAgents: WorkflowRuntime["agents"] = []) => {
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
          abortController: new AbortController(),
          store,
          events: new WorkflowEventWriter(store.eventsPath, { write: () => true } as never),
          resumeAgents,
          runPrompt: runPrompt as never,
        });
      };

      const first = await createRuntime("run_first");
      await expect(first.execute()).resolves.toBe("modified");
      const second = await createRuntime("run_second", first.agents);
      await expect(second.execute()).resolves.toBe("modified");
      const third = await createRuntime("run_third", second.agents);
      await expect(third.execute()).resolves.toBe("modified");

      expect(runPrompt).toHaveBeenCalledTimes(1);
      expect([first.agents[0].status, second.agents[0].status, third.agents[0].status]).toEqual([
        "done", "cached", "cached",
      ]);
      expect(second.agents[0].worktreeHead).toBe(first.agents[0].worktreeHead);
    });
  });

  it("distinguishes a registered linked worktree from an ordinary repository at the managed path", async () => {
    await withTempSession(async (root) => {
      const workspace = path.join(root, "repository");
      await fs.mkdir(workspace);
      await git(workspace, ["init"]);
      await git(workspace, ["config", "user.email", "workflow@example.test"]);
      await git(workspace, ["config", "user.name", "Workflow Test"]);
      await fs.writeFile(path.join(workspace, "README.md"), "base\n");
      await git(workspace, ["add", "README.md"]);
      await git(workspace, ["commit", "-m", "initial"]);
      const managed = await createManagedWorktree(workspace, path.join(root, "state"), "run_linked", "a1");
      await expect(isLinkedWorktree(managed.path)).resolves.toBe(true);

      await removeManagedWorktree(workspace, managed.path);
      await fs.mkdir(managed.path, { recursive: true });
      await git(managed.path, ["init"]);
      await expect(isLinkedWorktree(managed.path)).resolves.toBe(false);
    });
  });
});

async function git(cwd: string, args: string[]): Promise<void> {
  await execFileAsync("git", ["-C", cwd, ...args]);
}

function agentResult(finalText: string): AgentResult {
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

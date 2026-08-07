import fs from "node:fs/promises";
import path from "node:path";
import { describe, expect, it, vi } from "vitest";
import type { AgentResult } from "../src/types.js";
import { WorkflowEventWriter } from "../src/workflow/events.js";
import { childAbortController } from "../src/workflow/agent-invocation.js";
import { parseWorkflowScript } from "../src/workflow/parser.js";
import { WorkflowRuntime } from "../src/workflow/runtime.js";
import { WorkflowStateStore } from "../src/workflow/state.js";
import { withTempSession } from "./helpers.js";

describe("workflow runtime", () => {
  it("inherits cancellation when the parent signal is already aborted", () => {
    const parent = new AbortController();
    parent.abort();

    const child = childAbortController(parent.signal);
    expect(child.controller.signal.aborted).toBe(true);
    child.dispose();
  });

  it("runs parallel agents with stable invocation paths and scoped phases", async () => {
    await withTempSession(async (root) => {
      const runPrompt = vi.fn(async ({ promptText }: { promptText?: string }): Promise<AgentResult> => agentResult(promptText ?? ""));
      const runtime = await createRuntime(root, "run_parallel", `export const meta = { name: "parallel", description: "test" }
        return await parallel([
          () => phase("Backend", () => agent("backend", { key: "scan" })),
          () => phase("Frontend", () => agent("frontend", { key: "scan" })),
        ])`, runPrompt as never);

      await expect(runtime.execute()).resolves.toEqual(["backend", "frontend"]);
      expect(runtime.agents.map((agent) => [agent.invocationKey, agent.phase])).toEqual([
        ["root/parallel:0/key:scan", "Backend"],
        ["root/parallel:1/key:scan", "Frontend"],
      ]);
      expect(runPrompt).toHaveBeenCalledTimes(2);
    });
  });

  it("keeps agent ordinals unique across sequential scoped phases", async () => {
    await withTempSession(async (root) => {
      const runPrompt = vi.fn(async ({ promptText }: { promptText?: string }): Promise<AgentResult> => agentResult(promptText ?? ""));
      const runtime = await createRuntime(root, "run_phases", `export const meta = { name: "phases", description: "test" }
        await phase("First", () => agent("first"))
        await phase("Second", () => agent("second"))
        return await agent("third")`, runPrompt as never);

      await expect(runtime.execute()).resolves.toBe("third");
      expect(runtime.agents.map((agent) => agent.invocationKey)).toEqual([
        "root/agent:0",
        "root/agent:1",
        "root/agent:2",
      ]);
    });
  });

  it("derives default labels from stable context-local identities", async () => {
    await withTempSession(async (root) => {
      const runPrompt = vi.fn(async ({ promptText }: { promptText?: string }): Promise<AgentResult> => agentResult(promptText ?? ""));
      const runtime = await createRuntime(root, "run_stable_labels", `export const meta = { name: "stable-labels", description: "test" }
        return await parallel([
          async () => { await Promise.resolve(); return agent("first") },
          () => agent("second", { key: "named" }),
        ])`, runPrompt as never);

      await runtime.execute();
      expect(Object.fromEntries(runtime.agents.map((agent) => [agent.invocationKey, agent.label]))).toEqual({
        "root/parallel:0/agent:0": "agent 1",
        "root/parallel:1/key:named": "agent named",
      });
    });
  });

  it("settles parallel branch failures but direct agent failures reject", async () => {
    await withTempSession(async (root) => {
      const fail = vi.fn(async ({ promptText }: { promptText?: string }): Promise<AgentResult> => {
        if (promptText === "bad") {
          throw new Error("provider failed");
        }
        return agentResult(promptText ?? "");
      });
      const settled = await createRuntime(root, "run_settle", `export const meta = { name: "settle", description: "test" }
        return await parallel([() => agent("good"), () => agent("bad")])`, fail as never);
      await expect(settled.execute()).resolves.toEqual(["good", null]);
      expect(settled.logs).toContain("parallel branch 1 failed: provider failed");

      const direct = await createRuntime(root, "run_direct", `export const meta = { name: "direct", description: "test" }
        return await agent("bad")`, fail as never);
      await expect(direct.execute()).rejects.toThrow("provider failed");
    });
  });

  it("reuses completed agents by invocationKey and inputHash in a new run", async () => {
    await withTempSession(async (root) => {
      const source = `export const meta = { name: "resume", description: "test" }
        return await agent("cached answer", { key: "stable" })`;
      const firstPrompt = vi.fn(async () => agentResult("cached answer"));
      const first = await createRuntime(root, "run_first", source, firstPrompt as never);
      await first.execute();
      expect(firstPrompt).toHaveBeenCalledTimes(1);

      const secondPrompt = vi.fn(async () => agentResult("should not run"));
      const second = await createRuntime(root, "run_second", source, secondPrompt as never, first.agents);
      await expect(second.execute()).resolves.toBe("cached answer");
      expect(secondPrompt).not.toHaveBeenCalled();
      expect(second.agents[0].status).toBe("cached");

      const thirdPrompt = vi.fn(async () => agentResult("should not run either"));
      const third = await createRuntime(root, "run_third", source, thirdPrompt as never, second.agents);
      await expect(third.execute()).resolves.toBe("cached answer");
      expect(thirdPrompt).not.toHaveBeenCalled();
      expect(third.agents[0].status).toBe("cached");

      const changedPrompt = vi.fn(async () => agentResult("fresh answer"));
      const changed = await createRuntime(root, "run_changed", source.replace("cached answer", "changed answer"), changedPrompt as never, first.agents);
      await expect(changed.execute()).resolves.toBe("fresh answer");
      expect(changedPrompt).toHaveBeenCalledTimes(1);
    });
  });

  it("invalidates cached agents when their workflow system context changes", async () => {
    await withTempSession(async (root) => {
      const source = `export const meta = { name: "resume-context", description: "test" }
        return await agent("review", { key: "stable", label: args.label, phase: args.phase })`;
      const firstPrompt = vi.fn(async () => agentResult("first"));
      const first = await createRuntime(root, "run_context_first", source, firstPrompt as never, [], {
        args: { label: "Reviewer", phase: "Scan" },
      });
      await expect(first.execute()).resolves.toBe("first");

      const secondPrompt = vi.fn(async () => agentResult("second"));
      const second = await createRuntime(root, "run_context_second", source, secondPrompt as never, first.agents, {
        args: { label: "Approver", phase: "Review" },
      });
      await expect(second.execute()).resolves.toBe("second");
      expect(secondPrompt).toHaveBeenCalledTimes(1);
      expect(second.agents[0].status).toBe("done");
    });
  });

  it("shares budget across agents and rejects calls after exhaustion", async () => {
    await withTempSession(async (root) => {
      const runPrompt = vi.fn(async ({ promptText }: { promptText?: string }) => agentResult(promptText ?? ""));
      const runtime = await createRuntime(root, "run_budget", `export const meta = { name: "budget", description: "test" }
        await agent("x")
        return await agent("second")`, runPrompt as never, [], { tokenBudget: 1 });

      await expect(runtime.execute()).rejects.toThrow("workflow token budget exhausted");
      expect(runPrompt).toHaveBeenCalledTimes(1);
    });
  });

  it("times out an individual agent and propagates workflow abort", async () => {
    await withTempSession(async (root) => {
      const waitForAbort = vi.fn(async ({ abortController }: { abortController?: AbortController }) => {
        await new Promise<void>((resolve) => abortController?.signal.addEventListener("abort", () => resolve(), { once: true }));
        return { ...agentResult(""), stopReason: "cancelled" };
      });
      const timed = await createRuntime(root, "run_timeout", `export const meta = { name: "timeout", description: "test" }
        return await agent("slow", { timeoutMs: 10 })`, waitForAbort as never);
      await expect(timed.execute()).rejects.toThrow("workflow agent timed out after 10ms");

      const parent = new AbortController();
      const aborted = await createRuntime(root, "run_abort", `export const meta = { name: "abort", description: "test" }
        return await agent("slow")`, waitForAbort as never, [], { abortController: parent });
      const result = aborted.execute();
      parent.abort();
      await expect(result).rejects.toMatchObject({ code: "WORKFLOW_ABORTED" });
    });
  });

  it("loads one nested workflow and rejects a second nesting level", async () => {
    await withTempSession(async (root) => {
      const library = path.join(root, "workspace", ".agent-compose", "workflows");
      await fs.mkdir(library, { recursive: true });
      await fs.writeFile(path.join(library, "child.js"), `export const meta = { name: "child", description: "test" }
        return { nested: args.value }`);
      const runtime = await createRuntime(root, "run_nested", `export const meta = { name: "parent", description: "test" }
        return await workflow("child", { value: 9 })`, vi.fn() as never);
      await expect(runtime.execute()).resolves.toEqual({ nested: 9 });

      await fs.writeFile(path.join(library, "child.js"), `export const meta = { name: "child", description: "test" }
        return await workflow("grandchild")`);
      const depth = await createRuntime(root, "run_depth", `export const meta = { name: "parent", description: "test" }
        return await workflow("child")`, vi.fn() as never);
      await expect(depth.execute()).rejects.toThrow("nested workflow depth exceeded");
    });
  });

  it("assigns distinct stable paths and snapshots to repeated nested workflows", async () => {
    await withTempSession(async (root) => {
      const library = path.join(root, "workspace", ".agent-compose", "workflows");
      await fs.mkdir(library, { recursive: true });
      await fs.writeFile(path.join(library, "child.js"), `export const meta = { name: "child", description: "test" }
        return await agent(args.prompt)`);
      const source = `export const meta = { name: "parent", description: "test" }
        return await parallel([
          () => workflow("child", { prompt: "one" }),
          () => workflow("child", { prompt: "two" }),
        ])`;
      const runPrompt = vi.fn(async ({ promptText }: { promptText?: string }) => agentResult(promptText ?? ""));
      const runtime = await createRuntime(root, "run_repeated_nested", source, runPrompt as never);

      await expect(runtime.execute()).resolves.toEqual(["one", "two"]);
      expect(runtime.agents.map((agent) => agent.invocationKey).sort()).toEqual([
        "root/parallel:0/workflow:0:child/nested:child/agent:0",
        "root/parallel:1/workflow:0:child/nested:child/agent:0",
      ]);
      const nestedRoot = path.join(root, "state", "workflows", "runs", "run_repeated_nested", "nested");
      const snapshots = await Promise.all((await fs.readdir(nestedRoot)).map(async (name) =>
        JSON.parse(await fs.readFile(path.join(nestedRoot, name), "utf8"))));
      expect(snapshots).toHaveLength(2);
      expect(snapshots.map((snapshot) => snapshot.status)).toEqual(["completed", "completed"]);
      expect(new Set(snapshots.map((snapshot) => snapshot.invocationKey)).size).toBe(2);
    });
  });

  it("uses context intrinsics without exposing the host process", async () => {
    await withTempSession(async (root) => {
      const runtime = await createRuntime(root, "run_vm", `export const meta = { name: "vm", description: "test" }
        return {
          requireType: typeof require,
          envType: typeof process.env,
          escapedEnvType: Object.constructor("return typeof process.env")(),
          agentConstructorType: typeof agent.constructor,
          randomType: typeof Math.random,
          dateType: typeof Date,
        }`, vi.fn() as never);
      await expect(runtime.execute()).resolves.toEqual({
        requireType: "undefined",
        envType: "undefined",
        escapedEnvType: "undefined",
        agentConstructorType: "undefined",
        randomType: "undefined",
        dateType: "undefined",
      });
    });
  });

  it("settles a failed pipeline item and rejects non-serializable results", async () => {
    await withTempSession(async (root) => {
      const pipeline = await createRuntime(root, "run_pipeline_failure", `export const meta = { name: "pipeline", description: "test" }
        return await pipeline(["ok", "bad"],
          async (value) => value === "bad" ? (() => { throw new Error("bad item") })() : value + "-one",
          async (value) => value + "-two")`, vi.fn() as never);
      await expect(pipeline.execute()).resolves.toEqual(["ok-one-two", null]);

      const invalid = await createRuntime(root, "run_invalid_result", `export const meta = { name: "invalid", description: "test" }
        return () => "not json"`, vi.fn() as never);
      await expect(invalid.execute()).rejects.toThrow("workflow result must be JSON-serializable");
    });
  });

  it("enforces the 1000 agent invocation limit", async () => {
    await withTempSession(async (root) => {
      const runPrompt = vi.fn(async () => agentResult("ok"));
      const runtime = await createRuntime(root, "run_limit", `export const meta = { name: "limit", description: "test" }
        for (let index = 0; index < 1001; index++) await agent("call " + index)
        return "unreachable"`, runPrompt as never);
      await expect(runtime.execute()).rejects.toThrow("workflow agent limit exceeded: 1000");
      expect(runPrompt).toHaveBeenCalledTimes(1000);
    });
  });
});

async function createRuntime(
  root: string,
  runId: string,
  source: string,
  runPrompt: never,
  resumeAgents = [],
  overrides: { tokenBudget?: number; abortController?: AbortController; args?: unknown } = {},
) {
  const stateRoot = path.join(root, "state");
  const store = await WorkflowStateStore.create(stateRoot, runId);
  const events = new WorkflowEventWriter(store.eventsPath, { write: () => true } as never);
  const scriptFile = path.join(root, `${runId}.js`);
  await fs.writeFile(scriptFile, source, "utf8");
  return new WorkflowRuntime({
    runId,
    parsed: parseWorkflowScript(source),
    args: overrides.args ?? null,
    scriptFile,
    stateRoot,
    workspace: path.join(root, "workspace"),
    home: path.join(root, "home"),
    provider: "codex",
    concurrency: 2,
    abortController: overrides.abortController ?? new AbortController(),
    tokenBudget: overrides.tokenBudget,
    store,
    events,
    resumeAgents,
    runPrompt,
  });
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

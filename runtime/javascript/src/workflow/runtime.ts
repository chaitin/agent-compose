import { AsyncLocalStorage } from "node:async_hooks";
import os from "node:os";
import vm from "node:vm";
import { runPromptCommand } from "../prompt.js";
import type { AgentResult } from "../types.js";
import {
  agentSummary,
  childAbortController,
  createAgentRecord,
  normalizeAgentOptions,
  type NormalizedAgentOptions,
  workflowSystemContext,
} from "./agent-invocation.js";
import { WorkflowAbortError, errorData, errorMessage, isWorkflowAbort } from "./errors.js";
import { WorkflowEventWriter } from "./events.js";
import { canonicalJSON, sha256 } from "./hash.js";
import { WorkflowLimiter } from "./limiter.js";
import { resolveNestedWorkflow } from "./library.js";
import { parseWorkflowScript } from "./parser.js";
import { WorkflowStateStore, validateWorkflowID } from "./state.js";
import type {
  NestedWorkflowSnapshot,
  ParsedWorkflowScript,
  WorkflowAgentOptions,
  WorkflowAgentRecord,
} from "./types.js";
import {
  createManagedWorktree,
  isLinkedWorktree,
  isManagedWorktreePath,
  removeManagedWorktree,
  worktreeHead,
  worktreeStatus,
} from "./worktree.js";

interface ExecutionContext {
  phase: string;
  path: string[];
  ordinals: {
    agent: number;
    workflow: number;
  };
  usedKeys: Set<string>;
  nestedDepth: number;
  scriptHash: string;
}

export interface WorkflowRuntimeOptions {
  runId: string;
  parsed: ParsedWorkflowScript;
  args: unknown;
  scriptFile: string;
  stateRoot: string;
  workspace: string;
  home: string;
  provider: string;
  model?: string;
  concurrency?: number;
  tokenBudget?: number;
  abortController: AbortController;
  store: WorkflowStateStore;
  events: WorkflowEventWriter;
  resumeAgents?: WorkflowAgentRecord[];
  runPrompt?: typeof runPromptCommand;
}

export class WorkflowRuntime {
  readonly phases: string[] = [];
  readonly logs: string[] = [];
  readonly agents: WorkflowAgentRecord[] = [];

  private readonly contexts = new AsyncLocalStorage<ExecutionContext>();
  private readonly limiter: WorkflowLimiter;
  private readonly resumeAgents: Map<string, WorkflowAgentRecord>;
  private readonly runPrompt: typeof runPromptCommand;
  private invocationCount = 0;
  private spent = 0;
  private nestedCount = 0;
  private readonly invocationKeys = new Set<string>();

  constructor(private readonly options: WorkflowRuntimeOptions) {
    this.limiter = new WorkflowLimiter(normalizeConcurrency(options.concurrency));
    this.resumeAgents = new Map((options.resumeAgents ?? []).map((record) => [record.invocationKey, record]));
    this.runPrompt = options.runPrompt ?? runPromptCommand;
  }

  async execute(): Promise<unknown> {
    return await this.executeParsed(this.options.parsed, this.options.args, this.options.scriptFile, 0);
  }

  private async executeParsed(parsed: ParsedWorkflowScript, args: unknown, scriptFile: string, nestedDepth: number): Promise<unknown> {
    const rootContext: ExecutionContext = {
      phase: "",
      path: nestedDepth === 0 ? ["root"] : [...this.context().path, `nested:${parsed.meta.name}`],
      ordinals: { agent: 0, workflow: 0 },
      usedKeys: new Set(),
      nestedDepth,
      scriptHash: parsed.scriptHash,
    };
    return await this.contexts.run(rootContext, async () => {
      const context = vm.createContext(this.globals(args, scriptFile));
      vm.runInContext(
        "globalThis.Date = undefined; Math.random = undefined; Object.freeze(Math); Object.freeze(process); Object.freeze(budget)",
        context,
      );
      const script = new vm.Script(`(async () => {${parsed.body}\n})()`, { filename: scriptFile });
      const result = await script.runInContext(context, { timeout: 1000 });
      return assertJSONSerializable(result);
    });
  }

  private globals(args: unknown, scriptFile: string): Record<string, unknown> {
    const runtime = this;
    const cwd = hardenCallable(() => this.options.workspace);
    const spent = hardenCallable(() => this.spent);
    const remaining = hardenCallable(() => this.options.tokenBudget == null ? Infinity : this.options.tokenBudget - this.spent);
    return {
      agent: hardenCallable(async (prompt: string, options?: WorkflowAgentOptions) => await runtime.agent(prompt, options)),
      parallel: hardenCallable(async <T>(thunks: Array<() => Promise<T>>) => await runtime.parallel(thunks)),
      pipeline: hardenCallable(async <TItem, TResult>(items: TItem[], ...stages: Array<(previous: unknown, original: TItem, index: number) => TResult | Promise<TResult>>) => await runtime.pipeline(items, stages)),
      phase: hardenCallable(<T>(title: string, body?: () => T | Promise<T>) => runtime.phase(title, body)),
      log: hardenCallable((message: unknown) => runtime.log(message)),
      workflow: hardenCallable(async (reference: unknown, nestedArgs?: unknown) => await runtime.nestedWorkflow(reference, nestedArgs, scriptFile)),
      args: structuredClone(args),
      cwd: this.options.workspace,
      process: Object.freeze({ cwd }),
      budget: Object.freeze({
        total: this.options.tokenBudget ?? null,
        spent,
        remaining,
      }),
    };
  }

  private async agent(prompt: string, rawOptions: WorkflowAgentOptions = {}): Promise<unknown> {
    if (typeof prompt !== "string" || prompt.trim() === "") {
      throw new Error("workflow agent prompt must be a non-empty string");
    }
    this.throwIfAborted();
    if (++this.invocationCount > 1000) {
      throw new Error("workflow agent limit exceeded: 1000");
    }
    if (this.options.tokenBudget != null && this.spent >= this.options.tokenBudget) {
      throw new Error("workflow token budget exhausted");
    }

    const context = this.context();
    const options = normalizeAgentOptions(rawOptions, this.options.provider, this.options.model);
    const defaultLabelIdentity = options.key ?? String(context.ordinals.agent + 1);
    const invocationKey = this.invocationKey(context, options.key);
    if (this.invocationKeys.has(invocationKey)) {
      throw new Error(`duplicate workflow invocationKey: ${invocationKey}`);
    }
    this.invocationKeys.add(invocationKey);
    const agentId = `a${this.invocationCount}`;
    const phase = options.phase ?? context.phase;
    const label = options.label ?? `${phase ? `${phase} ` : ""}agent ${defaultLabelIdentity}`;
    const inputHash = sha256(canonicalJSON({
      scriptHash: context.scriptHash,
      invocationKey,
      prompt,
      provider: options.provider,
      model: options.model ?? "",
      effort: options.effort ?? "",
      schema: options.schema ?? null,
      label,
      phase,
      isolation: options.isolation ?? "",
      agentType: options.agentType ?? "",
    }));
    const record = createAgentRecord(agentId, this.invocationCount, invocationKey, inputHash, label, phase, prompt, options);
    this.agents.push(record);

    const cached = this.resumeAgents.get(invocationKey);
    if (
      (cached?.status === "done" || cached?.status === "cached") &&
      cached.inputHash === inputHash &&
      "result" in cached &&
      await this.cachedWorktreeIsValid(cached)
    ) {
      Object.assign(record, {
        status: "cached",
        result: structuredClone(cached.result),
        providerSessionId: cached.providerSessionId,
        worktreePath: cached.worktreePath,
        worktreeHead: cached.worktreeHead,
        gitStatus: cached.gitStatus,
        completedAt: new Date().toISOString(),
        durationMs: 0,
      });
      await this.options.store.writeAgent(record);
      await this.options.events.emit({ type: "agent_cached", runId: this.options.runId, agent: agentSummary(record) });
      this.charge(record.result);
      return record.result;
    }

    return await this.limiter.run(this.options.abortController.signal, async () => {
      const started = Date.now();
      record.status = "running";
      record.startedAt = new Date(started).toISOString();
      let workspace = this.options.workspace;
      await this.options.store.writeAgent(record);
      await this.options.events.emit({ type: "agent_start", runId: this.options.runId, agent: agentSummary(record) });
      try {
        if (options.isolation === "worktree") {
          const worktree = await createManagedWorktree(this.options.workspace, this.options.stateRoot, this.options.runId, agentId);
          workspace = worktree.path;
          record.worktreePath = worktree.path;
          record.worktreeHead = worktree.head;
        }
        const result = await this.runAgentPrompt(prompt, options, workspace, agentId, label, phase);
        record.result = options.schema ? parseStructuredResult(result.finalText) : result.finalText;
        record.providerSessionId = result.threadId;
        if (record.worktreePath) {
          record.gitStatus = await worktreeStatus(record.worktreePath);
          if (!record.gitStatus) {
            try {
              await removeManagedWorktree(this.options.workspace, record.worktreePath);
              record.worktreePath = "";
            } catch (error) {
              this.log(`could not remove clean worktree ${record.worktreePath}: ${errorMessage(error)}`);
            }
          }
        }
        record.status = "done";
        this.charge(record.result);
        return record.result;
      } catch (error) {
        record.status = this.options.abortController.signal.aborted || isWorkflowAbort(error) ? "skipped" : "error";
        record.error = errorData(error);
        if (record.worktreePath) {
          try {
            record.gitStatus = await worktreeStatus(record.worktreePath);
          } catch {
            // Preserve the original agent failure; worktree inspection is best effort on this path.
          }
        }
        if (this.options.abortController.signal.aborted) {
          throw new WorkflowAbortError();
        }
        throw error;
      } finally {
        const completed = Date.now();
        record.completedAt = new Date(completed).toISOString();
        record.durationMs = completed - started;
        await this.options.store.writeAgent(record);
        await this.options.events.emit({ type: "agent_end", runId: this.options.runId, agent: agentSummary(record) });
      }
    });
  }

  private async runAgentPrompt(
    prompt: string,
    options: NormalizedAgentOptions,
    workspace: string,
    agentId: string,
    label: string,
    phase: string,
  ): Promise<AgentResult> {
    const abortController = childAbortController(this.options.abortController.signal, options.timeoutMs);
    try {
      const result = await this.runPrompt({
        provider: options.provider,
        promptText: prompt,
        stateRoot: this.options.stateRoot,
        sessionRoot: this.options.store.agentSessionRoot(agentId),
        workspace,
        home: this.options.home,
        model: options.model,
        effort: options.effort,
        outputSchema: options.schema,
        systemContextPrefix: workflowSystemContext(label, phase, options),
        abortController: abortController.controller,
      });
      if (abortController.controller.signal.aborted) {
        if (this.options.abortController.signal.aborted) {
          throw new WorkflowAbortError();
        }
        throw new Error(`workflow agent timed out after ${options.timeoutMs}ms`);
      }
      return result;
    } finally {
      abortController.dispose();
    }
  }

  private async parallel<T>(thunks: Array<() => Promise<T>>): Promise<Array<T | null>> {
    if (!Array.isArray(thunks) || thunks.some((thunk) => typeof thunk !== "function")) {
      throw new Error("parallel() requires an array of functions; use () => agent(...)");
    }
    const parent = this.context();
    return await Promise.all(thunks.map(async (thunk, index) => {
      const child = childContext(parent, `parallel:${index}`);
      try {
        return await this.contexts.run(child, thunk);
      } catch (error) {
        if (isWorkflowAbort(error) || this.options.abortController.signal.aborted) {
          throw error;
        }
        this.log(`parallel branch ${index} failed: ${errorMessage(error)}`);
        return null;
      }
    }));
  }

  private async pipeline<TItem, TResult>(
    items: TItem[],
    stages: Array<(previous: unknown, original: TItem, index: number) => TResult | Promise<TResult>>,
  ): Promise<Array<TResult | null>> {
    if (!Array.isArray(items) || stages.length === 0 || stages.some((stage) => typeof stage !== "function")) {
      throw new Error("pipeline() requires an item array and one or more stage functions");
    }
    const parent = this.context();
    return await Promise.all(items.map(async (item, itemIndex) => {
      let previous: unknown = item;
      try {
        for (let stageIndex = 0; stageIndex < stages.length; stageIndex++) {
          const child = childContext(parent, `pipeline:item:${itemIndex}:stage:${stageIndex}`);
          previous = await this.contexts.run(child, () => stages[stageIndex](previous, item, itemIndex));
        }
        return previous as TResult;
      } catch (error) {
        if (isWorkflowAbort(error) || this.options.abortController.signal.aborted) {
          throw error;
        }
        this.log(`pipeline item ${itemIndex} failed: ${errorMessage(error)}`);
        return null;
      }
    }));
  }

  private phase<T>(title: string, body?: () => T | Promise<T>): void | Promise<T> {
    if (typeof title !== "string" || title.trim() === "") {
      throw new Error("phase title must be a non-empty string");
    }
    const normalized = title.trim();
    if (!this.phases.includes(normalized)) {
      this.phases.push(normalized);
    }
    void this.options.events.emit({ type: "phase", runId: this.options.runId, title: normalized });
    const current = this.context();
    if (!body) {
      current.phase = normalized;
      return;
    }
    return this.contexts.run({ ...current, phase: normalized }, async () => await body());
  }

  private log(message: unknown): void {
    const text = typeof message === "string" ? message : canonicalJSON(message);
    this.logs.push(text);
    void this.options.events.emit({ type: "log", runId: this.options.runId, message: text });
  }

  private async nestedWorkflow(reference: unknown, args: unknown, currentScriptFile: string): Promise<unknown> {
    const context = this.context();
    if (context.nestedDepth >= 1) {
      throw new Error("nested workflow depth exceeded");
    }
    const resolved = await resolveNestedWorkflow(reference, currentScriptFile, this.options.workspace, this.options.stateRoot);
    const parsed = parseWorkflowScript(resolved.source);
    const nestedPath = [...context.path, `workflow:${context.ordinals.workflow++}:${parsed.meta.name}`];
    const nestedId = `n${++this.nestedCount}`;
    const started = Date.now();
    const snapshot: NestedWorkflowSnapshot = {
      schemaVersion: 1,
      nestedId,
      invocationKey: nestedPath.join("/"),
      status: "running",
      meta: parsed.meta,
      argsHash: sha256(canonicalJSON(args ?? null)),
      scriptHash: parsed.scriptHash,
      startedAt: new Date(started).toISOString(),
    };
    await this.options.store.writeNested(snapshot);
    try {
      const result = await this.contexts.run({ ...context, path: nestedPath }, async () =>
        await this.executeParsed(parsed, args, resolved.path, context.nestedDepth + 1));
      await this.completeNested(snapshot, "completed", started, result);
      return result;
    } catch (error) {
      await this.completeNested(
        snapshot,
        isWorkflowAbort(error) || this.options.abortController.signal.aborted ? "aborted" : "failed",
        started,
        undefined,
        error,
      );
      throw error;
    }
  }

  private async completeNested(
    snapshot: NestedWorkflowSnapshot,
    status: "completed" | "failed" | "aborted",
    started: number,
    result?: unknown,
    error?: unknown,
  ): Promise<void> {
    const completed = Date.now();
    await this.options.store.writeNested({
      ...snapshot,
      status,
      ...(result !== undefined ? { result } : {}),
      ...(error !== undefined ? { error: errorData(error) } : {}),
      completedAt: new Date(completed).toISOString(),
      durationMs: completed - started,
    });
  }

  private invocationKey(context: ExecutionContext, key: string | undefined): string {
    let suffix: string;
    if (key !== undefined) {
      validateWorkflowID(key, "agent key");
      if (context.usedKeys.has(key)) {
        throw new Error(`duplicate workflow agent key in current context: ${key}`);
      }
      context.usedKeys.add(key);
      suffix = `key:${key}`;
    } else {
      suffix = `agent:${context.ordinals.agent++}`;
    }
    return [...context.path, suffix].join("/");
  }

  private async cachedWorktreeIsValid(record: WorkflowAgentRecord): Promise<boolean> {
    if (!record.worktreePath) {
      return true;
    }
    if (!record.worktreeHead || !isManagedWorktreePath(this.options.stateRoot, record.worktreePath)) {
      return false;
    }
    try {
      return await isLinkedWorktree(record.worktreePath) &&
        await worktreeHead(record.worktreePath) === record.worktreeHead &&
        await worktreeStatus(record.worktreePath) === record.gitStatus;
    } catch {
      return false;
    }
  }

  private context(): ExecutionContext {
    const context = this.contexts.getStore();
    if (!context) {
      throw new Error("workflow execution context is unavailable");
    }
    return context;
  }

  private throwIfAborted(): void {
    if (this.options.abortController.signal.aborted) {
      throw new WorkflowAbortError();
    }
  }

  private charge(result: unknown): void {
    this.spent += Math.ceil(JSON.stringify(result ?? "").length / 4);
  }
}

function childContext(parent: ExecutionContext, segment: string): ExecutionContext {
  return {
    phase: parent.phase,
    path: [...parent.path, segment],
    ordinals: { agent: 0, workflow: 0 },
    usedKeys: new Set(),
    nestedDepth: parent.nestedDepth,
    scriptHash: parent.scriptHash,
  };
}

function normalizeConcurrency(value: number | undefined): number {
  const fallback = Math.max(1, (os.availableParallelism?.() ?? 8) - 2);
  const candidate = value ?? fallback;
  if (!Number.isFinite(candidate)) {
    throw new Error("workflow concurrency must be a number");
  }
  return Math.max(1, Math.min(Math.trunc(candidate), 16));
}

function parseStructuredResult(value: string): unknown {
  try {
    return JSON.parse(value);
  } catch (error) {
    throw new Error(`workflow agent structured result is not valid JSON: ${errorMessage(error)}`);
  }
}

function assertJSONSerializable(value: unknown): unknown {
  if (value === undefined || typeof value === "function" || typeof value === "symbol") {
    throw new Error("workflow result must be JSON-serializable; did you forget to await agent(), parallel(), or pipeline()?");
  }
  try {
    return JSON.parse(JSON.stringify(value)) as unknown;
  } catch {
    throw new Error("workflow result must be JSON-serializable; did you forget to await agent(), parallel(), or pipeline()?");
  }
}

function hardenCallable<T extends (...args: never[]) => unknown>(callable: T): T {
  Object.setPrototypeOf(callable, null);
  return Object.freeze(callable);
}

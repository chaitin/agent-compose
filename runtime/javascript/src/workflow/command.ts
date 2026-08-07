import { randomUUID } from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { SANDBOX_ROOT } from "../constants.js";
import { normalizeProvider } from "../provider.js";
import { WorkflowAbortError, errorData, isWorkflowAbort } from "./errors.js";
import { WorkflowEventWriter } from "./events.js";
import { canonicalJSON, sha256 } from "./hash.js";
import { parseWorkflowScript } from "./parser.js";
import { WorkflowRuntime } from "./runtime.js";
import { WorkflowStateStore, validateWorkflowID } from "./state.js";
import type {
  WorkflowCommandOptions,
  WorkflowMeta,
  WorkflowResultPayload,
  WorkflowRunSnapshot,
} from "./types.js";

export async function runWorkflowCommand(options: WorkflowCommandOptions): Promise<WorkflowResultPayload> {
  const started = Date.now();
  const runId = options.runId ?? `run_${randomUUID()}`;
  validateWorkflowID(runId, "runId");
  if (options.resumeRunId === runId) {
    throw new Error("workflow runId must differ from resumeRunId");
  }
  const stateRoot = path.resolve(options.stateRoot || path.join(SANDBOX_ROOT, "state"));
  const workspace = path.resolve(
    options.workspace || process.env.WORKSPACE || process.env.AGENT_COMPOSE_WORKSPACE || path.join(SANDBOX_ROOT, "workspace"),
  );
  const home = path.resolve(options.home || process.env.HOME || path.join(SANDBOX_ROOT, "home"));
  const store = await WorkflowStateStore.create(stateRoot, runId);
  const events = new WorkflowEventWriter(store.eventsPath);
  let meta: WorkflowMeta | undefined;
  let runtime: WorkflowRuntime | undefined;
  let args: unknown = null;
  let scriptHash = "";

  try {
    const scriptFile = path.resolve(options.scriptFile);
    const source = await fs.readFile(scriptFile, "utf8");
    const parsed = parseWorkflowScript(source);
    meta = parsed.meta;
    scriptHash = parsed.scriptHash;
    args = options.argsFile ? JSON.parse(await fs.readFile(path.resolve(options.argsFile), "utf8")) as unknown : null;
    const resumeAgents = options.resumeRunId
      ? await loadResumeAgents(stateRoot, options.resumeRunId)
      : [];
    const abortController = options.abortController ?? new AbortController();
    runtime = new WorkflowRuntime({
      runId,
      parsed,
      args,
      scriptFile,
      stateRoot,
      workspace,
      home,
      provider: normalizeProvider(options.provider ?? "codex"),
      model: options.model,
      concurrency: options.concurrency,
      tokenBudget: options.tokenBudget,
      abortController,
      store,
      events,
      resumeAgents,
    });

    await store.writeRun(runSnapshot({
      runId,
      resumedFrom: options.resumeRunId,
      status: "running",
      meta,
      args,
      scriptHash,
      started,
      runtime,
    }));
    await events.emit({ type: "workflow_start", runId, meta });
    const result = await runtime.execute();
    const completed = Date.now();
    const snapshot = runSnapshot({
      runId,
      resumedFrom: options.resumeRunId,
      status: "completed",
      meta,
      args,
      scriptHash,
      started,
      completed,
      result,
      runtime,
    });
    await store.writeRun(snapshot);
    await events.emit({ type: "workflow_complete", runId, status: "completed", durationMs: completed - started });
    await events.flush();
    return {
      runId,
      status: "completed",
      meta,
      result,
      phases: runtime.phases,
      logs: runtime.logs,
      agents: runtime.agents,
      agentCount: runtime.agents.length,
      durationMs: completed - started,
    };
  } catch (error) {
    const completed = Date.now();
    const aborted = isWorkflowAbort(error) || options.abortController?.signal.aborted === true;
    const status = aborted ? "aborted" : "failed";
    const failure = aborted && !(error instanceof Error) ? new WorkflowAbortError() : error;
    const payload: WorkflowResultPayload = {
      runId,
      status,
      ...(meta ? { meta } : {}),
      error: errorData(failure),
      phases: runtime?.phases ?? [],
      logs: runtime?.logs ?? [],
      agents: runtime?.agents ?? [],
      durationMs: completed - started,
    };
    try {
      await store.writeRun(runSnapshot({
        runId,
        resumedFrom: options.resumeRunId,
        status,
        meta,
        args,
        scriptHash,
        started,
        completed,
        error: payload.error,
        runtime,
      }));
      await events.emit({ type: "workflow_error", runId, status, message: payload.error.message });
      await events.flush();
    } catch {
      // Preserve the original workflow failure when final failure persistence also fails.
    }
    return payload;
  }
}

async function loadResumeAgents(stateRoot: string, resumeRunId: string) {
  const store = WorkflowStateStore.open(stateRoot, resumeRunId);
  await store.readRun();
  return await store.readAgents();
}

function runSnapshot(input: {
  runId: string;
  resumedFrom?: string;
  status: WorkflowRunSnapshot["status"];
  meta?: WorkflowMeta;
  args: unknown;
  scriptHash: string;
  started: number;
  completed?: number;
  result?: unknown;
  error?: WorkflowRunSnapshot["error"];
  runtime?: WorkflowRuntime;
}): WorkflowRunSnapshot {
  return {
    schemaVersion: 1,
    runId: input.runId,
    ...(input.resumedFrom ? { resumedFrom: input.resumedFrom } : {}),
    status: input.status,
    ...(input.meta ? { meta: input.meta } : {}),
    argsHash: sha256(canonicalJSON(input.args)),
    scriptHash: input.scriptHash,
    phases: input.runtime?.phases ?? [],
    logs: input.runtime?.logs ?? [],
    ...(input.result !== undefined ? { result: input.result } : {}),
    ...(input.error ? { error: input.error } : {}),
    agentCount: input.runtime?.agents.length ?? 0,
    startedAt: new Date(input.started).toISOString(),
    ...(input.completed !== undefined ? {
      completedAt: new Date(input.completed).toISOString(),
      durationMs: input.completed - input.started,
    } : {}),
  };
}

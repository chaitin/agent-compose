import { spawn } from "node:child_process";
import crypto from "node:crypto";
import fsp from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { StringDecoder } from "node:string_decoder";
import { paths } from "./env.js";

const WORKFLOW_RESULT_PREFIX = "__WORKFLOW_RESULT__";
const WORKFLOW_EVENT_PREFIX = "__WORKFLOW_EVENT__";

export type RuntimeWorkflowProvider = "codex" | "claude" | "gemini" | "opencode" | "pi";

export interface RuntimeWorkflowMeta {
  name: string;
  description: string;
  whenToUse?: string;
  phases?: Array<{ title: string; detail?: string; model?: string }>;
}

export interface RuntimeWorkflowAgent {
  agentId: string;
  invocationKey: string;
  label: string;
  phase: string;
  provider: RuntimeWorkflowProvider;
  status: string;
  result?: unknown;
  error?: RuntimeWorkflowFailure;
  providerSessionId?: string;
  worktreePath?: string;
  gitStatus?: string;
  durationMs?: number;
}

export type RuntimeWorkflowEvent =
  | { type: "workflow_start"; runId: string; meta: RuntimeWorkflowMeta }
  | { type: "phase"; runId: string; title: string }
  | { type: "log"; runId: string; message: string }
  | { type: "agent_start" | "agent_cached" | "agent_end"; runId: string; agent: RuntimeWorkflowAgent }
  | { type: "workflow_complete"; runId: string; status: "completed"; durationMs: number }
  | { type: "workflow_error"; runId: string; status: "failed" | "aborted"; message: string };

export interface RuntimeWorkflowOptions {
  args?: unknown;
  provider?: RuntimeWorkflowProvider;
  model?: string;
  concurrency?: number;
  tokenBudget?: number;
  runId?: string;
  resumeRunId?: string;
  stateRoot?: string;
  workspace?: string;
  home?: string;
  timeoutMs?: number;
  onUpdate?: (event: RuntimeWorkflowEvent) => void;
}

export interface RuntimeWorkflowCompleted<T = unknown> {
  runId: string;
  status: "completed";
  meta: RuntimeWorkflowMeta;
  result: T;
  phases: string[];
  logs: string[];
  agents: RuntimeWorkflowAgent[];
  agentCount: number;
  durationMs: number;
  events: RuntimeWorkflowEvent[];
  stderr: string;
}

export interface RuntimeWorkflowFailure {
  message: string;
  name?: string;
  stack?: string;
}

export interface RuntimeWorkflowFailed {
  runId: string;
  status: "failed";
  meta?: RuntimeWorkflowMeta;
  error: RuntimeWorkflowFailure;
  phases: string[];
  logs: string[];
  agents: RuntimeWorkflowAgent[];
  durationMs: number;
  events: RuntimeWorkflowEvent[];
  stderr: string;
}

export interface RuntimeWorkflowAborted extends Omit<RuntimeWorkflowFailed, "status"> {
  status: "aborted";
}

export type RuntimeWorkflowOutcome<T = unknown> = RuntimeWorkflowCompleted<T> | RuntimeWorkflowFailed | RuntimeWorkflowAborted;
export type RuntimeWorkflowResult<T = unknown> = RuntimeWorkflowCompleted<T>;

export class RuntimeWorkflowError extends Error {
  constructor(readonly outcome: RuntimeWorkflowFailed | RuntimeWorkflowAborted) {
    super(outcome.error.message);
    this.name = "RuntimeWorkflowError";
  }
}

export class RuntimeWorkflowProtocolError extends Error {
  constructor(message: string, readonly events: RuntimeWorkflowEvent[] = [], readonly stderr = "") {
    super(message);
    this.name = "RuntimeWorkflowProtocolError";
  }
}

export class RuntimeWorkflowTimeoutError extends Error {
  constructor(timeoutMs: number, readonly events: RuntimeWorkflowEvent[], readonly stderr: string) {
    super(`runtime.workflow timed out after ${timeoutMs}ms`);
    this.name = "RuntimeWorkflowTimeoutError";
  }
}

export async function workflow<T = unknown>(script: string, options: RuntimeWorkflowOptions = {}): Promise<RuntimeWorkflowResult<T>> {
  const tempDir = await fsp.mkdtemp(path.join(os.tmpdir(), "agent-compose-runtime-sdk-workflow-"));
  const scriptFile = path.join(tempDir, `workflow-${crypto.randomUUID()}.js`);
  await fsp.writeFile(scriptFile, script, "utf8");
  try {
    return await runWorkflowFile<T>(scriptFile, options, tempDir);
  } finally {
    await fsp.rm(tempDir, { recursive: true, force: true });
  }
}

export async function workflowFile<T = unknown>(scriptFile: string, options: RuntimeWorkflowOptions = {}): Promise<RuntimeWorkflowResult<T>> {
  const tempDir = await fsp.mkdtemp(path.join(os.tmpdir(), "agent-compose-runtime-sdk-workflow-"));
  try {
    return await runWorkflowFile<T>(scriptFile, options, tempDir);
  } finally {
    await fsp.rm(tempDir, { recursive: true, force: true });
  }
}

async function runWorkflowFile<T>(
  scriptFile: string,
  options: RuntimeWorkflowOptions,
  tempDir: string,
): Promise<RuntimeWorkflowResult<T>> {
  const args = [
    "workflow",
    "--script-file", scriptFile,
    "--state-root", options.stateRoot ?? paths.stateRoot,
    "--workspace", options.workspace ?? paths.workspace,
    "--home", options.home ?? paths.home,
    "--provider", options.provider ?? "codex",
  ];
  if (options.args !== undefined) {
    const argsFile = path.join(tempDir, `args-${crypto.randomUUID()}.json`);
    await fsp.writeFile(argsFile, JSON.stringify(options.args), "utf8");
    args.push("--args-file", argsFile);
  }
  appendOption(args, "--model", options.model);
  appendOption(args, "--concurrency", options.concurrency);
  appendOption(args, "--token-budget", options.tokenBudget);
  appendOption(args, "--run-id", options.runId);
  appendOption(args, "--resume-run-id", options.resumeRunId);

  const events: RuntimeWorkflowEvent[] = [];
  const resultPayloads: RuntimeWorkflowOutcome<T>[] = [];
  let rawStderr = "";
  let decoderError: Error | undefined;
  const child = spawn("agent-compose-runtime", args, {
    cwd: options.workspace ?? paths.workspace,
    env: {
      ...process.env,
      WORKSPACE: options.workspace ?? paths.workspace,
      STATE_ROOT: options.stateRoot ?? paths.stateRoot,
      RUNTIME_ROOT: paths.runtimeRoot,
    },
    shell: false,
  });

  const stdoutDecoder = new LineDecoder((line) => {
    if (!line.startsWith(WORKFLOW_RESULT_PREFIX)) {
      return;
    }
    try {
      const payload = JSON.parse(line.slice(WORKFLOW_RESULT_PREFIX.length)) as unknown;
      if (!isWorkflowOutcome(payload)) {
        throw new Error("payload does not match the workflow result protocol");
      }
      resultPayloads.push(payload as RuntimeWorkflowOutcome<T>);
    } catch (error) {
      decoderError = new Error(`invalid workflow result payload: ${errorMessage(error)}`);
    }
  });
  const stderrDecoder = new LineDecoder((line, terminated) => {
    if (line.startsWith(WORKFLOW_EVENT_PREFIX)) {
      try {
        const event = JSON.parse(line.slice(WORKFLOW_EVENT_PREFIX.length)) as unknown;
        if (!isWorkflowEvent(event)) {
          throw new Error("payload does not match the workflow event protocol");
        }
        events.push(event);
        options.onUpdate?.(event);
      } catch (error) {
        decoderError = new Error(`invalid workflow event payload: ${errorMessage(error)}`);
      }
      return;
    }
    rawStderr += line + (terminated ? "\n" : "");
  });
  child.stdout.on("data", (chunk: Buffer) => stdoutDecoder.push(chunk));
  child.stderr.on("data", (chunk: Buffer) => stderrDecoder.push(chunk));

  let timedOut = false;
  let killTimer: NodeJS.Timeout | undefined;
  const timeout = options.timeoutMs && options.timeoutMs > 0
    ? setTimeout(() => {
      timedOut = true;
      child.kill("SIGTERM");
      killTimer = setTimeout(() => child.kill("SIGKILL"), 1000);
      killTimer.unref();
    }, options.timeoutMs)
    : undefined;
  timeout?.unref();

  try {
    await waitForChild(child);
  } finally {
    if (timeout) {
      clearTimeout(timeout);
    }
    if (killTimer) {
      clearTimeout(killTimer);
    }
    stdoutDecoder.finish();
    stderrDecoder.finish();
  }

  if (decoderError) {
    throw new RuntimeWorkflowProtocolError(decoderError.message, events, rawStderr);
  }
  if (resultPayloads.length !== 1) {
    if (timedOut) {
      throw new RuntimeWorkflowTimeoutError(options.timeoutMs as number, events, rawStderr);
    }
    throw new RuntimeWorkflowProtocolError(
      resultPayloads.length === 0
        ? "agent-compose-runtime did not emit a workflow result payload"
        : "agent-compose-runtime emitted multiple workflow result payloads",
      events,
      rawStderr,
    );
  }
  const outcome = { ...resultPayloads[0], events, stderr: rawStderr } as RuntimeWorkflowOutcome<T>;
  if (outcome.status !== "completed") {
    throw new RuntimeWorkflowError(outcome);
  }
  return outcome;
}

class LineDecoder {
  private buffer = "";
  private readonly decoder = new StringDecoder("utf8");

  constructor(private readonly onLine: (line: string, terminated: boolean) => void) {}

  push(chunk: Buffer): void {
    this.buffer += this.decoder.write(chunk);
    let newline = this.buffer.indexOf("\n");
    while (newline >= 0) {
      const line = this.buffer.slice(0, newline).replace(/\r$/, "");
      this.buffer = this.buffer.slice(newline + 1);
      this.onLine(line, true);
      newline = this.buffer.indexOf("\n");
    }
  }

  finish(): void {
    this.buffer += this.decoder.end();
    if (this.buffer) {
      this.onLine(this.buffer.replace(/\r$/, ""), false);
      this.buffer = "";
    }
  }
}

function isWorkflowOutcome(value: unknown): value is RuntimeWorkflowOutcome<unknown> {
  if (!isRecord(value) || typeof value.runId !== "string" || !Array.isArray(value.phases) ||
      !Array.isArray(value.logs) || !Array.isArray(value.agents) || typeof value.durationMs !== "number") {
    return false;
  }
  if (value.status === "completed") {
    return isWorkflowMeta(value.meta) && typeof value.agentCount === "number" && "result" in value;
  }
  return (value.status === "failed" || value.status === "aborted") &&
    isRecord(value.error) && typeof value.error.message === "string";
}

function isWorkflowEvent(value: unknown): value is RuntimeWorkflowEvent {
  if (!isRecord(value) || typeof value.type !== "string" || typeof value.runId !== "string") {
    return false;
  }
  switch (value.type) {
    case "workflow_start": return isWorkflowMeta(value.meta);
    case "phase": return typeof value.title === "string";
    case "log": return typeof value.message === "string";
    case "agent_start":
    case "agent_cached":
    case "agent_end": return isWorkflowAgent(value.agent);
    case "workflow_complete": return value.status === "completed" && typeof value.durationMs === "number";
    case "workflow_error": return (value.status === "failed" || value.status === "aborted") && typeof value.message === "string";
    default: return false;
  }
}

function isWorkflowMeta(value: unknown): value is RuntimeWorkflowMeta {
  return isRecord(value) && typeof value.name === "string" && typeof value.description === "string";
}

function isWorkflowAgent(value: unknown): value is RuntimeWorkflowAgent {
  return isRecord(value) && typeof value.agentId === "string" && typeof value.invocationKey === "string" &&
    typeof value.label === "string" && typeof value.phase === "string" && typeof value.provider === "string" &&
    typeof value.status === "string";
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function appendOption(args: string[], name: string, value: string | number | undefined): void {
  if (value !== undefined) {
    args.push(name, String(value));
  }
}

function waitForChild(child: ReturnType<typeof spawn>): Promise<number> {
  return new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("close", (code) => resolve(code ?? 1));
  });
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

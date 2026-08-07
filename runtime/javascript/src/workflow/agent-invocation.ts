import { normalizeProvider } from "../provider.js";
import type {
  WorkflowAgentOptions,
  WorkflowAgentRecord,
  WorkflowAgentSummary,
  WorkflowEffort,
} from "./types.js";

const agentKeyPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

export interface NormalizedAgentOptions extends Omit<WorkflowAgentOptions, "provider" | "effort"> {
  provider: ReturnType<typeof normalizeProvider>;
  effort?: WorkflowEffort;
}

export function normalizeAgentOptions(
  raw: WorkflowAgentOptions,
  defaultProvider: string,
  defaultModel?: string,
): NormalizedAgentOptions {
  const provider = normalizeProvider(raw.provider ?? defaultProvider);
  if (raw.key !== undefined && !agentKeyPattern.test(raw.key)) {
    throw new Error(`agent key must match ${agentKeyPattern.source}`);
  }
  if (raw.isolation !== undefined && raw.isolation !== "worktree") {
    throw new Error(`unsupported workflow agent isolation: ${String(raw.isolation)}`);
  }
  if (raw.timeoutMs !== undefined && (!Number.isFinite(raw.timeoutMs) || raw.timeoutMs <= 0)) {
    throw new Error("workflow agent timeoutMs must be a positive number");
  }
  if (raw.effort === "max" && provider === "codex") {
    throw new Error("workflow agent effort max is not supported");
  }
  if (raw.effort && provider !== "codex" && provider !== "claude") {
    throw new Error(`workflow agent effort is not supported by ${provider} runner`);
  }
  return { ...raw, provider, model: raw.model ?? defaultModel, effort: raw.effort };
}

export function createAgentRecord(
  agentId: string,
  index: number,
  invocationKey: string,
  inputHash: string,
  label: string,
  phase: string,
  prompt: string,
  options: NormalizedAgentOptions,
): WorkflowAgentRecord {
  return {
    schemaVersion: 1,
    agentId,
    invocationKey,
    index,
    inputHash,
    label,
    phase,
    provider: options.provider,
    model: options.model ?? "",
    effort: options.effort ?? "",
    agentType: options.agentType ?? "",
    isolation: options.isolation ?? "",
    status: "queued",
    promptPreview: prompt.slice(0, 200),
    providerSessionId: "",
    worktreePath: "",
    gitStatus: "",
  };
}

export function agentSummary(record: WorkflowAgentRecord): WorkflowAgentSummary {
  return {
    agentId: record.agentId,
    invocationKey: record.invocationKey,
    label: record.label,
    phase: record.phase,
    provider: record.provider,
    status: record.status,
  };
}

export function workflowSystemContext(label: string, phase: string, options: NormalizedAgentOptions): string {
  return [
    `Workflow agent label: ${label}`,
    phase ? `Workflow phase: ${phase}` : "",
    options.agentType ? `Workflow agent type: ${options.agentType}` : "",
    options.isolation ? `Workflow isolation: ${options.isolation}` : "",
  ].filter(Boolean).join("\n");
}

export function childAbortController(
  parent: AbortSignal,
  timeoutMs?: number,
): { controller: AbortController; dispose(): void } {
  const controller = new AbortController();
  const abort = () => controller.abort();
  if (parent.aborted) {
    abort();
  } else {
    parent.addEventListener("abort", abort, { once: true });
  }
  const timeout = timeoutMs === undefined ? undefined : setTimeout(abort, timeoutMs);
  timeout?.unref();
  return {
    controller,
    dispose() {
      parent.removeEventListener("abort", abort);
      if (timeout) {
        clearTimeout(timeout);
      }
    },
  };
}

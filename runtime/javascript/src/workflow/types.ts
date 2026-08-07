import type { Provider, RuntimeJsonSchema } from "../types.js";

export interface WorkflowMeta {
  name: string;
  description: string;
  whenToUse?: string;
  phases?: Array<{
    title: string;
    detail?: string;
    model?: string;
  }>;
}

export type WorkflowStatus = "running" | "completed" | "failed" | "aborted" | "interrupted";
export type WorkflowAgentStatus = "queued" | "running" | "done" | "error" | "cached" | "skipped" | "interrupted";
export type WorkflowEffort = "low" | "medium" | "high" | "xhigh" | "max";

export interface WorkflowAgentOptions {
  key?: string;
  label?: string;
  phase?: string;
  schema?: RuntimeJsonSchema;
  provider?: Provider;
  model?: string;
  effort?: WorkflowEffort;
  isolation?: "worktree";
  agentType?: string;
  timeoutMs?: number;
}

export interface WorkflowAgentRecord {
  schemaVersion: 1;
  agentId: string;
  invocationKey: string;
  index: number;
  inputHash: string;
  label: string;
  phase: string;
  provider: Provider;
  model: string;
  effort: string;
  agentType: string;
  isolation: string;
  status: WorkflowAgentStatus;
  promptPreview: string;
  result?: unknown;
  error?: WorkflowErrorData;
  providerSessionId: string;
  worktreePath: string;
  gitStatus: string;
  worktreeHead?: string;
  startedAt?: string;
  completedAt?: string;
  durationMs?: number;
}

export interface WorkflowRunSnapshot {
  schemaVersion: 1;
  runId: string;
  resumedFrom?: string;
  status: WorkflowStatus;
  meta?: WorkflowMeta;
  argsHash: string;
  scriptHash: string;
  phases: string[];
  logs: string[];
  result?: unknown;
  error?: WorkflowErrorData;
  agentCount: number;
  startedAt: string;
  completedAt?: string;
  durationMs?: number;
}

export interface NestedWorkflowSnapshot {
  schemaVersion: 1;
  nestedId: string;
  invocationKey: string;
  status: WorkflowStatus;
  meta: WorkflowMeta;
  argsHash: string;
  scriptHash: string;
  result?: unknown;
  error?: WorkflowErrorData;
  startedAt: string;
  completedAt?: string;
  durationMs?: number;
}

export interface WorkflowAgentSummary {
  agentId: string;
  invocationKey: string;
  label: string;
  phase: string;
  provider: Provider;
  status: WorkflowAgentStatus;
}

export type WorkflowEvent =
  | { type: "workflow_start"; runId: string; meta: WorkflowMeta }
  | { type: "phase"; runId: string; title: string }
  | { type: "log"; runId: string; message: string }
  | { type: "agent_start" | "agent_cached" | "agent_end"; runId: string; agent: WorkflowAgentSummary }
  | { type: "workflow_complete"; runId: string; status: "completed"; durationMs: number }
  | { type: "workflow_error"; runId: string; status: "failed" | "aborted"; message: string };

export interface WorkflowErrorData {
  message: string;
  name?: string;
  stack?: string;
}

export interface WorkflowCompletedPayload {
  runId: string;
  status: "completed";
  meta: WorkflowMeta;
  result: unknown;
  phases: string[];
  logs: string[];
  agents: WorkflowAgentRecord[];
  agentCount: number;
  durationMs: number;
}

export interface WorkflowErrorPayload {
  runId: string;
  status: "failed" | "aborted";
  meta?: WorkflowMeta;
  error: WorkflowErrorData;
  phases: string[];
  logs: string[];
  agents: WorkflowAgentRecord[];
  durationMs: number;
}

export type WorkflowResultPayload = WorkflowCompletedPayload | WorkflowErrorPayload;

export interface WorkflowCommandOptions {
  scriptFile: string;
  argsFile?: string;
  stateRoot?: string;
  workspace?: string;
  home?: string;
  provider?: string;
  model?: string;
  concurrency?: number;
  tokenBudget?: number;
  runId?: string;
  resumeRunId?: string;
  abortController?: AbortController;
}

export interface ParsedWorkflowScript {
  meta: WorkflowMeta;
  body: string;
  scriptHash: string;
}

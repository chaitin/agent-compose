import type { AgentEventSink } from "./agent-event.js";

export type Provider = "codex" | "claude" | "gemini" | "opencode" | "pi" | "dsh";
export type RuntimeJsonSchema = Record<string, unknown>;
export type FinalTextSource = "none" | "provider_message" | "transcript_fallback";

export interface AgentResult {
  provider: Provider;
  threadId: string;
  stopReason: string;
  finalText: string;
  finalTextSource: FinalTextSource;
  transcript: string;
  stderr: string;
}

export interface RunnerOptions {
  provider: Provider;
  model?: string;
  effort?: "low" | "medium" | "high" | "xhigh" | "max";
  stateRoot: string;
  sessionRoot: string;
  workspace: string;
  home: string;
  runtimeRoot: string;
  systemContext: string;
  mcpConfig?: Record<string, unknown>;
  skills?: string[];
  outputSchema?: RuntimeJsonSchema;
  abortController?: AbortController;
  /**
   * Receives provider-neutral events as the runner maps them. Optional and
   * defaulted to a no-op, so the non-attach `prompt` path pays nothing.
   */
  onEvent?: AgentEventSink;
}

export interface StoredThread {
  provider: string;
  threadId: string;
  sessionId?: string;
  updatedAt?: string;
  systemContextHash?: string;
  systemContextHashVersion?: number;
}

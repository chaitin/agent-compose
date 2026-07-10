export type Provider = "codex" | "claude" | "gemini" | "opencode";
export type RuntimeJsonSchema = Record<string, unknown>;

export interface AgentResult {
  provider: Provider;
  threadId: string;
  stopReason: string;
  finalText: string;
  transcript: string;
  stderr: string;
}

export interface RunnerOptions {
  provider: Provider;
  model?: string;
  stateRoot: string;
  workspace: string;
  home: string;
  runtimeRoot: string;
  systemContext: string;
  mcpConfig?: Record<string, unknown>;
  skills?: string[];
  outputSchema?: RuntimeJsonSchema;
}

export interface StoredThread {
  provider: string;
  threadId: string;
  updatedAt?: string;
}

export type Provider = "codex" | "claude" | "gemini" | "opencode";
export type RuntimeJsonSchema = Record<string, unknown>;

export interface AgentResult {
  provider: Provider;
  sessionId: string;
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
  outputSchema?: RuntimeJsonSchema;
}

export interface StoredSession {
  provider: string;
  sessionId: string;
  updatedAt?: string;
  systemContextHash?: string;
  systemContextHashVersion?: number;
}

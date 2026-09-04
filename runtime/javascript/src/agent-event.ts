/**
 * Provider-neutral agent event model. Every runner maps its provider's native
 * event stream onto this union so consumers do not need six parsers.
 *
 * Two rules the mappers must honour, both derived from measured provider
 * behaviour against the recorded fixtures under
 * test/fixtures/providers/:
 *
 * - A provider that structurally cannot produce a kind emits no event of that
 *   kind at all. Never emit a placeholder with empty fields: consumers cannot
 *   distinguish "did not happen" from "this provider never reports it".
 * - `inputTokens` always EXCLUDES cached tokens. Providers that report an
 *   inclusive count (codex, gemini) subtract before emitting.
 */

/** Tool categories, mirroring ACP's ToolKind minus its editor-only `switch_mode`. */
export type ToolKind =
  | "read"
  | "edit"
  | "delete"
  | "move"
  | "search"
  | "execute"
  | "think"
  | "fetch"
  | "other";

export type AgentStopReason = "stop" | "tool_use" | "max_tokens" | "cancelled" | "error";

export type ToolCallStatus = "pending" | "in_progress" | "completed" | "failed";

/**
 * Which aggregation level a usage record covers. Providers disagree: codex
 * reports per turn, gemini per run, the rest per step. Consumers must not sum
 * records of differing scope.
 */
export type UsageScope = "step" | "turn" | "run";

export interface FileChange {
  path: string;
  kind: "add" | "delete" | "update";
}

export interface TodoItem {
  text: string;
  completed: boolean;
}

export type AgentEvent =
  | { kind: "step_start"; step?: number }
  | { kind: "step_end"; step?: number; stopReason?: AgentStopReason; rawStopReason?: string }
  | { kind: "text_delta"; step?: number; blockIndex?: number; text: string }
  | { kind: "reasoning_delta"; step?: number; blockIndex?: number; text: string }
  | {
    kind: "tool_call";
    step?: number;
    parentToolUseId?: string;
    id: string;
    name: string;
    toolKind: ToolKind;
    status: ToolCallStatus;
    input?: unknown;
    /** Present when toolKind is "execute". */
    command?: string;
    exitCode?: number;
    /** Present when the call is a patch application. */
    changes?: FileChange[];
  }
  | {
    kind: "tool_result";
    step?: number;
    parentToolUseId?: string;
    id: string;
    ok: boolean;
    output?: string;
    error?: string;
  }
  | { kind: "todo"; items: TodoItem[] }
  | {
    kind: "usage";
    step?: number;
    scope: UsageScope;
    model?: string;
    /** Always excludes cached tokens; see the module comment. */
    inputTokens: number;
    /** Includes reasoning tokens, matching every provider's own convention. */
    outputTokens: number;
    reasoningTokens?: number;
    cachedTokens?: number;
    cacheWriteTokens?: number;
    costUsd?: number;
  }
  | {
    kind: "retry";
    reason: "rate_limit" | "overloaded" | "network" | "other";
    attempt: number;
    maxAttempts?: number;
    message?: string;
  }
  | { kind: "compaction"; phase: "start" | "end" }
  | {
    kind: "error";
    severity: "warning" | "error" | "fatal";
    code?: string;
    retryable?: boolean;
    message: string;
  };

/** Sink a runner calls for each mapped event. Sequencing is the sink's job. */
export type AgentEventSink = (event: AgentEvent) => void;

const shellToolNames = new Set([
  "bash",
  "shell",
  "run_shell_command",
  "run_command",
  "execute_command",
  "terminal",
]);
const readToolNames = new Set(["read", "read_file", "view", "cat", "read_many_files"]);
const editToolNames = new Set(["write", "edit", "write_file", "replace", "apply_patch", "str_replace", "multiedit"]);
const searchToolNames = new Set(["grep", "glob", "search", "search_file_content", "find"]);
const fetchToolNames = new Set(["fetch", "web_fetch", "web_search", "google_web_search", "webfetch", "websearch"]);

/**
 * Classify a provider tool name. Only codex separates shell and patch calls at
 * the protocol level; every other provider reports them as ordinary tools, so
 * the name is all we have. Cross-provider counting must therefore key on
 * `kind === "tool_call"`, never on the resulting ToolKind.
 */
export function toolKindForName(name: string): ToolKind {
  const normalized = String(name || "").trim().toLowerCase();
  if (!normalized) {
    return "other";
  }
  if (shellToolNames.has(normalized)) return "execute";
  if (readToolNames.has(normalized)) return "read";
  if (editToolNames.has(normalized)) return "edit";
  if (searchToolNames.has(normalized)) return "search";
  if (fetchToolNames.has(normalized)) return "fetch";
  if (normalized === "delete" || normalized === "rm") return "delete";
  if (normalized === "move" || normalized === "mv" || normalized === "rename") return "move";
  if (normalized.startsWith("think") || normalized === "sequentialthinking") return "think";
  return "other";
}

/** Coerce a tool result payload into the string shape `tool_result.output` expects. */
export function toolOutputText(value: unknown): string | undefined {
  if (value === undefined || value === null) {
    return undefined;
  }
  if (typeof value === "string") {
    return value;
  }
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

import { resolveCodexPath } from "../codex-path.js";
import {
  codexThreadStateMetadata,
  codexThreadStartWarning,
  decideCodexThreadResume,
  hashSystemContext,
} from "../codex-thread-resume.js";
import { stringEnv } from "../env.js";
import { warn } from "../mpi.js";
import { uniqueDirectories } from "../paths.js";
import { readStoredThread, writeStoredThread } from "../session-state.js";
import { extractText, jsonString } from "../text.js";
import { appendDelta, TranscriptWriter, type TextWriter } from "../transcript.js";
import type { AgentResult, RunnerOptions } from "../types.js";

interface CodexItemState {
  commandStarted?: boolean;
  commandOutput?: string;
  fileChangeEmitted?: boolean;
  mcpStarted?: boolean;
  mcpResultEmitted?: boolean;
  mcpErrorEmitted?: boolean;
  webSearchEmitted?: boolean;
}

interface TranscriptRecorder extends TextWriter {
  transcript(): string;
}

function webSearchQuery(item: Record<string, unknown>): string {
  if (typeof item.query === "string" && item.query.trim()) {
    return item.query;
  }
  const action = item.action as Record<string, unknown> | undefined;
  if (typeof action?.query === "string" && action.query.trim()) {
    return action.query;
  }
  if (Array.isArray(action?.queries)) {
    return action.queries.filter((entry) => typeof entry === "string" && entry.trim()).join(", ");
  }
  return "";
}

export class CodexRunner {
  private readonly itemState = new Map<string, string | CodexItemState>();

  constructor(
    private readonly options: RunnerOptions,
    private readonly writer: TranscriptRecorder = new TranscriptWriter(),
  ) {}

  transcript(): string {
    return this.writer.transcript();
  }

  threadOptions(): Record<string, unknown> {
    return {
      workingDirectory: this.options.workspace,
      additionalDirectories: uniqueDirectories([this.options.stateRoot, this.options.home, this.options.runtimeRoot]),
      skipGitRepoCheck: true,
      sandboxMode: "danger-full-access",
      approvalPolicy: "never",
      networkAccessEnabled: true,
    };
  }

  emitCommand(item: Record<string, unknown> & { id: string }): void {
    const state = (this.itemState.get(item.id) || {}) as CodexItemState;
    if (!state.commandStarted) {
      this.writer.line(`\n$ ${item.command}`);
      state.commandStarted = true;
      this.itemState.set(item.id, state);
    }
    appendDelta(this.writer, this.itemState as Map<string, string>, `${item.id}:command`, String(item.aggregated_output || ""));
    state.commandOutput = String(item.aggregated_output || "");
    this.itemState.set(item.id, state);
  }

  emitFileChange(item: Record<string, unknown> & { id: string }): void {
    const changes = Array.isArray(item.changes) ? item.changes : [];
    if (changes.length === 0) {
      return;
    }
    const state = (this.itemState.get(item.id) || {}) as CodexItemState;
    if (state.fileChangeEmitted) {
      return;
    }
    this.writer.line("\n[file_change]");
    for (const change of changes) {
      const record = change as Record<string, unknown>;
      this.writer.line(`${record.kind}: ${record.path}`);
    }
    state.fileChangeEmitted = true;
    this.itemState.set(item.id, state);
  }

  emitMcp(item: Record<string, unknown> & { id: string }): void {
    const state = (this.itemState.get(item.id) || {}) as CodexItemState;
    if (!state.mcpStarted) {
      this.writer.line(`\n[mcp:${item.server}/${item.tool}]`);
      if (item.arguments !== undefined) {
        this.writer.line(jsonString(item.arguments));
      }
      state.mcpStarted = true;
    }
    const error = item.error as Record<string, unknown> | undefined;
    if (item.status === "completed" && item.result && !state.mcpResultEmitted) {
      const content = extractText(item.result);
      if (content.trim()) {
        this.writer.line(content);
      }
      state.mcpResultEmitted = true;
    }
    if (item.status === "failed" && typeof error?.message === "string" && !state.mcpErrorEmitted) {
      this.writer.line(error.message);
      state.mcpErrorEmitted = true;
    }
    this.itemState.set(item.id, state);
  }

  emitTodo(item: Record<string, unknown> & { id: string }): void {
    const lines = Array.isArray(item.items)
      ? item.items.map((entry) => {
        const record = entry as Record<string, unknown>;
        return `${record.completed ? "[x]" : "[ ]"} ${record.text}`;
      })
      : [];
    const nextText = lines.length > 0 ? `\n[todo]\n${lines.join("\n")}\n` : "";
    appendDelta(this.writer, this.itemState as Map<string, string>, item.id, nextText);
  }

  emitWebSearch(item: Record<string, unknown> & { id: string }, eventType: unknown): void {
    const state = (this.itemState.get(item.id) || {}) as CodexItemState;
    if (state.webSearchEmitted) {
      return;
    }
    const query = webSearchQuery(item);
    // Match Codex CLI transcript behavior: if the search completes without an
    // exposed query, still emit the marker so the tool use remains visible.
    if (!query && eventType !== "item.completed") {
      return;
    }
    this.writer.line(`\n[web_search] ${query}`);
    state.webSearchEmitted = true;
    this.itemState.set(item.id, state);
  }

  handleEvent(event: Record<string, unknown>, result: AgentResult): void {
    if (event.type === "thread.started") {
      result.threadId = String(event.thread_id || result.threadId);
      return;
    }
    if (event.type === "turn.failed") {
      const error = event.error as Record<string, unknown> | undefined;
      throw new Error(String(error?.message || "codex turn failed"));
    }
    if (!event.item || typeof event.item !== "object") {
      return;
    }
    const item = event.item as Record<string, unknown> & { id: string; type: string };
    switch (item.type) {
      case "agent_message":
        appendDelta(this.writer, this.itemState as Map<string, string>, item.id, String(item.text || ""));
        if (event.type === "item.completed") {
          result.finalText = String(item.text || result.finalText);
        }
        break;
      case "reasoning":
        appendDelta(this.writer, this.itemState as Map<string, string>, item.id, String(item.text || ""));
        break;
      case "command_execution":
        this.emitCommand(item);
        break;
      case "file_change":
        this.emitFileChange(item);
        break;
      case "mcp_tool_call":
        this.emitMcp(item);
        break;
      case "web_search":
        this.emitWebSearch(item, event.type);
        break;
      case "todo_list":
        this.emitTodo(item);
        break;
      case "error":
        this.writer.line(String(item.message || "codex item error"));
        break;
      default:
        break;
    }
  }

  async runPrompt(promptText: string): Promise<AgentResult> {
    const { Codex } = await import("@openai/codex-sdk");
    const stored = await readStoredThread(this.options.stateRoot, "codex");
    const codex = new Codex({
      codexPathOverride: resolveCodexPath(),
      env: stringEnv(),
      // `config` (the `--config key=value` overrides) is a CodexOptions field on the
      // constructor; it is NOT read from ThreadOptions/startThread. The resume decision below
      // prevents a changed override from being combined with stale model-visible instructions.
      ...(this.options.systemContext
        ? { config: { developer_instructions: this.options.systemContext } }
        : {}),
    });
    const systemContextHash = hashSystemContext(this.options.systemContext);
    const resumeDecision = decideCodexThreadResume(stored, systemContextHash);
    const thread = resumeDecision.action === "resume"
      ? codex.resumeThread(resumeDecision.threadId, this.threadOptions())
      : codex.startThread(this.threadOptions());

    if (resumeDecision.action === "start") {
      const warning = codexThreadStartWarning(resumeDecision.reason);
      if (warning) {
        warn(warning);
      }
    }

    const result: AgentResult = {
      provider: "codex",
      threadId: resumeDecision.action === "resume" ? resumeDecision.threadId : "",
      stopReason: "completed",
      finalText: "",
      transcript: "",
      stderr: "",
    };

    const { events } = await thread.runStreamed(
      promptText,
      this.options.outputSchema ? { outputSchema: this.options.outputSchema } : undefined,
    );
    for await (const event of events) {
      this.handleEvent(event as Record<string, unknown>, result);
    }
    result.threadId = thread.id || result.threadId;
    result.transcript = this.writer.transcript();
    if (!result.finalText && result.transcript) {
      result.finalText = result.transcript;
    }
    await writeStoredThread(
      this.options.stateRoot,
      "codex",
      result.threadId,
      new Date(),
      codexThreadStateMetadata(systemContextHash),
    );
    return result;
  }
}

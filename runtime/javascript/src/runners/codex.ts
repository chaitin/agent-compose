import { resolveCodexPath } from "../codex-path.js";
import { stringEnv } from "../env.js";
import { uniqueDirectories } from "../paths.js";
import { readStoredSession, writeStoredSession } from "../session-state.js";
import { extractText } from "../text.js";
import { appendDelta, TranscriptWriter } from "../transcript.js";
import type { AgentResult, RunnerOptions } from "../types.js";

interface CodexItemState {
  commandStarted?: boolean;
  commandOutput?: string;
  fileChangeEmitted?: boolean;
  mcpStarted?: boolean;
  mcpResultEmitted?: boolean;
  mcpErrorEmitted?: boolean;
}

export class CodexRunner {
  private readonly writer = new TranscriptWriter();
  private readonly itemState = new Map<string, string | CodexItemState>();

  constructor(private readonly options: RunnerOptions) {}

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

  handleEvent(event: Record<string, unknown>, result: AgentResult): void {
    if (event.type === "thread.started") {
      result.sessionId = String(event.thread_id || result.sessionId);
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
        if (event.type === "item.started") {
          this.writer.line(`\n[web_search] ${item.query}`);
        }
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
    const stored = await readStoredSession(this.options.stateRoot, "codex");
    const codex = new Codex({
      codexPathOverride: resolveCodexPath(),
      env: stringEnv(),
      // `config` (the `--config key=value` overrides) is a CodexOptions field on the
      // constructor; it is NOT read from ThreadOptions/startThread. Injecting the MPI
      // capability catalog here is what makes it reach codex as developer instructions.
      ...(this.options.mpiContext
        ? { config: { developer_instructions: this.options.mpiContext } }
        : {}),
    });
    const thread = stored?.sessionId
      ? codex.resumeThread(stored.sessionId, this.threadOptions())
      : codex.startThread(this.threadOptions());

    const result: AgentResult = {
      provider: "codex",
      sessionId: stored?.sessionId || "",
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
    result.sessionId = thread.id || result.sessionId;
    result.transcript = this.writer.transcript();
    if (!result.finalText && result.transcript) {
      result.finalText = result.transcript;
    }
    await writeStoredSession(this.options.stateRoot, "codex", result.sessionId);
    return result;
  }
}

import { existsSync } from "node:fs";
import process from "node:process";
import { uniqueDirectories } from "../paths.js";
import { readStoredSession, writeStoredSession } from "../session-state.js";
import { jsonString } from "../text.js";
import { defaultToolDisplayMapper } from "../tool-display.js";
import { TranscriptWriter } from "../transcript.js";
import type { AgentResult, RunnerOptions, StoredSession } from "../types.js";

type PendingToolUse = {
  name: string;
  partialJson: string;
  input?: Record<string, unknown>;
};

function hasOwn(object: object, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(object, key);
}

function contentBlockKey(event: Record<string, unknown>, fallback = ""): string {
  for (const key of ["index", "content_block_index", "contentBlockIndex"]) {
    const value = event[key];
    if (typeof value === "string" || typeof value === "number") {
      return String(value);
    }
  }
  return fallback;
}

function claudeExecutable(): string | undefined {
  const configured = process.env.CLAUDE_CODE_EXECUTABLE || process.env.CLAUDE_CODE_PATH;
  if (configured) {
    return configured;
  }
  return existsSync("/usr/bin/claude") ? "/usr/bin/claude" : undefined;
}

function claudeEnvironment(): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = { ...process.env, IS_SANDBOX: "1" };
  if (!env.ANTHROPIC_API_KEY && env.LLM_API_KEY) {
    env.ANTHROPIC_API_KEY = env.LLM_API_KEY;
  }
  if (!env.ANTHROPIC_BASE_URL && env.LLM_API_ENDPOINT) {
    env.ANTHROPIC_BASE_URL = env.LLM_API_ENDPOINT;
  }
  return env;
}

export class ClaudeRunner {
  // Raw transcript: preserves [tool:Name] + JSON for daemon event parsing.
  // Does NOT stream to stderr — only accumulates.
  private readonly rawWriter = new TranscriptWriter(() => {});

  // Display sink: writes business-friendly text to stderr → cell.Output → UI.
  private readonly display = {
    write(text: string): void {
      process.stderr.write(text);
    },
    line(text = ""): void {
      this.write(text.endsWith("\n") ? text : `${text}\n`);
    },
  };

  private readonly pendingToolUses = new Map<string, PendingToolUse>();
  private readonly mapper = defaultToolDisplayMapper;

  constructor(private readonly options: RunnerOptions) {}

  queryOptions(stored: StoredSession | null): Record<string, unknown> {
    const executable = claudeExecutable();
    return {
      cwd: this.options.workspace,
      env: claudeEnvironment(),
      ...(executable ? { pathToClaudeCodeExecutable: executable } : {}),
      additionalDirectories: uniqueDirectories([this.options.stateRoot, this.options.home, this.options.runtimeRoot]),
      includePartialMessages: true,
      forwardSubagentText: true,
      permissionMode: "bypassPermissions",
      allowDangerouslySkipPermissions: true,
      resume: stored?.sessionId,
      ...(this.options.outputSchema ? {
        outputFormat: {
          type: "json_schema",
          schema: this.options.outputSchema,
        },
      } : {}),
      ...(this.options.systemContext ? {
        systemPrompt: {
          type: "preset",
          preset: "claude_code",
          append: this.options.systemContext,
        },
      } : {}),
    };
  }

  /**
   * Show a tool call on the display channel if the mapper produces a description.
   * Always writes to the raw transcript regardless of display decision.
   */
  private showToolCall(toolName: string, input: Record<string, unknown>, rawText: string): void {
    this.rawWriter.write(rawText);
    const entry = this.mapper.mapToolCall(toolName, input);
    if (entry) {
      this.display.line(`${entry.icon} ${entry.action}...`);
    }
  }

  handleStreamEvent(message: Record<string, unknown>): void {
    const event = message.event as Record<string, unknown> | undefined;
    if (!event || typeof event !== "object") {
      return;
    }

    // ── Tool call start ──────────────────────────────────────────────────
    if (event.type === "content_block_start") {
      const block = event.content_block as Record<string, unknown> | undefined;
      if (typeof block?.name === "string" && block.name) {
        const input = block.input;

        if (input && typeof input === "object" && Object.keys(input).length > 0) {
          this.showToolCall(
            block.name,
            input as Record<string, unknown>,
            `\n[tool:${block.name}]\n${jsonString(input as Record<string, unknown>)}\n`,
          );
          return;
        }

        if (input && typeof input === "object") {
          // Input will be streamed via deltas — stash for now
          this.pendingToolUses.set(contentBlockKey(event, String(block.id ?? this.pendingToolUses.size)), {
            name: block.name,
            partialJson: "",
            input: undefined,
          });

          // Show display entry immediately (with empty input — will update at stop)
          const displayEntry = this.mapper.mapToolCall(block.name, {});
          if (displayEntry) {
            this.display.line(`${displayEntry.icon} ${displayEntry.action}...`);
          }
          return;
        }

        // No input at all
        this.showToolCall(block.name, {}, `\n[tool:${block.name}]\n`);
      }
      return;
    }

    // ── Tool call end (for streamed-input tools) ─────────────────────────
    if (event.type === "content_block_stop") {
      const key = contentBlockKey(event);
      const pending = this.pendingToolUses.get(key);
      if (pending) {
        this.pendingToolUses.delete(key);

        // Raw transcript
        this.rawWriter.line(`\n[tool:${pending.name}]`);
        if (pending.partialJson.trim()) {
          try {
            const parsed = JSON.parse(pending.partialJson);
            this.rawWriter.line(jsonString(parsed));
            // Update input for potential result mapping
            pending.input = parsed;
          } catch {
            this.rawWriter.line(pending.partialJson);
          }
          this.rawWriter.line();
        } else {
          this.rawWriter.line();
        }

        // Display: no duplicate — the action was already shown at start
      }
      return;
    }

    // ── Streaming JSON deltas for tool input ─────────────────────────────
    if (event.type !== "content_block_delta") {
      return;
    }
    const delta = event.delta as Record<string, unknown> | undefined;

    if (delta?.type === "input_json_delta" && typeof delta.partial_json === "string") {
      const pending = this.pendingToolUses.get(contentBlockKey(event));
      if (pending) {
        pending.partialJson += delta.partial_json;
      }
      return;
    }

    // ── Assistant text ───────────────────────────────────────────────────
    if (delta?.type === "text_delta" && typeof delta.text === "string") {
      // Both channels: user-facing assistant text, show as-is
      this.rawWriter.write(delta.text);
      this.display.write(delta.text);
      return;
    }

    // ── Thinking text ────────────────────────────────────────────────────
    if (delta?.type === "thinking_delta" && typeof delta.thinking === "string") {
      // Raw transcript only — internal reasoning is not for end users
      this.rawWriter.write(delta.thinking);
      // Display: skip (users don't need to see internal reasoning)
    }
  }

  async runPrompt(promptText: string): Promise<AgentResult> {
    const { query: claudeQuery } = await import("@anthropic-ai/claude-agent-sdk");
    const stored = await readStoredSession(this.options.stateRoot, "claude");
    const stream = claudeQuery({
      prompt: promptText,
      options: this.queryOptions(stored),
    });

    const result: AgentResult = {
      provider: "claude",
      sessionId: stored?.sessionId || "",
      stopReason: "completed",
      finalText: "",
      transcript: "",
      stderr: "",
    };

    try {
      messages: for await (const rawMessage of stream) {
        const message = rawMessage as Record<string, unknown>;
        result.sessionId = String(message.session_id || result.sessionId);
        switch (message.type) {
          case "stream_event":
            this.handleStreamEvent(message);
            break;
          case "assistant": {
            if (!result.finalText) {
              const assistantMessage = message.message as Record<string, unknown> | undefined;
              const content = assistantMessage?.content;
              const textBlocks = Array.isArray(content)
                ? content
                  .filter((item) => (item as Record<string, unknown>)?.type === "text")
                  .map((item) => String((item as Record<string, unknown>).text || ""))
                  .join("")
                : "";
              if (textBlocks) {
                result.finalText = textBlocks;
              }
            }
            break;
          }
          case "tool_use_summary":
            if (typeof message.summary === "string" && message.summary.trim()) {
              // Raw transcript: include summary
              this.rawWriter.line(`\n${message.summary}`);

              // Display: show result summary if mapper provides one
              const resultText = this.mapper.mapToolResult("", {}, message.summary);
              if (resultText) {
                this.display.line(resultText);
              }
            }
            break;
          case "auth_status":
            if (Array.isArray(message.output) && message.output.length > 0) {
              const authText = message.output.join("\n");
              this.rawWriter.line(authText);
              // Display: auth status is not for end users
            }
            if (message.error) {
              const errText = String(message.error);
              this.rawWriter.line(errText);
              this.display.line(`❌ Auth error: ${errText}`);
            }
            break;
          case "system":
            if (message.subtype === "local_command_output" && typeof message.content === "string") {
              // Raw transcript: include command output for debugging
              this.rawWriter.line(message.content);
              // Display: skip — raw command output is technical detail
            }
            break;
          case "result":
            result.stopReason = String(message.stop_reason || result.stopReason);
            if (message.subtype === "success") {
              result.finalText = hasOwn(message, "structured_output")
                ? JSON.stringify(message.structured_output)
                : String(message.result || result.finalText);
              stream.close?.();
              break messages;
            } else {
              const errors = Array.isArray(message.errors)
                ? message.errors.filter(Boolean).join("; ")
                : "";
              const errorText = typeof message.result === "string" && message.result.trim()
                ? message.result
                : errors || String(message.api_error_status || "claude execution failed");
              throw new Error(errorText);
            }
            break;
          default:
            break;
        }
      }
    } finally {
      stream.close?.();
    }

    // Use raw transcript (preserves [tool:Name] markers for daemon event parsing)
    result.transcript = this.rawWriter.transcript();
    if (!result.finalText && result.transcript) {
      result.finalText = result.transcript;
    }
    await writeStoredSession(this.options.stateRoot, "claude", result.sessionId);
    return result;
  }
}

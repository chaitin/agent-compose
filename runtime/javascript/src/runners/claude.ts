import { existsSync } from "node:fs";
import { uniqueDirectories } from "../paths.js";
import { readStoredSession, writeStoredSession } from "../session-state.js";
import { jsonString } from "../text.js";
import { TranscriptWriter } from "../transcript.js";
import type { AgentResult, RunnerOptions, StoredSession } from "../types.js";

type PendingToolUse = {
  name: string;
  partialJson: string;
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
  private readonly writer = new TranscriptWriter();
  private readonly pendingToolUses = new Map<string, PendingToolUse>();

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
      ...(this.options.mpiContext ? {
        systemPrompt: {
          type: "preset",
          preset: "claude_code",
          append: this.options.mpiContext,
        },
      } : {}),
    };
  }

  handleStreamEvent(message: Record<string, unknown>): void {
    const event = message.event as Record<string, unknown> | undefined;
    if (!event || typeof event !== "object") {
      return;
    }
    if (event.type === "content_block_start") {
      const block = event.content_block as Record<string, unknown> | undefined;
      if (typeof block?.name === "string" && block.name) {
        const input = block.input;
        if (input && typeof input === "object" && Object.keys(input).length > 0) {
          this.writer.line(`\n[tool:${block.name}]`);
          this.writer.line(jsonString(input));
          this.writer.line();
          return;
        }
        if (input && typeof input === "object") {
          this.pendingToolUses.set(contentBlockKey(event, String(block.id ?? this.pendingToolUses.size)), {
            name: block.name,
            partialJson: "",
          });
          return;
        }
        this.writer.line(`\n[tool:${block.name}]`);
        this.writer.line();
      }
      return;
    }
    if (event.type === "content_block_stop") {
      const key = contentBlockKey(event);
      const pending = this.pendingToolUses.get(key);
      if (pending) {
        this.pendingToolUses.delete(key);
        this.writer.line(`\n[tool:${pending.name}]`);
        if (pending.partialJson.trim()) {
          try {
            this.writer.line(jsonString(JSON.parse(pending.partialJson)));
          } catch {
            this.writer.line(pending.partialJson);
          }
          this.writer.line();
        } else {
          this.writer.line();
        }
      }
      return;
    }
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
    if (delta?.type === "text_delta" && typeof delta.text === "string") {
      this.writer.write(delta.text);
      return;
    }
    if (delta?.type === "thinking_delta" && typeof delta.thinking === "string") {
      this.writer.write(delta.thinking);
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
              this.writer.line(`\n${message.summary}`);
            }
            break;
          case "auth_status":
            if (Array.isArray(message.output) && message.output.length > 0) {
              this.writer.line(message.output.join("\n"));
            }
            if (message.error) {
              this.writer.line(String(message.error));
            }
            break;
          case "system":
            if (message.subtype === "local_command_output" && typeof message.content === "string") {
              this.writer.line(message.content);
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

    result.transcript = this.writer.transcript();
    if (!result.finalText && result.transcript) {
      result.finalText = result.transcript;
    }
    await writeStoredSession(this.options.stateRoot, "claude", result.sessionId);
    return result;
  }
}

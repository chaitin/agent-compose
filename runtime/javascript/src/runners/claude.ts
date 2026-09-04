import { existsSync } from "node:fs";
import { flattenEnvMap } from "../mcp-config.js";
import { uniqueDirectories } from "../paths.js";
import { readStoredThread, writeStoredThread } from "../session-state.js";
import { jsonString } from "../text.js";
import { TranscriptWriter, type TranscriptTextWriter } from "../transcript.js";
import type { AgentEvent } from "../agent-event.js";
import { toolKindForName, toolOutputText } from "../agent-event.js";
import type { AgentResult, RunnerOptions, StoredThread } from "../types.js";
import { cancellationRequested } from "../shutdown.js";

type PendingToolUse = {
  id: string;
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

function toClaudeMCPConfig(config: Record<string, unknown> | undefined): Record<string, unknown> | undefined {
	if (!config || typeof config !== "object") {
		return undefined;
	}
	const mapped: Record<string, unknown> = {};
	for (const [name, server] of Object.entries(config)) {
		if (!server || typeof server !== "object") {
			continue;
		}
		const record = server as Record<string, unknown>;
		if (record.type === "local") {
			mapped[name] = {
				type: "stdio",
				command: record.command,
				args: Array.isArray(record.args) ? record.args : [],
				env: flattenEnvMap(record.env as Record<string, { value: string }> | undefined),
			};
			continue;
		}
		if (record.type === "remote") {
			mapped[name] = {
				type: record.transport === "sse" ? "sse" : "http",
				url: record.url,
				headers: flattenEnvMap(record.headers as Record<string, { value: string }> | undefined),
			};
		}
	}
	return Object.keys(mapped).length > 0 ? mapped : undefined;
}

export class ClaudeRunner {
  private readonly pendingToolUses = new Map<string, PendingToolUse>();
  private step = 0;

  private emit(event: AgentEvent): void {
    this.options.onEvent?.(event);
  }

  constructor(
    private readonly options: RunnerOptions,
    private readonly writer: TranscriptTextWriter = new TranscriptWriter(),
  ) {}

  queryOptions(stored: StoredThread | null): Record<string, unknown> {
    const executable = claudeExecutable();
    const mcpServers = toClaudeMCPConfig(this.options.mcpConfig as Record<string, unknown> | undefined);
    return {
      cwd: this.options.workspace,
      env: claudeEnvironment(),
      ...(executable ? { pathToClaudeCodeExecutable: executable } : {}),
      additionalDirectories: uniqueDirectories([this.options.stateRoot, this.options.home, this.options.runtimeRoot]),
      includePartialMessages: true,
      forwardSubagentText: true,
      permissionMode: "bypassPermissions",
      allowDangerouslySkipPermissions: true,
      ...(this.options.model ? { model: this.options.model } : {}),
      ...(this.options.effort ? { effort: this.options.effort } : {}),
      resume: stored?.threadId,
      ...(mcpServers ? {
        mcpServers,
        strictMcpConfig: true,
      } : {}),
      ...(this.options.skills && this.options.skills.length > 0 ? {
        settingSources: ["user"],
        skills: this.options.skills,
      } : {}),
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
      ...(this.options.abortController ? { abortController: this.options.abortController } : {}),
    };
  }

  /**
   * Map one partial-message stream event. Claude does not label model calls, so
   * `message_start` opens a step and `message_stop` closes it. Tool results
   * arrive later as a top-level `user` message (see emitTopLevel), i.e. after
   * the step that requested them has already ended.
   */
  private emitStreamEvent(event: Record<string, unknown>): void {
    const index = typeof event.index === "number" ? event.index : undefined;
    if (event.type === "message_start") {
      this.step += 1;
      this.emit({ kind: "step_start", step: this.step });
      return;
    }
    if (event.type === "content_block_start") {
      const block = event.content_block as Record<string, unknown> | undefined;
      if (block?.type === "tool_use" && typeof block.name === "string") {
        this.emit({
          kind: "tool_call",
          step: this.step,
          id: String(block.id || ""),
          name: block.name,
          toolKind: toolKindForName(block.name),
          status: "in_progress",
        });
      }
      return;
    }
    if (event.type === "content_block_delta") {
      const delta = event.delta as Record<string, unknown> | undefined;
      if (delta?.type === "text_delta" && typeof delta.text === "string") {
        this.emit({ kind: "text_delta", step: this.step, blockIndex: index, text: delta.text });
      } else if (delta?.type === "thinking_delta" && typeof delta.thinking === "string") {
        this.emit({ kind: "reasoning_delta", step: this.step, blockIndex: index, text: delta.thinking });
      }
      return;
    }
    if (event.type === "message_delta") {
      const usage = event.usage as Record<string, unknown> | undefined;
      if (usage) {
        const details = usage.output_tokens_details as Record<string, unknown> | undefined;
        this.emit({
          kind: "usage",
          step: this.step,
          scope: "step",
          // Claude reports input_tokens exclusive of cache already, so no
          // subtraction here (unlike codex/gemini).
          inputTokens: Number(usage.input_tokens ?? 0),
          outputTokens: Number(usage.output_tokens ?? 0),
          reasoningTokens: typeof details?.thinking_tokens === "number" ? details.thinking_tokens : undefined,
          cachedTokens: typeof usage.cache_read_input_tokens === "number" ? usage.cache_read_input_tokens : undefined,
          cacheWriteTokens: typeof usage.cache_creation_input_tokens === "number" ? usage.cache_creation_input_tokens : undefined,
        });
      }
      return;
    }
    if (event.type === "message_stop") {
      this.emit({ kind: "step_end", step: this.step });
    }
  }

  /** Map the top-level SDKMessage types that are not partial-message events. */
  emitTopLevel(message: Record<string, unknown>): void {
    if (message.type === "user") {
      const payload = message.message as Record<string, unknown> | undefined;
      const content = Array.isArray(payload?.content) ? payload.content : [];
      const parentToolUseId = typeof message.parent_tool_use_id === "string" ? message.parent_tool_use_id : undefined;
      for (const entry of content) {
        const block = entry as Record<string, unknown>;
        if (block?.type !== "tool_result") {
          continue;
        }
        const isError = block.is_error === true;
        this.emit({
          kind: "tool_result",
          step: this.step,
          ...(parentToolUseId ? { parentToolUseId } : {}),
          id: String(block.tool_use_id || ""),
          ok: !isError,
          output: toolOutputText(block.content),
          ...(isError ? { error: "tool error" } : {}),
        });
      }
      return;
    }
    if (message.type === "assistant" && typeof message.error === "string") {
      this.emit({
        kind: "error",
        severity: "error",
        code: message.error,
        retryable: ["rate_limit", "overloaded", "server_error"].includes(message.error),
        message: message.error,
      });
      return;
    }
    if (message.type === "system" && message.subtype === "api_retry") {
      this.emit({
        kind: "retry",
        reason: "other",
        attempt: Number(message.attempt ?? 0),
        message: typeof message.error === "string" ? message.error : undefined,
      });
      return;
    }
    if (message.type === "system" && message.subtype === "compact_boundary") {
      this.emit({ kind: "compaction", phase: "end" });
      return;
    }
    if (message.type === "result") {
      const usage = message.usage as Record<string, unknown> | undefined;
      // Emit usage only when the result actually reported it: a record of all
      // zeros is indistinguishable from a genuinely free turn.
      if (usage) {
        const details = usage.output_tokens_details as Record<string, unknown> | undefined;
        const modelUsage = (message.modelUsage || {}) as Record<string, unknown>;
        this.emit({
          kind: "usage",
          scope: "run",
          model: Object.keys(modelUsage)[0],
          inputTokens: Number(usage.input_tokens ?? 0),
          outputTokens: Number(usage.output_tokens ?? 0),
          reasoningTokens: typeof details?.thinking_tokens === "number" ? details.thinking_tokens : undefined,
          cachedTokens: typeof usage.cache_read_input_tokens === "number" ? usage.cache_read_input_tokens : undefined,
          cacheWriteTokens: typeof usage.cache_creation_input_tokens === "number" ? usage.cache_creation_input_tokens : undefined,
          costUsd: typeof message.total_cost_usd === "number" ? message.total_cost_usd : undefined,
        });
      }
      const stopReason = typeof message.stop_reason === "string" ? message.stop_reason : undefined;
      this.emit({
        kind: "step_end",
        stopReason: stopReason === "end_turn" ? "stop" : stopReason === "max_tokens" ? "max_tokens" : undefined,
        rawStopReason: stopReason,
      });
      if (message.subtype !== "success") {
        this.emit({
          kind: "error",
          severity: "fatal",
          message: typeof message.result === "string" && message.result.trim() ? message.result : "claude execution failed",
        });
      }
    }
  }

  handleStreamEvent(message: Record<string, unknown>): void {
    const event = message.event as Record<string, unknown> | undefined;
    if (!event || typeof event !== "object") {
      return;
    }
    this.emitStreamEvent(event);
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
            id: String(block.id ?? ""),
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
        // The tool's arguments only become complete here, so re-emit the call
        // with its parsed input rather than leaving consumers with the stub
        // published at content_block_start.
        let input: unknown;
        try {
          input = pending.partialJson.trim() ? JSON.parse(pending.partialJson) : {};
        } catch {
          input = pending.partialJson;
        }
        this.emit({
          kind: "tool_call",
          step: this.step,
          // pending.id is the tool_use id the matching tool_result will carry;
          // `key` is only the content-block index and would not correlate.
          id: pending.id || key,
          name: pending.name,
          toolKind: toolKindForName(pending.name),
          status: "completed",
          input,
        });
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
    const stored = await readStoredThread(this.options.sessionRoot, "claude");
    const stream = claudeQuery({
      prompt: promptText,
      options: this.queryOptions(stored),
    });

    const result: AgentResult = {
      provider: "claude",
      threadId: stored?.threadId || "",
      stopReason: "completed",
      finalText: "",
      finalTextSource: "none",
      transcript: "",
      stderr: "",
    };

    try {
      messages: for await (const rawMessage of stream) {
        const message = rawMessage as Record<string, unknown>;
        result.threadId = String(message.session_id || result.threadId);
        this.emitTopLevel(message);
        switch (message.type) {
          case "stream_event":
            this.handleStreamEvent(message);
            break;
          case "user":
            // Tool results reach the SDK as a user-role message. Nothing else
            // in the stream carries them, so without this branch the runner
            // sees every tool's input and none of its output.
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
                result.finalTextSource = "provider_message";
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
              const finalText = hasOwn(message, "structured_output")
                ? JSON.stringify(message.structured_output)
                : String(message.result || result.finalText);
              if (finalText) {
                result.finalText = finalText;
                result.finalTextSource = "provider_message";
              }
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
    } catch (error) {
      if (!cancellationRequested(this.options.abortController?.signal)) {
        throw error;
      }
      result.stopReason = "cancelled";
    } finally {
      stream.close?.();
    }

    if (cancellationRequested(this.options.abortController?.signal)) {
      result.stopReason = "cancelled";
    }

    result.transcript = this.writer.transcript();
    if (!result.finalText && result.transcript) {
      result.finalText = result.transcript;
      result.finalTextSource = "transcript_fallback";
    }
    if (result.threadId) {
      await writeStoredThread(this.options.sessionRoot, "claude", result.threadId);
    }
    return result;
  }
}

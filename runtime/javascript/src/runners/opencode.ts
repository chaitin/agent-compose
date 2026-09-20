import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import readline from "node:readline";
import { formatError } from "../errors.js";
import { readStoredThread, writeStoredThread } from "../session-state.js";
import { extractText, jsonString } from "../text.js";
import { TranscriptWriter, type TranscriptTextWriter } from "../transcript.js";
import type { AgentEvent } from "../agent-event.js";
import { toolKindForName, toolOutputText } from "../agent-event.js";
import type { AgentResult, RunnerOptions, StoredThread } from "../types.js";
import { flattenEnvMap } from "../mcp-config.js";
import { cancellationRequested } from "../shutdown.js";
import { waitForChildExit } from "../child-process.js";

export class OpenCodeRunner {
  private skillsConfigDir?: string;
  private step = 0;
  private providerMessageID = "";
  private providerMessageText = "";

  constructor(
    private readonly options: RunnerOptions,
    private readonly writer: TranscriptTextWriter = new TranscriptWriter(),
  ) {}

  async writeMCPConfig(): Promise<void> {
    const mcps = this.options.mcpConfig as Record<string, Record<string, unknown>> | undefined;
    if (!mcps || Object.keys(mcps).length === 0) {
      return;
    }
    const configPath = process.env.OPENCODE_CONFIG || path.join(this.options.home, ".config", "opencode", "opencode.json");
    await fs.mkdir(path.dirname(configPath), { recursive: true });
    let config: Record<string, unknown> = {};
    try {
      config = JSON.parse(await fs.readFile(configPath, "utf-8"));
    } catch {
      config = {};
    }
    const mcp: Record<string, unknown> = {};
    for (const [name, server] of Object.entries(mcps)) {
      if (server.type === "local") {
        mcp[name] = {
          type: "local",
          command: [server.command, ...(Array.isArray(server.args) ? server.args : [])],
          environment: flattenEnvMap(server.env as Record<string, { value: string }> | undefined),
        };
      } else if (server.type === "remote") {
        mcp[name] = {
          type: "remote",
          url: server.url,
          headers: flattenEnvMap(server.headers as Record<string, { value: string }> | undefined),
        };
      }
    }
    config.mcp = mcp;
    await fs.writeFile(configPath, JSON.stringify(config, null, 2) + "\n", "utf-8");
  }

  buildArgs(promptText: string, stored: StoredThread | null): string[] {
    const userPrompt = this.options.systemContext
      ? `${this.options.systemContext}\n\n${promptText}`
      : promptText;
    const args = [
      "run",
      userPrompt,
      "--format", "json",
      "--dir", this.options.workspace,
      "--dangerously-skip-permissions",
    ];
    const model = String(this.options.model || "").trim();
    if (model) {
      args.push("--model", model);
    }
    if (stored?.threadId) {
      args.push("--session", stored.threadId);
    }
    return args;
  }

  async environment(): Promise<NodeJS.ProcessEnv> {
    const env: NodeJS.ProcessEnv = {
      ...process.env,
      OPENCODE_DISABLE_AUTOUPDATE: process.env.OPENCODE_DISABLE_AUTOUPDATE || "true",
      OPENCODE_DISABLE_MODELS_FETCH: process.env.OPENCODE_DISABLE_MODELS_FETCH || "1",
    };
    if (this.options.skills && this.options.skills.length > 0) {
      const configPath = await this.writeSkillsConfig(this.baseConfigPath(process.env.OPENCODE_CONFIG));
      env.OPENCODE_CONFIG = configPath;
      env.AGENT_COMPOSE_OPENCODE_CONFIG = configPath;
    }
    return env;
  }

  baseConfigPath(configPath?: string): string {
    const trimmed = String(configPath || "").trim();
    return trimmed || path.join(this.options.home, ".config", "opencode", "opencode.json");
  }

  async writeSkillsConfig(baseConfigPath?: string): Promise<string> {
    await this.cleanupSkillsConfig();
    const dir = await fs.mkdtemp(path.join(os.tmpdir(), "agent-compose-opencode-"));
    this.skillsConfigDir = dir;
    const configPath = path.join(dir, "opencode.json");
    const skillsRoot = path.join(this.options.home, ".agents", "skills");
    const config = await readOpenCodeConfig(baseConfigPath);
    const existingSkills = isRecord(config.skills) ? config.skills : {};
    const existingPaths = Array.isArray(existingSkills.paths)
      ? existingSkills.paths.filter((value): value is string => typeof value === "string" && value.trim() !== "")
      : [];
    config.skills = {
      ...existingSkills,
      paths: uniqueStrings([...existingPaths, skillsRoot]),
    };
    await fs.writeFile(configPath, JSON.stringify(config, null, 2) + "\n", "utf8");
    return configPath;
  }

  async cleanupSkillsConfig(): Promise<void> {
    const dir = this.skillsConfigDir;
    this.skillsConfigDir = undefined;
    if (!dir) {
      return;
    }
    try {
      await fs.rm(dir, { recursive: true, force: true });
    } catch (error) {
      this.writer.line(`[opencode cleanup] ${formatError(error)}`);
    }
  }

  private emit(event: AgentEvent): void {
    this.options.onEvent?.(event);
  }

  /**
   * Map one `opencode run --format json` event. The CLI emits only six types
   * (`step_start` / `step_finish` / `text` / `reasoning` / `tool_use` / `error`),
   * all wrapped as `{type, timestamp, sessionID, part}`; the other branches
   * below are kept for older CLI shapes the transcript path still tolerates.
   */
  private emitNeutral(event: Record<string, unknown>): void {
    const type = String(event.type || event.event || "");
    const part = isRecord(event.part) ? event.part : {};
    switch (type) {
      case "step_start":
        this.step += 1;
        this.emit({ kind: "step_start", step: this.step });
        return;
      case "step_finish": {
        const tokens = isRecord(part.tokens) ? part.tokens as Record<string, unknown> : undefined;
        const cache = isRecord(tokens?.cache) ? tokens.cache as Record<string, unknown> : {};
        if (tokens) {
          this.emit({
            kind: "usage",
            step: this.step,
            scope: "step",
            inputTokens: Number(tokens.input ?? 0),
            outputTokens: Number(tokens.output ?? 0),
            reasoningTokens: numberOrUndefined(tokens.reasoning),
            cachedTokens: numberOrUndefined(cache.read),
            cacheWriteTokens: numberOrUndefined(cache.write),
            costUsd: numberOrUndefined(part.cost),
          });
        }
        this.emit({ kind: "step_end", step: this.step, rawStopReason: stringField(part, "reason") || undefined });
        return;
      }
      case "text":
        // opencode publishes a text part only once it is complete, so this is a
        // whole block rather than a token-level delta.
        if (typeof part.text === "string" && part.text) {
          this.emit({ kind: "text_delta", step: this.step, text: part.text });
        }
        return;
      case "reasoning":
        if (typeof part.text === "string" && part.text) {
          this.emit({ kind: "reasoning_delta", step: this.step, text: part.text });
        }
        return;
      case "tool_use": {
        const state = isRecord(part.state) ? part.state as Record<string, unknown> : {};
        const status = String(state.status || "");
        const name = String(part.tool || "tool");
        const id = String(part.id || name);
        this.emit({
          kind: "tool_call",
          step: this.step,
          id,
          name,
          toolKind: toolKindForName(name),
          status: status === "completed" ? "completed" : status === "error" ? "failed" : "in_progress",
          input: state.input,
        });
        if (status === "completed" || status === "error") {
          this.emit({
            kind: "tool_result",
            step: this.step,
            id,
            ok: status === "completed",
            output: toolOutputText(state.output),
            ...(status === "error" ? { error: toolOutputText(state.error) || "tool error" } : {}),
          });
        }
        return;
      }
      case "error": {
        const error = isRecord(event.error) ? event.error as Record<string, unknown> : {};
        const data = isRecord(error.data) ? error.data as Record<string, unknown> : {};
        this.emit({
          kind: "error",
          severity: "fatal",
          code: stringField(error, "name") || undefined,
          message: stringField(data, "message") || stringField(error, "name") || "opencode error",
        });
        return;
      }
      default:
        return;
    }
  }

  handleEvent(event: Record<string, unknown>, result: AgentResult): void {
    this.emitNeutral(event);
    const providerThreadID = stringField(event, "sessionID", "sessionId", "session_id");
    if (providerThreadID) {
      result.threadId = providerThreadID;
    }

    const eventType = String(event.type || event.event || "");
    if (eventType === "error") {
      const errorText = extractText(event.error) || extractText(event.message) || jsonString(event);
      this.writer.line(errorText);
      throw new Error(errorText);
    }

    if (eventType === "tool_use" || eventType === "tool") {
      const tool = event.tool as Record<string, unknown> | undefined;
      const toolName = stringField(event, "name", "toolName") || String(tool?.name || "tool");
      this.writer.line(`\n[tool:${toolName}]`);
      return;
    }

    if (eventType === "tool_result") {
      const text = extractText(event.result) || extractText(event.content) || jsonString(event.result || event);
      if (text.trim()) {
        this.writer.line(text);
      }
      return;
    }

    const text = extractText(event.message) ||
      extractText(event.content) ||
      extractText(event.part) ||
      extractText(event.text) ||
      extractText(event.delta);
    if (text) {
      this.writer.write(text);
    }

    if (eventType === "text" && text) {
      const part = isRecord(event.part) ? event.part : {};
      const messageID = stringField(part, "messageID", "messageId", "message_id") ||
        stringField(event, "messageID", "messageId", "message_id");
      if (messageID && messageID !== this.providerMessageID) {
        this.providerMessageID = messageID;
        this.providerMessageText = "";
      }
      this.providerMessageText += text;
    }

    if (eventType === "step_finish" || eventType === "step-finish") {
      const part = isRecord(event.part) ? event.part : {};
      const stopReason = stringField(part, "reason", "stopReason", "stop_reason");
      result.stopReason = stopReason || result.stopReason;
      if (this.providerMessageText && stopReason !== "tool-calls" && stopReason !== "tool_calls") {
        result.finalText = this.providerMessageText;
        result.finalTextSource = "provider_message";
      }
      this.providerMessageID = "";
      this.providerMessageText = "";
    }

    if (eventType === "result" || eventType === "complete" || eventType === "completed") {
      const finalText = extractText(event.response) || extractText(event.result) || text;
      if (finalText) {
        result.finalText = finalText;
        result.finalTextSource = "provider_message";
      }
      result.stopReason = stringField(event, "stopReason", "stop_reason", "finishReason", "finish_reason") || result.stopReason;
    }
  }

  async runPrompt(promptText: string): Promise<AgentResult> {
    await this.writeMCPConfig();
    if (this.options.outputSchema) {
      throw new Error("structured JSON output is not supported by opencode runner");
    }
    this.providerMessageID = "";
    this.providerMessageText = "";

    const stored = await readStoredThread(this.options.sessionRoot, "opencode");
    const result: AgentResult = {
      provider: "opencode",
      threadId: stored?.threadId || "",
      stopReason: "completed",
      finalText: "",
      finalTextSource: "none",
      transcript: "",
      stderr: "",
    };

    try {
      const child = spawn("opencode", this.buildArgs(promptText, stored), {
        cwd: this.options.workspace,
        env: await this.environment(),
        stdio: ["ignore", "pipe", "pipe"],
        signal: this.options.abortController?.signal,
      });
      const exit = waitForChildExit(child, this.options.abortController?.signal, "exit");

      const stderrChunks: string[] = [];
      child.stderr?.on("data", (chunk) => {
        const text = String(chunk || "");
        stderrChunks.push(text);
        this.writer.write(text);
      });

      const rl = readline.createInterface({ input: child.stdout, crlfDelay: Infinity });
      try {
        for await (const line of rl) {
          if (!line.trim()) {
            continue;
          }
          let event: Record<string, unknown>;
          try {
            event = JSON.parse(line) as Record<string, unknown>;
          } catch {
            this.writer.line(line);
            continue;
          }
          this.handleEvent(event, result);
        }
      } catch (error) {
        if (!cancellationRequested(this.options.abortController?.signal)) {
          child.kill("SIGTERM");
          throw error;
        }
      }

      const processResult = await exit;
      const cancelled = cancellationRequested(this.options.abortController?.signal);
      if (processResult.spawnError && !cancelled) {
        throw processResult.spawnError;
      }
      if (processResult.exitCode !== 0 && !cancelled) {
        throw new Error(`opencode exited with code ${processResult.exitCode}: ${stderrChunks.join("")}`);
      }
      if (cancelled) {
        result.stopReason = "cancelled";
      }
    } finally {
      await this.cleanupSkillsConfig();
    }

    result.transcript = this.writer.transcript();
    if (!result.finalText && result.transcript) {
      result.finalText = result.transcript;
      result.finalTextSource = "transcript_fallback";
    }
    if (result.threadId) {
      await writeStoredThread(this.options.sessionRoot, "opencode", result.threadId);
    }
    return result;
  }
}

function stringField(record: Record<string, unknown>, ...keys: string[]): string {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "string" && value.trim()) {
      return value.trim();
    }
  }
  return "";
}

async function readOpenCodeConfig(configPath?: string): Promise<Record<string, unknown>> {
  const trimmed = String(configPath || "").trim();
  if (!trimmed) {
    return {};
  }
  try {
    const content = await fs.readFile(trimmed, "utf8");
    const parsed = JSON.parse(content) as unknown;
    return isRecord(parsed) ? parsed : {};
  } catch (error) {
    const cause = error as NodeJS.ErrnoException;
    if (cause.code === "ENOENT") {
      return {};
    }
    throw error;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function numberOrUndefined(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function uniqueStrings(values: string[]): string[] {
  return Array.from(new Set(values));
}

import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import readline from "node:readline";
import { extractText, jsonString } from "../text.js";
import { readStoredThread, writeStoredThread } from "../session-state.js";
import { TranscriptWriter, type TranscriptTextWriter } from "../transcript.js";
import type { AgentResult, RunnerOptions } from "../types.js";
import { piMCPAdapterExtension, writePiMCPConfig } from "./pi-mcp.js";
import { cancellationRequested } from "../shutdown.js";
import { waitForChildExit } from "../child-process.js";

const maxDiagnosticBytes = 64 * 1024;

export class PiRunner {
  private reportedError: Error | null = null;
  private latestAssistantError: Error | null = null;

  constructor(
    private readonly options: RunnerOptions,
    private readonly writer: TranscriptTextWriter = new TranscriptWriter(),
  ) {}

  async runPrompt(promptText: string): Promise<AgentResult> {
    this.reportedError = null;
    this.latestAssistantError = null;
    if (this.options.outputSchema) {
      throw new Error("structured JSON output is not supported by pi runner");
    }
    const stored = await readStoredThread(this.options.sessionRoot, "pi");
    const sessionDir = path.join(this.options.sessionRoot, "agents", "providers", "pi", "sessions");
    const tempRoot = path.join(this.options.sessionRoot, "agents", "providers", "pi", "tmp");
    await fs.mkdir(sessionDir, { recursive: true });
    await fs.mkdir(tempRoot, { recursive: true });
    const invocationDir = await fs.mkdtemp(path.join(tempRoot, "prompt-"));
    const result: AgentResult = {
      provider: "pi",
      threadId: "",
      stopReason: "completed",
      finalText: "",
      finalTextSource: "none",
      transcript: "",
      stderr: "",
    };

    try {
      // Let Pi create the first session so its CLI follows the native creation
      // path. Supplying a new --session-id is treated as a resume miss and
      // intentionally produces a warning; only pass IDs Pi already emitted.
      const args = await this.buildArgs(promptText, stored?.threadId, sessionDir, invocationDir);
      const child = spawn("pi", args, {
        cwd: this.options.workspace,
        env: {
          ...process.env,
          HOME: this.options.home,
          PI_CODING_AGENT_DIR: process.env.PI_CODING_AGENT_DIR || path.join(this.options.home, ".pi", "agent"),
          PI_OFFLINE: "1",
          PI_SKIP_VERSION_CHECK: "1",
          PI_TELEMETRY: "0",
        },
        stdio: ["ignore", "pipe", "pipe"],
        signal: this.options.abortController?.signal,
      });
      // Attach the process error handler immediately. A failed spawn may emit
      // before stdout iteration completes, and an unhandled child "error"
      // event would otherwise terminate the runtime process.
      const exit = waitForChildExit(child, this.options.abortController?.signal);

      let stderrBytes: Buffer = Buffer.alloc(0);
      child.stderr?.on("data", (chunk) => {
        const text = String(chunk || "");
        stderrBytes = appendBounded(stderrBytes, Buffer.from(text), maxDiagnosticBytes);
        this.writer.write(text);
      });

      let protocolError: Error | null = null;
      const lines = readline.createInterface({ input: child.stdout, crlfDelay: Infinity });
      for await (const line of lines) {
        if (!line.trim()) continue;
        let event: Record<string, unknown>;
        try {
          const parsed: unknown = JSON.parse(line);
          if (!isRecord(parsed)) throw new Error("event is not an object");
          event = parsed;
        } catch (error) {
          protocolError ??= new Error(`pi emitted invalid JSON event: ${truncate(line, 4096)}`, { cause: error });
          continue;
        }
        this.handleEvent(event, result);
      }

      const processResult = await exit;
      const stderr = stderrBytes.toString("utf8");
      result.stderr = stderr;
      const cancelled = cancellationRequested(this.options.abortController?.signal);
      if (processResult.spawnError && !cancelled) throw processResult.spawnError;
      if (protocolError && !cancelled) throw protocolError;
      if (this.reportedError && !cancelled) throw this.reportedError;
      if (processResult.exitCode !== 0 && !cancelled) {
        throw new Error(`pi exited with code ${processResult.exitCode}${stderr ? `: ${stderr}` : ""}`);
      }
      if (!result.threadId && !cancelled) {
        throw new Error("pi completed without emitting a session id");
      }
      if (cancelled) {
        result.stopReason = "cancelled";
      }
      result.transcript = this.writer.transcript();
      if (!result.finalText && result.transcript) {
        result.finalText = lastAssistantTextFromTranscript(result.transcript);
        result.finalTextSource = "transcript_fallback";
      }
      if (result.threadId) {
        await writeStoredThread(this.options.sessionRoot, "pi", result.threadId);
      }
      return result;
    } finally {
      await fs.rm(invocationDir, { recursive: true, force: true });
    }
  }

  async buildArgs(promptText: string, sessionID: string | undefined, sessionDir: string, invocationDir: string): Promise<string[]> {
    const args = [
      "--mode", "json",
      "--session-dir", sessionDir,
      "--no-extensions",
      "--no-skills",
      "--no-prompt-templates",
      "--no-themes",
      "--no-context-files",
      "--no-approve",
      "--offline",
    ];
    if (sessionID) args.push("--session-id", sessionID);
    if (this.options.model?.trim()) {
      args.push("--model", piFacadeModel(this.options.model));
    }
    if (this.options.systemContext) {
      const systemPath = path.join(invocationDir, "system-context.md");
      await fs.writeFile(systemPath, this.options.systemContext, { encoding: "utf8", mode: 0o600 });
      args.push("--append-system-prompt", systemPath);
    }
    for (const skillPath of await this.resolveSkillPaths()) args.push("--skill", skillPath);
    const mcpConfigPath = await writePiMCPConfig(invocationDir, this.options.mcpConfig);
    if (mcpConfigPath) {
      args.push("--extension", piMCPAdapterExtension, "--mcp-config", mcpConfigPath);
    }
    args.push(promptText);
    return args;
  }

  handleEvent(event: Record<string, unknown>, result: AgentResult): void {
    const type = String(event.type || "");
    if (type === "session") {
      result.threadId = firstString(event, "id", "sessionId", "session_id") || result.threadId;
      return;
    }
    if (type === "message_update") {
      const update = recordValue(event.assistantMessageEvent) || recordValue(event.assistant_message_event);
      if (String(update?.type || "") === "text_delta") {
        this.writer.write(firstString(update, "delta", "text"));
      }
      return;
    }
    if (type === "message_end") {
      const message = recordValue(event.message);
      if (String(message?.role || event.role || "") === "assistant") {
        const stopReason = firstString(message, "stopReason", "stop_reason")
          || firstString(event, "stopReason", "stop_reason");
        if (stopReason === "error") {
          const detail = firstString(message, "errorMessage", "error_message")
            || firstString(event, "errorMessage", "error_message")
            || "unknown Pi model error";
          this.latestAssistantError = new Error(`pi model request failed: ${detail}`);
        } else {
          this.latestAssistantError = null;
        }
        const finalText = extractText(message?.content) || extractText(event.content);
        if (finalText) {
          result.finalText = finalText;
          result.finalTextSource = "provider_message";
        }
      }
      return;
    }
    if (type.startsWith("tool_execution_")) {
      return;
    }
    if (type === "agent_end") {
      result.stopReason = firstString(event, "stopReason", "stop_reason") || "completed";
      if (!result.finalText) {
        const finalText = lastAssistantMessage(event.messages);
        if (finalText) {
          result.finalText = finalText;
          result.finalTextSource = "provider_message";
        }
      }
      this.reportedError ??= this.latestAssistantError
        || (result.stopReason === "error" ? new Error("pi agent ended with an error") : null);
      return;
    }
    if (type.startsWith("auto_retry_") || type.startsWith("compaction_")) {
      this.writer.line(`\n[pi:${type}]`);
      return;
    }
    if (type === "error") {
      const message = extractText(event.error) || extractText(event.message) || jsonString(event);
      this.reportedError ??= new Error(`pi reported an error: ${message}`);
    }
  }

  private async resolveSkillPaths(): Promise<string[]> {
    if (!this.options.skills?.length) return [];
    const root = path.join(this.options.home, ".agents", "skills");
    const realRoot = await fs.realpath(root);
    const resolved: string[] = [];
    for (const name of this.options.skills) {
      if (!name || path.isAbsolute(name) || name.includes("/") || name.includes("\\")) {
        throw new Error(`invalid pi skill name ${JSON.stringify(name)}`);
      }
      const skill = await fs.realpath(path.join(realRoot, name, "SKILL.md"));
      if (!isWithin(realRoot, skill)) throw new Error(`pi skill ${JSON.stringify(name)} escapes the skills directory`);
      resolved.push(skill);
    }
    return resolved;
  }
}

function piFacadeModel(model: string): string {
  const normalized = model.trim();
  if (normalized.startsWith("agent-compose/")) return normalized;
  const separator = normalized.indexOf("/");
  return `agent-compose/${separator >= 0 ? normalized.slice(separator + 1) : normalized}`;
}

function appendBounded(current: Buffer, next: Buffer, limit: number): Buffer {
  if (current.length >= limit) return current;
  return Buffer.concat([current, next.subarray(0, limit - current.length)]);
}

function truncate(value: string, limit: number): string {
  return value.length <= limit ? value : `${value.slice(0, limit)}...[truncated]`;
}

function isWithin(root: string, target: string): boolean {
  const relative = path.relative(root, target);
  return relative !== ".." && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative);
}

function firstString(value: Record<string, unknown> | undefined, ...keys: string[]): string {
  for (const key of keys) if (typeof value?.[key] === "string") return String(value[key]);
  return "";
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return isRecord(value) ? value : undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function lastAssistantMessage(value: unknown): string {
  if (!Array.isArray(value)) return "";
  for (let index = value.length - 1; index >= 0; index--) {
    const message = recordValue(value[index]);
    if (message?.role === "assistant") return extractText(message.content);
  }
  return "";
}

function lastAssistantTextFromTranscript(transcript: string): string {
	return transcript.trim();
}

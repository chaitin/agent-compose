import { spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import readline from "node:readline";
import { extractText, jsonString } from "../text.js";
import { readStoredThread, writeStoredThread } from "../session-state.js";
import { TranscriptWriter, type TranscriptTextWriter } from "../transcript.js";
import type { AgentResult, RunnerOptions } from "../types.js";

const maxDiagnosticBytes = 64 * 1024;
const maxToolResultBytes = 16 * 1024;

export class PiRunner {
  private reportedError: Error | null = null;

  constructor(
    private readonly options: RunnerOptions,
    private readonly writer: TranscriptTextWriter = new TranscriptWriter(),
  ) {}

  async runPrompt(promptText: string): Promise<AgentResult> {
    this.reportedError = null;
    if (this.options.outputSchema) {
      throw new Error("structured JSON output is not supported by pi runner");
    }
    if (this.options.mcpConfig && Object.keys(this.options.mcpConfig).length > 0) {
      throw new Error("pi provider does not support configured MCP servers in this build");
    }

    const stored = await readStoredThread(this.options.stateRoot, "pi");
    const requestedSessionID = stored?.threadId || createHash("sha256").update(randomBytes(32)).digest("hex");
    const sessionDir = path.join(this.options.stateRoot, "agents", "providers", "pi", "sessions");
    const tempRoot = path.join(this.options.stateRoot, "agents", "providers", "pi", "tmp");
    await fs.mkdir(sessionDir, { recursive: true });
    await fs.mkdir(tempRoot, { recursive: true });
    const invocationDir = await fs.mkdtemp(path.join(tempRoot, "prompt-"));

    try {
      await ensureManagedPiModel(this.options.home, this.options.model);
      const args = await this.buildArgs(promptText, requestedSessionID, sessionDir, invocationDir);
      const child = spawn("pi", args, {
        cwd: this.options.workspace,
        env: {
          ...process.env,
          HOME: this.options.home,
          PI_CODING_AGENT_DIR: path.join(this.options.home, ".pi", "agent"),
          PI_OFFLINE: "1",
          PI_SKIP_VERSION_CHECK: "1",
          PI_TELEMETRY: "0",
        },
        stdio: ["ignore", "pipe", "pipe"],
      });
      // Attach the process error handler immediately. A failed spawn may emit
      // before stdout iteration completes, and an unhandled child "error"
      // event would otherwise terminate the runtime process.
      const exit = waitForExit(child);

      let stderrBytes: Buffer = Buffer.alloc(0);
      child.stderr?.on("data", (chunk) => {
        const text = String(chunk || "");
        stderrBytes = appendBounded(stderrBytes, Buffer.from(text), maxDiagnosticBytes);
        this.writer.write(text);
      });

      const result: AgentResult = {
        provider: "pi",
        threadId: "",
        stopReason: "completed",
        finalText: "",
        transcript: "",
        stderr: "",
      };
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
      if (processResult.spawnError) throw processResult.spawnError;
      if (protocolError) throw protocolError;
      if (this.reportedError) throw this.reportedError;
      if (processResult.exitCode !== 0) {
        throw new Error(`pi exited with code ${processResult.exitCode}${stderr ? `: ${stderr}` : ""}`);
      }
      if (!result.threadId) {
        throw new Error("pi completed without emitting a session id");
      }
      result.transcript = this.writer.transcript();
      if (!result.finalText) result.finalText = lastAssistantTextFromTranscript(result.transcript);
      await writeStoredThread(this.options.stateRoot, "pi", result.threadId);
      return result;
    } finally {
      await fs.rm(invocationDir, { recursive: true, force: true });
    }
  }

  async buildArgs(promptText: string, sessionID: string, sessionDir: string, invocationDir: string): Promise<string[]> {
    const args = [
      "--mode", "json",
      "--session-dir", sessionDir,
      "--session-id", sessionID,
      "--no-extensions",
      "--no-skills",
      "--no-prompt-templates",
      "--no-themes",
      "--no-context-files",
      "--no-approve",
      "--offline",
    ];
    if (this.options.model?.trim()) {
      args.push("--model", piFacadeModel(this.options.model));
    }
    if (this.options.systemContext) {
      const systemPath = path.join(invocationDir, "system-context.md");
      await fs.writeFile(systemPath, this.options.systemContext, { encoding: "utf8", mode: 0o600 });
      args.push("--append-system-prompt", systemPath);
    }
    for (const skillPath of await this.resolveSkillPaths()) args.push("--skill", skillPath);
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
        result.finalText = extractText(message?.content) || extractText(event.content) || result.finalText;
      }
      return;
    }
    if (type === "tool_execution_start") {
      this.writer.line(`\n[tool:${firstString(event, "toolName", "tool_name", "name") || "tool"}]`);
      return;
    }
    if (type === "tool_execution_end") {
      const output = extractText(event.result) || extractText(event.content) || jsonString(event.result);
      if (output.trim()) this.writer.line(truncateUTF8(output, maxToolResultBytes));
      return;
    }
    if (type === "agent_end") {
      result.stopReason = firstString(event, "stopReason", "stop_reason") || "completed";
      result.finalText ||= lastAssistantMessage(event.messages);
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

async function ensureManagedPiModel(home: string, model = ""): Promise<void> {
  if (!model.trim()) return;
  const facadeModel = piFacadeModel(model);
  const separator = facadeModel.indexOf("/");
  const modelID = separator >= 0 ? facadeModel.slice(separator + 1) : facadeModel;
  const baseUrl = process.env.LLM_API_ENDPOINT?.trim();
  const protocol = process.env.LLM_API_PROTOCOL?.trim();
  if (!modelID || !baseUrl || !protocol) {
    throw new Error("Pi facade configuration is incomplete; LLM_API_ENDPOINT and LLM_API_PROTOCOL are required");
  }

  const api = protocol === "messages"
    ? "anthropic-messages"
    : protocol === "chat_completions"
      ? "openai-completions"
      : protocol === "responses"
        ? "openai-responses"
        : "";
  if (!api) throw new Error(`unsupported Pi facade protocol ${JSON.stringify(protocol)}`);

  const agentDir = path.join(home, ".pi", "agent");
  await fs.mkdir(agentDir, { recursive: true, mode: 0o700 });
  const payload = {
    providers: {
      "agent-compose": {
        baseUrl: baseUrl.replace(/\/$/, ""),
        apiKey: "$AGENT_COMPOSE_SANDBOX_TOKEN",
        api,
        models: [{ id: modelID, name: modelID, contextWindow: 128000, maxTokens: 16384 }],
      },
    },
  };
  const target = path.join(agentDir, "models.json");
  const temporary = path.join(agentDir, `.models-${process.pid}-${randomBytes(8).toString("hex")}.json`);
  await fs.writeFile(temporary, `${JSON.stringify(payload, null, 2)}\n`, { encoding: "utf8", mode: 0o600 });
  try {
    await fs.rename(temporary, target);
  } catch (error) {
    await fs.rm(temporary, { force: true });
    throw error;
  }
}

function piFacadeModel(model: string): string {
  const normalized = model.trim();
  if (normalized.startsWith("agent-compose/")) return normalized;
  const separator = normalized.indexOf("/");
  return `agent-compose/${separator >= 0 ? normalized.slice(separator + 1) : normalized}`;
}

function waitForExit(child: ReturnType<typeof spawn>): Promise<{ exitCode: number; spawnError?: Error }> {
  return new Promise((resolve) => {
    child.once("error", (error) => resolve({ exitCode: 1, spawnError: new Error("failed to start pi", { cause: error }) }));
    child.once("exit", (code) => resolve({ exitCode: code ?? 1 }));
  });
}

function appendBounded(current: Buffer, next: Buffer, limit: number): Buffer {
  if (current.length >= limit) return current;
  return Buffer.concat([current, next.subarray(0, limit - current.length)]);
}

function truncate(value: string, limit: number): string {
  return value.length <= limit ? value : `${value.slice(0, limit)}...[truncated]`;
}

function truncateUTF8(value: string, limit: number): string {
  const bytes = Buffer.from(value);
  return bytes.length <= limit ? value : `${bytes.subarray(0, limit).toString("utf8")}...[truncated]`;
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

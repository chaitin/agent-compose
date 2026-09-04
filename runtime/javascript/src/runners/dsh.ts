import { createHash, randomUUID } from "node:crypto";
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import readline from "node:readline";
import { extractText } from "../text.js";
import { flattenEnvMap, type RuntimeMCPServer } from "../mcp-config.js";
import { readStoredThread, writeStoredThread } from "../session-state.js";
import { TranscriptWriter, type TranscriptTextWriter } from "../transcript.js";
import type { AgentEvent } from "../agent-event.js";
import { toolKindForName } from "../agent-event.js";
import type { AgentResult, RunnerOptions } from "../types.js";
import { cancellationRequested } from "../shutdown.js";
import { waitForChildExit } from "../child-process.js";

const maxDiagnosticBytes = 64 * 1024;

// Linux caps a single argv/envp string at MAX_ARG_STRLEN (PAGE_SIZE * 32,
// conventionally 128 KiB); exceeding it fails spawn() with E2BIG. DSH_MCP_SERVERS
// is still passed as a single env var (unlike DSH_SYSTEM_CONTEXT, which now
// goes through a temp file — see runPrompt), so check proactively and fail
// with an actionable message instead of letting the OS reject exec() opaquely.
const maxExecEnvValueBytes = 128 * 1024;

function assertEnvValueWithinExecLimit(name: string, value: string): void {
  const byteLength = Buffer.byteLength(`${name}=${value}`, "utf8");
  if (byteLength > maxExecEnvValueBytes) {
    throw new Error(
      `dsh runner: ${name} is ${byteLength} bytes, exceeding the ${maxExecEnvValueBytes}-byte exec() argument limit (Linux MAX_ARG_STRLEN); reduce its size before running`,
    );
  }
}

export class DshRunner {
  private reportedError: Error | null = null;

  constructor(
    private readonly options: RunnerOptions,
    private readonly writer: TranscriptTextWriter = new TranscriptWriter(),
  ) {}

  async runPrompt(promptText: string): Promise<AgentResult> {
    this.reportedError = null;
    if (this.options.outputSchema) {
      throw new Error("structured JSON output is not supported by dsh runner");
    }
    const mcpServers = toDshMcpServers(this.options.mcpConfig as Record<string, RuntimeMCPServer> | undefined);

    const stored = await readStoredThread(this.options.sessionRoot, "dsh");
    const providerRoot = path.join(this.options.sessionRoot, "agents", "providers", "dsh");
    const sessionRoot = path.join(providerRoot, "sessions");
    const tempRoot = path.join(providerRoot, "tmp");
    await fs.mkdir(sessionRoot, { recursive: true });
    await fs.mkdir(tempRoot, { recursive: true });
    const invocationDir = await fs.mkdtemp(path.join(tempRoot, "prompt-"));

    const resume = Boolean(stored?.threadId);
    const sessionId = stored?.threadId || `session-${randomUUID()}`;

    const result: AgentResult = {
      provider: "dsh",
      threadId: sessionId,
      stopReason: "completed",
      finalText: "",
      finalTextSource: "none",
      transcript: "",
      stderr: "",
    };

    try {
      const promptFile = path.join(invocationDir, "prompt.txt");
      await fs.writeFile(promptFile, promptText, { encoding: "utf8", mode: 0o600 });

      // Not an env var: runner.js is real ESM (unlike cordis.patch.yml's
      // `!!js` eval sandbox, which has no `require`), so it can read this
      // file directly and inject it as an agent-scoped persona section via
      // `agent.ctx.systemPrompt.section()` — see docs/design/dsh_agent_provider_design.md
      // §3.2. Avoids the exec() argument-length limit a large system context
      // would risk as a single env var (§3.5).
      const systemContext = this.options.systemContext || "";
      let systemContextFile = "";
      if (systemContext) {
        systemContextFile = path.join(invocationDir, "system-context.txt");
        await fs.writeFile(systemContextFile, systemContext, { encoding: "utf8", mode: 0o600 });
      }

      const env: NodeJS.ProcessEnv = {
        ...process.env,
        HOME: this.options.home,
        DSH_PERMISSION_MODE: "danger-full-access",
        DSH_SESSION_ROOT: sessionRoot,
        DSH_PROMPT_FILE: promptFile,
        DSH_SESSION_ID: sessionId,
      };
      // env starts from ...process.env (line 86), so an unset key below isn't
      // "absent" — it's whatever the host process happened to have exported.
      // Every conditional DSH_* var must therefore have an explicit else-branch
      // delete, not just a truthy-branch set, or a host-exported value with the
      // same name passes straight through to the dsh child. DSH_SKILL_DIRS is
      // the sharpest case: an inherited value would have dsh load an
      // unvalidated skill directory (resolveSkillPaths()'s symlink-escape check
      // never sees it) under danger-full-access permissions.
      if (systemContextFile) {
        env.DSH_SYSTEM_CONTEXT_FILE = systemContextFile;
      } else {
        delete env.DSH_SYSTEM_CONTEXT_FILE;
      }
      if (resume) {
        env.DSH_RESUME = "1";
      } else {
        delete env.DSH_RESUME;
      }
      // DSH_MODEL is the one conditional DSH_* var with a legitimate inherited
      // value: the daemon's facade config sets it to the model it resolved and
      // minted the token against. Deleting it when no --model was passed would
      // drop that and let the profile fall back to its hardcoded default, so
      // only overwrite when this invocation actually names a model.
      const modelName = dshModelName(this.options.model);
      if (modelName) {
        env.DSH_MODEL = modelName;
      }
      const effort = dshReasoningEffort(this.options.effort);
      if (effort) {
        env.DSH_REASONING_EFFORT = effort;
      } else {
        delete env.DSH_REASONING_EFFORT;
      }
      if (mcpServers.length > 0) {
        const mcpServersJson = JSON.stringify(mcpServers);
        assertEnvValueWithinExecLimit("DSH_MCP_SERVERS", mcpServersJson);
        env.DSH_MCP_SERVERS = mcpServersJson;
      } else {
        delete env.DSH_MCP_SERVERS;
      }
      const skillDirs = await this.resolveSkillPaths();
      if (skillDirs.length > 0) {
        env.DSH_SKILL_DIRS = skillDirs.join(":");
      } else {
        delete env.DSH_SKILL_DIRS;
      }

      const child = spawn("dsh", ["--profile", "agent-compose"], {
        cwd: this.options.workspace,
        env,
        stdio: ["ignore", "pipe", "pipe"],
        signal: this.options.abortController?.signal,
      });
      // Attach the process error handler immediately. A failed spawn may emit
      // before stdout iteration completes, and an unhandled child "error"
      // event would otherwise terminate the runtime process.
      const exit = waitForChildExit(child, this.options.abortController?.signal);

      let stderrBytes: Buffer = Buffer.alloc(0);
      child.stderr?.on("data", (chunk) => {
        const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(String(chunk || ""));
        stderrBytes = appendBounded(stderrBytes, bytes, maxDiagnosticBytes);
        process.stderr.write(bytes);
      });

      let protocolError: Error | null = null;
      const lines = readline.createInterface({ input: child.stdout, crlfDelay: Infinity });
      for await (const line of lines) {
        if (!line.trim()) continue;
        let parsed: unknown;
        try {
          parsed = JSON.parse(line);
        } catch (error) {
          protocolError ??= new Error(`dsh emitted invalid JSON event: ${truncate(line, 4096)}`, { cause: error });
          continue;
        }
        if (!isRecord(parsed) || parsed.type !== "session_event") {
          continue;
        }
        const eventSessionId = firstString(parsed, "sessionId", "session_id");
        if (eventSessionId && eventSessionId !== sessionId) {
          protocolError ??= new Error(`dsh emitted an event for unexpected session ${eventSessionId}`);
          continue;
        }
        const event = recordValue(parsed.event);
        if (!event) continue;
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
        throw new Error(`dsh exited with code ${processResult.exitCode}${stderr ? `: ${stderr}` : ""}`);
      }
      if (cancelled) {
        result.stopReason = "cancelled";
      }
      result.transcript = this.writer.transcript();
      if (!result.finalText && result.transcript) {
        result.finalText = result.transcript;
        result.finalTextSource = "transcript_fallback";
      }
      if (!cancelled) {
        await writeStoredThread(this.options.sessionRoot, "dsh", sessionId);
      }
      return result;
    } finally {
      await fs.rm(invocationDir, { recursive: true, force: true });
    }
  }

  private emit(event: AgentEvent): void {
    this.options.onEvent?.(event);
  }

  /**
   * Map one DSH SessionEvent onto the neutral model.
   *
   * DSH reports the same per-step usage twice — once as an `assistant/chunk`
   * of type "usage" and once on `assistant/message.usage`, byte-identical.
   * Only the latter is mapped; taking both doubles every token count.
   */
  private emitNeutral(event: Record<string, unknown>): void {
    const type = String(event.type || "");
    const data = recordValue(event.data);
    const step = typeof data?.step === "number" ? data.step : undefined;
    if (type === "step/start") {
      this.emit({ kind: "step_start", step });
      return;
    }
    if (type === "step/end") {
      this.emit({ kind: "step_end", step });
      return;
    }
    if (type === "assistant/chunk") {
      const chunk = recordValue(data?.chunk);
      const chunkType = String(chunk?.type || "");
      const index = typeof chunk?.index === "number" ? chunk.index : undefined;
      if (chunkType === "text-delta" && typeof chunk?.text === "string" && chunk.text) {
        this.emit({ kind: "text_delta", step, blockIndex: index, text: chunk.text });
      } else if (chunkType === "reasoning-delta" && typeof chunk?.text === "string" && chunk.text) {
        this.emit({ kind: "reasoning_delta", step, blockIndex: index, text: chunk.text });
      }
      return;
    }
    if (type === "assistant/message") {
      const usage = recordValue(data?.usage);
      if (usage) {
        this.emit({
          kind: "usage",
          step,
          scope: "step",
          inputTokens: Number(usage.inputTokens ?? 0),
          outputTokens: Number(usage.outputTokens ?? 0),
          reasoningTokens: typeof usage.reasoningTokens === "number" ? usage.reasoningTokens : undefined,
          cachedTokens: typeof usage.cacheReadTokens === "number" ? usage.cacheReadTokens : undefined,
        });
      }
      return;
    }
    if (type === "tool/call") {
      const name = firstString(data, "name");
      let input: unknown;
      try {
        input = JSON.parse(String(data?.arguments ?? "null"));
      } catch {
        input = data?.arguments;
      }
      this.emit({
        kind: "tool_call",
        step,
        id: firstString(data, "callId"),
        name,
        toolKind: toolKindForName(name),
        status: "in_progress",
        input,
      });
      return;
    }
    if (type === "tool/result") {
      const message = recordValue(data?.message);
      const blocks = Array.isArray(message?.content) ? message.content : [];
      const results = blocks.filter((block): block is Record<string, unknown> => isRecord(block) && block.type === "tool-result");
      const text = results
        .flatMap((block) => (Array.isArray(block.content) ? block.content : []))
        .filter((entry): entry is Record<string, unknown> => isRecord(entry) && entry.type === "text")
        .map((entry) => String(entry.text || ""))
        .join("");
      const errorDetail = recordValue(data?.error);
      const failed = Boolean(errorDetail) || results.some((block) => block.isError === true);
      this.emit({
        kind: "tool_result",
        step,
        id: firstString(results[0], "toolCallId") || firstString(recordValue(message?.source), "callId"),
        ok: !failed,
        output: text,
        ...(errorDetail ? { error: firstString(errorDetail, "message") || "dsh tool error" } : {}),
      });
      return;
    }
    if (type === "todo/write") {
      const todos = Array.isArray(data?.todos) ? data.todos : [];
      this.emit({
        kind: "todo",
        items: todos.map((entry) => {
          const record = entry as Record<string, unknown>;
          return { text: String(record.text ?? record.content ?? ""), completed: String(record.status || "") === "completed" || record.completed === true };
        }),
      });
      return;
    }
    if (type === "turn/end") {
      const reason = recordValue(data?.reason);
      const kind = firstString(reason, "kind") || "completed";
      this.emit({
        kind: "step_end",
        stopReason: kind === "completed" ? "stop" : kind === "cancelled" ? "cancelled" : kind === "error" ? "error" : undefined,
        rawStopReason: kind,
      });
      if (kind === "error") {
        const errorDetail = recordValue(reason?.error);
        this.emit({
          kind: "error",
          severity: "fatal",
          code: firstString(errorDetail, "code") || undefined,
          message: firstString(errorDetail, "message") || "unknown dsh error",
        });
      }
      return;
    }
    if (type.startsWith("compaction/")) {
      this.emit({ kind: "compaction", phase: type.endsWith("start") ? "start" : "end" });
      return;
    }
    if (type.startsWith("llm/retry")) {
      this.emit({ kind: "retry", reason: "other", attempt: 0 });
    }
  }

  handleEvent(event: Record<string, unknown>, result: AgentResult): void {
    this.emitNeutral(event);
    // event is a DSH SessionEvent: {type, seq, time, data}. See
    // packages/core/session/src/types.ts in deepseek-harness.
    const type = String(event.type || "");
    const data = recordValue(event.data);
    if (type === "assistant/chunk") {
      const chunk = recordValue(data?.chunk);
      if (String(chunk?.type || "") === "text-delta" && typeof chunk?.text === "string" && chunk.text) {
        this.writer.write(chunk.text);
      }
      return;
    }
    if (type === "assistant/message") {
      const text = extractAssistantMessageText(data);
      if (text) {
        result.finalText = text;
        result.finalTextSource = "provider_message";
      }
      return;
    }
    if (type === "tool/call" || type === "tool/result" || type.startsWith("tool/code-dispatch")) {
      return;
    }
    if (type === "turn/start" || type === "step/start" || type === "step/end") {
      return;
    }
    if (type === "turn/end") {
      const reason = recordValue(data?.reason);
      const kind = firstString(reason, "kind") || "completed";
      result.stopReason = kind;
      if (kind === "error") {
        const errorDetail = recordValue(reason?.error);
        const code = firstString(errorDetail, "code");
        const message = firstString(errorDetail, "message") || "unknown dsh error";
        this.reportedError ??= new Error(`dsh turn ended with error${code ? ` (${code})` : ""}: ${message}`);
      }
      return;
    }
    if (type.startsWith("compaction/") || type.startsWith("llm/retry")) {
      this.writer.line(`\n[dsh:${type}]`);
      return;
    }
  }

  private async resolveSkillPaths(): Promise<string[]> {
    if (!this.options.skills?.length) return [];
    const root = path.join(this.options.home, ".agents", "skills");
    const realRoot = await fs.realpath(root);
    const resolved: string[] = [];
    for (const name of this.options.skills) {
      if (!name || path.isAbsolute(name) || name.includes("/") || name.includes("\\")) {
        throw new Error(`invalid dsh skill name ${JSON.stringify(name)}`);
      }
      const skillDir = path.join(realRoot, name);
      // Resolve SKILL.md's real target (not just the directory) so a
      // directory that stays under root but symlinks its SKILL.md elsewhere
      // is still caught as an escape.
      const skillFile = await fs.realpath(path.join(skillDir, "SKILL.md"));
      if (!isWithin(realRoot, skillFile)) throw new Error(`dsh skill ${JSON.stringify(name)} escapes the skills directory`);
      resolved.push(await fs.realpath(skillDir));
    }
    return resolved;
  }
}

// Matches SplitDshModel's (pkg/llms/dsh_facade.go) strings.Cut(value, "/")
// semantics: split on the FIRST slash, not the last. The model remainder may
// itself contain slashes (see agent-compose-yaml-manual.md), and the facade
// token daemon-side is bound to that full remainder — extracting anything
// else here would send DSH a model name that doesn't match the token.
function dshModelName(model: string | undefined): string {
  const trimmed = (model || "").trim();
  if (!trimmed) return "";
  const separator = trimmed.indexOf("/");
  return separator >= 0 ? trimmed.slice(separator + 1) : trimmed;
}

function dshReasoningEffort(effort: RunnerOptions["effort"]): string {
  switch (effort) {
    case "low":
    case "medium":
    case "high":
      return "high";
    case "xhigh":
    case "max":
      return "max";
    default:
      return "";
  }
}

// See docs/design/dsh_agent_provider_design.md §6.
type DshMcpServerConfig =
  | { transport: "stdio"; serverName: string; command: string; args: string[]; env: Record<string, string> }
  | { transport: "streamable-http"; serverName: string; url: string; headers: Record<string, string> };

// dsh-mcp-client requires serverName to match [A-Za-z0-9_-]{1,32} and be
// unique across live instances. agent-compose's mcp_servers keys have no
// such constraint (see pkg/compose/normalize.go's validateNamedMap), so
// every name is sanitized and suffixed with a deterministic hash of the raw
// name — this guarantees both validity and uniqueness without tracking a
// "seen names" set across servers.
function sanitizeDshServerName(name: string): string {
  const hash = createHash("sha256").update(name).digest("hex").slice(0, 8);
  const cleaned = name.replace(/[^A-Za-z0-9_-]/g, "_");
  const base = cleaned.slice(0, 32 - 1 - hash.length) || "server";
  return `${base}-${hash}`;
}

function toDshMcpServers(mcpConfig: Record<string, RuntimeMCPServer> | undefined): DshMcpServerConfig[] {
  return Object.entries(mcpConfig || {}).map(([name, server]) => {
    const serverName = sanitizeDshServerName(name);
    if (server.type === "local") {
      return {
        transport: "stdio",
        serverName,
        command: server.command || "",
        args: Array.isArray(server.args) ? server.args : [],
        env: flattenEnvMap(server.env) ?? {},
      };
    }
    if (server.transport === "sse") {
      throw new Error(`dsh runner does not support MCP transport "sse" (server "${name}")`);
    }
    return {
      transport: "streamable-http",
      serverName,
      url: server.url || "",
      headers: flattenEnvMap(server.headers) ?? {},
    };
  });
}

function extractAssistantMessageText(data: Record<string, unknown> | undefined): string {
  const message = recordValue(data?.message);
  const content = message?.content;
  if (Array.isArray(content)) {
    return content
      .filter((block): block is Record<string, unknown> => isRecord(block) && block.type === "text")
      .map((block) => String(block.text || ""))
      .join("");
  }
  return extractText(content) || extractText(message);
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

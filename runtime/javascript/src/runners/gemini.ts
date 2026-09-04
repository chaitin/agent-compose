import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import readline from "node:readline";
import { flattenEnvMap } from "../mcp-config.js";
import { extractText, jsonString } from "../text.js";
import { TranscriptWriter } from "../transcript.js";
import type { AgentEvent } from "../agent-event.js";
import { toolKindForName } from "../agent-event.js";
import type { AgentResult, RunnerOptions } from "../types.js";
import { cancellationRequested } from "../shutdown.js";
import { waitForChildExit } from "../child-process.js";

export class GeminiRunner {
  private readonly writer = new TranscriptWriter();

  constructor(private readonly options: RunnerOptions) {}

  private emit(event: AgentEvent): void {
    this.options.onEvent?.(event);
  }

  /**
   * Map one `--output-format stream-json` event.
   *
   * The vocabulary is exactly six types: init, message, tool_use, tool_result,
   * error, result. Gemini reports no step boundaries and no reasoning on this
   * channel (thought chunks exist only under --experimental-acp), so
   * `step_start` / `step_end`(per call) / `reasoning_delta` never appear.
   *
   * Field names here intentionally differ from the legacy transcript branch
   * below, which reads `name`/`result`/`response` — none of which the CLI
   * actually sends. Fixing that path changes existing finalText behaviour and
   * is tracked separately; see the design doc §4.2.
   */
  handleEvent(event: Record<string, unknown>, result: AgentResult): void {
    const type = String(event?.type || "");
    if (type === "message") {
      // role is "user" for the echoed prompt; only assistant text is agent output.
      if (String(event.role || "") !== "assistant") {
        return;
      }
      const text = typeof event.content === "string" ? event.content : extractText(event.content);
      if (text) {
        this.emit({ kind: "text_delta", text });
      }
      return;
    }
    if (type === "tool_use") {
      const name = String(event.tool_name || event.toolName || event.name || "tool");
      this.emit({
        kind: "tool_call",
        id: String(event.tool_id || event.toolId || name),
        name,
        toolKind: toolKindForName(name),
        status: "in_progress",
        input: event.parameters,
      });
      return;
    }
    if (type === "tool_result") {
      const errorDetail = event.error as Record<string, unknown> | undefined;
      const ok = String(event.status || "") !== "error" && !errorDetail;
      this.emit({
        kind: "tool_result",
        id: String(event.tool_id || event.toolId || ""),
        ok,
        output: typeof event.output === "string" ? event.output : undefined,
        ...(errorDetail ? { error: String(errorDetail.message || "tool error") } : {}),
      });
      return;
    }
    if (type === "error") {
      const severity = String(event.severity || "error");
      this.emit({
        kind: "error",
        severity: severity === "warning" ? "warning" : "error",
        message: String(event.message || "gemini error"),
      });
      return;
    }
    if (type === "result") {
      const stats = (event.stats || {}) as Record<string, unknown>;
      const models = (stats.models || {}) as Record<string, unknown>;
      // `stats.input_tokens` counts cached tokens too; `stats.input` is the
      // uncached remainder, which is what inputTokens means here.
      this.emit({
        kind: "usage",
        scope: "run",
        model: Object.keys(models)[0],
        inputTokens: Number(stats.input ?? 0),
        outputTokens: Number(stats.output_tokens ?? 0),
        cachedTokens: typeof stats.cached === "number" ? stats.cached : undefined,
      });
      const errorDetail = event.error as Record<string, unknown> | undefined;
      this.emit({
        kind: "step_end",
        stopReason: errorDetail ? "error" : "stop",
        rawStopReason: String(event.status || ""),
      });
      if (errorDetail) {
        this.emit({
          kind: "error",
          severity: "fatal",
          code: typeof errorDetail.type === "string" ? errorDetail.type : undefined,
          message: String(errorDetail.message || "gemini execution failed"),
        });
      }
      void result;
    }
  }

  async writeSettingsFile(): Promise<void> {
    const mcps = this.options.mcpConfig as Record<string, Record<string, unknown>> | undefined;
    const geminiDir = path.join(this.options.home, ".gemini");
    await fs.mkdir(geminiDir, { recursive: true });
    const settingsPath = path.join(geminiDir, "settings.json");
    let settings: Record<string, unknown> = {};
    try {
      settings = JSON.parse(await fs.readFile(settingsPath, "utf-8"));
    } catch {
      settings = {};
    }
    if (!mcps || Object.keys(mcps).length === 0) {
      if (Object.prototype.hasOwnProperty.call(settings, "mcpServers")) {
        delete settings.mcpServers;
        await fs.writeFile(settingsPath, JSON.stringify(settings, null, 2) + "\n", "utf-8");
      }
      return;
    }
    const mcpServers: Record<string, unknown> = {};
    for (const [name, server] of Object.entries(mcps)) {
      if (server.type === "local") {
        mcpServers[name] = {
          command: server.command,
          args: Array.isArray(server.args) ? server.args : [],
          env: flattenEnvMap(server.env as Record<string, { value: string }> | undefined),
        };
      } else if (server.type === "remote") {
        mcpServers[name] = {
          ...(server.transport === "http" ? { httpUrl: server.url } : { url: server.url }),
          headers: flattenEnvMap(server.headers as Record<string, { value: string }> | undefined),
        };
      }
    }
    settings.mcpServers = mcpServers;
    await fs.writeFile(settingsPath, JSON.stringify(settings, null, 2) + "\n", "utf-8");
  }

  async runPrompt(promptText: string): Promise<AgentResult> {
    if (this.options.outputSchema) {
      throw new Error("structured JSON output is not supported by gemini runner");
    }

    const result: AgentResult = {
      provider: "gemini",
      threadId: "",
      stopReason: "completed",
      finalText: "",
      finalTextSource: "none",
      transcript: "",
      stderr: "",
    };

    const userPrompt = this.options.systemContext
      ? `${this.options.systemContext}\n\n${promptText}`
      : promptText;

    await this.writeSettingsFile();

    const child = spawn("gemini", [
      "-p", userPrompt,
      ...(this.options.model ? ["--model", this.options.model] : []),
      "--output-format", "stream-json",
      "--approval-mode", "yolo",
    ], {
      cwd: this.options.workspace,
      env: { ...process.env },
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
        event = JSON.parse(line);
      } catch {
        continue;
      }
      this.handleEvent(event, result);
      const eventType = String(event?.type || "");
      if (eventType === "init") {
        result.threadId = String(event.sessionId || event.session_id || result.threadId);
        continue;
      }
      if (eventType === "message") {
        const text = extractText(event?.message) || extractText(event?.content) || extractText(event?.text);
        if (text) {
          this.writer.write(text);
        }
        continue;
      }
      if (eventType === "tool_use") {
        const tool = event.tool as Record<string, unknown> | undefined;
        const toolName = event?.name || event?.toolName || tool?.name || "tool";
        this.writer.line(`\n[tool:${toolName}]`);
        continue;
      }
      if (eventType === "tool_result") {
        const text = extractText(event?.result) || extractText(event?.content) || jsonString(event?.result || event);
        if (text.trim()) {
          this.writer.line(text);
        }
        continue;
      }
      if (eventType === "error") {
        const text = extractText(event?.error) || extractText(event?.message) || jsonString(event);
        this.writer.line(text);
        continue;
      }
      if (eventType === "result") {
        const finalText = extractText(event?.response) || extractText(event?.result);
        if (finalText) {
          result.finalText = finalText;
          result.finalTextSource = "provider_message";
        }
        result.stopReason = event?.error ? "error" : "completed";
      }
      }
    } catch (error) {
      if (!cancellationRequested(this.options.abortController?.signal)) {
        throw error;
      }
    }

    const processResult = await exit;
    const cancelled = cancellationRequested(this.options.abortController?.signal);
    if (processResult.spawnError && !cancelled) {
      throw processResult.spawnError;
    }
    if (processResult.exitCode !== 0 && !cancelled) {
      throw new Error(`gemini exited with code ${processResult.exitCode}: ${stderrChunks.join("")}`);
    }
    if (cancelled) {
      result.stopReason = "cancelled";
    }

    result.transcript = this.writer.transcript();
    if (!result.finalText && result.transcript) {
      result.finalText = result.transcript;
      result.finalTextSource = "transcript_fallback";
    }
    return result;
  }
}

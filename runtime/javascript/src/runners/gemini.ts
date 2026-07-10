import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import readline from "node:readline";
import { flattenEnvMap } from "../mcp-config.js";
import { extractText, jsonString } from "../text.js";
import { TranscriptWriter } from "../transcript.js";
import type { AgentResult, RunnerOptions } from "../types.js";

export class GeminiRunner {
  private readonly writer = new TranscriptWriter();

  constructor(private readonly options: RunnerOptions) {}

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
      transcript: "",
      stderr: "",
    };

    const userPrompt = this.options.systemContext
      ? `${this.options.systemContext}\n\n${promptText}`
      : promptText;

    await this.writeSettingsFile();

    const child = spawn("gemini", [
      "-p", userPrompt,
      "--output-format", "stream-json",
      "--approval-mode", "yolo",
    ], {
      cwd: this.options.workspace,
      env: { ...process.env },
      stdio: ["ignore", "pipe", "pipe"],
    });

    const stderrChunks: string[] = [];
    child.stderr?.on("data", (chunk) => {
      const text = String(chunk || "");
      stderrChunks.push(text);
      this.writer.write(text);
    });

    const rl = readline.createInterface({ input: child.stdout, crlfDelay: Infinity });
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
        result.finalText = extractText(event?.response) || extractText(event?.result) || result.finalText;
        result.stopReason = event?.error ? "error" : "completed";
      }
    }

    const exitCode = await new Promise<number>((resolve, reject) => {
      child.once("error", reject);
      child.once("exit", (code) => resolve(code ?? 1));
    });
    if (exitCode !== 0) {
      throw new Error(`gemini exited with code ${exitCode}: ${stderrChunks.join("")}`);
    }

    result.transcript = this.writer.transcript();
    if (!result.finalText && result.transcript) {
      result.finalText = result.transcript;
    }
    return result;
  }
}

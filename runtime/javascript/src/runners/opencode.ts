import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import readline from "node:readline";
import { readStoredThread, writeStoredThread } from "../session-state.js";
import { extractText, jsonString } from "../text.js";
import { TranscriptWriter } from "../transcript.js";
import type { AgentResult, RunnerOptions, StoredThread } from "../types.js";
import { flattenEnvMap } from "../mcp-config.js";

export class OpenCodeRunner {
  private readonly writer = new TranscriptWriter();

  constructor(private readonly options: RunnerOptions) {}

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

  environment(): NodeJS.ProcessEnv {
    return {
      ...process.env,
      OPENCODE_DISABLE_AUTOUPDATE: process.env.OPENCODE_DISABLE_AUTOUPDATE || "true",
      OPENCODE_DISABLE_MODELS_FETCH: process.env.OPENCODE_DISABLE_MODELS_FETCH || "1",
    };
  }

  handleEvent(event: Record<string, unknown>, result: AgentResult): void {
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

    if (eventType === "result" || eventType === "complete" || eventType === "completed") {
      const finalText = extractText(event.response) || extractText(event.result) || text;
      if (finalText) {
        result.finalText = finalText;
      }
      result.stopReason = stringField(event, "stopReason", "stop_reason", "finishReason", "finish_reason") || result.stopReason;
    }
  }

  async runPrompt(promptText: string): Promise<AgentResult> {
    await this.writeMCPConfig();
    if (this.options.outputSchema) {
      throw new Error("structured JSON output is not supported by opencode runner");
    }

    const stored = await readStoredThread(this.options.stateRoot, "opencode");
    const result: AgentResult = {
      provider: "opencode",
      threadId: stored?.threadId || "",
      stopReason: "completed",
      finalText: "",
      transcript: "",
      stderr: "",
    };

    const child = spawn("opencode", this.buildArgs(promptText, stored), {
      cwd: this.options.workspace,
      env: this.environment(),
      stdio: ["ignore", "pipe", "pipe"],
    });

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
      child.kill("SIGTERM");
      throw error;
    }

    const exitCode = await new Promise<number>((resolve, reject) => {
      child.once("error", reject);
      child.once("exit", (code) => resolve(code ?? 1));
    });
    if (exitCode !== 0) {
      throw new Error(`opencode exited with code ${exitCode}: ${stderrChunks.join("")}`);
    }

    result.transcript = this.writer.transcript();
    if (!result.finalText && result.transcript) {
      result.finalText = result.transcript;
    }
    await writeStoredThread(this.options.stateRoot, "opencode", result.threadId);
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

import { afterEach, describe, expect, it, vi } from "vitest";
import fs from "node:fs/promises";
import path from "node:path";
import { ClaudeRunner } from "../src/runners/claude.js";
import { CodexRunner } from "../src/runners/codex.js";
import { GeminiRunner } from "../src/runners/gemini.js";
import { OpenCodeRunner } from "../src/runners/opencode.js";
import { captureStdio, runnerOptions, withTempSession } from "./helpers.js";

afterEach(() => {
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

describe("CodexRunner", () => {
  it("exposes Codex thread options without constructor-only config", async () => {
    await withTempSession(async (root) => {
      const systemContext = "## MPI Catalog\n\ncatalog body";
      const runner = new CodexRunner(runnerOptions(root, systemContext));

      const threadOptions = runner.threadOptions();

      expect(threadOptions.additionalDirectories).toEqual([
        `${root}/state`,
        `${root}/home`,
        `${root}/runtime`,
      ]);
      expect(threadOptions).not.toHaveProperty("config");
    });
  });

  it("translates common events into transcript and final text", async () => {
    await withTempSession(async (root) => {
      const runner = new CodexRunner(runnerOptions(root));
      const result = {
        provider: "codex" as const,
        threadId: "",
        stopReason: "completed",
        finalText: "",
        transcript: "",
        stderr: "",
      };
      const stdio = captureStdio();
      try {
        runner.handleEvent({ type: "thread.started", thread_id: "thread-1" }, result);
        runner.handleEvent({ type: "item.updated", item: { id: "m1", type: "agent_message", text: "hello" } }, result);
        runner.handleEvent({ type: "item.completed", item: { id: "m1", type: "agent_message", text: "hello!" } }, result);
        runner.handleEvent({ type: "item.updated", item: { id: "r1", type: "reasoning", text: " because" } }, result);
        runner.handleEvent({
          type: "item.updated",
          item: { id: "cmd", type: "command_execution", command: "pwd", aggregated_output: "/work\n" },
        }, result);
        runner.handleEvent({
          type: "item.updated",
          item: { id: "cmd", type: "command_execution", command: "pwd", aggregated_output: "/work\nnext\n" },
        }, result);
        runner.handleEvent({
          type: "item.completed",
          item: { id: "fc", type: "file_change", changes: [{ kind: "edit", path: "a.ts" }] },
        }, result);
        runner.handleEvent({
          type: "item.completed",
          item: {
            id: "mcp",
            type: "mcp_tool_call",
            server: "srv",
            tool: "lookup",
            arguments: { query: "docs" },
            status: "completed",
            result: { text: "mcp result" },
          },
        }, result);
        runner.handleEvent({
          type: "item.completed",
          item: { id: "mcp2", type: "mcp_tool_call", server: "srv", tool: "lookup", status: "failed", error: { message: "mcp failed" } },
        }, result);
        runner.handleEvent({
          type: "item.updated",
          item: { id: "todo", type: "todo_list", items: [{ text: "one", completed: true }, { text: "two", completed: false }] },
        }, result);
        runner.handleEvent({ type: "item.completed", item: { id: "err", type: "error", message: "item bad" } }, result);
        runner.handleEvent({ type: "item.completed", item: { id: "unknown", type: "new_type" } }, result);
        runner.handleEvent({ type: "item.completed" }, result);
        runner.handleEvent({ type: "item.started", item: { id: "ws", type: "web_search" } }, result);
        runner.handleEvent({ type: "item.completed", item: { id: "ws", type: "web_search", action: { query: "docs" } } }, result);
      } finally {
        stdio.restore();
      }

      expect(result.threadId).toBe("thread-1");
      expect(result.finalText).toBe("hello!");
      expect(stdio.stderr).toContain("hello");
      expect(stdio.stderr).toContain("$ pwd");
      expect(stdio.stderr).toContain("[file_change]");
      expect(stdio.stderr).toContain("edit: a.ts");
      expect(stdio.stderr).toContain("[mcp:srv/lookup]");
      expect(stdio.stderr).toContain("\"query\": \"docs\"");
      expect(stdio.stderr).toContain("mcp result");
      expect(stdio.stderr).toContain("[mcp:srv/lookup]\n{\n  \"query\": \"docs\"\n}\nmcp result");
      expect(stdio.stderr).toContain("mcp failed");
      expect(stdio.stderr).toContain("[todo]");
      expect(stdio.stderr).toContain("[x] one");
      expect(stdio.stderr).toContain("item bad");
      expect(stdio.stderr).toContain("[web_search] docs");
      expect(stdio.stderr).not.toContain("[web_search] \n");
    });
  });

  it("skips duplicate and empty Codex event payloads", async () => {
    await withTempSession(async (root) => {
      const runner = new CodexRunner(runnerOptions(root));
      const result = {
        provider: "codex" as const,
        threadId: "",
        stopReason: "completed",
        finalText: "",
        transcript: "",
        stderr: "",
      };
      const stdio = captureStdio();
      try {
        runner.emitFileChange({ id: "empty", changes: [] });
        runner.emitFileChange({ id: "fc", changes: [{ kind: "create", path: "a" }] });
        runner.emitFileChange({ id: "fc", changes: [{ kind: "create", path: "a" }] });
        runner.emitMcp({ id: "mcp", server: "srv", tool: "tool", status: "completed", result: { text: "" } });
        runner.emitMcp({ id: "mcp", server: "srv", tool: "tool", status: "completed", result: { text: "ignored" } });
        runner.emitMcp({ id: "mcp2", server: "srv", tool: "tool", status: "failed", error: {} });
        runner.emitTodo({ id: "todo-empty", items: [] });
        runner.handleEvent({ type: "item.completed", item: { id: "msg", type: "agent_message", text: "" } }, result);
      } finally {
        stdio.restore();
      }

      expect(stdio.stderr.match(/\[file_change\]/g)).toHaveLength(1);
      expect(stdio.stderr).toContain("[mcp:srv/tool]");
      expect(result.finalText).toBe("");
    });
  });

  it("keeps the Codex web search marker when no query is exposed", async () => {
    await withTempSession(async (root) => {
      const runner = new CodexRunner(runnerOptions(root));
      const result = {
        provider: "codex" as const,
        threadId: "",
        stopReason: "completed",
        finalText: "",
        transcript: "",
        stderr: "",
      };
      const stdio = captureStdio();
      try {
        runner.handleEvent({ type: "item.started", item: { id: "ws", type: "web_search" } }, result);
        runner.handleEvent({ type: "item.completed", item: { id: "ws", type: "web_search" } }, result);
      } finally {
        stdio.restore();
      }

      expect(stdio.stderr).toBe("\n[web_search] \n");
    });
  });

  it("throws turn failure messages", async () => {
    await withTempSession(async (root) => {
      const runner = new CodexRunner(runnerOptions(root));
      const result = {
        provider: "codex" as const,
        threadId: "",
        stopReason: "completed",
        finalText: "",
        transcript: "",
        stderr: "",
      };

      expect(() => runner.handleEvent({ type: "turn.failed", error: { message: "bad" } }, result)).toThrow("bad");
    });
  });
});

describe("ClaudeRunner", () => {
  it("bridges generic LLM settings into Claude SDK environment variables", async () => {
    await withTempSession(async (root) => {
      vi.stubEnv("LLM_API_KEY", "generic-key");
      vi.stubEnv("LLM_API_ENDPOINT", "https://llm.example.invalid");
      vi.stubEnv("ANTHROPIC_API_KEY", "");
      vi.stubEnv("ANTHROPIC_BASE_URL", "");
      const runner = new ClaudeRunner(runnerOptions(root));

      const env = runner.queryOptions(null).env as NodeJS.ProcessEnv;

      expect(env.ANTHROPIC_API_KEY).toBe("generic-key");
      expect(env.ANTHROPIC_BASE_URL).toBe("https://llm.example.invalid");
    });
  });

  it("keeps explicit Claude SDK environment variables over generic LLM settings", async () => {
    await withTempSession(async (root) => {
      vi.stubEnv("LLM_API_KEY", "generic-key");
      vi.stubEnv("LLM_API_ENDPOINT", "https://llm.example.invalid");
      vi.stubEnv("ANTHROPIC_API_KEY", "anthropic-key");
      vi.stubEnv("ANTHROPIC_BASE_URL", "https://anthropic.example.invalid");
      const runner = new ClaudeRunner(runnerOptions(root));

      const env = runner.queryOptions(null).env as NodeJS.ProcessEnv;

      expect(env.ANTHROPIC_API_KEY).toBe("anthropic-key");
      expect(env.ANTHROPIC_BASE_URL).toBe("https://anthropic.example.invalid");
    });
  });

  it("passes explicit Claude executable paths and bypass permissions for non-root users", async () => {
    await withTempSession(async (root) => {
      vi.stubEnv("CLAUDE_CODE_EXECUTABLE", "/custom/bin/claude");
      vi.spyOn(process, "getuid").mockReturnValue(501);
      const runner = new ClaudeRunner(runnerOptions(root));

      expect(runner.queryOptions(null)).toMatchObject({
        pathToClaudeCodeExecutable: "/custom/bin/claude",
        permissionMode: "bypassPermissions",
        allowDangerouslySkipPermissions: true,
      });
    });
  });

  it("passes executable paths and bypass permissions when running as root", async () => {
    await withTempSession(async (root) => {
      vi.stubEnv("CLAUDE_CODE_EXECUTABLE", "/usr/bin/claude");
      vi.spyOn(process, "getuid").mockReturnValue(0);
      const runner = new ClaudeRunner(runnerOptions(root));

      expect(runner.queryOptions(null)).toMatchObject({
        pathToClaudeCodeExecutable: "/usr/bin/claude",
        permissionMode: "bypassPermissions",
        allowDangerouslySkipPermissions: true,
      });
    });
  });

  it("appends system context and exposes runtime directory", async () => {
    await withTempSession(async (root) => {
      const systemContext = "## MPI Catalog\n\ncatalog body";
      const runner = new ClaudeRunner(runnerOptions(root, systemContext));

      const queryOptions = runner.queryOptions({
        provider: "claude",
        threadId: "existing-session",
      });

      expect(queryOptions.additionalDirectories).toEqual([
        `${root}/state`,
        `${root}/home`,
        `${root}/runtime`,
      ]);
      expect(queryOptions.systemPrompt).toEqual({
        type: "preset",
        preset: "claude_code",
        append: systemContext,
      });
      expect(queryOptions.resume).toBe("existing-session");
    });
  });

  it("flattens Claude MCP env and headers for SDK options", async () => {
    await withTempSession(async (root) => {
      const runner = new ClaudeRunner({
        ...runnerOptions(root, "", "claude"),
        mcpConfig: {
          localFs: {
            type: "local",
            command: "npx",
            args: ["-y", "server"],
            env: {
              TOKEN: { value: "secret", secret: true },
            },
          },
          docs: {
            type: "remote",
            transport: "http",
            url: "https://docs.example.invalid/mcp",
            headers: {
              Authorization: { value: "Bearer token", secret: true },
            },
          },
        },
      });

      expect(runner.queryOptions(null)).toMatchObject({
        strictMcpConfig: true,
        mcpServers: {
          localFs: {
            type: "stdio",
            command: "npx",
            args: ["-y", "server"],
            env: {
              TOKEN: "secret",
            },
          },
          docs: {
            type: "http",
            url: "https://docs.example.invalid/mcp",
            headers: {
              Authorization: "Bearer token",
            },
          },
        },
      });
    });
  });

  it("handles stream text and tool starts", async () => {
    await withTempSession(async (root) => {
      const runner = new ClaudeRunner(runnerOptions(root));
      const stdio = captureStdio();
      try {
        runner.handleStreamEvent({
          event: { type: "content_block_start", content_block: { name: "Edit" } },
        });
        runner.handleStreamEvent({
          event: { type: "content_block_delta", delta: { type: "text_delta", text: "answer" } },
        });
        runner.handleStreamEvent({
          event: { type: "content_block_delta", delta: { type: "thinking_delta", thinking: "thinking" } },
        });
      } finally {
        stdio.restore();
      }

      expect(stdio.stderr).toContain("[tool:Edit]");
      expect(stdio.stderr).toContain("answer");
      expect(stdio.stderr).toContain("thinking");
    });
  });

  it("prints Claude tool input after streamed JSON deltas complete", async () => {
    await withTempSession(async (root) => {
      const runner = new ClaudeRunner(runnerOptions(root));
      const stdio = captureStdio();
      try {
        runner.handleStreamEvent({
          event: { type: "content_block_start", index: 0, content_block: { type: "tool_use", name: "WebSearch", input: {} } },
        });
        runner.handleStreamEvent({
          event: { type: "content_block_delta", index: 0, delta: { type: "input_json_delta", partial_json: "{\"query\":" } },
        });
        runner.handleStreamEvent({
          event: { type: "content_block_delta", index: 0, delta: { type: "input_json_delta", partial_json: "\"baidu news\"}" } },
        });
        expect(stdio.stderr).not.toContain("[tool:WebSearch]");
        runner.handleStreamEvent({ event: { type: "content_block_stop", index: 0 } });
      } finally {
        stdio.restore();
      }

      expect(stdio.stderr).toContain("[tool:WebSearch]");
      expect(stdio.stderr).toContain("\"query\": \"baidu news\"");
      expect(stdio.stderr).not.toContain("{}");
    });
  });

  it("ignores unrelated Claude stream events", async () => {
    await withTempSession(async (root) => {
      const runner = new ClaudeRunner(runnerOptions(root));
      const stdio = captureStdio();
      try {
        runner.handleStreamEvent({});
        runner.handleStreamEvent({ event: { type: "content_block_start", content_block: {} } });
        runner.handleStreamEvent({ event: { type: "message_stop" } });
        runner.handleStreamEvent({ event: { type: "content_block_delta", delta: { type: "other" } } });
      } finally {
        stdio.restore();
      }
      expect(stdio.stderr).toBe("");
    });
  });
});

describe("GeminiRunner", () => {
  it("can be constructed with compatible options", async () => {
    await withTempSession(async (root) => {
      const runner = new GeminiRunner(runnerOptions(root, "", "gemini"));
      expect(runner).toBeInstanceOf(GeminiRunner);
    });
  });

  it("removes stale Gemini MCP settings when current config is empty", async () => {
    await withTempSession(async (root) => {
      const settingsDir = path.join(root, "home", ".gemini");
      await fs.mkdir(settingsDir, { recursive: true });
      const settingsPath = path.join(settingsDir, "settings.json");
      await fs.writeFile(settingsPath, JSON.stringify({ theme: "dark", mcpServers: { stale: { url: "http://stale" } } }, null, 2) + "\n", "utf-8");

      const runner = new GeminiRunner({
        ...runnerOptions(root, "", "gemini"),
        mcpConfig: {},
      });
      await runner.writeSettingsFile();

      const settings = JSON.parse(await fs.readFile(settingsPath, "utf-8")) as Record<string, unknown>;
      expect(settings.theme).toBe("dark");
      expect(settings).not.toHaveProperty("mcpServers");
    });
  });
});

describe("OpenCodeRunner", () => {
  it("builds non-interactive args for fresh and resumed sessions", async () => {
    await withTempSession(async (root) => {
      const runner = new OpenCodeRunner({
        ...runnerOptions(root, "be concise", "opencode"),
        model: "anthropic/claude-sonnet-4-5",
      });

      expect(runner.buildArgs("review", null)).toEqual([
        "run",
        "be concise\n\nreview",
        "--format",
        "json",
        "--dir",
        `${root}/workspace`,
        "--dangerously-skip-permissions",
        "--model",
        "anthropic/claude-sonnet-4-5",
      ]);
      expect(runner.buildArgs("review", { provider: "opencode", threadId: "ses_1" })).toContain("ses_1");
    });
  });

  it("keeps explicit OpenCode auto-update environment overrides", async () => {
    await withTempSession(async (root) => {
      vi.stubEnv("OPENCODE_DISABLE_AUTOUPDATE", "false");
      const runner = new OpenCodeRunner(runnerOptions(root, "", "opencode"));

      expect(runner.environment().OPENCODE_DISABLE_AUTOUPDATE).toBe("false");
    });
  });

  it("disables OpenCode model catalog fetch by default", async () => {
    await withTempSession(async (root) => {
      const runner = new OpenCodeRunner(runnerOptions(root, "", "opencode"));

      expect(runner.environment().OPENCODE_DISABLE_MODELS_FETCH).toBe("1");

      vi.stubEnv("OPENCODE_DISABLE_MODELS_FETCH", "0");
      expect(runner.environment().OPENCODE_DISABLE_MODELS_FETCH).toBe("0");
    });
  });

  it("translates OpenCode JSON events into transcript and final text", async () => {
    await withTempSession(async (root) => {
      const runner = new OpenCodeRunner(runnerOptions(root, "", "opencode"));
      const result = {
        provider: "opencode" as const,
        threadId: "",
        stopReason: "completed",
        finalText: "",
        transcript: "",
        stderr: "",
      };
      const stdio = captureStdio();
      try {
        runner.handleEvent({ type: "message", sessionId: "session-1", message: { content: "hello" } }, result);
        runner.handleEvent({ type: "text", part: { type: "text", text: " from part" } }, result);
        runner.handleEvent({ type: "tool_use", name: "bash" }, result);
        runner.handleEvent({ type: "tool_result", result: { text: "tool output" } }, result);
        runner.handleEvent({ type: "result", response: "done", stopReason: "completed" }, result);
      } finally {
        stdio.restore();
      }

      expect(result.threadId).toBe("session-1");
      expect(result.finalText).toBe("done");
      expect(stdio.stderr).toContain("hello");
      expect(stdio.stderr).toContain(" from part");
      expect(stdio.stderr).toContain("[tool:bash]");
      expect(stdio.stderr).toContain("tool output");
    });
  });

  it("throws OpenCode event errors and rejects structured output", async () => {
    await withTempSession(async (root) => {
      const runner = new OpenCodeRunner(runnerOptions(root, "", "opencode"));
      expect(() => runner.handleEvent({ type: "error", error: { text: "bad" } }, {
        provider: "opencode",
        threadId: "",
        stopReason: "completed",
        finalText: "",
        transcript: "",
        stderr: "",
      })).toThrow("bad");

      const schemaRunner = new OpenCodeRunner({
        ...runnerOptions(root, "", "opencode"),
        outputSchema: { type: "object" },
      });
      await expect(schemaRunner.runPrompt("hello")).rejects.toThrow("structured JSON output is not supported by opencode runner");
    });
  });
});

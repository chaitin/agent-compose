import fs from "node:fs/promises";
import path from "node:path";
import { EventEmitter } from "node:events";
import { Readable } from "node:stream";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { captureStdio, runnerOptions, withTempSession } from "./helpers.js";
import { hashSystemContext } from "../src/session-state.js";

const codexState = vi.hoisted(() => ({
  constructorOptions: [] as Array<Record<string, unknown>>,
  events: [] as Array<Record<string, unknown>>,
  threadId: "thread-new",
  resumed: "",
  runStreamedCalls: [] as Array<{ input: unknown; options: unknown }>,
}));

vi.mock("@openai/codex-sdk", () => ({
  Codex: vi.fn().mockImplementation(function Codex(options: Record<string, unknown>) {
    codexState.constructorOptions.push(options);
    return {
      startThread: vi.fn(() => ({
        id: codexState.threadId,
        runStreamed: vi.fn(async (input: unknown, options: unknown) => {
          codexState.runStreamedCalls.push({ input, options });
          return {
            events: (async function* events() {
              for (const event of codexState.events) {
                yield event;
              }
            })(),
          };
        }),
      })),
      resumeThread: vi.fn((sessionId: string) => {
        codexState.resumed = sessionId;
        return {
          id: "thread-resumed",
          runStreamed: vi.fn(async (input: unknown, options: unknown) => {
            codexState.runStreamedCalls.push({ input, options });
            return {
              events: (async function* events() {
                for (const event of codexState.events) {
                  yield event;
                }
              })(),
            };
          }),
        };
      }),
    };
  }),
}));

const claudeState = vi.hoisted(() => ({
  messages: [] as Array<Record<string, unknown>>,
  hangAfterMessages: false,
  closed: false,
  queryCalls: [] as Array<Record<string, unknown>>,
}));

vi.mock("@anthropic-ai/claude-agent-sdk", () => ({
  query: vi.fn((request: Record<string, unknown>) => {
    claudeState.queryCalls.push(request);
    return {
      close: () => {
        claudeState.closed = true;
      },
      [Symbol.asyncIterator]: async function* iterator() {
        for (const message of claudeState.messages) {
          yield message;
        }
        if (claudeState.hangAfterMessages) {
          await new Promise(() => undefined);
        }
      },
    };
  }),
}));

const childProcessState = vi.hoisted(() => ({
  stdoutLines: [] as string[],
  stderrChunks: [] as string[],
  exitCode: 0,
  error: null as Error | null,
  spawnCalls: [] as Array<{ command: string; args: string[]; options: Record<string, unknown> }>,
  spawnSyncStdout: "",
}));

vi.mock("node:child_process", () => ({
  spawnSync: vi.fn(() => ({ stdout: childProcessState.spawnSyncStdout })),
  spawn: vi.fn((command: string, args: string[], options: Record<string, unknown>) => {
    childProcessState.spawnCalls.push({ command, args, options });
    const child = new EventEmitter() as EventEmitter & { stdout: Readable; stderr: EventEmitter };
    child.stdout = Readable.from(childProcessState.stdoutLines.map((line) => `${line}\n`));
    child.stderr = new EventEmitter();
    const originalOnce = child.once.bind(child);
    child.once = ((eventName: string | symbol, listener: (...args: unknown[]) => void) => {
      if (eventName === "exit" && !childProcessState.error) {
        queueMicrotask(() => listener(childProcessState.exitCode));
        return child;
      }
      if (eventName === "error" && childProcessState.error) {
        queueMicrotask(() => listener(childProcessState.error));
        return child;
      }
      return originalOnce(eventName, listener);
    }) as typeof child.once;
    queueMicrotask(() => {
      for (const chunk of childProcessState.stderrChunks) {
        child.stderr.emit("data", chunk);
      }
    });
    return child;
  }),
}));

describe("runner execution", () => {
  beforeEach(() => {
    codexState.constructorOptions = [];
    codexState.events = [];
    codexState.threadId = "thread-new";
    codexState.resumed = "";
    codexState.runStreamedCalls = [];
    claudeState.hangAfterMessages = false;
  });

  it("runs a new Codex thread and persists the resulting session", async () => {
    const { CodexRunner } = await import("../src/runners/codex.js");
    await withTempSession(async (root) => {
      codexState.events = [
        { type: "thread.started", thread_id: "thread-started" },
        { type: "item.completed", item: { id: "a1", type: "agent_message", text: "codex answer" } },
      ];
      const stdio = captureStdio();
      try {
        const result = await new CodexRunner(runnerOptions(root, "catalog body")).runPrompt("prompt");

        expect(result).toMatchObject({
          provider: "codex",
          sessionId: "thread-new",
          finalText: "codex answer",
          transcript: "codex answer",
        });
      } finally {
        stdio.restore();
      }

      expect(codexState.constructorOptions.at(-1)).toMatchObject({
        config: { developer_instructions: "catalog body" },
      });
      const stored = JSON.parse(await fs.readFile(path.join(root, "state", "agents", "providers", "codex.json"), "utf8"));
      expect(stored.sessionId).toBe("thread-new");
      expect(stored.systemContextHash).toBe(hashSystemContext("catalog body"));
      expect(stored.systemContextHashVersion).toBe(1);
    });
  });

  it("passes Codex output schema to the streamed turn", async () => {
    const { CodexRunner } = await import("../src/runners/codex.js");
    await withTempSession(async (root) => {
      const schema = { type: "object", properties: { summary: { type: "string" } } };
      codexState.events = [
        { type: "thread.started", thread_id: "thread-started" },
        { type: "item.completed", item: { id: "a1", type: "agent_message", text: "{\"summary\":\"ok\"}" } },
      ];
      codexState.runStreamedCalls = [];
      const stdio = captureStdio();
      try {
        await new CodexRunner({ ...runnerOptions(root), outputSchema: schema }).runPrompt("prompt");
      } finally {
        stdio.restore();
      }

      expect(codexState.runStreamedCalls.at(-1)).toEqual({
        input: "prompt",
        options: { outputSchema: schema },
      });
    });
  });

  it("resumes a stored Codex thread", async () => {
    const { CodexRunner } = await import("../src/runners/codex.js");
    await withTempSession(async (root) => {
      const providerRoot = path.join(root, "state", "agents", "providers");
      await fs.mkdir(providerRoot, { recursive: true });
      await fs.writeFile(path.join(providerRoot, "codex.json"), JSON.stringify({
        provider: "codex",
        sessionId: "old-thread",
      }), "utf8");
      codexState.events = [{ type: "item.completed", item: { id: "a2", type: "agent_message", text: "resumed" } }];
      const stdio = captureStdio();
      try {
        const result = await new CodexRunner(runnerOptions(root)).runPrompt("prompt");
        expect(result.sessionId).toBe("thread-resumed");
      } finally {
        stdio.restore();
      }
      expect(codexState.resumed).toBe("old-thread");
      const stored = JSON.parse(await fs.readFile(path.join(providerRoot, "codex.json"), "utf8"));
      expect(stored.systemContextHash).toBe(hashSystemContext(""));
      expect(stored.systemContextHashVersion).toBe(1);
    });
  });

  it("resumes a stored Codex thread when the system context hash matches", async () => {
    const { CodexRunner } = await import("../src/runners/codex.js");
    await withTempSession(async (root) => {
      const systemContext = "catalog body";
      const providerRoot = path.join(root, "state", "agents", "providers");
      await fs.mkdir(providerRoot, { recursive: true });
      await fs.writeFile(path.join(providerRoot, "codex.json"), JSON.stringify({
        provider: "codex",
        sessionId: "old-thread",
        systemContextHash: hashSystemContext(systemContext),
        systemContextHashVersion: 1,
      }), "utf8");
      codexState.events = [{ type: "item.completed", item: { id: "a2", type: "agent_message", text: "resumed" } }];
      const stdio = captureStdio();
      try {
        const result = await new CodexRunner(runnerOptions(root, systemContext)).runPrompt("prompt");
        expect(result.sessionId).toBe("thread-resumed");
      } finally {
        stdio.restore();
      }
      expect(codexState.resumed).toBe("old-thread");
    });
  });

  it("starts a new Codex thread when the system context hash changes", async () => {
    const { CodexRunner } = await import("../src/runners/codex.js");
    await withTempSession(async (root) => {
      const providerRoot = path.join(root, "state", "agents", "providers");
      await fs.mkdir(providerRoot, { recursive: true });
      await fs.writeFile(path.join(providerRoot, "codex.json"), JSON.stringify({
        provider: "codex",
        sessionId: "old-thread",
        systemContextHash: hashSystemContext("old context"),
        systemContextHashVersion: 1,
      }), "utf8");
      codexState.events = [{ type: "item.completed", item: { id: "a3", type: "agent_message", text: "fresh" } }];
      const stdio = captureStdio();
      try {
        const result = await new CodexRunner(runnerOptions(root, "new context")).runPrompt("prompt");
        expect(result.sessionId).toBe("thread-new");
        expect(stdio.stderr).toMatch(/warning: system context changed; started new Codex thread/);
      } finally {
        stdio.restore();
      }
      expect(codexState.resumed).toBe("");
      const stored = JSON.parse(await fs.readFile(path.join(providerRoot, "codex.json"), "utf8"));
      expect(stored.sessionId).toBe("thread-new");
      expect(stored.systemContextHash).toBe(hashSystemContext("new context"));
    });
  });

  it("starts a new Codex thread when the stored hash version is unsupported", async () => {
    const { CodexRunner } = await import("../src/runners/codex.js");
    await withTempSession(async (root) => {
      const systemContext = "catalog body";
      const providerRoot = path.join(root, "state", "agents", "providers");
      await fs.mkdir(providerRoot, { recursive: true });
      await fs.writeFile(path.join(providerRoot, "codex.json"), JSON.stringify({
        provider: "codex",
        sessionId: "old-thread",
        systemContextHash: hashSystemContext(systemContext),
        systemContextHashVersion: 999,
      }), "utf8");
      codexState.events = [{ type: "item.completed", item: { id: "a3", type: "agent_message", text: "fresh" } }];
      const stdio = captureStdio();
      try {
        const result = await new CodexRunner(runnerOptions(root, systemContext)).runPrompt("prompt");
        expect(result.sessionId).toBe("thread-new");
      } finally {
        stdio.restore();
      }
      expect(codexState.resumed).toBe("");
      const stored = JSON.parse(await fs.readFile(path.join(providerRoot, "codex.json"), "utf8"));
      expect(stored.sessionId).toBe("thread-new");
      expect(stored.systemContextHash).toBe(hashSystemContext(systemContext));
      expect(stored.systemContextHashVersion).toBe(1);
    });
  });

  it("starts a new Codex thread when system context changes from non-empty to empty", async () => {
    const { CodexRunner } = await import("../src/runners/codex.js");
    await withTempSession(async (root) => {
      const providerRoot = path.join(root, "state", "agents", "providers");
      await fs.mkdir(providerRoot, { recursive: true });
      await fs.writeFile(path.join(providerRoot, "codex.json"), JSON.stringify({
        provider: "codex",
        sessionId: "old-thread",
        systemContextHash: hashSystemContext("old context"),
        systemContextHashVersion: 1,
      }), "utf8");
      codexState.events = [{ type: "item.completed", item: { id: "a4", type: "agent_message", text: "cleared" } }];
      const stdio = captureStdio();
      try {
        await new CodexRunner(runnerOptions(root)).runPrompt("prompt");
        expect(stdio.stderr).toMatch(/warning: system context changed; started new Codex thread/);
      } finally {
        stdio.restore();
      }
      expect(codexState.resumed).toBe("");
    });
  });

  it("runs Claude stream messages, persists state, and closes the stream", async () => {
    const { ClaudeRunner } = await import("../src/runners/claude.js");
    await withTempSession(async (root) => {
      claudeState.closed = false;
      claudeState.messages = [
        { type: "stream_event", session_id: "claude-session", event: { type: "content_block_delta", delta: { type: "text_delta", text: "partial" } } },
        { type: "assistant", message: { content: [{ type: "text", text: "assistant text" }] } },
        { type: "tool_use_summary", summary: "used tool" },
        { type: "auth_status", output: ["auth ok"] },
        { type: "system", subtype: "local_command_output", content: "local output" },
        { type: "result", subtype: "success", stop_reason: "end_turn", result: "final claude" },
      ];
      const stdio = captureStdio();
      try {
        const result = await new ClaudeRunner(runnerOptions(root, "", "claude")).runPrompt("prompt");

        expect(result).toMatchObject({
          provider: "claude",
          sessionId: "claude-session",
          stopReason: "end_turn",
          finalText: "final claude",
        });
        expect(result.transcript).toContain("partial");
        expect(result.transcript).toContain("used tool");
        expect(result.transcript).toContain("auth ok");
        expect(result.transcript).toContain("local output");
      } finally {
        stdio.restore();
      }
      expect(claudeState.closed).toBe(true);
      const stored = JSON.parse(await fs.readFile(path.join(root, "state", "agents", "providers", "claude.json"), "utf8"));
      expect(stored.sessionId).toBe("claude-session");
    });
  });

  it("returns immediately after a Claude success result even if the SDK stream stays open", async () => {
    const { ClaudeRunner } = await import("../src/runners/claude.js");
    await withTempSession(async (root) => {
      claudeState.closed = false;
      claudeState.hangAfterMessages = true;
      claudeState.messages = [
        { type: "stream_event", session_id: "claude-session", event: { type: "content_block_delta", delta: { type: "text_delta", text: "done" } } },
        { type: "result", subtype: "success", stop_reason: "end_turn", result: "final claude" },
      ];
      const stdio = captureStdio();
      try {
        const result = await Promise.race([
          new ClaudeRunner(runnerOptions(root, "", "claude")).runPrompt("prompt"),
          new Promise<never>((_, reject) => setTimeout(() => reject(new Error("runner did not return after result")), 100)),
        ]);

        expect(result.finalText).toBe("final claude");
      } finally {
        stdio.restore();
      }
      expect(claudeState.closed).toBe(true);
    });
  });

  it("passes Claude output schema through the native output format option and returns structured output JSON", async () => {
    const { ClaudeRunner } = await import("../src/runners/claude.js");
    await withTempSession(async (root) => {
      const schema = { type: "object", properties: { summary: { type: "string" } } };
      claudeState.messages = [{
        type: "result",
        subtype: "success",
        session_id: "claude-session",
        result: "human readable fallback",
        structured_output: { summary: "ok" },
      }];
      claudeState.queryCalls = [];
      const stdio = captureStdio();
      try {
        const result = await new ClaudeRunner({ ...runnerOptions(root, "", "claude"), outputSchema: schema }).runPrompt("prompt");
        expect(result.finalText).toBe("{\"summary\":\"ok\"}");
      } finally {
        stdio.restore();
      }

      expect(claudeState.queryCalls.at(-1)).toMatchObject({
        prompt: "prompt",
        options: {
          outputFormat: {
            type: "json_schema",
            schema,
          },
        },
      });
    });
  });

  it.each([
    ["false", false, "false"],
    ["null", null, "null"],
    ["empty string", "", "\"\""],
    ["zero", 0, "0"],
  ])("serializes Claude structured output when the value is %s", async (_label, structuredOutput, expectedFinalText) => {
    const { ClaudeRunner } = await import("../src/runners/claude.js");
    await withTempSession(async (root) => {
      claudeState.messages = [{
        type: "result",
        subtype: "success",
        session_id: "claude-session",
        result: "fallback text",
        structured_output: structuredOutput,
      }];
      const stdio = captureStdio();
      try {
        const result = await new ClaudeRunner({
          ...runnerOptions(root, "", "claude"),
          outputSchema: { type: ["boolean", "null", "string", "number"] },
        }).runPrompt("prompt");
        expect(result.finalText).toBe(expectedFinalText);
      } finally {
        stdio.restore();
      }
    });
  });

  it("falls back to Claude result text when no structured output field is present", async () => {
    const { ClaudeRunner } = await import("../src/runners/claude.js");
    await withTempSession(async (root) => {
      claudeState.messages = [{
        type: "result",
        subtype: "success",
        session_id: "claude-session",
        result: "{\"summary\":\"fallback\"}",
      }];
      const stdio = captureStdio();
      try {
        const result = await new ClaudeRunner({
          ...runnerOptions(root, "", "claude"),
          outputSchema: { type: "object", properties: { summary: { type: "string" } } },
        }).runPrompt("prompt");
        expect(result.finalText).toBe("{\"summary\":\"fallback\"}");
      } finally {
        stdio.restore();
      }
    });
  });

  it("turns Claude result errors into thrown errors", async () => {
    const { ClaudeRunner } = await import("../src/runners/claude.js");
    await withTempSession(async (root) => {
      claudeState.messages = [{ type: "result", subtype: "error", errors: ["api failed"] }];
      const stdio = captureStdio();
      try {
        await expect(new ClaudeRunner(runnerOptions(root, "", "claude")).runPrompt("prompt")).rejects.toThrow("api failed");
      } finally {
        stdio.restore();
      }
    });
  });

  it("turns Claude structured output retry exhaustion into a thrown error", async () => {
    const { ClaudeRunner } = await import("../src/runners/claude.js");
    await withTempSession(async (root) => {
      claudeState.messages = [{
        type: "result",
        subtype: "error_max_structured_output_retries",
        stop_reason: "max_structured_output_retries",
        errors: ["structured output did not match schema"],
      }];
      const stdio = captureStdio();
      try {
        await expect(new ClaudeRunner({
          ...runnerOptions(root, "", "claude"),
          outputSchema: { type: "object", required: ["summary"], properties: { summary: { type: "string" } } },
        }).runPrompt("prompt")).rejects.toThrow("structured output did not match schema");
      } finally {
        stdio.restore();
      }
    });
  });

  it("uses Claude transcript as final text when no result text is present", async () => {
    const { ClaudeRunner } = await import("../src/runners/claude.js");
    await withTempSession(async (root) => {
      claudeState.messages = [
        { type: "stream_event", session_id: "claude-session", event: { type: "content_block_delta", delta: { type: "text_delta", text: "transcript only" } } },
      ];
      const stdio = captureStdio();
      try {
        const result = await new ClaudeRunner(runnerOptions(root, "", "claude")).runPrompt("prompt");
        expect(result.finalText).toBe("transcript only");
      } finally {
        stdio.restore();
      }
    });
  });

  it("runs Gemini stream-json output and prepends system context to the user prompt", async () => {
    const { GeminiRunner } = await import("../src/runners/gemini.js");
    await withTempSession(async (root) => {
      childProcessState.stdoutLines = [
        JSON.stringify({ type: "init", sessionId: "gemini-session" }),
        JSON.stringify({ type: "message", message: { text: "hello" } }),
        JSON.stringify({ type: "result", response: "gemini final" }),
      ];
      childProcessState.stderrChunks = [];
      childProcessState.exitCode = 0;
      childProcessState.error = null;
      const systemContext = "## Agent Identity\n\nReply only in Chinese";
      const stdio = captureStdio();
      try {
        const result = await new GeminiRunner(runnerOptions(root, systemContext, "gemini")).runPrompt("prompt");

        expect(result).toMatchObject({
          provider: "gemini",
          sessionId: "gemini-session",
          finalText: "gemini final",
        });
        expect(childProcessState.spawnCalls.at(-1)).toMatchObject({
          command: "gemini",
          args: ["-p", `${systemContext}\n\nprompt`, "--output-format", "stream-json", "--approval-mode", "yolo"],
        });
      } finally {
        stdio.restore();
      }
    });
  });

  it("runs Gemini stream-json output and keeps stdout protocol clean", async () => {
    const { GeminiRunner } = await import("../src/runners/gemini.js");
    await withTempSession(async (root) => {
      childProcessState.stdoutLines = [
        JSON.stringify({ type: "init", sessionId: "gemini-session" }),
        JSON.stringify({ type: "message", message: { text: "hello" } }),
        JSON.stringify({ type: "tool_use", tool: { name: "ReadFile" } }),
        JSON.stringify({ type: "tool_result", result: { text: "file contents" } }),
        JSON.stringify({ type: "result", response: "gemini final" }),
        "not-json",
        "",
      ];
      childProcessState.stderrChunks = ["warn\n"];
      childProcessState.exitCode = 0;
      childProcessState.error = null;
      const stdio = captureStdio();
      try {
        const result = await new GeminiRunner(runnerOptions(root, "", "gemini")).runPrompt("prompt");

        expect(result).toMatchObject({
          provider: "gemini",
          sessionId: "gemini-session",
          finalText: "gemini final",
        });
        expect(result.transcript).toContain("hello");
        expect(result.transcript).toContain("[tool:ReadFile]");
        expect(result.transcript).toContain("file contents");
        expect(childProcessState.spawnCalls.at(-1)).toMatchObject({
          command: "gemini",
          args: ["-p", "prompt", "--output-format", "stream-json", "--approval-mode", "yolo"],
        });
      } finally {
        stdio.restore();
      }
    });
  });

  it("rejects structured output for Gemini until a native schema flag is available", async () => {
    const { GeminiRunner } = await import("../src/runners/gemini.js");
    await withTempSession(async (root) => {
      await expect(new GeminiRunner({
        ...runnerOptions(root, "", "gemini"),
        outputSchema: { type: "object" },
      }).runPrompt("prompt")).rejects.toThrow("structured JSON output is not supported by gemini runner");
    });
  });

  it("throws when Gemini exits unsuccessfully", async () => {
    const { GeminiRunner } = await import("../src/runners/gemini.js");
    await withTempSession(async (root) => {
      childProcessState.stdoutLines = [];
      childProcessState.stderrChunks = ["bad"];
      childProcessState.exitCode = 2;
      childProcessState.error = null;

      const stdio = captureStdio();
      try {
        await expect(new GeminiRunner(runnerOptions(root, "", "gemini")).runPrompt("prompt")).rejects.toThrow(
          "gemini exited with code 2: bad",
        );
      } finally {
        stdio.restore();
      }
    });
  });

  it("handles Gemini error and fallback result events", async () => {
    const { GeminiRunner } = await import("../src/runners/gemini.js");
    await withTempSession(async (root) => {
      childProcessState.stdoutLines = [
        JSON.stringify({ type: "message", content: { text: "content text" } }),
        JSON.stringify({ type: "message", text: { text: "text payload" } }),
        JSON.stringify({ type: "tool_use", name: "NamedTool" }),
        JSON.stringify({ type: "tool_use", toolName: "ToolName" }),
        JSON.stringify({ type: "tool_use" }),
        JSON.stringify({ type: "error", error: { message: "model error" } }),
        JSON.stringify({ type: "error", message: { text: "message error" } }),
        JSON.stringify({ type: "error", other: true }),
        JSON.stringify({ type: "tool_result", result: { nested: true } }),
        JSON.stringify({ type: "result", result: "fallback final", error: true }),
      ];
      childProcessState.stderrChunks = [];
      childProcessState.exitCode = 0;
      childProcessState.error = null;

      const stdio = captureStdio();
      try {
        const result = await new GeminiRunner(runnerOptions(root, "", "gemini")).runPrompt("prompt");
        expect(result.finalText).toBe("fallback final");
        expect(result.stopReason).toBe("error");
        expect(result.transcript).toContain("content text");
        expect(result.transcript).toContain("text payload");
        expect(result.transcript).toContain("[tool:NamedTool]");
        expect(result.transcript).toContain("[tool:ToolName]");
        expect(result.transcript).toContain("[tool:tool]");
        expect(result.transcript).toContain("model error");
        expect(result.transcript).toContain("message error");
        expect(result.transcript).toContain("\"other\": true");
        expect(result.transcript).toContain("\"nested\": true");
      } finally {
        stdio.restore();
      }
    });
  });

  it("rejects when the Gemini child process errors", async () => {
    const { GeminiRunner } = await import("../src/runners/gemini.js");
    await withTempSession(async (root) => {
      childProcessState.stdoutLines = [];
      childProcessState.stderrChunks = [];
      childProcessState.exitCode = 0;
      childProcessState.error = new Error("spawn failed");

      await expect(new GeminiRunner(runnerOptions(root, "", "gemini")).runPrompt("prompt")).rejects.toThrow("spawn failed");
      childProcessState.error = null;
    });
  });
});

import { EventEmitter } from "node:events";
import fs from "node:fs/promises";
import path from "node:path";
import { Readable } from "node:stream";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { captureStdio, runnerOptions, withTempSession } from "./helpers.js";

const processState = vi.hoisted(() => ({
  lines: [] as string[],
  stderr: [] as string[],
  exitCode: 0,
  error: null as Error | null,
  calls: [] as Array<{ command: string; args: string[]; options: Record<string, unknown> }>,
}));

vi.mock("node:child_process", () => ({
  spawn: vi.fn((command: string, args: string[], options: Record<string, unknown>) => {
    processState.calls.push({ command, args, options });
    const child = new EventEmitter() as EventEmitter & { stdout: Readable; stderr: EventEmitter };
    child.stdout = Readable.from(processState.lines.map((line) => `${line}\n`));
    child.stderr = new EventEmitter();
    const once = child.once.bind(child);
    child.once = ((event: string | symbol, listener: (...args: unknown[]) => void) => {
      if (event === "error" && processState.error) {
        queueMicrotask(() => listener(processState.error));
        return child;
      }
      if (event === "exit" && !processState.error) {
        queueMicrotask(() => listener(processState.exitCode));
        return child;
      }
      return once(event, listener);
    }) as typeof child.once;
    queueMicrotask(() => processState.stderr.forEach((chunk) => child.stderr.emit("data", chunk)));
    return child;
  }),
}));

describe("PiRunner", () => {
  afterEach(() => vi.unstubAllEnvs());

  beforeEach(() => {
    processState.lines = [];
    processState.stderr = [];
    processState.exitCode = 0;
    processState.error = null;
    processState.calls = [];
  });

  it("runs Pi deterministically, translates events, and persists its session", async () => {
    vi.stubEnv("LLM_API_ENDPOINT", "http://runtime.test/openai/v1");
    vi.stubEnv("LLM_API_PROTOCOL", "responses");
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      const skillDir = path.join(root, "home", ".agents", "skills", "review");
      await fs.mkdir(skillDir, { recursive: true });
      await fs.writeFile(path.join(skillDir, "SKILL.md"), "---\nname: review\n---\n");
      processState.lines = [
        JSON.stringify({ type: "session", id: "pi-session" }),
        JSON.stringify({ type: "message_update", assistantMessageEvent: { type: "text_delta", delta: "hel" }, message: { content: "hel" } }),
        JSON.stringify({ type: "message_update", assistantMessageEvent: { type: "text_delta", delta: "lo" }, message: { content: "hello" } }),
        JSON.stringify({ type: "tool_execution_start", toolName: "read" }),
        JSON.stringify({ type: "tool_execution_update", result: "ignored partial" }),
        JSON.stringify({ type: "tool_execution_end", result: { content: [{ type: "text", text: "contents" }] } }),
        JSON.stringify({ type: "message_end", message: { role: "assistant", content: [{ type: "text", text: "final answer" }] } }),
        JSON.stringify({ type: "agent_end", stopReason: "end_turn" }),
      ];
      const stdio = captureStdio();
      try {
        const result = await new PiRunner({
          ...runnerOptions(root, "system context", "pi"),
          model: "openai/gpt-5",
          skills: ["review"],
        }).runPrompt("user prompt");
        expect(result).toMatchObject({
          provider: "pi",
          threadId: "pi-session",
          stopReason: "end_turn",
          finalText: "final answer",
        });
        expect(result.transcript).toContain("hello");
        expect(result.transcript).toContain("[tool:read]");
        expect(result.transcript).toContain("contents");
        expect(result.transcript).not.toContain("ignored partial");
      } finally {
        stdio.restore();
      }

      const call = processState.calls[0];
      expect(call.command).toBe("pi");
      expect(call.args).toEqual(expect.arrayContaining([
        "--mode", "json", "--session-id", expect.any(String), "--model", "agent-compose/gpt-5",
        "--no-extensions", "--no-skills", "--no-context-files", "--no-approve", "--offline",
        "--skill", path.join(skillDir, "SKILL.md"), "user prompt",
      ]));
      const generatedSessionID = call.args[call.args.indexOf("--session-id") + 1];
      expect(generatedSessionID).toMatch(/^[0-9a-f]{64}$/);
      expect(call.options).toMatchObject({ cwd: path.join(root, "workspace") });
      expect(call.options.env).toMatchObject({
        HOME: path.join(root, "home"),
        PI_CODING_AGENT_DIR: path.join(root, "home", ".pi", "agent"),
        PI_OFFLINE: "1",
        PI_SKIP_VERSION_CHECK: "1",
        PI_TELEMETRY: "0",
      });
      const systemPath = call.args[call.args.indexOf("--append-system-prompt") + 1];
      await expect(fs.access(systemPath)).rejects.toThrow();
      const stored = JSON.parse(await fs.readFile(path.join(root, "state", "agents", "providers", "pi.json"), "utf8"));
      expect(stored.threadId).toBe("pi-session");
    });
  });

  it("materializes the managed model catalog inside the live guest home", async () => {
    vi.stubEnv("LLM_API_ENDPOINT", "http://runtime.test/openai/v1/");
    vi.stubEnv("LLM_API_PROTOCOL", "responses");
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      processState.lines = [JSON.stringify({ type: "session", id: "pi-session" })];
      await new PiRunner({ ...runnerOptions(root, "", "pi"), model: "openai/qwen3.6-27b" }).runPrompt("prompt");

      const catalog = JSON.parse(await fs.readFile(path.join(root, "home", ".pi", "agent", "models.json"), "utf8"));
      expect(catalog.providers["agent-compose"]).toMatchObject({
        baseUrl: "http://runtime.test/openai/v1",
        apiKey: "$AGENT_COMPOSE_SANDBOX_TOKEN",
        api: "openai-responses",
        models: [{ id: "qwen3.6-27b", name: "qwen3.6-27b" }],
      });
    });
  });

  it("resumes a stored session without replacing it after a failed run", async () => {
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      const providerDir = path.join(root, "state", "agents", "providers");
      await fs.mkdir(providerDir, { recursive: true });
      const statePath = path.join(providerDir, "pi.json");
      await fs.writeFile(statePath, JSON.stringify({ provider: "pi", threadId: "existing" }));
      processState.lines = [JSON.stringify({ type: "session", id: "replacement" })];
      processState.stderr = ["x".repeat(80 * 1024)];
      processState.exitCode = 2;
      const stdio = captureStdio();
      try {
        await expect(new PiRunner(runnerOptions(root, "", "pi")).runPrompt("prompt")).rejects.toThrow("pi exited with code 2");
      } finally {
        stdio.restore();
      }
      expect(processState.calls[0].args).toEqual(expect.arrayContaining(["--session-id", "existing"]));
      expect(JSON.parse(await fs.readFile(statePath, "utf8")).threadId).toBe("existing");
    });
  });

  it("fails fast for unsupported capabilities before spawning", async () => {
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      await expect(new PiRunner({
        ...runnerOptions(root, "", "pi"),
        mcpConfig: { docs: { type: "remote" } },
      }).runPrompt("prompt")).rejects.toThrow("does not support configured MCP servers");
      await expect(new PiRunner({
        ...runnerOptions(root, "", "pi"),
        outputSchema: { type: "object" },
      }).runPrompt("prompt")).rejects.toThrow("structured JSON output is not supported");
      expect(processState.calls).toHaveLength(0);
    });
  });

  it("fails before spawning when the managed facade environment is incomplete", async () => {
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      await expect(new PiRunner({ ...runnerOptions(root, "", "pi"), model: "openai/gpt-5" }).runPrompt("prompt"))
        .rejects.toThrow("Pi facade configuration is incomplete");
      expect(processState.calls).toHaveLength(0);
    });
  });

  it("rejects corrupt event streams and missing session headers", async () => {
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      processState.lines = ["not-json"];
      await expect(new PiRunner(runnerOptions(root, "", "pi")).runPrompt("prompt")).rejects.toThrow("invalid JSON event");
      processState.lines = [JSON.stringify({ type: "agent_end" })];
      await expect(new PiRunner(runnerOptions(root, "", "pi")).runPrompt("prompt")).rejects.toThrow("without emitting a session id");
    });
  });

  it("rejects missing, traversing, and symlink-escaping skills", async () => {
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      const skillsRoot = path.join(root, "home", ".agents", "skills");
      await fs.mkdir(skillsRoot, { recursive: true });
      await expect(new PiRunner({ ...runnerOptions(root, "", "pi"), skills: ["../secret"] }).runPrompt("prompt"))
        .rejects.toThrow("invalid pi skill name");
      await expect(new PiRunner({ ...runnerOptions(root, "", "pi"), skills: ["missing"] }).runPrompt("prompt"))
        .rejects.toThrow();
      const outside = path.join(root, "outside.md");
      await fs.writeFile(outside, "outside");
      const escape = path.join(skillsRoot, "escape");
      await fs.mkdir(escape);
      await fs.symlink(outside, path.join(escape, "SKILL.md"));
      await expect(new PiRunner({ ...runnerOptions(root, "", "pi"), skills: ["escape"] }).runPrompt("prompt"))
        .rejects.toThrow("escapes the skills directory");
    });
  });
});

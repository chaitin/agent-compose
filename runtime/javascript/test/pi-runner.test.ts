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
      if (event === "close" && !processState.error) {
        queueMicrotask(() => {
          processState.stderr.forEach((chunk) => child.stderr.emit("data", chunk));
          listener(processState.exitCode);
        });
        return child;
      }
      return once(event, listener);
    }) as typeof child.once;
    return child;
  }),
}));

describe("PiRunner", () => {
  afterEach(() => vi.unstubAllEnvs());

  beforeEach(() => {
    vi.stubEnv("AGENT_COMPOSE_RESOLVED_MODEL", "");
    processState.lines = [];
    processState.stderr = [];
    processState.exitCode = 0;
    processState.error = null;
    processState.calls = [];
  });

  it("runs Pi deterministically, translates events, and persists its session", async () => {
    vi.stubEnv("PI_CODING_AGENT_DIR", "/managed/pi-agent");
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      const skillDir = path.join(root, "home", ".agents", "skills", "review");
      await fs.mkdir(skillDir, { recursive: true });
      await fs.writeFile(path.join(skillDir, "SKILL.md"), "---\nname: review\n---\n");
      processState.lines = [
        JSON.stringify({ type: "session", id: "pi-session" }),
        JSON.stringify({ type: "message_update", assistantMessageEvent: { type: "text_delta", delta: "hel" }, message: { content: "hel" } }),
        JSON.stringify({ type: "message_update", assistantMessageEvent: { type: "text_delta", delta: "lo" }, message: { content: "hello" } }),
        JSON.stringify({ type: "tool_execution_start", toolName: "secret-tool" }),
        JSON.stringify({ type: "tool_execution_update", result: "ignored partial" }),
        JSON.stringify({ type: "tool_execution_end", result: { content: [{ type: "text", text: "secret tool output" }] } }),
        JSON.stringify({ type: "message_end", message: { role: "assistant", content: [{ type: "text", text: "final answer" }] } }),
        JSON.stringify({ type: "agent_end", stopReason: "end_turn" }),
      ];
      const stdio = captureStdio();
      try {
        const result = await new PiRunner({
          ...runnerOptions(root, "system context", "pi"),
          // The daemon publishes the guest-facing reference (provider namespace
          // included) as AGENT_COMPOSE_RESOLVED_MODEL; the runner forwards it.
          model: "openai/gpt-5",
          skills: ["review"],
        }).runPrompt("user prompt");
        expect(result).toMatchObject({
          provider: "pi",
          threadId: "pi-session",
          stopReason: "end_turn",
          finalText: "final answer",
          finalTextSource: "provider_message",
        });
        expect(result.transcript).toContain("hello");
        expect(result.transcript).not.toContain("secret-tool");
        expect(result.transcript).not.toContain("secret tool output");
        expect(result.transcript).not.toContain("ignored partial");
        expect(result.stderr).toBe("");
      } finally {
        stdio.restore();
      }

      const call = processState.calls[0];
      expect(call.command).toBe("pi");
      expect(call.args).toEqual(expect.arrayContaining([
        "--mode", "json", "--model", "agent-compose/gpt-5",
        "--no-extensions", "--no-skills", "--no-context-files", "--no-approve", "--offline",
        "--skill", path.join(skillDir, "SKILL.md"), "user prompt",
      ]));
      expect(call.args).not.toContain("--session-id");
      expect(call.options).toMatchObject({ cwd: path.join(root, "workspace") });
      expect(call.options.env).toMatchObject({
        HOME: path.join(root, "home"),
        PI_CODING_AGENT_DIR: "/managed/pi-agent",
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

  it("forwards a daemon-resolved model whose id contains slashes without stripping", async () => {
    vi.stubEnv("AGENT_COMPOSE_RESOLVED_MODEL", "agent-compose/anthropic/claude-3.5-sonnet");
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      processState.lines = [
        JSON.stringify({ type: "session", id: "pi-session" }),
        JSON.stringify({ type: "agent_end", stopReason: "end_turn" }),
      ];
      // The daemon resolved "openrouter/anthropic/claude-3.5-sonnet" down to
      // this reference. Stripping the first segment here would address
      // "agent-compose/claude-3.5-sonnet", a model models.json never declared.
      await new PiRunner({
        ...runnerOptions(root, "", "pi"),
        model: "gateway/ignored-model",
      }).runPrompt("prompt");
      const call = processState.calls[0];
      expect(call.args[call.args.indexOf("--model") + 1]).toBe("agent-compose/anthropic/claude-3.5-sonnet");
    });
  });

  it("marks Pi transcript fallback when no assistant message completes", async () => {
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      processState.lines = [
        JSON.stringify({ type: "session", id: "pi-session" }),
        JSON.stringify({ type: "message_update", assistantMessageEvent: { type: "text_delta", delta: "partial output" } }),
        JSON.stringify({ type: "agent_end", stopReason: "completed" }),
      ];
      const result = await new PiRunner(runnerOptions(root, "", "pi")).runPrompt("prompt");
      expect(result.finalText).toBe("partial output");
      expect(result.finalTextSource).toBe("transcript_fallback");
      expect(result.transcript).toBe(result.finalText);
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

  it("loads the managed MCP adapter with an invocation-scoped config", async () => {
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      processState.lines = [
        JSON.stringify({ type: "session", id: "pi-session" }),
        JSON.stringify({ type: "agent_end", stopReason: "completed" }),
      ];
      await new PiRunner({
        ...runnerOptions(root, "", "pi"),
        mcpConfig: {
          local: { type: "local", command: "server", args: ["--stdio"], env: { TOKEN: { value: "secret", secret: true } } },
          remote: { type: "remote", transport: "http", url: "https://mcp.example", headers: { Authorization: { value: "Bearer secret", secret: true } } },
        },
      }).runPrompt("prompt");
      const args = processState.calls[0].args;
      expect(args).toEqual(expect.arrayContaining([
        "--no-extensions",
        "--extension", "/usr/local/share/agent-compose/pi-mcp-adapter/index.ts",
        "--mcp-config",
      ]));
      const configPath = args[args.indexOf("--mcp-config") + 1];
      await expect(fs.access(configPath)).rejects.toThrow();
    });
  });

  it("fails fast for unsupported structured output before spawning", async () => {
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      await expect(new PiRunner({
        ...runnerOptions(root, "", "pi"),
        outputSchema: { type: "object" },
      }).runPrompt("prompt")).rejects.toThrow("structured JSON output is not supported");
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

  it("surfaces the final Pi model error instead of returning an empty turn", async () => {
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      processState.lines = [
        JSON.stringify({ type: "session", id: "pi-session" }),
        JSON.stringify({ type: "message_end", message: { role: "assistant", content: [], stopReason: "error", errorMessage: "OpenAI API error (401): Unauthorized" } }),
        JSON.stringify({ type: "agent_end", stopReason: "completed" }),
      ];
      await expect(new PiRunner(runnerOptions(root, "", "pi")).runPrompt("prompt"))
        .rejects.toThrow("pi model request failed: OpenAI API error (401): Unauthorized");
    });
  });

  it("accepts flattened Pi assistant error events", async () => {
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      processState.lines = [
        JSON.stringify({ type: "session", id: "pi-session" }),
        JSON.stringify({
          type: "message_end",
          role: "assistant",
          content: [],
          stop_reason: "error",
          error_message: "flattened failure",
        }),
        JSON.stringify({ type: "agent_end", stopReason: "completed" }),
      ];
      await expect(new PiRunner(runnerOptions(root, "", "pi")).runPrompt("prompt"))
        .rejects.toThrow("pi model request failed: flattened failure");
    });
  });

  it("rejects an agent-level error without an assistant error message", async () => {
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      processState.lines = [
        JSON.stringify({ type: "session", id: "pi-session" }),
        JSON.stringify({ type: "agent_end", stopReason: "error" }),
      ];
      await expect(new PiRunner(runnerOptions(root, "", "pi")).runPrompt("prompt"))
        .rejects.toThrow("pi agent ended with an error");
    });
  });

  it("collects stderr emitted before the child process closes", async () => {
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      processState.lines = [JSON.stringify({ type: "session", id: "pi-session" })];
      processState.stderr = ["late diagnostic"];
      processState.exitCode = 2;
      await expect(new PiRunner(runnerOptions(root, "", "pi")).runPrompt("prompt"))
        .rejects.toThrow("pi exited with code 2: late diagnostic");
    });
  });

  it("does not retain a retry error after a successful assistant response", async () => {
    const { PiRunner } = await import("../src/runners/pi.js");
    await withTempSession(async (root) => {
      processState.lines = [
        JSON.stringify({ type: "session", id: "pi-session" }),
        JSON.stringify({ type: "message_end", message: { role: "assistant", content: [], stopReason: "error", errorMessage: "temporary" } }),
        JSON.stringify({ type: "message_end", message: { role: "assistant", content: [{ type: "text", text: "recovered" }], stopReason: "stop" } }),
        JSON.stringify({ type: "agent_end", stopReason: "completed" }),
      ];
      const result = await new PiRunner(runnerOptions(root, "", "pi")).runPrompt("prompt");
      expect(result.finalText).toBe("recovered");
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

import { EventEmitter } from "node:events";
import { readFileSync } from "node:fs";
import fs from "node:fs/promises";
import path from "node:path";
import { Readable } from "node:stream";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { runnerOptions, withTempSession } from "./helpers.js";

const processState = vi.hoisted(() => ({
  lines: [] as string[],
  stderr: [] as Array<string | Buffer>,
  exitCode: 0,
  error: null as Error | null,
  calls: [] as Array<{ command: string; args: string[]; options: Record<string, unknown>; systemContextFileContent: string | undefined }>,
}));

vi.mock("node:child_process", () => ({
  spawn: vi.fn((command: string, args: string[], options: Record<string, unknown>) => {
    // Read eagerly: dsh.ts removes its temp dir once the process "exits" below,
    // so the file is gone by the time a test's `await runPrompt(...)` resolves.
    const systemContextFile = (options.env as Record<string, string> | undefined)?.DSH_SYSTEM_CONTEXT_FILE;
    const systemContextFileContent = systemContextFile ? readFileSync(systemContextFile, "utf8") : undefined;
    processState.calls.push({ command, args, options, systemContextFileContent });
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

function sessionEventLine(sessionId: string | undefined, event: Record<string, unknown>): string {
  return JSON.stringify({ type: "session_event", sessionId, event });
}

describe("DshRunner", () => {
  afterEach(() => vi.unstubAllEnvs());

  beforeEach(() => {
    processState.lines = [];
    processState.stderr = [];
    processState.exitCode = 0;
    processState.error = null;
    processState.calls = [];
  });

  it("creates a new session, streams text, and persists the host-generated session id", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      const skillDir = path.join(root, "home", ".agents", "skills", "review");
      await fs.mkdir(skillDir, { recursive: true });
      await fs.writeFile(path.join(skillDir, "SKILL.md"), "---\nname: review\n---\n");

      // Event lines omit sessionId (undefined): the runner only cross-checks
      // it when the field is present, so the fixture need not know the
      // host-generated uuid ahead of time.
      processState.lines = [
        sessionEventLine(undefined, { type: "assistant/chunk", seq: 1, time: 1, data: { turn: 0, step: 0, chunk: { type: "text-delta", index: 0, text: "hel" } } }),
        sessionEventLine(undefined, { type: "assistant/chunk", seq: 2, time: 2, data: { turn: 0, step: 0, chunk: { type: "text-delta", index: 0, text: "lo" } } }),
        sessionEventLine(undefined, { type: "tool/call", seq: 3, time: 3, data: { turn: 0, step: 0, callId: "c1", name: "secret-tool", arguments: "{}" } }),
        sessionEventLine(undefined, { type: "tool/result", seq: 4, time: 4, data: { turn: 0, step: 0, callId: "c1", message: { content: [{ content: [{ type: "text", text: "secret tool output" }] }] } } }),
        sessionEventLine(undefined, { type: "assistant/message", seq: 5, time: 5, data: { turn: 0, step: 0, message: { role: "assistant", content: [{ type: "text", text: "final answer" }] } } }),
        sessionEventLine(undefined, { type: "turn/end", seq: 6, time: 6, data: { turn: 0, reason: { kind: "completed" } } }),
      ];

      const result = await new DshRunner({
        ...runnerOptions(root, "system context", "dsh"),
        model: "deepseek-official/deepseek-v4-flash",
        effort: "high",
        skills: ["review"],
      }).runPrompt("user prompt");

      expect(result.provider).toBe("dsh");
      expect(result.stopReason).toBe("completed");
      expect(result.finalText).toBe("final answer");
      expect(result.finalTextSource).toBe("provider_message");
      expect(result.threadId).toMatch(/^session-[0-9a-f-]{36}$/);
      expect(result.transcript).toContain("hello");
      expect(result.transcript).not.toContain("secret-tool");
      expect(result.transcript).not.toContain("secret tool output");

      const call = processState.calls[0];
      expect(call.command).toBe("dsh");
      expect(call.args).toEqual(["--profile", "agent-compose"]);
      expect(call.options).toMatchObject({ cwd: path.join(root, "workspace") });
      const env = call.options.env as Record<string, string>;
      expect(env.HOME).toBe(path.join(root, "home"));
      expect(env.DSH_PERMISSION_MODE).toBe("danger-full-access");
      expect(env.DSH_MODEL).toBe("deepseek-v4-flash");
      expect(env.DSH_REASONING_EFFORT).toBe("high");
      expect(env.DSH_SKILL_DIRS).toBe(await fs.realpath(skillDir));
      expect(env.DSH_SESSION_ROOT).toBe(path.join(root, "state", "agents", "providers", "dsh", "sessions"));
      expect(env.DSH_SESSION_ID).toBe(result.threadId);
      expect(env.DSH_RESUME).toBeUndefined();
      expect(env.DSH_SYSTEM_CONTEXT).toBeUndefined();
      expect(env.DSH_SYSTEM_CONTEXT_FILE).toBe(path.join(path.dirname(env.DSH_PROMPT_FILE), "system-context.txt"));
      expect(call.systemContextFileContent).toBe("system context");
      await expect(fs.access(env.DSH_PROMPT_FILE)).rejects.toThrow();
      await expect(fs.access(env.DSH_SYSTEM_CONTEXT_FILE)).rejects.toThrow();

      const storedThread = JSON.parse(await fs.readFile(path.join(root, "state", "agents", "providers", "dsh.json"), "utf8"));
      expect(storedThread.threadId).toBe(result.threadId);
    });
  });

  it("keeps every slash after the provider id when the model remainder itself contains slashes", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      await new DshRunner({
        ...runnerOptions(root, "", "dsh"),
        model: "deepseek-official/org/deepseek-v4-flash",
      }).runPrompt("prompt");
      const env = processState.calls[0].options.env as Record<string, string>;
      expect(env.DSH_MODEL).toBe("org/deepseek-v4-flash");
    });
  });

  it("resumes a stored session with DSH_RESUME=1 and the stored id", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      const providerDir = path.join(root, "state", "agents", "providers");
      await fs.mkdir(providerDir, { recursive: true });
      await fs.writeFile(path.join(providerDir, "dsh.json"), JSON.stringify({ provider: "dsh", threadId: "session-existing" }));
      processState.lines = [
        sessionEventLine("session-existing", { type: "turn/end", seq: 1, time: 1, data: { turn: 0, reason: { kind: "completed" } } }),
      ];
      const result = await new DshRunner(runnerOptions(root, "", "dsh")).runPrompt("prompt");
      expect(result.threadId).toBe("session-existing");
      const call = processState.calls[0];
      const env = call.options.env as Record<string, string>;
      expect(env.DSH_SESSION_ID).toBe("session-existing");
      expect(env.DSH_RESUME).toBe("1");
    });
  });

  it.each([
    ["low", "high"],
    ["medium", "high"],
    ["high", "high"],
    ["xhigh", "max"],
    ["max", "max"],
  ] as const)("maps effort %s to DSH_REASONING_EFFORT=%s", async (effort, expected) => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      processState.lines = [];
      await new DshRunner({ ...runnerOptions(root, "", "dsh"), effort }).runPrompt("prompt");
      const env = processState.calls[0].options.env as Record<string, string>;
      expect(env.DSH_REASONING_EFFORT).toBe(expected);
    });
  });

  it("does not set DSH_REASONING_EFFORT when effort is unset", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      await new DshRunner(runnerOptions(root, "", "dsh")).runPrompt("prompt");
      const env = processState.calls[0].options.env as Record<string, string>;
      expect(env.DSH_REASONING_EFFORT).toBeUndefined();
    });
  });

  it("maps all six turn/end reason kinds to stopReason, surfacing structured errors", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      processState.lines = [
        sessionEventLine(undefined, { type: "turn/end", seq: 1, time: 1, data: { turn: 0, reason: { kind: "error", error: { code: "MISSING_CREDENTIAL", message: "no key" } } } }),
      ];
      await expect(new DshRunner(runnerOptions(root, "", "dsh")).runPrompt("prompt"))
        .rejects.toThrow("dsh turn ended with error (MISSING_CREDENTIAL): no key");
    });
    for (const kind of ["aborted", "blocked", "max-tokens", "interrupted"]) {
      await withTempSession(async (root) => {
        processState.lines = [
          sessionEventLine(undefined, { type: "turn/end", seq: 1, time: 1, data: { turn: 0, reason: { kind } } }),
        ];
        const result = await new DshRunner(runnerOptions(root, "", "dsh")).runPrompt("prompt");
        expect(result.stopReason).toBe(kind);
      });
    }
  });

  it("maps local and remote/http MCP servers onto DSH_MCP_SERVERS", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      await new DshRunner({
        ...runnerOptions(root, "", "dsh"),
        mcpConfig: {
          localFs: {
            type: "local",
            command: "npx",
            args: ["-y", "server"],
            env: { TOKEN: { value: "secret", secret: true } },
          },
          docs: {
            type: "remote",
            transport: "http",
            url: "https://docs.example.invalid/mcp",
            headers: { Authorization: { value: "Bearer token", secret: true } },
          },
        },
      }).runPrompt("prompt");

      const env = processState.calls[0].options.env as Record<string, string>;
      const servers = JSON.parse(env.DSH_MCP_SERVERS) as Array<Record<string, unknown>>;
      expect(servers).toHaveLength(2);

      const stdio = servers.find((s) => s.transport === "stdio");
      expect(stdio).toMatchObject({
        command: "npx",
        args: ["-y", "server"],
        env: { TOKEN: "secret" },
      });
      expect(stdio?.serverName).toMatch(/^[A-Za-z0-9_-]{1,32}$/);
      expect(stdio?.serverName).toMatch(/^localFs-/);

      const http = servers.find((s) => s.transport === "streamable-http");
      expect(http).toMatchObject({
        url: "https://docs.example.invalid/mcp",
        headers: { Authorization: "Bearer token" },
      });
      expect(http?.serverName).toMatch(/^[A-Za-z0-9_-]{1,32}$/);
      expect(http?.serverName).toMatch(/^docs-/);

      expect(stdio?.serverName).not.toBe(http?.serverName);
    });
  });

  it("does not set DSH_MCP_SERVERS when no MCP servers are configured", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      await new DshRunner(runnerOptions(root, "", "dsh")).runPrompt("prompt");
      const env = processState.calls[0].options.env as Record<string, string>;
      expect(env.DSH_MCP_SERVERS).toBeUndefined();
    });
  });

  it("carries a system context well past the exec() env-var limit, since it now goes through a file", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      const oversized = "x".repeat(129 * 1024);
      await new DshRunner(runnerOptions(root, oversized, "dsh")).runPrompt("prompt");
      const env = processState.calls[0].options.env as Record<string, string>;
      expect(env.DSH_SYSTEM_CONTEXT).toBeUndefined();
      expect(processState.calls[0].systemContextFileContent).toBe(oversized);
    });
  });

  it("does not set DSH_SYSTEM_CONTEXT_FILE when there is no system context", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      await new DshRunner(runnerOptions(root, "", "dsh")).runPrompt("prompt");
      const env = processState.calls[0].options.env as Record<string, string>;
      expect(env.DSH_SYSTEM_CONTEXT_FILE).toBeUndefined();
    });
  });

  it("clears a host-inherited DSH_SYSTEM_CONTEXT_FILE when this run has no system context", async () => {
    vi.stubEnv("DSH_SYSTEM_CONTEXT_FILE", "/host/leaked/persona.txt");
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      await new DshRunner(runnerOptions(root, "", "dsh")).runPrompt("prompt");
      const env = processState.calls[0].options.env as Record<string, string>;
      expect(env.DSH_SYSTEM_CONTEXT_FILE).toBeUndefined();
    });
  });

  it("clears a host-inherited DSH_MCP_SERVERS when this run has no MCP servers", async () => {
    vi.stubEnv("DSH_MCP_SERVERS", JSON.stringify([{ transport: "stdio", serverName: "leaked", command: "evil" }]));
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      await new DshRunner(runnerOptions(root, "", "dsh")).runPrompt("prompt");
      const env = processState.calls[0].options.env as Record<string, string>;
      expect(env.DSH_MCP_SERVERS).toBeUndefined();
    });
  });

  it("clears inherited DSH_RESUME/DSH_REASONING_EFFORT/DSH_SKILL_DIRS when this run doesn't set them", async () => {
    vi.stubEnv("DSH_RESUME", "1");
    vi.stubEnv("DSH_REASONING_EFFORT", "max");
    vi.stubEnv("DSH_SKILL_DIRS", "/host/leaked/skills");
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      // No stored thread (so resume=false), no effort/skills configured.
      await new DshRunner(runnerOptions(root, "", "dsh")).runPrompt("prompt");
      const env = processState.calls[0].options.env as Record<string, string>;
      expect(env.DSH_RESUME).toBeUndefined();
      expect(env.DSH_REASONING_EFFORT).toBeUndefined();
      expect(env.DSH_SKILL_DIRS).toBeUndefined();
    });
  });

  it("keeps an inherited DSH_MODEL when the agent names no model", async () => {
    // DSH_MODEL is deliberately exempt from the clearing rule the test above
    // covers: the daemon's facade config sets it to the model it resolved and
    // minted the run's token against, and the runner cannot tell that apart
    // from ambient environment. Dropping it would send DSH to the profile's
    // hardcoded fallback model, which the facade token does not authorise.
    // Unlike DSH_SKILL_DIRS an unexpected value here is not a privilege
    // escalation: the facade rejects a model the token is not bound to.
    vi.stubEnv("DSH_MODEL", "daemon-resolved-model");
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      await new DshRunner(runnerOptions(root, "", "dsh")).runPrompt("prompt");
      const env = processState.calls[0].options.env as Record<string, string>;
      expect(env.DSH_MODEL).toBe("daemon-resolved-model");
    });
  });

  it("overrides an inherited DSH_MODEL when the agent does name one", async () => {
    vi.stubEnv("DSH_MODEL", "daemon-resolved-model");
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      await new DshRunner({ ...runnerOptions(root, "", "dsh"), model: "default/configured-model" }).runPrompt("prompt");
      const env = processState.calls[0].options.env as Record<string, string>;
      expect(env.DSH_MODEL).toBe("configured-model");
    });
  });

  it("fails fast when DSH_MCP_SERVERS would exceed the exec() argument limit", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      await expect(new DshRunner({
        ...runnerOptions(root, "", "dsh"),
        mcpConfig: {
          docs: {
            type: "remote",
            transport: "http",
            url: "https://docs.example.invalid/mcp",
            headers: { Authorization: { value: "x".repeat(129 * 1024) } },
          },
        },
      }).runPrompt("prompt")).rejects.toThrow(/DSH_MCP_SERVERS is \d+ bytes, exceeding the \d+-byte exec\(\) argument limit/);
      expect(processState.calls).toHaveLength(0);
    });
  });

  it("fails fast for sse-transport MCP servers before spawning", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      await expect(new DshRunner({
        ...runnerOptions(root, "", "dsh"),
        mcpConfig: {
          docs: { type: "remote", transport: "sse", url: "https://docs.example.invalid/sse" },
        },
      }).runPrompt("prompt")).rejects.toThrow('does not support MCP transport "sse" (server "docs")');
      expect(processState.calls).toHaveLength(0);
    });
  });

  it("sanitizes MCP server names deterministically", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    const buildOptions = (root: string) => ({
      ...runnerOptions(root, "", "dsh"),
      mcpConfig: {
        "docs server (v2)!": { type: "local" as const, command: "server" },
      },
    });
    let firstServerName: string | undefined;
    await withTempSession(async (root) => {
      await new DshRunner(buildOptions(root)).runPrompt("prompt");
      const env = processState.calls[0].options.env as Record<string, string>;
      const servers = JSON.parse(env.DSH_MCP_SERVERS) as Array<Record<string, unknown>>;
      firstServerName = servers[0].serverName as string;
      expect(firstServerName).toMatch(/^[A-Za-z0-9_-]{1,32}$/);
    });
    await withTempSession(async (root) => {
      await new DshRunner(buildOptions(root)).runPrompt("prompt");
      const env = processState.calls[0].options.env as Record<string, string>;
      const servers = JSON.parse(env.DSH_MCP_SERVERS) as Array<Record<string, unknown>>;
      expect(servers[0].serverName).toBe(firstServerName);
    });
  });

  it("fails fast for structured output before spawning", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      await expect(new DshRunner({
        ...runnerOptions(root, "", "dsh"),
        outputSchema: { type: "object" },
      }).runPrompt("prompt")).rejects.toThrow("structured JSON output is not supported");
      expect(processState.calls).toHaveLength(0);
    });
  });

  it("rejects malformed JSON lines and events for an unexpected session", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      processState.lines = ["not-json"];
      await expect(new DshRunner(runnerOptions(root, "", "dsh")).runPrompt("prompt"))
        .rejects.toThrow("invalid JSON event");
    });
    await withTempSession(async (root) => {
      processState.lines = [sessionEventLine("some-other-session", { type: "turn/end", seq: 1, time: 1, data: { turn: 0, reason: { kind: "completed" } } })];
      await expect(new DshRunner(runnerOptions(root, "", "dsh")).runPrompt("prompt"))
        .rejects.toThrow("unexpected session");
    });
  });

  it("surfaces a non-zero exit with truncated stderr", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      processState.lines = [];
      processState.stderr = ["late diagnostic"];
      processState.exitCode = 2;
      await expect(new DshRunner(runnerOptions(root, "", "dsh")).runPrompt("prompt"))
        .rejects.toThrow("dsh exited with code 2: late diagnostic");
    });
  });

  it("keeps successful stderr diagnostics out of the transcript and final text", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      const stderrWrite = vi.spyOn(process.stderr, "write").mockImplementation(() => true);
      processState.stderr = ["MCP server startup log\n"];

      const result = await new DshRunner(runnerOptions(root, "", "dsh")).runPrompt("prompt");

      expect(stderrWrite).toHaveBeenCalledWith(Buffer.from("MCP server startup log\n"));
      expect(result.stderr).toBe("MCP server startup log\n");
      expect(result.transcript).toBe("");
      expect(result.finalText).toBe("");
      expect(result.finalTextSource).toBe("none");
      stderrWrite.mockRestore();
    });
  });

  it("forwards raw stderr bytes without corrupting a split UTF-8 character", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      const stderrWrite = vi.spyOn(process.stderr, "write").mockImplementation(() => true);
      const multibyte = Buffer.from("好");
      processState.stderr = [multibyte.subarray(0, 1), multibyte.subarray(1)];

      const result = await new DshRunner(runnerOptions(root, "", "dsh")).runPrompt("prompt");

      const forwarded = Buffer.concat(stderrWrite.mock.calls.map(([chunk]) => Buffer.from(chunk as Uint8Array)));
      expect(forwarded).toEqual(multibyte);
      expect(result.stderr).toBe("好");
      stderrWrite.mockRestore();
    });
  });

  it("propagates a spawn failure", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      processState.error = new Error("ENOENT: dsh not found");
      await expect(new DshRunner(runnerOptions(root, "", "dsh")).runPrompt("prompt"))
        .rejects.toThrow("ENOENT");
    });
  });

  it("rejects missing, traversing, and symlink-escaping skills", async () => {
    const { DshRunner } = await import("../src/runners/dsh.js");
    await withTempSession(async (root) => {
      const skillsRoot = path.join(root, "home", ".agents", "skills");
      await fs.mkdir(skillsRoot, { recursive: true });
      await expect(new DshRunner({ ...runnerOptions(root, "", "dsh"), skills: ["../secret"] }).runPrompt("prompt"))
        .rejects.toThrow("invalid dsh skill name");
      await expect(new DshRunner({ ...runnerOptions(root, "", "dsh"), skills: ["missing"] }).runPrompt("prompt"))
        .rejects.toThrow();
      const outside = path.join(root, "outside.md");
      await fs.writeFile(outside, "outside");
      const escape = path.join(skillsRoot, "escape");
      await fs.mkdir(escape);
      await fs.symlink(outside, path.join(escape, "SKILL.md"));
      await expect(new DshRunner({ ...runnerOptions(root, "", "dsh"), skills: ["escape"] }).runPrompt("prompt"))
        .rejects.toThrow("escapes the skills directory");
    });
  });
});

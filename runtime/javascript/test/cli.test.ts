import fs from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createProgram, isMainModule, main } from "../src/cli.js";
import { COMMAND_RESULT_PREFIX, RESULT_PREFIX } from "../src/constants.js";
import * as commandModule from "../src/command.js";
import * as promptModule from "../src/prompt.js";
import * as serviceModule from "../src/service.js";
import { captureStdio, withTempSession } from "./helpers.js";

describe("commander CLI", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("prints the prefixed result for prompt command", async () => {
    const runPrompt = vi.spyOn(promptModule, "runPromptCommand").mockResolvedValue({
      provider: "codex",
      sessionId: "s1",
      stopReason: "completed",
      finalText: "done",
      json: null,
      transcript: "done",
      stderr: "",
    });
    const stdio = captureStdio();
    try {
      await createProgram({ exitOverride: true }).parseAsync([
        "node",
        "cli",
        "prompt",
        "--provider",
        "codex",
        "--message-file",
        "/tmp/message.txt",
        "--state-root",
        "/data/state",
        "--workspace",
        "/data/workspace",
        "--home",
        "/data/home",
        "--model",
        "anthropic/claude-sonnet-4-5",
        "--output-schema-file",
        "/tmp/schema.json",
      ]);
    } finally {
      stdio.restore();
    }

    expect(runPrompt).toHaveBeenCalledWith({
      provider: "codex",
      messageFile: "/tmp/message.txt",
      stateRoot: "/data/state",
      workspace: "/data/workspace",
      home: "/data/home",
      model: "anthropic/claude-sonnet-4-5",
      outputSchemaFile: "/tmp/schema.json",
    });
    expect(stdio.stdout).toBe(`${RESULT_PREFIX}{"provider":"codex","sessionId":"s1","stopReason":"completed","finalText":"done","json":null,"transcript":"done","stderr":""}\n`);
  });

  it("rejects missing required options through commander", async () => {
    const program = createProgram({ exitOverride: true });
    const stdio = captureStdio();
    try {
      await expect(program.parseAsync(["node", "cli", "prompt", "--provider", "codex"])).rejects.toMatchObject({
        code: "commander.missingMandatoryOptionValue",
      });
    } finally {
      stdio.restore();
    }
  });

  it("rejects an empty prompt provider", async () => {
    const runPrompt = vi.spyOn(promptModule, "runPromptCommand");
    const program = createProgram({ exitOverride: true });
    const stdio = captureStdio();
    try {
      await expect(program.parseAsync([
        "node",
        "cli",
        "prompt",
        "--provider",
        "",
        "--message-file",
        "/tmp/message.txt",
      ])).rejects.toThrow(/provider is required/);
    } finally {
      stdio.restore();
    }
    expect(runPrompt).toHaveBeenCalledTimes(1);
  });

  it("prints the prefixed result for exec command", async () => {
    const runExec = vi.spyOn(commandModule, "runExecCommand").mockResolvedValue({
      stdout: "ok\n",
      stderr: "",
      output: "ok\n",
      exitCode: 0,
      success: true,
      stdoutTruncated: false,
      stderrTruncated: false,
      outputTruncated: false,
      artifacts: {
        stdout: "/tmp/stdout.txt",
        stderr: "/tmp/stderr.txt",
        output: "/tmp/output.txt",
        request: "/tmp/request.json",
        result: "/tmp/result.json",
      },
    });
    const stdio = captureStdio();
    try {
      await createProgram({ exitOverride: true }).parseAsync([
        "node",
        "cli",
        "exec",
        "--request-file",
        "/tmp/request.json",
        "--state-root",
        "/data/state",
        "--workspace",
        "/data/workspace",
        "--home",
        "/data/home",
      ]);
    } finally {
      stdio.restore();
    }

    expect(runExec).toHaveBeenCalledWith({
      requestFile: "/tmp/request.json",
      stateRoot: "/data/state",
      workspace: "/data/workspace",
      home: "/data/home",
    });
    expect(stdio.stdout).toBe(`${COMMAND_RESULT_PREFIX}{"stdout":"ok\\n","stderr":"","output":"ok\\n","exitCode":0,"success":true,"stdoutTruncated":false,"stderrTruncated":false,"outputTruncated":false,"artifacts":{"stdout":"/tmp/stdout.txt","stderr":"/tmp/stderr.txt","output":"/tmp/output.txt","request":"/tmp/request.json","result":"/tmp/result.json"}}\n`);
  });

  it("prints the prefixed result for service command", async () => {
    const runService = vi.spyOn(serviceModule, "runServiceCommand").mockResolvedValue({
      serviceName: "echo",
      outputJson: "{\"ok\":true}",
      success: true,
      artifacts: { request: "/tmp/service-request.json", result: "/tmp/service-result.json" },
      metrics: { durationMs: "1" },
    });
    const stdio = captureStdio();
    try {
      await createProgram({ exitOverride: true }).parseAsync([
        "node",
        "cli",
        "service",
        "--request-json",
        "{\"entry\":\"service.js\"}",
        "--state-root",
        "/data/state",
        "--workspace",
        "/workspace",
      ]);
    } finally {
      stdio.restore();
    }

    expect(runService).toHaveBeenCalledWith({
      requestJson: "{\"entry\":\"service.js\"}",
      stateRoot: "/data/state",
      workspace: "/workspace",
    });
    expect(stdio.stdout).toBe("__SERVICE_RESULT__{\"serviceName\":\"echo\",\"outputJson\":\"{\\\"ok\\\":true}\",\"success\":true,\"artifacts\":{\"request\":\"/tmp/service-request.json\",\"result\":\"/tmp/service-result.json\"},\"metrics\":{\"durationMs\":\"1\"}}\n");
  });

  it("main parses argv through the configured program", async () => {
    vi.spyOn(promptModule, "runPromptCommand").mockResolvedValue({
      provider: "codex",
      sessionId: "s2",
      stopReason: "completed",
      finalText: "ok",
      json: null,
      transcript: "ok",
      stderr: "",
    });
    const stdio = captureStdio();
    try {
      await main(["node", "cli", "prompt", "--provider", "codex", "--message-file", "/tmp/message.txt"]);
    } finally {
      stdio.restore();
    }
    expect(stdio.stdout).toContain("\"sessionId\":\"s2\"");
  });

  it("recognizes a symlinked bin path as the main module", async () => {
    await withTempSession(async (root) => {
      const realEntry = path.join(root, "dist", "cli.js");
      const linkedEntry = path.join(root, "bin", "agent-compose-runtime");
      await fs.mkdir(path.dirname(realEntry), { recursive: true });
      await fs.mkdir(path.dirname(linkedEntry), { recursive: true });
      await fs.writeFile(realEntry, "#!/usr/bin/env node\n", "utf8");
      await fs.symlink(realEntry, linkedEntry);

      const canonicalEntry = await fs.realpath(realEntry);
      expect(isMainModule(pathToFileURL(canonicalEntry).href, linkedEntry)).toBe(true);
    });
  });

  it("runPromptCommand reads the message file and resolves default paths", async () => {
    await withTempSession(async (root) => {
      const messageFile = path.join(root, "message.txt");
      const schemaFile = path.join(root, "schema.json");
      const stateRoot = path.join(root, "state");
      const systemPromptPath = path.join(stateRoot, "agents", "system-prompts", "system-prompt.txt");
      await fs.mkdir(stateRoot, { recursive: true });
      await fs.mkdir(path.dirname(systemPromptPath), { recursive: true });
      await fs.writeFile(messageFile, "hello", "utf8");
      await fs.writeFile(schemaFile, JSON.stringify({ type: "object", properties: { answer: { type: "string" } } }), "utf8");
      await fs.writeFile(systemPromptPath, "system body", "utf8");
      const runPrompt = vi.fn().mockResolvedValue({
        provider: "gemini",
        sessionId: "",
        stopReason: "completed",
        finalText: "ok",
        transcript: "ok",
        stderr: "",
      });
      const geminiSpy = vi.spyOn(await import("../src/runners/gemini.js"), "GeminiRunner").mockImplementation(function mockGemini(this: unknown, options: unknown) {
        Object.assign(this as object, { options, runPrompt });
      } as never);
      const { runPromptCommand } = await import("../src/prompt.js");
      const oldWorkspace = process.env.WORKSPACE;
      process.env.WORKSPACE = path.join(root, "workspace-from-env");
      try {
        const result = await runPromptCommand({
          provider: "gemini",
          messageFile,
          model: "models/gemini-test",
          outputSchemaFile: schemaFile,
          stateRoot,
          home: path.join(root, "home"),
        });

        expect(result.finalText).toBe("ok");
        expect(runPrompt).toHaveBeenCalledWith("hello");
        expect(geminiSpy).toHaveBeenCalledWith(expect.objectContaining({
          provider: "gemini",
          workspace: path.join(root, "workspace-from-env"),
          home: path.join(root, "home"),
          model: "models/gemini-test",
          systemContext: expect.stringContaining("system body"),
          runtimeRoot: path.join(root, "runtime"),
          outputSchema: { type: "object", properties: { answer: { type: "string" } } },
        }));
      } finally {
        if (oldWorkspace === undefined) {
          delete process.env.WORKSPACE;
        } else {
          process.env.WORKSPACE = oldWorkspace;
        }
      }
    });
  });

  it("runPromptCommand composes agent identity and mpi into systemContext", async () => {
    await withTempSession(async (root) => {
      const messageFile = path.join(root, "message.txt");
      const stateRoot = path.join(root, "state");
      const mpiRoot = path.join(root, "runtime", "mpi");
      const systemPromptPath = path.join(stateRoot, "agents", "system-prompts", "system-prompt.txt");
      await fs.mkdir(path.dirname(systemPromptPath), { recursive: true });
      await fs.mkdir(mpiRoot, { recursive: true });
      await fs.writeFile(messageFile, "task body", "utf8");
      await fs.writeFile(systemPromptPath, "Reply only in Chinese", "utf8");
      await fs.writeFile(path.join(mpiRoot, "catalog.md"), "# Email tools\n", "utf8");

      const runPrompt = vi.fn().mockResolvedValue({
        provider: "codex",
        sessionId: "",
        stopReason: "completed",
        finalText: "ok",
        transcript: "ok",
        stderr: "",
      });
      const codexSpy = vi.spyOn(await import("../src/runners/codex.js"), "CodexRunner").mockImplementation(function mockCodex(this: unknown, options: unknown) {
        Object.assign(this as object, { options, runPrompt });
      } as never);
      const { runPromptCommand } = await import("../src/prompt.js");

      await runPromptCommand({
        provider: "codex",
        messageFile,
        stateRoot,
        workspace: path.join(root, "workspace"),
        home: path.join(root, "home"),
      });

      expect(runPrompt).toHaveBeenCalledWith("task body");
      const options = codexSpy.mock.calls.at(-1)?.[0] as { systemContext: string };
      expect(options.systemContext).toContain("## Agent Identity");
      expect(options.systemContext).toContain("Reply only in Chinese");
      expect(options.systemContext).toContain("## MPI Catalog");
      expect(options.systemContext).toContain("# Email tools");
    });
  });

  it("runPromptCommand rejects invalid output schema files", async () => {
    await withTempSession(async (root) => {
      const messageFile = path.join(root, "message.txt");
      const schemaFile = path.join(root, "schema.json");
      await fs.writeFile(messageFile, "hello", "utf8");
      await fs.writeFile(schemaFile, "[]", "utf8");
      const { runPromptCommand } = await import("../src/prompt.js");

      await expect(runPromptCommand({
        provider: "codex",
        messageFile,
        outputSchemaFile: schemaFile,
      })).rejects.toThrow("--output-schema-file must contain a JSON object");
    });
  });
});

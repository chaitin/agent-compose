import fs from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createProgram, isMainModule, main } from "../src/cli.js";
import { COMMAND_RESULT_PREFIX, RESULT_PREFIX } from "../src/constants.js";
import * as commandModule from "../src/command.js";
import * as promptModule from "../src/prompt.js";
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
      await fs.mkdir(stateRoot, { recursive: true });
      await fs.writeFile(messageFile, "hello", "utf8");
      await fs.writeFile(schemaFile, JSON.stringify({ type: "object", properties: { answer: { type: "string" } } }), "utf8");
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

import fs from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";
import {
  RuntimeWorkflowError,
  RuntimeWorkflowProtocolError,
  RuntimeWorkflowTimeoutError,
  runtime,
} from "../src/index.js";
import { withTempDir } from "./helpers.js";

describe("runtime.workflow", () => {
  it("streams events, preserves raw stderr, parses the result, and cleans temporary files", async () => {
    await withTempDir(async (root) => {
      const callsFile = path.join(root, "calls.json");
      const restorePath = await installFakeRuntime(root, [
        "const fs = require('node:fs');",
        "const args = process.argv.slice(2);",
        "const scriptFile = args[args.indexOf('--script-file') + 1];",
        "const argsFile = args[args.indexOf('--args-file') + 1];",
        `fs.writeFileSync(${JSON.stringify(callsFile)}, JSON.stringify({ args, scriptFile, argsFile, script: fs.readFileSync(scriptFile, 'utf8'), input: JSON.parse(fs.readFileSync(argsFile, 'utf8')) }));`,
        "process.stderr.write('__WORKFLOW_EVE');",
        "process.stderr.write('NT__' + JSON.stringify({ type: 'phase', runId: 'run_sdk', title: 'Scan' }) + '\\n');",
        "process.stderr.write('provider transcript\\n');",
        "process.stdout.write('__WORKFLOW_RES');",
        "process.stdout.write('ULT__' + JSON.stringify({ runId: 'run_sdk', status: 'completed', meta: { name: 'sdk', description: 'test' }, result: { ok: true }, phases: ['Scan'], logs: [], agents: [], agentCount: 0, durationMs: 2 }) + '\\n');",
      ]);
      const updates: string[] = [];
      try {
        const result = await runtime.workflow("export const meta = { name: 'sdk', description: 'test' }; return args", {
          args: { value: 7 },
          workspace: root,
          stateRoot: path.join(root, "state"),
          home: path.join(root, "home"),
          provider: "claude",
          model: "claude-sonnet",
          concurrency: 3,
          tokenBudget: 500,
          runId: "run_sdk",
          resumeRunId: "run_old",
          onUpdate: (event) => updates.push(event.type),
        });

        expect(result).toMatchObject({
          status: "completed",
          result: { ok: true },
          stderr: "provider transcript\n",
        });
        expect(result.events).toEqual([{ type: "phase", runId: "run_sdk", title: "Scan" }]);
        expect(updates).toEqual(["phase"]);
      } finally {
        restorePath();
      }

      const call = JSON.parse(await fs.readFile(callsFile, "utf8"));
      expect(call.input).toEqual({ value: 7 });
      expect(call.script).toContain("export const meta");
      expect(call.args).toEqual(expect.arrayContaining([
        "workflow", "--provider", "claude", "--model", "claude-sonnet",
        "--concurrency", "3", "--token-budget", "500", "--run-id", "run_sdk",
        "--resume-run-id", "run_old",
      ]));
      await expect(fs.access(call.scriptFile)).rejects.toThrow();
      await expect(fs.access(call.argsFile)).rejects.toThrow();
    });
  });

  it("workflowFile passes the caller file directly and exposes typed failed outcomes", async () => {
    await withTempDir(async (root) => {
      const callsFile = path.join(root, "calls.json");
      const scriptFile = path.join(root, "workflow.js");
      await fs.writeFile(scriptFile, "export const meta = { name: 'x', description: 'y' }");
      const restorePath = await installFakeRuntime(root, [
        "const fs = require('node:fs');",
        "const args = process.argv.slice(2);",
        `fs.writeFileSync(${JSON.stringify(callsFile)}, JSON.stringify(args));`,
        "process.stdout.write('__WORKFLOW_RESULT__' + JSON.stringify({ runId: 'run_failed', status: 'failed', error: { message: 'agent failed' }, phases: [], logs: [], agents: [], durationMs: 1 }) + '\\n');",
      ]);
      try {
        await expect(runtime.workflowFile(scriptFile, { workspace: root })).rejects.toMatchObject({
          name: "RuntimeWorkflowError",
          outcome: { runId: "run_failed", status: "failed", error: { message: "agent failed" } },
        });
      } finally {
        restorePath();
      }
      const args = JSON.parse(await fs.readFile(callsFile, "utf8"));
      expect(args[args.indexOf("--script-file") + 1]).toBe(scriptFile);
    });
  });

  it("rejects missing result payloads and timeouts with typed errors", async () => {
    await withTempDir(async (root) => {
      let restorePath = await installFakeRuntime(root, ["process.stderr.write('no result\\n');"]);
      try {
        await expect(runtime.workflow("export const meta = { name: 'x', description: 'y' }", { workspace: root }))
          .rejects.toBeInstanceOf(RuntimeWorkflowProtocolError);
      } finally {
        restorePath();
      }

      restorePath = await installFakeRuntime(root, ["setInterval(() => {}, 1000);"]);
      try {
        await expect(runtime.workflow("export const meta = { name: 'x', description: 'y' }", { workspace: root, timeoutMs: 20 }))
          .rejects.toBeInstanceOf(RuntimeWorkflowTimeoutError);
      } finally {
        restorePath();
      }
    });
  });

  it("exports workflow error classes", () => {
    expect(RuntimeWorkflowError).toBeTypeOf("function");
  });

  it("decodes UTF-8 characters split across chunks", async () => {
    await withTempDir(async (root) => {
      const restorePath = await installFakeRuntime(root, [
        "const payload = '__WORKFLOW_RESULT__' + JSON.stringify({ runId: 'run_utf8', status: 'completed', meta: { name: 'utf8', description: '中文' }, result: '完成', phases: [], logs: [], agents: [], agentCount: 0, durationMs: 1 }) + '\\n';",
        "const bytes = Buffer.from(payload);",
        "const marker = Buffer.from('中文');",
        "const split = bytes.indexOf(marker) + 1;",
        "process.stdout.write(bytes.subarray(0, split));",
        "setTimeout(() => process.stdout.write(bytes.subarray(split)), 5);",
      ]);
      try {
        await expect(runtime.workflow("export const meta = { name: 'x', description: 'y' }", { workspace: root }))
          .resolves.toMatchObject({ result: "完成", meta: { description: "中文" } });
      } finally {
        restorePath();
      }
    });
  });

  it("rejects structurally invalid result and event payloads", async () => {
    await withTempDir(async (root) => {
      let restorePath = await installFakeRuntime(root, ["process.stdout.write('__WORKFLOW_RESULT__{}\\n');"]);
      try {
        await expect(runtime.workflow("export const meta = { name: 'x', description: 'y' }", { workspace: root }))
          .rejects.toBeInstanceOf(RuntimeWorkflowProtocolError);
      } finally {
        restorePath();
      }

      restorePath = await installFakeRuntime(root, [
        "process.stderr.write('__WORKFLOW_EVENT__' + JSON.stringify({ type: 'phase', runId: 3, title: 'bad' }) + '\\n');",
        "process.stdout.write('__WORKFLOW_RESULT__' + JSON.stringify({ runId: 'run', status: 'completed', meta: { name: 'x', description: 'y' }, result: null, phases: [], logs: [], agents: [], agentCount: 0, durationMs: 1 }) + '\\n');",
      ]);
      try {
        await expect(runtime.workflow("export const meta = { name: 'x', description: 'y' }", { workspace: root }))
          .rejects.toBeInstanceOf(RuntimeWorkflowProtocolError);
      } finally {
        restorePath();
      }
    });
  });

  it("returns a typed aborted outcome when the CLI handles the timeout signal", async () => {
    await withTempDir(async (root) => {
      const restorePath = await installFakeRuntime(root, [
        "process.on('SIGTERM', () => {",
        "  process.stdout.write('__WORKFLOW_RESULT__' + JSON.stringify({ runId: 'run_aborted', status: 'aborted', error: { message: 'workflow aborted' }, phases: [], logs: [], agents: [], durationMs: 10 }) + '\\n');",
        "  process.exit(0);",
        "});",
        "setInterval(() => {}, 1000);",
      ]);
      try {
        await expect(runtime.workflow("export const meta = { name: 'x', description: 'y' }", { workspace: root, timeoutMs: 100 }))
          .rejects.toMatchObject({
            name: "RuntimeWorkflowError",
            outcome: { runId: "run_aborted", status: "aborted" },
          });
      } finally {
        restorePath();
      }
    });
  });
});

async function installFakeRuntime(root: string, body: string[]): Promise<() => void> {
  const binDir = path.join(root, "bin");
  await fs.mkdir(binDir, { recursive: true });
  const executable = path.join(binDir, "agent-compose-runtime");
  await fs.writeFile(executable, ["#!/usr/bin/env node", ...body].join("\n"), "utf8");
  await fs.chmod(executable, 0o755);
  const previous = process.env.PATH;
  process.env.PATH = `${binDir}${path.delimiter}${previous ?? ""}`;
  return () => {
    process.env.PATH = previous;
  };
}

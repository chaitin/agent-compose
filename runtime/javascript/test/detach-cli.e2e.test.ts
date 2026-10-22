import fs from "node:fs/promises";
import path from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createProgram } from "../src/cli.js";
import * as detachCommand from "../src/detach/command.js";
import * as supervisorModule from "../src/detach/supervisor.js";
import { captureStdio, withTempSession } from "./helpers.js";

describe("detach CLI commands", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    process.exitCode = undefined;
  });

  it("prints detach capability as JSON", async () => {
    const stdio = captureStdio();
    try {
      await createProgram({ exitOverride: true }).parseAsync(["node", "cli", "capabilities", "--json"]);
    } finally {
      stdio.restore();
    }
    expect(stdio.stdout).toBe('{"detach":true}\n');
    expect(stdio.stderr).toBe("");
  });

  it("start forwards the execution id and trailing task args", async () => {
    const startDetached = vi.spyOn(detachCommand, "startDetached").mockResolvedValue(undefined);
    await createProgram({ exitOverride: true }).parseAsync([
      "node", "cli", "start", "--detach", "--execution-id", "ex1", "--state-root", "/data/state",
      "--", "prompt", "--provider", "codex", "--message-file", "/tmp/m.txt",
    ]);
    expect(startDetached).toHaveBeenCalledWith({
      stateRoot: "/data/state",
      executionId: "ex1",
      taskArgs: ["prompt", "--provider", "codex", "--message-file", "/tmp/m.txt"],
      cliPath: expect.any(String),
    });
  });

  it("attach sets the process exit code from the returned code", async () => {
    vi.spyOn(detachCommand, "attachExecution").mockResolvedValue(7);
    await createProgram({ exitOverride: true }).parseAsync([
      "node", "cli", "attach", "--execution-id", "ex1", "--from", "10", "--state-root", "/data/state",
    ]);
    expect(process.exitCode).toBe(7);
  });

  it("stop rejects an invalid reason", async () => {
    const stopExecution = vi.spyOn(detachCommand, "stopExecution").mockResolvedValue(undefined);
    const program = createProgram({ exitOverride: true });
    await expect(program.parseAsync([
      "node", "cli", "stop", "--execution-id", "ex1", "--reason", "bogus", "--state-root", "/data/state",
    ])).rejects.toThrow(/--reason must be user or timeout/);
    expect(stopExecution).not.toHaveBeenCalled();
  });

  it("stop forwards user and timeout reasons", async () => {
    const stopExecution = vi.spyOn(detachCommand, "stopExecution").mockResolvedValue(undefined);
    for (const reason of ["user", "timeout"]) {
      await createProgram({ exitOverride: true }).parseAsync([
        "node", "cli", "stop", "--execution-id", "ex1", "--reason", reason, "--state-root", "/data/state",
      ]);
    }
    expect(stopExecution).toHaveBeenCalledTimes(2);
    expect(stopExecution).toHaveBeenNthCalledWith(1, { stateRoot: "/data/state", executionId: "ex1", reason: "user" });
    expect(stopExecution).toHaveBeenNthCalledWith(2, { stateRoot: "/data/state", executionId: "ex1", reason: "timeout" });
  });

  it("supervisor re-enters the runtime CLI for the task", async () => {
    const runSupervisor = vi.spyOn(supervisorModule, "runSupervisor").mockResolvedValue(undefined);
    await createProgram({ exitOverride: true }).parseAsync([
      "node", "/tmp/cli.js", "supervisor", "--execution-id", "ex1", "--state-root", "/data/state", "--nonce", "n1",
      "--", "exec", "--request-file", "/tmp/req.json",
    ]);
    expect(runSupervisor).toHaveBeenCalledWith({
      stateRoot: "/data/state",
      executionId: "ex1",
      nonce: "n1",
      taskCommand: process.execPath,
      taskArgs: [expect.any(String), "exec", "--request-file", "/tmp/req.json"],
    });
  });

  it("start rejects an execution id whose state directory already exists", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex-reuse";
      const dir = path.join(stateRoot, "executions", executionId);
      await fs.mkdir(dir, { recursive: true });
      await fs.writeFile(path.join(dir, "ready.json"), '{"pid":1,"nonce":"stale","taskPid":2}\n');

      await expect(
        detachCommand.startDetached({ stateRoot, executionId, taskArgs: ["exec", "--request-file", "/tmp/r.json"] }),
      ).rejects.toThrow(/already exists/);
    });
  });
});

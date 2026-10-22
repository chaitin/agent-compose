import fs from "node:fs/promises";
import path from "node:path";
import type { Writable } from "node:stream";
import { describe, expect, it } from "vitest";
import { attachExecution } from "../src/detach/command.js";
import {
  cleanupExecution,
  executionDir,
  pruneFinishedExecutions,
  readExit,
  readStop,
  supervisorAlive,
  writeExit,
  writeReady,
} from "../src/detach/execution-state.js";
import { runSupervisor } from "../src/detach/supervisor.js";
import type { AttachRecord } from "../src/detach/contract.js";
import { withTempSession } from "./helpers.js";

async function fakeProc(root: string, pid: number, startTime: string, bootId: string): Promise<string> {
  const procRoot = path.join(root, "proc");
  await fs.mkdir(path.join(procRoot, String(pid)), { recursive: true });
  await fs.mkdir(path.join(procRoot, "sys", "kernel", "random"), { recursive: true });
  // Field 2 contains spaces and parens on purpose; starttime is field 22.
  const fields = Array.from({ length: 20 }, (_, i) => String(i + 3));
  fields[19] = startTime;
  await fs.writeFile(path.join(procRoot, String(pid), "stat"), `${pid} (node (x) y) ${fields.join(" ")}\n`);
  await fs.writeFile(path.join(procRoot, "sys", "kernel", "random", "boot_id"), `${bootId}\n`);
  return procRoot;
}

describe("supervisorAlive", () => {
  it("matches pid, start time and boot id", async () => {
    await withTempSession(async (root) => {
      const procRoot = await fakeProc(root, process.pid, "4242", "boot-a");
      const ready = { pid: process.pid, nonce: "n", taskPid: 1, startTime: "4242", bootId: "boot-a" };
      expect(await supervisorAlive(ready, procRoot)).toBe(true);
    });
  });

  it("rejects a recycled pid with a different start time", async () => {
    await withTempSession(async (root) => {
      const procRoot = await fakeProc(root, process.pid, "9999", "boot-a");
      const ready = { pid: process.pid, nonce: "n", taskPid: 1, startTime: "4242", bootId: "boot-a" };
      expect(await supervisorAlive(ready, procRoot)).toBe(false);
    });
  });

  it("rejects a pid from an earlier boot", async () => {
    await withTempSession(async (root) => {
      const procRoot = await fakeProc(root, process.pid, "4242", "boot-b");
      const ready = { pid: process.pid, nonce: "n", taskPid: 1, startTime: "4242", bootId: "boot-a" };
      expect(await supervisorAlive(ready, procRoot)).toBe(false);
    });
  });

  it("falls back to the pid when /proc is unavailable or the record predates identity", async () => {
    await withTempSession(async (root) => {
      const missing = path.join(root, "no-proc");
      expect(await supervisorAlive({ pid: process.pid, nonce: "n", taskPid: 1, startTime: "1", bootId: "x" }, missing)).toBe(true);
      expect(await supervisorAlive({ pid: process.pid, nonce: "n", taskPid: 1 }, missing)).toBe(true);
      expect(await supervisorAlive({ pid: 999_999_999, nonce: "n", taskPid: 1 }, missing)).toBe(false);
    });
  });
});

describe("execution cleanup", () => {
  it("removes a finished execution", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      await writeReady(stateRoot, "done", { pid: process.pid, nonce: "n", taskPid: 1 });
      await writeExit(stateRoot, "done", { code: 0, reason: "completed" });
      await cleanupExecution(stateRoot, "done");
      await expect(fs.stat(executionDir(stateRoot, "done"))).rejects.toThrow();
    });
  });

  it("refuses to remove a running execution", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      await writeReady(stateRoot, "live", { pid: process.pid, nonce: "n", taskPid: 1 });
      await expect(cleanupExecution(stateRoot, "live")).rejects.toThrow(/still running/);
      await expect(fs.stat(executionDir(stateRoot, "live"))).resolves.toBeTruthy();
    });
  });

  it("removes an abandoned execution whose supervisor is gone", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      await writeReady(stateRoot, "dead", { pid: 999_999_999, nonce: "n", taskPid: 1 });
      await cleanupExecution(stateRoot, "dead");
      await expect(fs.stat(executionDir(stateRoot, "dead"))).rejects.toThrow();
    });
  });

  it("is a no-op for an unknown execution", async () => {
    await withTempSession(async (root) => {
      await expect(cleanupExecution(path.join(root, "state"), "missing")).resolves.toBeUndefined();
    });
  });

  it("prunes only finished executions past retention", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      await writeExit(stateRoot, "old", { code: 0, reason: "completed" });
      await writeExit(stateRoot, "recent", { code: 0, reason: "completed" });
      await writeReady(stateRoot, "running", { pid: process.pid, nonce: "n", taskPid: 1 });
      const old = new Date(Date.now() - 10_000);
      await fs.utimes(path.join(executionDir(stateRoot, "old"), "exit.json"), old, old);

      await pruneFinishedExecutions(stateRoot, 5_000);

      await expect(fs.stat(executionDir(stateRoot, "old"))).rejects.toThrow();
      await expect(fs.stat(executionDir(stateRoot, "recent"))).resolves.toBeTruthy();
      await expect(fs.stat(executionDir(stateRoot, "running"))).resolves.toBeTruthy();
    });
  });
});

describe("supervisor task timeout", () => {
  it("stops the task with reason timeout when no daemon is attached", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex-timeout";
      await runSupervisor({
        stateRoot,
        executionId,
        nonce: "n",
        taskCommand: process.execPath,
        taskArgs: ["-e", "setInterval(() => {}, 1000);"],
        pollMs: 10,
        graceMs: 200,
        taskTimeoutMs: 150,
      });
      const exit = await readExit(stateRoot, executionId);
      expect(exit?.reason).toBe("stopped");
      expect(exit?.stopReason).toBe("timeout");
      expect(await readStop(stateRoot, executionId)).toEqual({ reason: "timeout" });
    });
  });
});

describe("attach journal reads", () => {
  it("does not re-emit records when the journal is unchanged between polls", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex-idle";
      const supervisor = runSupervisor({
        stateRoot,
        executionId,
        nonce: "n",
        taskCommand: process.execPath,
        taskArgs: ["-e", "console.log('a'); setTimeout(() => { console.log('b'); }, 300);"],
        pollMs: 10,
      });
      const records: AttachRecord[] = [];
      const sink = {
        write(chunk: string | Uint8Array) {
          for (const line of String(chunk).split("\n")) {
            if (line.trim()) {
              records.push(JSON.parse(line) as AttachRecord);
            }
          }
          return true;
        },
      } as unknown as Writable;
      await new Promise((resolve) => setTimeout(resolve, 50));
      const exitCode = await attachExecution({ stateRoot, executionId, from: 0, stdout: sink, pollMs: 10 });
      await supervisor;
      expect(exitCode).toBe(0);
      expect(records.filter((r) => r.t === "out").map((r) => (r as { text: string }).text)).toEqual(["a\n", "b\n"]);
    });
  });
});

import fs from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { runSupervisor } from "../src/detach/supervisor.js";
import { readExit, readReady, writeStop, exitPath, journalPath } from "../src/detach/execution-state.js";
import { journalRecordsBeyond } from "../src/detach/journal.js";
import { withTempSession } from "./helpers.js";

async function readRaw(file: string): Promise<string> {
  try {
    return await fs.readFile(file, "utf8");
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") {
      return "";
    }
    throw error;
  }
}

async function waitFor(predicate: () => Promise<boolean>, timeoutMs = 5000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await predicate()) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error("condition not met within timeout");
}

describe("runSupervisor", () => {
  it("captures task stdout/stderr into the journal and writes the exit record", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex-complete";
      const taskArgs = ["-e", "console.log('out1'); console.error('err1'); process.exit(3);"];
      await runSupervisor({
        stateRoot,
        executionId,
        nonce: "n1",
        taskCommand: process.execPath,
        taskArgs,
        pollMs: 10,
      });

      const exit = await readExit(stateRoot, executionId);
      expect(exit).toEqual({ code: 3, reason: "completed" });

      const records = journalRecordsBeyond(await readRaw(journalPath(stateRoot, executionId)), 0);
      expect(records).toEqual([
        { kind: "out", offset: 0, stream: "stdout", text: "out1\n" },
        { kind: "out", offset: 5, stream: "stderr", text: "err1\n" },
      ]);
    });
  });

  it("forwards a stop request to the task and records the stop reason", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex-stop";
      // Trap SIGTERM and exit 0 so the stop path is observable without SIGKILL.
      const taskArgs = ["-e", "console.log('up'); process.on('SIGTERM', () => { console.log('bye'); process.exit(0); }); setInterval(() => {}, 1000);"];
      const supervisor = runSupervisor({
        stateRoot,
        executionId,
        nonce: "n2",
        taskCommand: process.execPath,
        taskArgs,
        pollMs: 10,
        graceMs: 500,
      });

      await waitFor(async () => (await readReady(stateRoot, executionId)) !== null);
      // Wait until the task actually registered its SIGTERM handler before
      // requesting the stop, so the trap (not the default handler) runs.
      await waitFor(async () =>
        journalRecordsBeyond(await readRaw(journalPath(stateRoot, executionId)), 0)
          .some((r) => r.kind === "out" && r.text === "up\n"));
      await writeStop(stateRoot, executionId, { reason: "user" });
      await supervisor;

      const exit = await readExit(stateRoot, executionId);
      expect(exit?.reason).toBe("stopped");
      expect(exit?.stopReason).toBe("user");
      expect(exit?.code).toBe(0);
      const records = journalRecordsBeyond(await readRaw(journalPath(stateRoot, executionId)), 0);
      expect(records.some((r) => r.kind === "out" && r.text === "bye\n")).toBe(true);
    });
  });

  it("kills a task that ignores SIGTERM after the grace period", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex-kill";
      // Ignore SIGTERM so the supervisor escalates to SIGKILL.
      const taskArgs = ["-e", "console.log('up'); process.on('SIGTERM', () => {}); setInterval(() => {}, 1000);"];
      const supervisor = runSupervisor({
        stateRoot,
        executionId,
        nonce: "n3",
        taskCommand: process.execPath,
        taskArgs,
        pollMs: 10,
        graceMs: 200,
      });

      await waitFor(async () => (await readReady(stateRoot, executionId)) !== null);
      await waitFor(async () =>
        journalRecordsBeyond(await readRaw(journalPath(stateRoot, executionId)), 0)
          .some((r) => r.kind === "out" && r.text === "up\n"));
      await writeStop(stateRoot, executionId, { reason: "timeout" });
      await supervisor;

      const exit = await readExit(stateRoot, executionId);
      expect(exit?.reason).toBe("stopped");
      expect(exit?.stopReason).toBe("timeout");
      expect(exit?.signal).toBe("SIGKILL");
    });
  });

  it("reassembles a multi-byte UTF-8 character split across stdout chunks", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex-utf8";
      // Write the three bytes of '中' (U+4E2D) as two separate pipe writes so the
      // decoder must reassemble them across chunks rather than emitting the
      // replacement character for each half.
      const script = [
        "const fs = require('node:fs');",
        "fs.writeSync(1, Buffer.from([0xe4]));",
        "setTimeout(() => { fs.writeSync(1, Buffer.from([0xb8, 0xad, 0x0a])); process.exit(0); }, 100);",
      ].join("\n");
      await runSupervisor({
        stateRoot,
        executionId,
        nonce: "n5",
        taskCommand: process.execPath,
        taskArgs: ["-e", script],
        pollMs: 10,
      });

      const records = journalRecordsBeyond(await readRaw(journalPath(stateRoot, executionId)), 0);
      expect(records).toEqual([
        { kind: "out", offset: 0, stream: "stdout", text: "中\n" },
      ]);
    });
  });

  it("records a signal kill of the task as reason signal", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex-signal";
      const taskArgs = ["-e", "process.kill(process.pid, 'SIGKILL');"];
      await runSupervisor({
        stateRoot,
        executionId,
        nonce: "n4",
        taskCommand: process.execPath,
        taskArgs,
        pollMs: 10,
      });

      const exit = await readExit(stateRoot, executionId);
      expect(exit?.reason).toBe("signal");
      expect(exit?.signal).toBe("SIGKILL");
      expect(exit?.code).toBe(128 + 9);
      expect(await fs.stat(exitPath(stateRoot, executionId))).toBeDefined();
    });
  });

  it("records an abnormal exit and stops the task when the journal cannot be written", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex-journal-fail";
      // Make the journal path a directory so the first append fails (EISDIR).
      await fs.mkdir(journalPath(stateRoot, executionId), { recursive: true });
      // Write output to trigger the failing append, then keep running so the
      // supervisor must terminate the task itself rather than wait for it.
      const taskArgs = ["-e", "console.log('out'); setInterval(() => {}, 1000);"];
      await runSupervisor({
        stateRoot,
        executionId,
        nonce: "n6",
        taskCommand: process.execPath,
        taskArgs,
        pollMs: 10,
        graceMs: 200,
      });

      const exit = await readExit(stateRoot, executionId);
      expect(exit).not.toBeNull();
      expect(exit?.reason).toBe("abnormal");
      expect(exit?.code).toBe(-1);
      expect(exit?.detail).toContain("EISDIR");
    });
  });

  it("records an abnormal exit when the task fails to spawn", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex-spawn-fail";
      await runSupervisor({
        stateRoot,
        executionId,
        nonce: "n7",
        taskCommand: "/nonexistent/agent-compose-task",
        taskArgs: [],
        pollMs: 10,
      });

      const exit = await readExit(stateRoot, executionId);
      expect(exit).not.toBeNull();
      expect(exit?.reason).toBe("abnormal");
      expect(exit?.detail).toBe("task failed to spawn");
    });
  });
});

import path from "node:path";
import type { Writable } from "node:stream";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AttachRecord, ExitRecord } from "../src/detach/contract.js";
import { JournalWriter } from "../src/detach/journal.js";
import { withTempSession } from "./helpers.js";

// These cases pin the interleavings between attach's journal read, its exit
// check and its supervisor liveness check, so readExit/supervisorAlive are
// replaced with hooks that run the supervisor's side at the racy moment.
const hooks = vi.hoisted(() => ({
  readExit: null as null | ((stateRoot: string, executionId: string) => Promise<ExitRecord | null>),
  supervisorAlive: null as null | (() => Promise<boolean>),
}));

vi.mock("../src/detach/execution-state.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../src/detach/execution-state.js")>();
  return {
    ...actual,
    readExit: (stateRoot: string, executionId: string) =>
      hooks.readExit ? hooks.readExit(stateRoot, executionId) : actual.readExit(stateRoot, executionId),
    supervisorAlive: (...args: Parameters<typeof actual.supervisorAlive>) =>
      hooks.supervisorAlive ? hooks.supervisorAlive() : actual.supervisorAlive(...args),
  };
});

const { attachExecution } = await import("../src/detach/command.js");
const { writeReady } = await import("../src/detach/execution-state.js");

function captureWritable(): { sink: Writable; records: AttachRecord[] } {
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
  };
  return { sink: sink as unknown as Writable, records };
}

afterEach(() => {
  hooks.readExit = null;
  hooks.supervisorAlive = null;
});

describe("attachExecution races with the supervisor", () => {
  it("keeps output flushed between the journal read and the exit check", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "race-tail";
      const writer = new JournalWriter(path.join(stateRoot, "executions", executionId, "journal"), 1 << 20);
      await writer.append("stdout", "first\n");
      await writer.flush();
      await writeReady(stateRoot, executionId, { pid: process.pid, nonce: "n", taskPid: 1 });
      // The supervisor flushes the final output and writes the exit record
      // after attach has already read the journal.
      hooks.readExit = async () => {
        await writer.append("stdout", "__COMMAND_RESULT__{}\n");
        await writer.flush();
        return { code: 0, reason: "completed" };
      };

      const { sink, records } = captureWritable();
      const exitCode = await attachExecution({ stateRoot, executionId, from: 0, stdout: sink, pollMs: 10 });

      expect(exitCode).toBe(0);
      expect(records).toEqual([
        { t: "out", s: "stdout", text: "first\n", offset: 0 },
        { t: "out", s: "stdout", text: "__COMMAND_RESULT__{}\n", offset: 6 },
        { t: "exit", code: 0, reason: "completed" },
      ]);
    });
  });

  it("reports the real exit when the supervisor exits right after writing it", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "race-exit";
      await writeReady(stateRoot, executionId, { pid: 999999, nonce: "n", taskPid: 1 });
      // First exit check: not written yet. Then the supervisor writes it and
      // exits before the liveness check, so the pid is already gone.
      let calls = 0;
      hooks.readExit = async () => {
        calls += 1;
        return calls === 1 ? null : { code: 3, reason: "completed" };
      };
      hooks.supervisorAlive = async () => false;

      const { sink, records } = captureWritable();
      const exitCode = await attachExecution({ stateRoot, executionId, from: 0, stdout: sink, pollMs: 10 });

      expect(exitCode).toBe(3);
      expect(records).toEqual([{ t: "exit", code: 3, reason: "completed" }]);
    });
  });

  it("still reports abnormal when the supervisor is gone without an exit record", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "race-abnormal";
      await writeReady(stateRoot, executionId, { pid: 999999, nonce: "n", taskPid: 1 });
      hooks.readExit = async () => null;
      hooks.supervisorAlive = async () => false;

      const { sink, records } = captureWritable();
      const exitCode = await attachExecution({ stateRoot, executionId, from: 0, stdout: sink, pollMs: 10 });

      expect(exitCode).toBe(1);
      expect(records).toEqual([{ t: "exit", code: -1, reason: "abnormal" }]);
    });
  });
});

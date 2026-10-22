import fs from "node:fs/promises";
import path from "node:path";
import type { Writable } from "node:stream";
import { describe, expect, it } from "vitest";
import { attachExecution } from "../src/detach/command.js";
import { runSupervisor } from "../src/detach/supervisor.js";
import { readReady, writeExit, writeReady } from "../src/detach/execution-state.js";
import { JournalWriter } from "../src/detach/journal.js";
import type { AttachRecord } from "../src/detach/contract.js";
import { withTempSession } from "./helpers.js";

function captureWritable(): { sink: Writable; records: AttachRecord[] } {
  const records: AttachRecord[] = [];
  const sink = {
    write(chunk: string | Uint8Array) {
      const text = String(chunk);
      for (const line of text.split("\n")) {
        if (line.trim()) {
          records.push(JSON.parse(line) as AttachRecord);
        }
      }
      return true;
    },
  };
  return { sink: sink as unknown as Writable, records };
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

describe("attachExecution", () => {
  it("replays a finished execution from the cursor and returns its exit code", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex1";
      const writer = new JournalWriter(path.join(stateRoot, "executions", executionId, "journal"), 1 << 20);
      await writer.append("stdout", "hello\n");
      await writer.append("stderr", "warn\n");
      await writer.flush();
      await writeReady(stateRoot, executionId, { pid: process.pid, nonce: "n", taskPid: 1 });
      await writeExit(stateRoot, executionId, { code: 7, reason: "completed" });

      const { sink, records } = captureWritable();
      const exitCode = await attachExecution({ stateRoot, executionId, from: 0, stdout: sink, pollMs: 10 });

      expect(exitCode).toBe(7);
      expect(records).toEqual([
        { t: "out", s: "stdout", text: "hello\n", offset: 0 },
        { t: "out", s: "stderr", text: "warn\n", offset: 6 },
        { t: "exit", code: 7, reason: "completed" },
      ]);
    });
  });

  it("follows a live execution to completion without duplicating output", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex2";
      const taskArgs = ["-e", "console.log('a'); setTimeout(() => { console.log('b'); process.exit(0); }, 200);"];
      const supervisor = runSupervisor({
        stateRoot,
        executionId,
        nonce: "n",
        taskCommand: process.execPath,
        taskArgs,
        pollMs: 10,
      });
      await waitFor(async () => (await readReady(stateRoot, executionId)) !== null);

      const { sink, records } = captureWritable();
      const exitCode = await attachExecution({ stateRoot, executionId, from: 0, stdout: sink, pollMs: 10 });
      await supervisor;

      expect(exitCode).toBe(0);
      const outs = records.filter((r) => r.t === "out");
      expect(outs).toEqual([
        { t: "out", s: "stdout", text: "a\n", offset: 0 },
        { t: "out", s: "stdout", text: "b\n", offset: 2 },
      ]);
      expect(records.at(-1)).toEqual({ t: "exit", code: 0, reason: "completed" });
    });
  });

  it("resumes from an offset without replaying already-consumed output", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex3";
      const writer = new JournalWriter(path.join(stateRoot, "executions", executionId, "journal"), 1 << 20);
      await writer.append("stdout", "0123456789");
      await writer.flush();
      await writeReady(stateRoot, executionId, { pid: process.pid, nonce: "n", taskPid: 1 });
      await writeExit(stateRoot, executionId, { code: 0, reason: "completed" });

      const { sink, records } = captureWritable();
      await attachExecution({ stateRoot, executionId, from: 6, stdout: sink, pollMs: 10 });

      expect(records).toEqual([
        { t: "out", s: "stdout", text: "6789", offset: 6 },
        { t: "exit", code: 0, reason: "completed" },
      ]);
    });
  });

  it("reports an abnormal death when the supervisor is gone without an exit record", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex4";
      await writeReady(stateRoot, executionId, { pid: 999_999_999, nonce: "n", taskPid: 1 });

      const { sink, records } = captureWritable();
      const exitCode = await attachExecution({ stateRoot, executionId, from: 0, stdout: sink, pollMs: 10 });

      expect(exitCode).toBe(1);
      expect(records).toEqual([{ t: "exit", code: -1, reason: "abnormal" }]);
    });
  });

  it("emits gap markers from the journal verbatim", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex5";
      const journal = path.join(stateRoot, "executions", executionId, "journal");
      await fs.mkdir(path.dirname(journal), { recursive: true });
      await fs.writeFile(
        journal,
        `${JSON.stringify({ kind: "gap", from: 0, to: 100 })}\n${JSON.stringify({ kind: "out", offset: 100, stream: "stdout", text: "tail" })}\n`,
        "utf8",
      );
      await writeReady(stateRoot, executionId, { pid: process.pid, nonce: "n", taskPid: 1 });
      await writeExit(stateRoot, executionId, { code: 0, reason: "completed" });

      const { sink, records } = captureWritable();
      await attachExecution({ stateRoot, executionId, from: 0, stdout: sink, pollMs: 10 });

      expect(records).toEqual([
        { t: "gap", from: 0, to: 100 },
        { t: "out", s: "stdout", text: "tail", offset: 100 },
        { t: "exit", code: 0, reason: "completed" },
      ]);
    });
  });
});

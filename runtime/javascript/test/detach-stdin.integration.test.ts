import fs from "node:fs/promises";
import net from "node:net";
import path from "node:path";
import { PassThrough, type Writable } from "node:stream";
import { describe, expect, it } from "vitest";
import { attachExecution } from "../src/detach/command.js";
import type { AttachRecord } from "../src/detach/contract.js";
import { readExit, readReady, stdinSocketPath } from "../src/detach/execution-state.js";
import { encodeStdinFrame, StdinFrameDecoder } from "../src/detach/stdin-frames.js";
import { runSupervisor } from "../src/detach/supervisor.js";

// Unix socket paths are limited to ~104 bytes on macOS, so these tests keep
// their state root short instead of using the (long) per-user temp dir.
async function withShortStateRoot(run: (root: string) => Promise<void>): Promise<void> {
  const root = await fs.mkdtemp("/tmp/acd-");
  try {
    await run(root);
  } finally {
    await fs.rm(root, { recursive: true, force: true });
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

function connect(socketPath: string): Promise<net.Socket> {
  return new Promise((resolve, reject) => {
    const socket = net.createConnection(socketPath, () => resolve(socket));
    socket.once("error", reject);
  });
}

function capture(): { sink: Writable; records: AttachRecord[] } {
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
  return { sink, records };
}

// Echo each stdin line back on stdout; exit 0 at end of input.
const ECHO_TASK = ["-e", "process.stdin.setEncoding('utf8'); process.stdin.on('data', (d) => process.stdout.write('echo:' + d)); process.stdin.on('end', () => process.exit(0));"];

describe("stdin frames", () => {
  it("round-trips frames split at any byte boundary", () => {
    const bytes = Buffer.concat([
      encodeStdinFrame({ type: "data", data: Buffer.from("héllo\n") }),
      encodeStdinFrame({ type: "signal", signal: "SIGINT" }),
      encodeStdinFrame({ type: "eof" }),
    ]);
    const decoder = new StdinFrameDecoder();
    const frames = [];
    for (const byte of bytes) {
      frames.push(...decoder.push(Buffer.from([byte])));
    }
    expect(frames).toEqual([
      { type: "data", data: Buffer.from("héllo\n") },
      { type: "signal", signal: "SIGINT" },
      { type: "eof" },
    ]);
  });

  it("rejects an unknown frame type", () => {
    expect(() => new StdinFrameDecoder().push(Buffer.from("X\0\0\0\0"))).toThrow(/invalid stdin frame/);
  });
});

describe("supervisor stdin socket", () => {
  it("keeps the task's stdin open across a dropped connection and closes it on an eof frame", async () => {
    await withShortStateRoot(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex-stdin";
      const supervisor = runSupervisor({ stateRoot, executionId, nonce: "n", taskCommand: process.execPath, taskArgs: ECHO_TASK, pollMs: 10, stdin: true });
      await waitFor(async () => (await readReady(stateRoot, executionId)) !== null);
      const socketPath = stdinSocketPath(stateRoot, executionId);

      // First daemon: sends a line, then dies without ending input.
      const first = await connect(socketPath);
      first.write(encodeStdinFrame({ type: "data", data: Buffer.from("one\n") }));
      await new Promise((resolve) => setTimeout(resolve, 100));
      first.destroy();
      await new Promise((resolve) => setTimeout(resolve, 100));
      expect(await readExit(stateRoot, executionId)).toBeNull();

      // Next daemon: reconnects, sends more, then ends input.
      const second = await connect(socketPath);
      second.write(encodeStdinFrame({ type: "data", data: Buffer.from("two\n") }));
      second.write(encodeStdinFrame({ type: "eof" }));
      await supervisor;
      second.destroy();

      expect(await readExit(stateRoot, executionId)).toEqual({ code: 0, reason: "completed" });
      const { sink, records } = capture();
      await attachExecution({ stateRoot, executionId, from: 0, stdout: sink, pollMs: 10 });
      const text = records.filter((r) => r.t === "out").map((r) => (r as { text: string }).text).join("");
      expect(text).toBe("echo:one\necho:two\n");
    });
  });

  it("attach --stdin forwards frames from its input to the task", async () => {
    await withShortStateRoot(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex-attach-stdin";
      const supervisor = runSupervisor({ stateRoot, executionId, nonce: "n", taskCommand: process.execPath, taskArgs: ECHO_TASK, pollMs: 10, stdin: true });
      await waitFor(async () => (await readReady(stateRoot, executionId)) !== null);

      const input = new PassThrough();
      const { sink, records } = capture();
      const attached = attachExecution({ stateRoot, executionId, from: 0, stdout: sink, pollMs: 10, stdin: input });
      input.write(encodeStdinFrame({ type: "data", data: Buffer.from("hi\n") }));
      input.write(encodeStdinFrame({ type: "eof" }));

      expect(await attached).toBe(0);
      await supervisor;
      expect(records.filter((r) => r.t === "out").map((r) => (r as { text: string }).text).join("")).toBe("echo:hi\n");
      expect(records.at(-1)).toEqual({ t: "exit", code: 0, reason: "completed" });
    });
  });
});

describe("supervisor stdin socket failure", () => {
  it("kills the task and records an abnormal exit when the socket cannot be served", async () => {
    await withShortStateRoot(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex-long";
      // Occupy the stdin socket path with a directory so serving it fails
      // deterministically on every platform. A too-long path only fails on
      // macOS: Linux binds over-long unix paths through the abstract namespace.
      await fs.mkdir(stdinSocketPath(stateRoot, executionId), { recursive: true });
      await expect(runSupervisor({
        stateRoot, executionId, nonce: "n", taskCommand: process.execPath, taskArgs: ["-e", "setInterval(() => {}, 1000)"], pollMs: 10, stdin: true,
      })).rejects.toThrow();
      expect(await readExit(stateRoot, executionId)).toEqual({ code: -1, reason: "abnormal" });
      expect(await readReady(stateRoot, executionId)).toBeNull();
    });
  });
});

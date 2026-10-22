import { spawn } from "node:child_process";
import type { ChildProcess } from "node:child_process";
import fs from "node:fs/promises";
import net from "node:net";
import path from "node:path";
import { StringDecoder } from "node:string_decoder";
import type { ExitRecord, StopReason, StopRecord } from "./contract.js";
import { journalMaxBytes, stopGraceMs } from "./contract.js";
import { processIdentity, writeExit, writeReady, writeStop, readStop } from "./execution-state.js";
import { JournalWriter } from "./journal.js";
import { journalPath, stdinSocketPath } from "./execution-state.js";
import { StdinFrameDecoder } from "./stdin-frames.js";

export interface SupervisorOptions {
  stateRoot: string;
  executionId: string;
  nonce: string;
  /** Task command (normally the node binary that re-enters the CLI). */
  taskCommand: string;
  /** Task arguments (the subcommand plus its flags). */
  taskArgs: string[];
  /** Overrides for tests. */
  maxBytes?: number;
  graceMs?: number;
  pollMs?: number;
  /** Environment additions for the task. */
  extraEnv?: Record<string, string>;
  /**
   * Run timeout, measured from supervisor start on the guest clock. When it
   * passes the supervisor stops the task itself with reason "timeout", so the
   * timeout holds while no daemon is attached.
   */
  taskTimeoutMs?: number;
  /** Overrides /proc for tests. */
  procRoot?: string;
  /**
   * Give the task a stdin fed through a unix socket (see stdin-frames.ts),
   * so an interactive task keeps its input open across daemon restarts.
   */
  stdin?: boolean;
}

interface CloseOutcome {
  code: number | null;
  signal: NodeJS.Signals | null;
}

/**
 * Run the detached task to completion and maintain the execution state files.
 *
 * The supervisor is the task's parent for the whole run: it spawns the task in
 * its own process group (`detached`), redirects the task's stdout/stderr into
 * the journal, publishes readiness, forwards a stop request when one arrives,
 * and finally writes the atomic exit record.
 */
export async function runSupervisor(options: SupervisorOptions): Promise<void> {
  const { stateRoot, executionId, nonce } = options;
  const maxBytes = options.maxBytes ?? journalMaxBytes();
  const graceMs = options.graceMs ?? stopGraceMs();
  const pollMs = options.pollMs ?? 50;
  const deadline = options.taskTimeoutMs && options.taskTimeoutMs > 0 ? Date.now() + options.taskTimeoutMs : undefined;

  const task = spawn(options.taskCommand, options.taskArgs, {
    detached: true,
    stdio: [options.stdin ? "pipe" : "ignore", "pipe", "pipe"],
    env: { ...process.env, AGENT_COMPOSE_EXECUTION_NONCE: nonce, ...options.extraEnv },
  });

  const closePromise = waitForClose(task);
  const journal = new JournalWriter(journalPath(stateRoot, executionId), maxBytes);
  // Decode per stream so a UTF-8 multi-byte sequence split across chunks is
  // reassembled instead of being written as replacement characters.
  const stdoutDecoder = new StringDecoder("utf8");
  const stderrDecoder = new StringDecoder("utf8");

  // The first journal write failure. Appends are chained on the writer, so one
  // rejected write poisons the chain; track it once so the supervisor can end
  // the task and record the cause instead of crashing without an exit record.
  // Each append still carries a rejection handler so a failed write can never
  // surface as an unhandled rejection.
  let journalFailure: unknown = null;
  const appendJournal = (stream: "stdout" | "stderr", text: string): void => {
    if (text.length === 0) {
      return;
    }
    journal.append(stream, text).catch((error: unknown) => {
      journalFailure ??= error;
    });
  };
  task.stdout?.on("data", (chunk: Buffer) => appendJournal("stdout", stdoutDecoder.write(chunk)));
  task.stderr?.on("data", (chunk: Buffer) => appendJournal("stderr", stderrDecoder.write(chunk)));

  // A spawn failure (e.g. a raw command that does not exist) surfaces only as
  // an 'error' event; without a listener the supervisor would crash uncaught.
  let spawnFailure = false;
  task.once("error", () => {
    spawnFailure = true;
  });

  // The socket is listening before ready.json exists, so an attach that saw
  // ready can always connect.
  let stdinServer: StdinServer | undefined;
  if (options.stdin) {
    try {
      stdinServer = await serveTaskStdin(task, stdinSocketPath(stateRoot, executionId));
    } catch (error) {
      // Without its input channel the task cannot be driven: do not leave it
      // running unmanaged, and record why it ended.
      signalGroup(task, "SIGKILL");
      await waitForClose(task);
      await writeExit(stateRoot, executionId, { code: -1, reason: "abnormal" });
      throw error;
    }
  }
  const identity = await processIdentity(process.pid, options.procRoot);
  await writeReady(stateRoot, executionId, { pid: process.pid, nonce, taskPid: task.pid ?? 0, ...identity });

  let stopRecord: StopRecord | null = null;
  let outcome: CloseOutcome | null = null;
  while (stopRecord === null && outcome === null && journalFailure === null && !spawnFailure) {
    const raced = await Promise.race([
      closePromise.then((closed) => ({ closed }) as const),
      sleep(pollMs).then(() => ({ closed: null }) as const),
    ]);
    if (raced.closed) {
      outcome = raced.closed;
      break;
    }
    stopRecord = await readStop(stateRoot, executionId);
    if (stopRecord === null && deadline !== undefined && Date.now() >= deadline) {
      stopRecord = { reason: "timeout" };
      await writeStop(stateRoot, executionId, stopRecord);
    }
  }

  if (stopRecord !== null && !spawnFailure) {
    await terminateTask(task, graceMs);
    outcome = await waitForClose(task);
  }

  // Flush any trailing multi-byte sequence buffered by the decoders so a
  // truncated final character still lands in the journal before the exit record.
  appendJournal("stdout", stdoutDecoder.end());
  appendJournal("stderr", stderrDecoder.end());

  await stdinServer?.close();
  try {
    await journal.flush();
  } catch (error) {
    journalFailure ??= error;
  }

  if (journalFailure !== null || spawnFailure) {
    // The task's output can no longer be recorded (or the task never started):
    // end it and record why, so the daemon reports the real cause instead of a
    // task that keeps running unmanaged with no exit record.
    if (!spawnFailure) {
      await terminateTask(task, graceMs);
    }
    const detail = journalFailure !== null ? errorMessage(journalFailure) : "task failed to spawn";
    await writeExit(stateRoot, executionId, { code: -1, reason: "abnormal", detail });
    return;
  }

  const record = buildExitRecord(outcome ?? { code: null, signal: null }, stopRecord);
  await writeExit(stateRoot, executionId, record);
}

function errorMessage(error: unknown): string {
  if (error instanceof Error) {
    return error.message;
  }
  return String(error);
}

interface StdinServer {
  close(): Promise<void>;
}

const FORWARDABLE_SIGNALS = new Set(["SIGINT", "SIGTERM", "SIGHUP", "SIGQUIT", "SIGKILL", "SIGUSR1", "SIGUSR2"]);

/**
 * Feed the task's stdin from frames on a unix socket. One connection is
 * served at a time: a new one (the next daemon's attach) replaces the old.
 * A dropped connection leaves stdin open; only an explicit end-of-input
 * frame closes it.
 */
async function serveTaskStdin(task: ChildProcess, socketPath: string): Promise<StdinServer> {
  const stdin = task.stdin;
  // Writes after the task exits fail with EPIPE; the exit record reports it.
  stdin?.on("error", () => undefined);
  let active: net.Socket | undefined;
  const server = net.createServer((connection) => {
    active?.destroy();
    active = connection;
    const decoder = new StdinFrameDecoder();
    connection.on("error", () => undefined);
    connection.on("data", (chunk: Buffer) => {
      let frames;
      try {
        frames = decoder.push(chunk);
      } catch {
        connection.destroy();
        return;
      }
      for (const frame of frames) {
        if (frame.type === "data") {
          stdin?.write(frame.data);
        } else if (frame.type === "eof") {
          stdin?.end();
        } else if (FORWARDABLE_SIGNALS.has(frame.signal)) {
          signalGroup(task, frame.signal as NodeJS.Signals);
        }
      }
    });
  });
  await fs.rm(socketPath, { force: true });
  await fs.mkdir(path.dirname(socketPath), { recursive: true });
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(socketPath, () => {
      server.off("error", reject);
      resolve();
    });
  });
  return {
    async close() {
      active?.destroy();
      await new Promise<void>((resolve) => server.close(() => resolve()));
      await fs.rm(socketPath, { force: true });
    },
  };
}

async function terminateTask(task: ChildProcess, graceMs: number): Promise<void> {
  if (task.exitCode !== null || task.signalCode !== null) {
    return;
  }
  signalGroup(task, "SIGTERM");
  const closed = await Promise.race([
    waitForClose(task).then(() => true),
    sleep(graceMs).then(() => false),
  ]);
  if (!closed) {
    signalGroup(task, "SIGKILL");
    await waitForClose(task);
  }
}

function signalGroup(task: ChildProcess, signal: NodeJS.Signals): void {
  const pid = task.pid;
  if (!pid) {
    return;
  }
  try {
    process.kill(-pid, signal);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ESRCH") {
      // Delivery can race with process-group exit; the close event is the
      // source of truth.
    }
  }
}

function waitForClose(task: ChildProcess): Promise<CloseOutcome> {
  return new Promise((resolve) => {
    if (task.exitCode !== null || task.signalCode !== null) {
      resolve({ code: task.exitCode, signal: task.signalCode });
      return;
    }
    task.once("close", (code, signal) => resolve({ code, signal }));
  });
}

function buildExitRecord(outcome: CloseOutcome, stopRecord: StopRecord | null): ExitRecord {
  if (stopRecord !== null) {
    const reason: StopReason = stopRecord.reason;
    return {
      code: normalizeCode(outcome),
      reason: "stopped",
      stopReason: reason,
      ...(outcome.signal ? { signal: outcome.signal } : {}),
    };
  }
  if (outcome.signal) {
    return { code: 128 + signalNumber(outcome.signal), reason: "signal", signal: outcome.signal };
  }
  return { code: outcome.code ?? 0, reason: "completed" };
}

function normalizeCode(outcome: CloseOutcome): number {
  if (outcome.signal) {
    return 128 + signalNumber(outcome.signal);
  }
  return outcome.code ?? 0;
}

function signalNumber(signal: string): number {
  const table: Record<string, number> = {
    SIGHUP: 1,
    SIGINT: 2,
    SIGQUIT: 3,
    SIGKILL: 9,
    SIGSEGV: 11,
    SIGPIPE: 13,
    SIGALRM: 14,
    SIGTERM: 15,
  };
  return table[signal] ?? 128;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

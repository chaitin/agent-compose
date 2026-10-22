import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import type { Writable } from "node:stream";
import { DETACH_POLL_INTERVAL_MS, DEFAULT_READY_TIMEOUT_MS, FINISHED_EXECUTION_RETENTION_MS, type AttachRecord, type Capabilities, type ExitRecord, type JournalRecord, type StopReason } from "./contract.js";
import fs from "node:fs/promises";
import net from "node:net";
import type { Readable } from "node:stream";
import {
  cleanupExecution,
  executionExists,
  journalPath,
  pruneFinishedExecutions,
  readExit,
  readReady,
  stdinSocketPath,
  supervisorAlive,
  writeStop,
} from "./execution-state.js";
import { readJournalFrom } from "./journal.js";

export function capabilities(): Capabilities {
  return { detach: true };
}

export function resolveCliPath(argv1: string = process.argv[1]): string {
  return argv1;
}

export interface StartDetachedOptions {
  stateRoot: string;
  executionId: string;
  /** The subcommand and flags to run, e.g. ["prompt", "--provider", "codex", ...]. */
  taskArgs: string[];
  cliPath?: string;
  /** How long to wait for the supervisor's readiness file. */
  timeoutMs?: number;
  /** Run timeout the supervisor enforces on its own; 0 or absent disables it. */
  taskTimeoutMs?: number;
  /** Give the task a socket-fed stdin for interactive use. */
  stdin?: boolean;
  /** taskArgs are a command line, not a runtime subcommand. */
  rawCommand?: boolean;
  pollMs?: number;
}

/**
 * Start a detached execution: spawn the supervisor in its own session and
 * return as soon as the supervisor reports readiness. The task keeps running
 * after this exec ends.
 */
export async function startDetached(options: StartDetachedOptions): Promise<void> {
  // Best-effort: a failed prune must not block the run.
  await pruneFinishedExecutions(options.stateRoot, FINISHED_EXECUTION_RETENTION_MS).catch(() => undefined);
  // A fresh start must never re-enter an execution directory a previous start
  // left behind (a retried request reusing the same execution id): a stale
  // ready/exit/stop record would make the new supervisor look ready, kill its
  // task at once, or race the old supervisor for the same journal.
  if (await executionExists(options.stateRoot, options.executionId)) {
    throw new Error(`detached execution ${options.executionId} already exists`);
  }
  const nonce = randomBytes(16).toString("hex");
  const cliPath = options.cliPath ?? resolveCliPath();
  const supervisorArgs = [
    "supervisor",
    "--execution-id",
    options.executionId,
    "--state-root",
    options.stateRoot,
    "--nonce",
    nonce,
    ...(options.taskTimeoutMs && options.taskTimeoutMs > 0 ? ["--task-timeout-ms", String(Math.floor(options.taskTimeoutMs))] : []),
    ...(options.stdin ? ["--stdin"] : []),
    ...(options.rawCommand ? ["--raw-command"] : []),
    "--",
    ...options.taskArgs,
  ];
  const child = spawn(process.execPath, [cliPath, ...supervisorArgs], {
    detached: true,
    stdio: "ignore",
  });
  child.unref();
  await waitForReady(options.stateRoot, options.executionId, nonce, options.timeoutMs ?? DEFAULT_READY_TIMEOUT_MS, options.pollMs);
}

async function waitForReady(stateRoot: string, executionId: string, nonce: string, timeoutMs: number, pollMs?: number): Promise<void> {
  const interval = pollMs ?? DETACH_POLL_INTERVAL_MS;
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const ready = await readReady(stateRoot, executionId);
    if (ready) {
      // The ready record is only trustworthy when it came from this start's
      // supervisor: a stale record from a reused execution id would otherwise
      // make the exec report success without a live supervisor behind it.
      if (ready.nonce !== nonce) {
        throw new Error(`detached execution ${executionId} is owned by a different supervisor`);
      }
      return;
    }
    if (await readExit(stateRoot, executionId)) {
      throw new Error(`detached execution ${executionId} ended before it became ready`);
    }
    if (Date.now() >= deadline) {
      throw new Error(`detached execution ${executionId} did not become ready within ${timeoutMs}ms`);
    }
    await sleep(interval);
  }
}

export interface AttachExecutionOptions {
  stateRoot: string;
  executionId: string;
  from: number;
  stdout?: Writable;
  pollMs?: number;
  /**
   * Forward this stream (normally process.stdin, carrying stdin frames from
   * the daemon) to the supervisor's stdin socket.
   */
  stdin?: Readable;
  stderr?: Writable;
}

/**
 * Replay the journal from `from` and follow it until the execution ends, then
 * emit the exit record and return the process exit code to use. Emits NDJSON
 * AttachRecord lines to `stdout`.
 */
export async function attachExecution(options: AttachExecutionOptions): Promise<number> {
  const stdout = options.stdout ?? process.stdout;
  const pollMs = options.pollMs ?? DETACH_POLL_INTERVAL_MS;
  const input = options.stdin ? forwardStdin(options.stdin, stdinSocketPath(options.stateRoot, options.executionId), options.stderr ?? process.stderr) : undefined;
  try {
    return await followExecution(options, stdout, pollMs);
  } finally {
    input?.close();
  }
}

/**
 * Pipe input frames to the supervisor's stdin socket. The bytes are already
 * framed by the daemon and are forwarded unchanged; when this exec dies the
 * connection just drops, which leaves the task's stdin open.
 */
function forwardStdin(input: Readable, socketPath: string, stderr: Writable): { close(): void } {
  const connection = net.createConnection(socketPath);
  connection.on("error", (error) => {
    stderr.write(`detach attach: stdin unavailable: ${error.message}\n`);
    input.unpipe(connection);
  });
  input.pipe(connection);
  return {
    close() {
      input.unpipe(connection);
      connection.destroy();
    },
  };
}

async function followExecution(options: AttachExecutionOptions, stdout: Writable, pollMs: number): Promise<number> {

  let cursor = options.from;
  // Skip re-reading the journal while it is unchanged: appends grow it and
  // compaction replaces the file (new inode), so either changes the signature.
  let journalSignature = "";
  const drainJournal = async (): Promise<void> => {
    const signature = await statSignature(journalPath(options.stateRoot, options.executionId));
    if (signature !== "" && signature === journalSignature) {
      return;
    }
    journalSignature = signature;
    const records = await readJournalFrom(options.stateRoot, options.executionId, cursor);
    for (const record of records) {
      emit(stdout, journalToAttach(record));
      cursor = journalRecordEnd(record);
    }
  };
  // The supervisor flushes the journal before it writes the exit record, so
  // once the exit record is seen one more journal read is complete. Without
  // it, output flushed between the journal read and the exit check is lost.
  const finish = async (exit: ExitRecord): Promise<number> => {
    await drainJournal();
    emit(stdout, exitRecord(exit));
    return processExitCode(exit.code);
  };
  for (;;) {
    await drainJournal();
    const exit = await readExit(options.stateRoot, options.executionId);
    if (exit) {
      return finish(exit);
    }
    // No exit record yet: the task is only alive if the supervisor that owns
    // it is. A dead supervisor with no exit record means the task died
    // unmanaged (e.g. the supervisor itself was killed), so report it instead
    // of hanging forever.
    const ready = await readReady(options.stateRoot, options.executionId);
    if (!ready || !(await supervisorAlive(ready))) {
      // The supervisor may have written the exit record and exited right
      // after the check above; re-check before calling it abnormal.
      const lateExit = await readExit(options.stateRoot, options.executionId);
      if (lateExit) {
        return finish(lateExit);
      }
      await drainJournal();
      emit(stdout, { t: "exit", code: -1, reason: "abnormal" });
      return 1;
    }
    await sleep(pollMs);
  }
}

export interface StopExecutionOptions {
  stateRoot: string;
  executionId: string;
  reason: StopReason;
}

export async function stopExecution(options: StopExecutionOptions): Promise<void> {
  await writeStop(options.stateRoot, options.executionId, { reason: options.reason });
}

export interface CleanupExecutionOptions {
  stateRoot: string;
  executionId: string;
}

/** Remove a finished execution's state; see cleanupExecution. */
export async function cleanupDetached(options: CleanupExecutionOptions): Promise<void> {
  await cleanupExecution(options.stateRoot, options.executionId);
}

async function statSignature(filePath: string): Promise<string> {
  try {
    const stat = await fs.stat(filePath);
    return `${stat.ino}:${stat.size}:${stat.mtimeMs}`;
  } catch {
    return "";
  }
}

function journalToAttach(record: JournalRecord): AttachRecord {
  if (record.kind === "out") {
    return { t: "out", s: record.stream, text: record.text, offset: record.offset };
  }
  return { t: "gap", from: record.from, to: record.to };
}

function exitRecord(record: { code: number; reason: string; stopReason?: StopReason; signal?: string; detail?: string }): Extract<AttachRecord, { t: "exit" }> {
  return {
    t: "exit",
    code: record.code,
    reason: record.reason as Extract<AttachRecord, { t: "exit" }>["reason"],
    ...(record.stopReason ? { stopReason: record.stopReason } : {}),
    ...(record.signal ? { signal: record.signal } : {}),
    ...(record.detail ? { detail: record.detail } : {}),
  };
}

function journalRecordEnd(record: JournalRecord): number {
  return record.kind === "out" ? record.offset + Buffer.byteLength(record.text, "utf8") : record.to;
}

function processExitCode(code: number): number {
  return code < 0 ? 1 : code;
}

function emit(stdout: Writable, record: AttachRecord): void {
  stdout.write(`${JSON.stringify(record)}\n`);
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

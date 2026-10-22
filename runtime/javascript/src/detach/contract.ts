/**
 * Detached-execution contract shared by the supervisor, the `start`/`attach`/
 * `stop` subcommands, and (on the daemon side) the detached exec wrapper.
 *
 * Layout under `<state-root>/executions/<execution-id>/`:
 *
 * - `journal`   NDJSON of JournalRecord, one JSON object per line. `offset`
 *               is a monotonic byte offset in the task's merged stdout+stderr
 *               stream, so a consumer can resume from any point without gaps
 *               or duplicates.
 * - `ready.json` written by the supervisor once the task process is up.
 * - `exit.json`  written atomically by the supervisor when the task ends.
 * - `stop.json`  written by `stop`; the supervisor forwards it to the task.
 */

export type StdioStream = "stdout" | "stderr";

/** One line of the journal file. */
export type JournalRecord =
  | { kind: "out"; offset: number; stream: StdioStream; text: string }
  | { kind: "gap"; from: number; to: number };

/** Contents of `ready.json`. */
export interface ReadyRecord {
  /** Supervisor pid: the durable process that owns the task across the exec. */
  pid: number;
  /** Per-execution nonce, unique even if a pid is recycled. */
  nonce: string;
  /** Task (runner) pid. */
  taskPid: number;
  /**
   * Supervisor start time (field 22 of /proc/<pid>/stat) and the guest boot
   * id. Together with pid they identify the supervisor exactly, so a pid
   * recycled after a guest reboot or a supervisor crash is not mistaken for
   * it. Absent where /proc is unavailable.
   */
  startTime?: string;
  bootId?: string;
}

export type ExitReason = "completed" | "stopped" | "signal" | "abnormal";

export type StopReason = "user" | "timeout";

/** Contents of `exit.json`. */
export interface ExitRecord {
  /** Task exit code, or 128+signal when signal-killed, or -1 on abnormal death. */
  code: number;
  reason: ExitReason;
  /** Present when reason is "stopped". */
  stopReason?: StopReason;
  /** Signal name when reason is "signal". */
  signal?: string;
  /** Free-form cause for an abnormal exit (e.g. a journal write failure). */
  detail?: string;
}

/** Contents of `stop.json`. */
export interface StopRecord {
  reason: StopReason;
}

/** One line the `attach` command writes to its own stdout. */
export type AttachRecord =
  | { t: "out"; s: StdioStream; text: string; offset: number }
  | { t: "gap"; from: number; to: number }
  | { t: "exit"; code: number; reason: ExitReason; stopReason?: StopReason; signal?: string; detail?: string };

export interface Capabilities {
  detach: boolean;
}

/** Maximum journal bytes before the supervisor drops the oldest records. */
export const DEFAULT_JOURNAL_MAX_BYTES = 8 * 1024 * 1024;

/** Grace period between SIGTERM and SIGKILL when the supervisor stops a task. */
export const DEFAULT_STOP_GRACE_MS = 2000;

/** Poll interval for a follower (attach) or the stop/readiness watchers. */
export const DETACH_POLL_INTERVAL_MS = 50;

/** How long `start --detach` waits for the supervisor's readiness file. */
export const DEFAULT_READY_TIMEOUT_MS = 30_000;

/** Finished executions older than this are removed when a new one starts. */
export const FINISHED_EXECUTION_RETENTION_MS = 7 * 24 * 60 * 60 * 1000;

const JOURNAL_MAX_BYTES_ENV = "AGENT_COMPOSE_JOURNAL_MAX_BYTES";
const STOP_GRACE_MS_ENV = "AGENT_COMPOSE_DETACH_STOP_GRACE_MS";

export function journalMaxBytes(): number {
  const raw = process.env[JOURNAL_MAX_BYTES_ENV];
  const parsed = raw ? Number(raw) : NaN;
  if (!raw || !Number.isFinite(parsed) || parsed <= 0) {
    return DEFAULT_JOURNAL_MAX_BYTES;
  }
  return Math.floor(parsed);
}

export function stopGraceMs(): number {
  const raw = process.env[STOP_GRACE_MS_ENV];
  const parsed = raw ? Number(raw) : NaN;
  if (!raw || !Number.isFinite(parsed) || parsed <= 0) {
    return DEFAULT_STOP_GRACE_MS;
  }
  return Math.floor(parsed);
}

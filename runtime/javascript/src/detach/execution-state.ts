import fs from "node:fs/promises";
import path from "node:path";
import type { ExitRecord, ReadyRecord, StopRecord } from "./contract.js";

export function executionDir(stateRoot: string, executionId: string): string {
  return path.join(stateRoot, "executions", executionId);
}

export function journalPath(stateRoot: string, executionId: string): string {
  return path.join(executionDir(stateRoot, executionId), "journal");
}

export function readyPath(stateRoot: string, executionId: string): string {
  return path.join(executionDir(stateRoot, executionId), "ready.json");
}

export function exitPath(stateRoot: string, executionId: string): string {
  return path.join(executionDir(stateRoot, executionId), "exit.json");
}

/**
 * Unix socket the supervisor of an interactive execution listens on for the
 * task's stdin. Kept short: socket paths are limited to ~108 bytes.
 */
export function stdinSocketPath(stateRoot: string, executionId: string): string {
  return path.join(executionDir(stateRoot, executionId), "stdin.sock");
}

export function stopPath(stateRoot: string, executionId: string): string {
  return path.join(executionDir(stateRoot, executionId), "stop.json");
}

/** True when an execution directory already holds state from a previous run. */
export async function executionExists(stateRoot: string, executionId: string): Promise<boolean> {
  try {
    const entries = await fs.readdir(executionDir(stateRoot, executionId));
    return entries.length > 0;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") {
      return false;
    }
    throw error;
  }
}

/** Write a file atomically: temp file + rename so readers never see a partial write. */
export async function writeFileAtomic(filePath: string, content: string): Promise<void> {
  const dir = path.dirname(filePath);
  await fs.mkdir(dir, { recursive: true });
  const tmp = `${filePath}.${process.pid}.${Date.now()}.tmp`;
  await fs.writeFile(tmp, content, { encoding: "utf8", mode: 0o600 });
  await fs.rename(tmp, filePath);
}

export async function writeReady(stateRoot: string, executionId: string, record: ReadyRecord): Promise<void> {
  await writeFileAtomic(readyPath(stateRoot, executionId), `${JSON.stringify(record)}\n`);
}

export async function readReady(stateRoot: string, executionId: string): Promise<ReadyRecord | null> {
  try {
    const raw = await fs.readFile(readyPath(stateRoot, executionId), "utf8");
    const parsed = JSON.parse(raw) as Partial<ReadyRecord>;
    if (typeof parsed.pid !== "number" || typeof parsed.nonce !== "string" || typeof parsed.taskPid !== "number") {
      return null;
    }
    return {
      pid: parsed.pid,
      nonce: parsed.nonce,
      taskPid: parsed.taskPid,
      ...(typeof parsed.startTime === "string" ? { startTime: parsed.startTime } : {}),
      ...(typeof parsed.bootId === "string" ? { bootId: parsed.bootId } : {}),
    };
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") {
      return null;
    }
    throw error;
  }
}

export async function writeExit(stateRoot: string, executionId: string, record: ExitRecord): Promise<void> {
  await writeFileAtomic(exitPath(stateRoot, executionId), `${JSON.stringify(record)}\n`);
}

export async function readExit(stateRoot: string, executionId: string): Promise<ExitRecord | null> {
  try {
    const raw = await fs.readFile(exitPath(stateRoot, executionId), "utf8");
    return parseExit(raw);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") {
      return null;
    }
    throw error;
  }
}

export function parseExit(raw: string): ExitRecord | null {
  try {
    const parsed = JSON.parse(raw) as Partial<ExitRecord>;
    if (typeof parsed.code !== "number" || typeof parsed.reason !== "string") {
      return null;
    }
    return {
      code: parsed.code,
      reason: parsed.reason as ExitRecord["reason"],
      stopReason: parsed.stopReason as ExitRecord["stopReason"] | undefined,
      signal: parsed.signal,
      detail: parsed.detail,
    };
  } catch {
    return null;
  }
}

export async function writeStop(stateRoot: string, executionId: string, record: StopRecord): Promise<void> {
  await writeFileAtomic(stopPath(stateRoot, executionId), `${JSON.stringify(record)}\n`);
}

export async function readStop(stateRoot: string, executionId: string): Promise<StopRecord | null> {
  try {
    const raw = await fs.readFile(stopPath(stateRoot, executionId), "utf8");
    const parsed = JSON.parse(raw) as Partial<StopRecord>;
    if (parsed.reason !== "user" && parsed.reason !== "timeout") {
      return null;
    }
    return { reason: parsed.reason };
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") {
      return null;
    }
    throw error;
  }
}

/** True when the process identified by pid exists. */
export function processAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === "EPERM";
  }
}

export interface ProcessIdentity {
  startTime?: string;
  bootId?: string;
}

/**
 * Read the identity of pid that survives pid reuse: its start time since boot
 * and the boot id. Fields are omitted when /proc cannot be read (non-Linux, or
 * the process is gone), so callers fall back to the pid alone.
 */
export async function processIdentity(pid: number, procRoot = "/proc"): Promise<ProcessIdentity> {
  const identity: ProcessIdentity = {};
  const startTime = await readProcStartTime(pid, procRoot);
  if (startTime) {
    identity.startTime = startTime;
  }
  const bootId = await readBootId(procRoot);
  if (bootId) {
    identity.bootId = bootId;
  }
  return identity;
}

/**
 * True when the supervisor that wrote ready is still the process running
 * under its pid. A pid alone is not enough: after a guest reboot (a VM cold
 * started from the same disk keeps the state root) or a supervisor crash the
 * pid can belong to an unrelated process, and attach would wait forever.
 */
export async function supervisorAlive(ready: ReadyRecord, procRoot = "/proc"): Promise<boolean> {
  if (!processAlive(ready.pid)) {
    return false;
  }
  if (ready.bootId) {
    const bootId = await readBootId(procRoot);
    if (bootId && bootId !== ready.bootId) {
      return false;
    }
  }
  if (ready.startTime) {
    const startTime = await readProcStartTime(ready.pid, procRoot);
    if (startTime && startTime !== ready.startTime) {
      return false;
    }
  }
  return true;
}

async function readBootId(procRoot: string): Promise<string | undefined> {
  try {
    const raw = await fs.readFile(path.join(procRoot, "sys", "kernel", "random", "boot_id"), "utf8");
    return raw.trim() || undefined;
  } catch {
    return undefined;
  }
}

async function readProcStartTime(pid: number, procRoot: string): Promise<string | undefined> {
  try {
    const raw = await fs.readFile(path.join(procRoot, String(pid), "stat"), "utf8");
    // The command name (field 2) is parenthesised and may contain spaces, so
    // count fields from the last ")": state is field 3, starttime field 22.
    const fields = raw.slice(raw.lastIndexOf(")") + 1).trim().split(/\s+/);
    return fields[19] || undefined;
  } catch {
    return undefined;
  }
}

/**
 * Remove an execution's state directory. Refuses while the task may still be
 * running (no exit record and a live supervisor), so a late cleanup cannot
 * pull the journal out from under a running task.
 */
export async function cleanupExecution(stateRoot: string, executionId: string): Promise<void> {
  const exit = await readExit(stateRoot, executionId);
  if (!exit) {
    const ready = await readReady(stateRoot, executionId);
    if (ready && (await supervisorAlive(ready))) {
      throw new Error(`detached execution ${executionId} is still running`);
    }
  }
  await fs.rm(executionDir(stateRoot, executionId), { recursive: true, force: true });
}

/**
 * Remove finished executions whose exit record is older than maxAgeMs. This
 * backs up the daemon's explicit cleanup for runs it never came back for.
 * Unfinished executions are never touched.
 */
export async function pruneFinishedExecutions(stateRoot: string, maxAgeMs: number, now = Date.now()): Promise<void> {
  const root = path.join(stateRoot, "executions");
  let entries: string[];
  try {
    entries = await fs.readdir(root);
  } catch {
    return;
  }
  for (const entry of entries) {
    try {
      const stat = await fs.stat(path.join(root, entry, "exit.json"));
      if (now - stat.mtimeMs > maxAgeMs) {
        await fs.rm(path.join(root, entry), { recursive: true, force: true });
      }
    } catch {
      // No exit record (still running, or not an execution): keep it.
    }
  }
}

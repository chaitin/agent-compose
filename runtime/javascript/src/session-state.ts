import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { ensureDir, readText } from "./fs.js";
import type { Provider, StoredSession } from "./types.js";

export const SYSTEM_CONTEXT_HASH_VERSION = 1;

export function sessionStatePath(stateRoot: string, provider: Provider): string {
  return path.join(stateRoot, "agents", "providers", `${provider}.json`);
}

export function hashSystemContext(context: string): string {
  return crypto.createHash("sha256").update(context, "utf8").digest("hex");
}

export function shouldResumeCodexThread(stored: StoredSession | null, hashNow: string): boolean {
  if (!stored?.sessionId) {
    return false;
  }
  if (!stored.systemContextHash) {
    return true;
  }
  if (
    stored.systemContextHashVersion !== undefined
    && stored.systemContextHashVersion !== SYSTEM_CONTEXT_HASH_VERSION
  ) {
    return false;
  }
  return stored.systemContextHash === hashNow;
}

function parseStoredSession(payload: unknown, provider: Provider): StoredSession | null {
  if (typeof payload !== "object" || payload === null || typeof (payload as StoredSession).sessionId !== "string") {
    return null;
  }
  const record = payload as StoredSession;
  const session: StoredSession = {
    provider: typeof record.provider === "string" ? record.provider : provider,
    sessionId: record.sessionId,
  };
  if (typeof record.updatedAt === "string") {
    session.updatedAt = record.updatedAt;
  }
  if (typeof record.systemContextHash === "string") {
    session.systemContextHash = record.systemContextHash;
  }
  if (typeof record.systemContextHashVersion === "number") {
    session.systemContextHashVersion = record.systemContextHashVersion;
  }
  return session;
}

export async function readStoredSession(stateRoot: string, provider: Provider): Promise<StoredSession | null> {
  try {
    const raw = await readText(sessionStatePath(stateRoot, provider));
    return parseStoredSession(JSON.parse(raw), provider);
  } catch {
    return null;
  }
}

export async function writeStoredSession(
  stateRoot: string,
  provider: Provider,
  sessionId: string,
  now: Date = new Date(),
  systemContextHash?: string,
): Promise<void> {
  if (!sessionId) {
    return;
  }
  const target = sessionStatePath(stateRoot, provider);
  await ensureDir(path.dirname(target));
  const payload: StoredSession = {
    provider,
    sessionId,
    updatedAt: now.toISOString(),
  };
  if (systemContextHash !== undefined) {
    payload.systemContextHash = systemContextHash;
    payload.systemContextHashVersion = SYSTEM_CONTEXT_HASH_VERSION;
  }
  await fs.writeFile(target, `${JSON.stringify(payload, null, 2)}\n`, "utf8");
}

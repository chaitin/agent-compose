import fs from "node:fs/promises";
import path from "node:path";
import { ensureDir, readText } from "./fs.js";
import type { Provider, StoredThread } from "./types.js";

type StoredThreadMetadata = Required<Pick<StoredThread, "systemContextHash" | "systemContextHashVersion">>;

export function providerStatePath(stateRoot: string, provider: Provider): string {
  return path.join(stateRoot, "agents", "providers", `${provider}.json`);
}

export async function readStoredThread(stateRoot: string, provider: Provider): Promise<StoredThread | null> {
  try {
    const raw = await readText(providerStatePath(stateRoot, provider));
    const payload: unknown = JSON.parse(raw);
    if (typeof payload !== "object" || payload === null) {
      return null;
    }
    const record = payload as Record<string, unknown>;
    const threadId = typeof record.threadId === "string"
      ? record.threadId
      : typeof record.sessionId === "string"
        ? record.sessionId
        : null;
    if (threadId === null) {
      return null;
    }
    const stored: StoredThread = {
      provider: typeof record.provider === "string" ? record.provider : provider,
      threadId,
    };
    if (typeof record.sessionId === "string") {
      stored.sessionId = record.sessionId;
    }
    if (typeof record.updatedAt === "string") {
      stored.updatedAt = record.updatedAt;
    }
    if (typeof record.systemContextHash === "string") {
      stored.systemContextHash = record.systemContextHash;
    }
    if (typeof record.systemContextHashVersion === "number" && Number.isInteger(record.systemContextHashVersion)) {
      stored.systemContextHashVersion = record.systemContextHashVersion;
    }
    return stored;
  } catch {
    return null;
  }
}

export async function writeStoredThread(
  stateRoot: string,
  provider: Provider,
  threadId: string,
  now: Date = new Date(),
  metadata?: StoredThreadMetadata,
): Promise<void> {
  if (!threadId) {
    return;
  }
  const target = providerStatePath(stateRoot, provider);
  await ensureDir(path.dirname(target));
  const payload: StoredThread = {
    provider,
    threadId,
    updatedAt: now.toISOString(),
  };
  if (metadata) {
    payload.systemContextHash = metadata.systemContextHash;
    payload.systemContextHashVersion = metadata.systemContextHashVersion;
  }
  await fs.writeFile(target, `${JSON.stringify(payload, null, 2)}\n`, "utf8");
}

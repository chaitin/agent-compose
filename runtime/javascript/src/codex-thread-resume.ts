import { createHash } from "node:crypto";
import type { StoredThread } from "./types.js";

export const CODEX_SYSTEM_CONTEXT_HASH_VERSION = 1;

export type CodexThreadStartReason =
  | "no-stored-thread"
  | "missing-system-context-hash"
  | "unsupported-system-context-hash-version"
  | "system-context-changed";

export type CodexThreadResumeDecision =
  | { action: "resume"; threadId: string }
  | { action: "start"; reason: CodexThreadStartReason };

export type CodexThreadStateMetadata = Required<
  Pick<StoredThread, "systemContextHash" | "systemContextHashVersion">
>;

export function hashSystemContext(systemContext: string): string {
  return createHash("sha256").update(systemContext, "utf8").digest("hex");
}

export function codexThreadStateMetadata(systemContextHash: string): CodexThreadStateMetadata {
  return {
    systemContextHash,
    systemContextHashVersion: CODEX_SYSTEM_CONTEXT_HASH_VERSION,
  };
}

export function decideCodexThreadResume(
  stored: StoredThread | null,
  currentSystemContextHash: string,
): CodexThreadResumeDecision {
  if (!stored?.threadId) {
    return { action: "start", reason: "no-stored-thread" };
  }
  if (!stored.systemContextHash) {
    return { action: "start", reason: "missing-system-context-hash" };
  }
  if (stored.systemContextHashVersion !== CODEX_SYSTEM_CONTEXT_HASH_VERSION) {
    return { action: "start", reason: "unsupported-system-context-hash-version" };
  }
  if (stored.systemContextHash !== currentSystemContextHash) {
    return { action: "start", reason: "system-context-changed" };
  }
  return { action: "resume", threadId: stored.threadId };
}

export function codexThreadStartWarning(reason: CodexThreadStartReason): string | null {
  switch (reason) {
    case "no-stored-thread":
      return null;
    case "missing-system-context-hash":
      return "stored Codex thread has no system context fingerprint; started a new thread";
    case "unsupported-system-context-hash-version":
      return "stored Codex thread has an unsupported system context fingerprint version; started a new thread";
    case "system-context-changed":
      return "system context changed; started a new Codex thread";
  }
}

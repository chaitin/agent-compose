import fs from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";
import {
  hashSystemContext,
  readStoredSession,
  sessionStatePath,
  shouldResumeCodexThread,
  writeStoredSession,
} from "../src/session-state.js";
import { withTempSession } from "./helpers.js";

describe("provider session state", () => {
  it("uses the compatible provider state path", async () => {
    await withTempSession(async (root) => {
      expect(sessionStatePath(path.join(root, "state"), "codex")).toBe(
        path.join(root, "state", "agents", "providers", "codex.json"),
      );
    });
  });

  it("returns null for absent or malformed state", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      expect(await readStoredSession(stateRoot, "codex")).toBeNull();

      const target = sessionStatePath(stateRoot, "codex");
      await fs.mkdir(path.dirname(target), { recursive: true });
      await fs.writeFile(target, "{\"sessionId\": 3}", "utf8");

      expect(await readStoredSession(stateRoot, "codex")).toBeNull();
    });
  });

  it("writes and reads session id state", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const now = new Date("2026-01-01T00:00:00.000Z");

      await writeStoredSession(stateRoot, "claude", "session-1", now);

      await expect(readStoredSession(stateRoot, "claude")).resolves.toEqual({
        provider: "claude",
        sessionId: "session-1",
        updatedAt: now.toISOString(),
      });
    });
  });

  it("does not create state for an empty session id", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");

      await writeStoredSession(stateRoot, "gemini", "");

      await expect(fs.stat(sessionStatePath(stateRoot, "gemini"))).rejects.toMatchObject({ code: "ENOENT" });
    });
  });

  it("hashes system context deterministically", () => {
    expect(hashSystemContext("catalog body")).toBe(hashSystemContext("catalog body"));
    expect(hashSystemContext("")).toBe("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855");
    expect(hashSystemContext("a")).not.toBe(hashSystemContext("b"));
  });

  it("writes and reads system context hash for Codex", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const hash = hashSystemContext("catalog body");
      const now = new Date("2026-01-01T00:00:00.000Z");

      await writeStoredSession(stateRoot, "codex", "session-1", now, hash);

      await expect(readStoredSession(stateRoot, "codex")).resolves.toEqual({
        provider: "codex",
        sessionId: "session-1",
        updatedAt: now.toISOString(),
        systemContextHash: hash,
        systemContextHashVersion: 1,
      });
    });
  });
});

describe("shouldResumeCodexThread", () => {
  it("returns false when no stored session exists", () => {
    expect(shouldResumeCodexThread(null, hashSystemContext(""))).toBe(false);
  });

  it("returns false when session id is missing", () => {
    expect(shouldResumeCodexThread({ provider: "codex", sessionId: "" }, hashSystemContext(""))).toBe(false);
  });

  it("returns true for legacy state without a stored hash", () => {
    expect(shouldResumeCodexThread({ provider: "codex", sessionId: "old-thread" }, hashSystemContext("ctx"))).toBe(true);
  });

  it("returns true when the stored hash and version match", () => {
    const hash = hashSystemContext("ctx");
    expect(shouldResumeCodexThread({
      provider: "codex",
      sessionId: "old-thread",
      systemContextHash: hash,
      systemContextHashVersion: 1,
    }, hash)).toBe(true);
  });

  it("returns true when a legacy hashed state has no version", () => {
    const hash = hashSystemContext("ctx");
    expect(shouldResumeCodexThread({
      provider: "codex",
      sessionId: "old-thread",
      systemContextHash: hash,
    }, hash)).toBe(true);
  });

  it("returns false when the hash version does not match", () => {
    const hash = hashSystemContext("ctx");
    expect(shouldResumeCodexThread({
      provider: "codex",
      sessionId: "old-thread",
      systemContextHash: hash,
      systemContextHashVersion: 999,
    }, hash)).toBe(false);
  });

  it("returns false when the stored hash differs", () => {
    expect(shouldResumeCodexThread({
      provider: "codex",
      sessionId: "old-thread",
      systemContextHash: hashSystemContext("old"),
    }, hashSystemContext("new"))).toBe(false);
  });
});

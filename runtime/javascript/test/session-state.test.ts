import fs from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { CODEX_SYSTEM_CONTEXT_HASH_VERSION, hashSystemContext } from "../src/codex-thread-resume.js";
import { providerStatePath, readStoredThread, writeStoredThread } from "../src/session-state.js";
import { withTempSession } from "./helpers.js";

describe("provider thread state", () => {
  it("uses the compatible provider state path", async () => {
    await withTempSession(async (root) => {
      expect(providerStatePath(path.join(root, "state"), "codex")).toBe(
        path.join(root, "state", "agents", "providers", "codex.json"),
      );
    });
  });

  it("returns null for absent or malformed state", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      expect(await readStoredThread(stateRoot, "codex")).toBeNull();

      const target = providerStatePath(stateRoot, "codex");
      await fs.mkdir(path.dirname(target), { recursive: true });
      await fs.writeFile(target, "{\"threadId\": 3}", "utf8");

      expect(await readStoredThread(stateRoot, "codex")).toBeNull();

      await fs.writeFile(target, "{\"provider\":\"codex\"}", "utf8");

      expect(await readStoredThread(stateRoot, "codex")).toBeNull();
    });
  });

  it("characterizes current whitespace thread id compatibility", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const target = providerStatePath(stateRoot, "codex");
      await fs.mkdir(path.dirname(target), { recursive: true });
      await fs.writeFile(target, "{\"threadId\":\"   \",\"updatedAt\":\"2026-01-01T00:00:00.000Z\"}", "utf8");

      await expect(readStoredThread(stateRoot, "codex")).resolves.toEqual({
        provider: "codex",
        threadId: "   ",
        updatedAt: "2026-01-01T00:00:00.000Z",
      });
    });
  });

  it("writes and reads thread id state", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const now = new Date("2026-01-01T00:00:00.000Z");

      await writeStoredThread(stateRoot, "claude", "thread-1", now);

      await expect(readStoredThread(stateRoot, "claude")).resolves.toEqual({
        provider: "claude",
        threadId: "thread-1",
        updatedAt: now.toISOString(),
      });
    });
  });

  it("writes and reads Codex system context fingerprint state", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const now = new Date("2026-01-01T00:00:00.000Z");
      const systemContextHash = hashSystemContext("catalog body");

      await writeStoredThread(stateRoot, "codex", "thread-1", now, {
        systemContextHash,
        systemContextHashVersion: CODEX_SYSTEM_CONTEXT_HASH_VERSION,
      });

      await expect(readStoredThread(stateRoot, "codex")).resolves.toEqual({
        provider: "codex",
        threadId: "thread-1",
        updatedAt: now.toISOString(),
        systemContextHash,
        systemContextHashVersion: CODEX_SYSTEM_CONTEXT_HASH_VERSION,
      });
    });
  });

  it("ignores malformed optional fingerprint fields", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const target = providerStatePath(stateRoot, "codex");
      await fs.mkdir(path.dirname(target), { recursive: true });
      await fs.writeFile(target, JSON.stringify({
        provider: "codex",
        threadId: "thread-1",
        systemContextHash: 3,
        systemContextHashVersion: "1",
      }), "utf8");

      await expect(readStoredThread(stateRoot, "codex")).resolves.toEqual({
        provider: "codex",
        threadId: "thread-1",
      });
    });
  });

  it("reads legacy session id state", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const target = providerStatePath(stateRoot, "codex");
      await fs.mkdir(path.dirname(target), { recursive: true });
      await fs.writeFile(target, '{"provider":"codex","sessionId":"legacy-thread"}', "utf8");

      await expect(readStoredThread(stateRoot, "codex")).resolves.toEqual({
        provider: "codex",
        sessionId: "legacy-thread",
        threadId: "legacy-thread",
      });
    });
  });

  it("does not create state for an empty thread id", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");

      await writeStoredThread(stateRoot, "gemini", "");

      await expect(fs.stat(providerStatePath(stateRoot, "gemini"))).rejects.toMatchObject({ code: "ENOENT" });
    });
  });
});

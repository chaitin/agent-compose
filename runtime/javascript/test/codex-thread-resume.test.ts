import { describe, expect, it } from "vitest";
import {
  CODEX_SYSTEM_CONTEXT_HASH_VERSION,
  codexThreadStateMetadata,
  codexThreadStartWarning,
  decideCodexThreadResume,
  hashSystemContext,
} from "../src/codex-thread-resume.js";

describe("Codex thread resume policy", () => {
  it("hashes the exact system context deterministically", () => {
    expect(hashSystemContext("")).toBe("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855");
    expect(hashSystemContext("context")).toBe(hashSystemContext("context"));
    expect(hashSystemContext("context")).not.toBe(hashSystemContext("context\n"));
  });

  it("builds versioned persistence metadata", () => {
    const systemContextHash = hashSystemContext("context");
    expect(codexThreadStateMetadata(systemContextHash)).toEqual({
      systemContextHash,
      systemContextHashVersion: CODEX_SYSTEM_CONTEXT_HASH_VERSION,
    });
  });

  it("starts when no stored thread exists", () => {
    expect(decideCodexThreadResume(null, hashSystemContext("context"))).toEqual({
      action: "start",
      reason: "no-stored-thread",
    });
  });

  it("starts when legacy state has no system context hash", () => {
    expect(decideCodexThreadResume({ provider: "codex", threadId: "legacy" }, hashSystemContext("context"))).toEqual({
      action: "start",
      reason: "missing-system-context-hash",
    });
  });

  it("starts when the hash version is absent or unsupported", () => {
    const systemContextHash = hashSystemContext("context");
    expect(decideCodexThreadResume({
      provider: "codex",
      threadId: "old-thread",
      systemContextHash,
    }, systemContextHash)).toEqual({
      action: "start",
      reason: "unsupported-system-context-hash-version",
    });
    expect(decideCodexThreadResume({
      provider: "codex",
      threadId: "old-thread",
      systemContextHash,
      systemContextHashVersion: CODEX_SYSTEM_CONTEXT_HASH_VERSION + 1,
    }, systemContextHash)).toEqual({
      action: "start",
      reason: "unsupported-system-context-hash-version",
    });
  });

  it("starts when the system context changed", () => {
    expect(decideCodexThreadResume({
      provider: "codex",
      threadId: "old-thread",
      systemContextHash: hashSystemContext("old"),
      systemContextHashVersion: CODEX_SYSTEM_CONTEXT_HASH_VERSION,
    }, hashSystemContext("new"))).toEqual({
      action: "start",
      reason: "system-context-changed",
    });
  });

  it("treats empty and non-empty context transitions as changes", () => {
    const emptyHash = hashSystemContext("");
    const nonEmptyHash = hashSystemContext("context");
    const stored = {
      provider: "codex",
      threadId: "old-thread",
      systemContextHashVersion: CODEX_SYSTEM_CONTEXT_HASH_VERSION,
    };

    expect(decideCodexThreadResume({ ...stored, systemContextHash: emptyHash }, nonEmptyHash)).toMatchObject({
      action: "start",
      reason: "system-context-changed",
    });
    expect(decideCodexThreadResume({ ...stored, systemContextHash: nonEmptyHash }, emptyHash)).toMatchObject({
      action: "start",
      reason: "system-context-changed",
    });
  });

  it("resumes only when the hash and version match", () => {
    const systemContextHash = hashSystemContext("context");
    expect(decideCodexThreadResume({
      provider: "codex",
      threadId: "old-thread",
      systemContextHash,
      systemContextHashVersion: CODEX_SYSTEM_CONTEXT_HASH_VERSION,
    }, systemContextHash)).toEqual({
      action: "resume",
      threadId: "old-thread",
    });
  });

  it("emits warnings only when an existing thread is rejected", () => {
    expect(codexThreadStartWarning("no-stored-thread")).toBeNull();
    expect(codexThreadStartWarning("missing-system-context-hash")).toContain("no system context fingerprint");
    expect(codexThreadStartWarning("unsupported-system-context-hash-version")).toContain("unsupported");
    expect(codexThreadStartWarning("system-context-changed")).toContain("system context changed");
  });
});

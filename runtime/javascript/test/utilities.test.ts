import fs from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";
import * as runtimeIndex from "../src/index.js";
import { COMMAND_RESULT_PREFIX, RESULT_PREFIX, SANDBOX_ROOT } from "../src/constants.js";
import { ensureDir, isExecutable, readText } from "../src/fs.js";
import { formatError } from "../src/errors.js";
import { stringEnv } from "../src/env.js";
import { resolveCodexPath } from "../src/codex-path.js";
import { runtimeRootForStateRoot, uniqueDirectories } from "../src/paths.js";
import { withTempSession } from "./helpers.js";

describe("utility modules", () => {
  it("exports the public runtime helpers from the package entrypoint", () => {
    expect(runtimeIndex.RESULT_PREFIX).toBe(RESULT_PREFIX);
    expect(runtimeIndex.SANDBOX_ROOT).toBe(SANDBOX_ROOT);
    expect(runtimeIndex.runtimeRootForStateRoot("/tmp/state")).toBe(path.join("/tmp", "runtime"));
    expect(runtimeIndex.uniqueDirectories(["/a", "/a", undefined, "/b"])).toEqual(["/a", "/b"]);
    expect(runtimeIndex.normalizeProvider("claude-code")).toBe("claude");
  });

  it("keeps runtime protocol constants stable", () => {
    expect(SANDBOX_ROOT).toBe("/srv/agent-compose/sandbox");
    expect(RESULT_PREFIX).toBe("__AGENT_RESULT__");
    expect(COMMAND_RESULT_PREFIX).toBe("__COMMAND_RESULT__");
  });

  it("filters process-style env objects down to string values", () => {
    expect(stringEnv({ A: "1", B: undefined, C: "3" })).toEqual({ A: "1", C: "3" });
  });

  it("derives runtime paths and de-duplicates directory lists", () => {
    expect(runtimeRootForStateRoot("/tmp/session/state")).toBe(path.join("/tmp/session", "runtime"));
    expect(uniqueDirectories(["/workspace", "", undefined, "/workspace", "/data"])).toEqual(["/workspace", "/data"]);
  });

  it("resolves codex from an executable env override", async () => {
    await withTempSession(async (root) => {
      const script = path.join(root, "codex");
      await fs.writeFile(script, "#!/bin/sh\n", "utf8");
      await fs.chmod(script, 0o755);
      const oldValue = process.env.CODEX_BIN;
      process.env.CODEX_BIN = script;
      try {
        expect(resolveCodexPath()).toBe(script);
      } finally {
        if (oldValue === undefined) {
          delete process.env.CODEX_BIN;
        } else {
          process.env.CODEX_BIN = oldValue;
        }
      }
    });
  });

  it("detects executable files", async () => {
    await withTempSession(async (root) => {
      const script = path.join(root, "script");
      await fs.writeFile(script, "#!/bin/sh\n", "utf8");
      await fs.chmod(script, 0o755);

      expect(isExecutable(script)).toBe(true);
      expect(isExecutable(path.join(root, "missing"))).toBe(false);
    });
  });

  it("reads text files", async () => {
    await withTempSession(async (root) => {
      const dir = path.join(root, "nested");
      await ensureDir(dir);
      const file = path.join(dir, "file.txt");
      await fs.writeFile(file, "body", "utf8");
      await expect(readText(file)).resolves.toBe("body");
    });
  });

  it("formats errors and unknown thrown values", () => {
    expect(formatError(new Error("boom"))).toContain("boom");
    expect(formatError({ ok: true })).toContain("\"ok\": true");
    const circular: Record<string, unknown> = {};
    circular.self = circular;
    expect(formatError(circular)).toContain("<ref");
  });
});

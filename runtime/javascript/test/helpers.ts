import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { vi } from "vitest";
import type { RunnerOptions } from "../src/types.js";

export async function withTempSession<T>(fn: (root: string) => Promise<T>): Promise<T> {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "agent-compose-runtime-"));
  try {
    return await fn(root);
  } finally {
    await fs.rm(root, { recursive: true, force: true });
  }
}

export function runnerOptions(root: string, systemContext = "", provider: RunnerOptions["provider"] = "codex"): RunnerOptions {
  const stateRoot = path.join(root, "state");
  return {
    provider,
    stateRoot,
    sessionRoot: stateRoot,
    workspace: path.join(root, "workspace"),
    home: path.join(root, "home"),
    runtimeRoot: path.join(root, "runtime"),
    systemContext,
  };
}

export function captureStdio() {
  let stdout = "";
  let stderr = "";
  const stdoutSpy = vi.spyOn(process.stdout, "write").mockImplementation((chunk: string | Uint8Array) => {
    stdout += String(chunk);
    return true;
  });
  const stderrSpy = vi.spyOn(process.stderr, "write").mockImplementation((chunk: string | Uint8Array) => {
    stderr += String(chunk);
    return true;
  });

  return {
    get stdout() {
      return stdout;
    },
    get stderr() {
      return stderr;
    },
    restore() {
      stdoutSpy.mockRestore();
      stderrSpy.mockRestore();
    },
  };
}

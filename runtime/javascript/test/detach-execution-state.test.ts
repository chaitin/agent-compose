import path from "node:path";
import { describe, expect, it } from "vitest";
import { executionDir, parseExit, processAlive, readExit, readReady, readStop, writeExit, writeReady, writeStop } from "../src/detach/execution-state.js";
import { withTempSession } from "./helpers.js";

describe("execution state files", () => {
  it("round-trips ready, exit, and stop records", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const id = "ex1";
      await writeReady(stateRoot, id, { pid: 42, nonce: "abc", taskPid: 43 });
      await expect(readReady(stateRoot, id)).resolves.toEqual({ pid: 42, nonce: "abc", taskPid: 43 });

      await writeExit(stateRoot, id, { code: 9, reason: "stopped", stopReason: "timeout" });
      await expect(readExit(stateRoot, id)).resolves.toEqual({ code: 9, reason: "stopped", stopReason: "timeout" });

      await writeStop(stateRoot, id, { reason: "user" });
      await expect(readStop(stateRoot, id)).resolves.toEqual({ reason: "user" });
    });
  });

  it("returns null for missing state files", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      expect(await readReady(stateRoot, "missing")).toBeNull();
      expect(await readExit(stateRoot, "missing")).toBeNull();
      expect(await readStop(stateRoot, "missing")).toBeNull();
    });
  });

  it("parseExit tolerates malformed payloads", () => {
    expect(parseExit("not json")).toBeNull();
    expect(parseExit('{"code":"0","reason":"completed"}')).toBeNull();
    expect(parseExit('{"code":0,"reason":"completed"}')).toEqual({ code: 0, reason: "completed" });
  });

  it("processAlive reports the current process as alive", () => {
    expect(processAlive(process.pid)).toBe(true);
    expect(processAlive(999_999_999)).toBe(false);
  });

  it("executionDir is nested under the state root", () => {
    expect(executionDir("/data/state", "run-1")).toBe(path.join("/data/state", "executions", "run-1"));
  });
});

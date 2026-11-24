import { afterEach, describe, expect, it, vi } from "vitest";
import { AGENT_OUTPUT_ENV, RESULT_PREFIX } from "../src/constants.js";
import { providerEnv } from "../src/env.js";
import { decodeFrame } from "../src/frame.js";
import { runPromptWithOutput } from "../src/prompt-output.js";
import * as prompt from "../src/prompt.js";

const result = {
  provider: "codex" as const, threadId: "t1", finalText: "done", stopReason: "completed",
  finalTextSource: "provider_message" as const, transcript: "done", stderr: "",
};
afterEach(() => { vi.restoreAllMocks(); });

describe("prompt output compatibility", () => {
  it("keeps the default result readable by existing SDKs", async () => {
    const run = vi.spyOn(prompt, "runPromptCommand").mockResolvedValue(result);
    const lines: string[] = [];
    await runPromptWithOutput({ provider: "codex", promptText: "go" }, {}, (line) => lines.push(line));
    expect(run.mock.calls[0][0].onEvent).toBeUndefined();
    expect(lines).toEqual([`${RESULT_PREFIX}${JSON.stringify(result)}\n`]);
  });

  it("emits the stream envelopes only when explicitly selected", async () => {
    vi.spyOn(prompt, "runPromptCommand").mockImplementation(async ({ onEvent }) => {
      onEvent?.({ kind: "text_delta", text: "done" });
      return result;
    });
    const lines: string[] = [];
    await runPromptWithOutput({ provider: "codex" }, {
      [AGENT_OUTPUT_ENV]: "frames",
    }, (line) => lines.push(line));
    expect(lines.map(decodeFrame)).toEqual([
      { v: 1, seq: 0, type: "started", provider: "codex" },
      { v: 1, seq: 1, type: "agent_event", event: { kind: "text_delta", text: "done" } },
      { ...result, v: 1, seq: 2, type: "agent_turn_completed" },
      { ...result, v: 1, seq: 3, type: "result" },
    ]);
    expect(lines.join("")).not.toContain("__AGENT_");
  });

  it("reports failure without emitting a successful terminal result", async () => {
    const failure = new Error("provider failed");
    vi.spyOn(prompt, "runPromptCommand").mockRejectedValue(failure);
    const lines: string[] = [];
    await expect(runPromptWithOutput({ provider: "codex" }, {
      [AGENT_OUTPUT_ENV]: "frames",
    }, (line) => lines.push(line))).rejects.toBe(failure);
    const frames = lines.map(decodeFrame);
    expect(frames.map((frame) => frame.type)).toEqual(["started", "error"]);
    expect(frames[1]).toMatchObject({ code: "prompt_failed", message: expect.stringContaining("provider failed") });
  });

  it("preserves cancellation as an unsuccessful stop reason in result frames", async () => {
    vi.spyOn(prompt, "runPromptCommand").mockResolvedValue({ ...result, stopReason: "cancelled" });
    const lines: string[] = [];
    await runPromptWithOutput({}, { [AGENT_OUTPUT_ENV]: "frames" }, (line) => lines.push(line));
    expect(decodeFrame(lines.at(-1)!)).toMatchObject({ type: "result", stopReason: "cancelled" });
  });

  it("rejects unknown formats before launching an agent", async () => {
    const run = vi.spyOn(prompt, "runPromptCommand");
    await expect(runPromptWithOutput({}, { [AGENT_OUTPUT_ENV]: "typo" })).rejects.toThrow("unsupported");
    expect(run).not.toHaveBeenCalled();
  });

  it("does not pass invocation protocol switches to provider tools and nested SDKs", () => {
    const source = { [AGENT_OUTPUT_ENV]: "frames", PATH: "/bin" };
    expect(providerEnv(source)).toEqual({ PATH: "/bin" });
    expect(source[AGENT_OUTPUT_ENV]).toBe("frames");
  });
});

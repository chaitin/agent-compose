import { afterEach, expect, it, vi } from "vitest";
import { CodexInteractiveSession } from "../src/interactive.js";
import { CodexRunner } from "../src/runners/codex.js";
import { runnerOptions, withTempSession } from "./helpers.js";

const configs = vi.hoisted(() => [] as Array<Record<string, unknown>>);
vi.mock("@openai/codex-sdk", () => ({ Codex: class {
  constructor(options: { config: Record<string, unknown> }) { configs.push(options.config); }
  startThread() {
    return { id: "thread-window", runStreamed: async () => ({ events: (async function* () {
      yield { type: "turn.completed", usage: {} };
    })() }) };
  }
  resumeThread() { return this.startThread(); }
} }));

afterEach(() => { vi.unstubAllEnvs(); configs.length = 0; });

it("removes window overrides from prompt and interactive launches after a reset", async () => {
  await withTempSession(async (root) => {
    const options = runnerOptions(root, "project instructions");
    for (const window of ["128000", "0"]) {
      vi.stubEnv("AGENT_COMPOSE_CODEX_CONTEXT_WINDOW", window);
      await new CodexRunner(options).runPrompt("prompt");
      const interactive = new CodexInteractiveSession(options, () => {});
      await interactive.start();
      await interactive.runHumanMessage("next turn");
      await interactive.finish("completed");
    }
    expect(configs).toHaveLength(4);
    expect(configs.slice(0, 2)).toEqual(Array(2).fill({
      model_context_window: 128_000,
      model_auto_compact_token_limit: 96_000,
      developer_instructions: "project instructions",
    }));
    expect(configs.slice(2)).toEqual(Array(2).fill({ developer_instructions: "project instructions" }));
  });
});

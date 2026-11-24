import { describe, expect, it } from "vitest";
import { codexModelConfig } from "../src/codex-model-config.js";

describe("codex model configuration", () => {
  it("uses the selected model window and scales the compaction limit", () => {
    expect(codexModelConfig({ AGENT_COMPOSE_CODEX_CONTEXT_WINDOW: "32000" })).toEqual({
      model_context_window: 32_000,
      model_auto_compact_token_limit: 24_000,
    });
    expect(codexModelConfig({ AGENT_COMPOSE_CODEX_CONTEXT_WINDOW: "128000" })).toEqual({
      model_context_window: 128_000,
      model_auto_compact_token_limit: 96_000,
    });
  });

  it.each([undefined, "", "0", "-1", "1.5", "2147483648"])("leaves native defaults alone for %s", (value) => {
    expect(codexModelConfig({ AGENT_COMPOSE_CODEX_CONTEXT_WINDOW: value })).toEqual({});
  });
});

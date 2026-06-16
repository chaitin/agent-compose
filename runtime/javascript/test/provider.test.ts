import { describe, expect, it } from "vitest";
import { normalizeProvider } from "../src/provider.js";

describe("provider normalization", () => {
  it.each([
    ["", "codex"],
    ["codex", "codex"],
    ["CLAUDE", "claude"],
    ["claude-code", "claude"],
    ["claude_code", "claude"],
    ["gemini-cli", "gemini"],
    ["gemini_cli", "gemini"],
    ["deepseek", "deepseek"],
  ])("maps %j to %s", (input, expected) => {
    expect(normalizeProvider(input)).toBe(expected);
  });

  it("rejects unsupported providers", () => {
    expect(() => normalizeProvider("qwen")).toThrow(/unsupported provider/);
  });

  it("trims provider names before normalization", () => {
    expect(normalizeProvider(" Codex ")).toBe("codex");
  });
});

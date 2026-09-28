import { describe, expect, it } from "vitest";
import { normalizeProvider } from "../src/provider.js";

describe("provider normalization", () => {
  it.each([
    ["codex", "codex"],
    ["CLAUDE", "claude"],
    ["claude-code", "claude"],
    ["claude_code", "claude"],
    ["opencode", "opencode"],
    ["open-code", "opencode"],
  ["open_code", "opencode"],
  ["pi", "pi"],
  ["pi-agent", "pi"],
  ["pi_agent", "pi"],
  ])("maps %j to %s", (input, expected) => {
    expect(normalizeProvider(input)).toBe(expected);
  });

  it.each(["qwen", "gemini", "gemini-cli", "gemini_cli"])("rejects unsupported provider %j", (provider) => {
    expect(() => normalizeProvider(provider)).toThrow(
      /unsupported provider .*; expected one of: codex, claude, opencode, pi, dsh/,
    );
  });

  it.each([
    "",
    "   ",
    undefined,
    null,
  ])("rejects missing provider %j", (input) => {
    expect(() => normalizeProvider(input)).toThrow(/provider is required; expected one of: codex, claude, opencode, pi/);
  });

  it("trims provider names before normalization", () => {
    expect(normalizeProvider(" Codex ")).toBe("codex");
  });
});

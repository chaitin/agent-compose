import type { Provider } from "./types.js";

export function normalizeProvider(raw: unknown): Provider {
  const provider = String(raw || "").trim().toLowerCase();
  switch (provider) {
    case "":
    case "codex":
      return "codex";
    case "claude":
    case "claude-code":
    case "claude_code":
      return "claude";
    case "gemini":
    case "gemini-cli":
    case "gemini_cli":
      return "gemini";
    case "deepseek":
      return "deepseek";
    default:
      throw new Error(`unsupported provider ${JSON.stringify(raw)}`);
  }
}

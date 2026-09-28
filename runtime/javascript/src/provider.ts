import type { Provider } from "./types.js";

const providerList = "codex, claude, opencode, pi, dsh";

export function normalizeProvider(raw: unknown): Provider {
  const provider = String(raw ?? "").trim().toLowerCase();
  if (!provider) {
    throw new Error(`provider is required; expected one of: ${providerList}`);
  }
  switch (provider) {
    case "codex":
      return "codex";
    case "claude":
    case "claude-code":
    case "claude_code":
      return "claude";
    case "opencode":
    case "open-code":
    case "open_code":
      return "opencode";
    case "pi":
    case "pi-agent":
    case "pi_agent":
      return "pi";
    case "dsh":
    case "deepseek":
    case "deepseek-harness":
    case "deepseek_harness":
      return "dsh";
    default:
      throw new Error(`unsupported provider ${JSON.stringify(raw)}; expected one of: ${providerList}`);
  }
}

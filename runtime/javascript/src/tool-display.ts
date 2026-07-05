/**
 * Tool Display Mapper — transforms raw tool call events into user-friendly
 * progress descriptions for the display channel.
 *
 * Architecture:
 * - The runner writes to TWO channels simultaneously:
 *   1. Raw transcript — preserves `[tool:Name]` markers + JSON for daemon-side
 *      event parsing and debugging. Does NOT stream to stderr.
 *   2. Display channel — writes business-friendly text to stderr → cell output → UI.
 * - The mapper decides what appears on the display channel; the raw transcript
 *   is always complete regardless of display decisions.
 *
 * Design principles:
 * - Internal/infrastructure tool calls are suppressed (return null) — the user
 *   doesn't need to see mkdir, echo, file reads, or other Claude Code machinery
 * - Unknown tools are silently suppressed — never floods the user with noise
 * - The mapping is extensible: custom entries can be added via `registerMapping()`
 *   to surface application-specific RPC calls (e.g. grpcurl methods)
 *
 * This module is intended for use by runner implementations. It produces display
 * text for stderr → cell.Output → UI, while the raw transcript channel preserves
 * `[tool:Name]` markers for daemon-side event parsing.
 */

// ─── Types ──────────────────────────────────────────────────────────────────

export interface ToolDisplayEntry {
  /** Emoji icon shown before the action text */
  icon: string;
  /** Human-readable action description (e.g. "Matching project") */
  action: string;
}

export interface ToolDisplayMapping {
  /**
   * Map a tool call to a display entry.
   * Returns null to suppress this tool from the display channel
   * (it will still appear in the raw transcript for debugging).
   */
  mapToolCall(toolName: string, input: Record<string, unknown>): ToolDisplayEntry | null;
  /** Map a tool result to a brief display string (empty string = skip) */
  mapToolResult(toolName: string, input: Record<string, unknown>, summary: string): string;
}

// ─── RPC method extraction from grpcurl command ─────────────────────────────

function extractRpcMethod(command: string): string | null {
  const match = command.match(/\/([A-Z][a-zA-Z0-9]+)\s*$/);
  if (match) return match[1];
  const parts = command.trim().split(/\s+/);
  const lastPart = parts[parts.length - 1];
  const slashMatch = lastPart.match(/\/([A-Z][a-zA-Z0-9]+)$/);
  return slashMatch ? slashMatch[1] : null;
}

// ─── Custom mapping registry ────────────────────────────────────────────────

const customMappings = new Map<string, ToolDisplayEntry>();

/**
 * Register a custom display mapping for an RPC method name or tool name.
 * Custom mappings take precedence over built-in ones.
 *
 * Example — surface grpcurl RPC calls in the display channel:
 * ```ts
 * registerMapping("GetProject",  { icon: "📋", action: "Fetching project" });
 * registerMapping("CreateDoc",   { icon: "📄", action: "Creating document" });
 * registerMapping("SendMail",    { icon: "📧", action: "Sending email" });
 * ```
 */
export function registerMapping(key: string, entry: ToolDisplayEntry): void {
  customMappings.set(key, entry);
}

// ─── Default mapper implementation ──────────────────────────────────────────

export const defaultToolDisplayMapper: ToolDisplayMapping = {
  mapToolCall(toolName: string, input: Record<string, unknown>): ToolDisplayEntry | null {
    // ── Bash tool ──────────────────────────────────────────────────────────
    if (toolName === "Bash") {
      const command = String(input.command || "");

      // grpcurl commands → delegate to custom mapping registry
      if (command.includes("grpcurl")) {
        const method = extractRpcMethod(command);
        if (method) {
          const custom = customMappings.get(method);
          if (custom) return custom;
          // Unregistered RPC method — suppress from display
          return null;
        }
        return null;
      }

      // All other Bash commands (mkdir, echo, ls, cd, npm, pip, etc.)
      // are Claude Code internal machinery. Suppress from display.
      return null;
    }

    // ── WebSearch — show it ───────────────────────────────────────────────
    if (toolName === "WebSearch") {
      return { icon: "🌐", action: "Searching the web" };
    }

    // ── All other tools (Read, Write, Edit, Glob, Grep, etc.) ────────────
    // These are internal Claude Code operations (reading config, writing temp
    // files, searching code). Suppress from display — users don't need to
    // see the agent's file I/O.
    return null;
  },

  mapToolResult(toolName: string, input: Record<string, unknown>, summary: string): string {
    // If the LLM provided a human-readable summary, show it
    if (summary && summary.trim()) {
      const trimmed = summary.trim();
      return trimmed.length > 200
        ? "✅ " + trimmed.slice(0, 197) + "..."
        : "✅ " + trimmed;
    }
    // Otherwise skip — the assistant text will communicate the result
    return "";
  },
};

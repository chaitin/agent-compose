import type { CodexOptions } from "@openai/codex-sdk";
import type { Provider } from "./types.js";

/** Daemon settings are scoped to one runtime invocation, never process globals. */
export interface AgentTelemetry {
  endpoint: string;
  headers: Record<string, string>;
  captureContent: boolean;
  attributes: Record<string, string>;
  /**
   * Caller's inbound W3C trace context, already validated. Present only when
   * the daemon relayed a syntactically valid value; a malformed value is
   * dropped rather than failing the run.
   */
  traceparent?: string;
}

export function readAgentTelemetry(provider: Provider, env: NodeJS.ProcessEnv): AgentTelemetry | undefined {
  if (!env.AGENT_COMPOSE_TELEMETRY) return undefined;
  let value: unknown;
  try { value = JSON.parse(env.AGENT_COMPOSE_TELEMETRY); }
  catch { throw new Error("Invalid agent telemetry configuration"); }
  if (!isRecord(value) || typeof value.endpoint !== "string" || !stringMap(value.headers ?? {}) ||
      !stringMap(value.attributes ?? {}) || (value.captureContent !== undefined && typeof value.captureContent !== "boolean")) {
    throw new Error("Invalid agent telemetry configuration");
  }
  let url: URL;
  try { url = new URL(value.endpoint); }
  catch { throw new Error("Invalid agent telemetry endpoint"); }
  if (!["http:", "https:"].includes(url.protocol) || url.username || url.password || url.search || url.hash) {
    throw new Error("Invalid agent telemetry endpoint");
  }
  if (provider === "pi" || provider === "gemini") {
    process.stderr.write(`[agent-compose-runtime] native telemetry integration is unavailable for ${provider}; no collector credentials forwarded\n`);
    return undefined;
  }
  const attributes: Record<string, string> = { ...(value.attributes as Record<string, string> ?? {}), "agent_compose.provider": provider };
  for (const [key, name] of [["AGENT_COMPOSE_RUN_ID", "agent_compose.run.id"], ["AGENT_COMPOSE_PROJECT_ID", "agent_compose.project.id"]]) {
    if (env[key]) attributes[name] = env[key]!;
  }
  // Malformed trace context is dropped, never fatal: the run must still start
  // on its own root trace exactly as it did before the field existed.
  const traceparent = validTraceparent(value.traceparent);
  return {
    endpoint: value.endpoint.replace(/\/+$/, ""),
    headers: { ...(value.headers as Record<string, string> ?? {}) },
    captureContent: value.captureContent === true,
    attributes,
    ...(traceparent === undefined ? {} : { traceparent }),
  };
}

/** Builds an isolated child environment; native settings remain untouched when unmanaged. */
export function providerTelemetryEnv(provider: Provider, telemetry: AgentTelemetry | undefined, source: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const env = { ...source };
  delete env.AGENT_COMPOSE_TELEMETRY;
  delete env.AGENT_COMPOSE_DSH_TELEMETRY;
  if (!telemetry) return env;
  // Per-signal destinations and legacy beta tracing must not redirect the
  // daemon's headers to a destination inherited from the guest image.
  for (const key of Object.keys(env)) {
    if (key.startsWith("OTEL_") || key === "BETA_TRACING_ENDPOINT" || key === "ENABLE_BETA_TRACING_DETAILED") delete env[key];
  }
  if (provider === "dsh") {
    delete env.DSH_TELEMETRY_DISABLED;
    env.DSH_TELEMETRY_MODE = "DISABLED";
    env.AGENT_COMPOSE_DSH_TELEMETRY = JSON.stringify(telemetry);
    return env;
  }
  env.OTEL_EXPORTER_OTLP_ENDPOINT = telemetry.endpoint;
  env.OTEL_EXPORTER_OTLP_PROTOCOL = "http/json";
  env.OTEL_EXPORTER_OTLP_HEADERS = encodedPairs(telemetry.headers);
  env.OTEL_RESOURCE_ATTRIBUTES = encodedPairs(telemetry.attributes);
  env.OTEL_EXPORTER_OTLP_TIMEOUT = "3000";
  // Codex and Claude Code parent their root span from an inbound W3C
  // TRACEPARENT environment variable — no OTEL_* key carries a parent, so it
  // must be set explicitly. Codex's `exec` path calls `set_parent_from_context`
  // on its root span (codex-rs/exec/src/lib.rs); Claude Code extracts it when
  // starting `claude_code.interaction` in Agent SDK / `-p` sessions and stamps
  // its OTLP event records with the resulting trace_id/span_id even while the
  // traces exporter stays disabled. Providers with no supported parent input
  // (opencode, dsh, pi, gemini) never receive it.
  if (telemetry.traceparent !== undefined && (provider === "codex" || provider === "claude")) {
    env.TRACEPARENT = telemetry.traceparent;
  }
  if (provider === "claude") {
    env.CLAUDE_CODE_ENABLE_TELEMETRY = "1";
    env.OTEL_METRICS_EXPORTER = "otlp";
    env.OTEL_LOGS_EXPORTER = "otlp";
    // Beta traces are not part of the supported Claude version contract.
    env.OTEL_TRACES_EXPORTER = "none";
    env.CLAUDE_CODE_ENHANCED_TELEMETRY_BETA = "0";
    env.ENABLE_ENHANCED_TELEMETRY_BETA = "0";
    env.OTEL_METRIC_EXPORT_INTERVAL = "1000";
    env.OTEL_LOGS_EXPORT_INTERVAL = "1000";
    for (const key of ["OTEL_LOG_USER_PROMPTS", "OTEL_LOG_TOOL_DETAILS", "OTEL_LOG_TOOL_CONTENT"]) {
      env[key] = telemetry.captureContent ? "1" : "0";
    }
  }
  if (provider === "opencode") {
    let config: Record<string, unknown> = {};
    if (source.OPENCODE_CONFIG_CONTENT) {
      try {
        const parsed: unknown = JSON.parse(source.OPENCODE_CONFIG_CONTENT);
        if (!isRecord(parsed)) throw new Error();
        config = parsed;
      } catch { throw new Error("Invalid OPENCODE_CONFIG_CONTENT for telemetry configuration"); }
    }
    // This version cannot independently redact AI SDK span inputs/outputs.
    // Keep infrastructure telemetry, but require opt-in for LLM content spans.
    config.experimental = { ...(isRecord(config.experimental) ? config.experimental : {}), openTelemetry: telemetry.captureContent };
    env.OPENCODE_CONFIG_CONTENT = JSON.stringify(config);
    // OpenCode parses headers literally rather than URI-decoding their values.
    env.OTEL_EXPORTER_OTLP_HEADERS = Object.entries(telemetry.headers).map(([k, v]) => `${k}=${v}`).join(",");
    if (Object.values(telemetry.headers).some((v) => v.includes(","))) {
      throw new Error("OpenCode telemetry header values cannot contain commas");
    }
  }
  return env;
}

export function codexTelemetryConfig(telemetry?: AgentTelemetry): NonNullable<CodexOptions["config"]> {
  if (!telemetry) return {};
  const exporter = (signal: string) => ({ "otlp-http": {
    endpoint: `${telemetry.endpoint}/v1/${signal}`, headers: telemetry.headers, protocol: "json",
  } });
  if (Object.keys(telemetry.headers).some((key) => key.includes("."))) {
    throw new Error("Codex telemetry header names cannot contain dots");
  }
  // SDK flattens map keys with dots, so attribution is carried through the
  // standard OTEL_RESOURCE_ATTRIBUTES environment instead of span_attributes.
  return { otel: {
    exporter: exporter("logs"), trace_exporter: exporter("traces"), metrics_exporter: exporter("metrics"),
    log_user_prompt: telemetry.captureContent,
  } };
}

function encodedPairs(values: Record<string, string>): string {
  return Object.entries(values).map(([key, value]) => `${encodeURIComponent(key)}=${encodeURIComponent(value)}`).join(",");
}

// W3C Trace Context version 00: 00-<32 lowercase hex trace-id>-<16 lowercase
// hex span-id>-<2 lowercase hex flags>. The all-zero trace-id and span-id are
// reserved and never valid.
const traceparentFormat = /^00-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}$/;

function validTraceparent(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const match = traceparentFormat.exec(value);
  if (!match || /^0+$/.test(match[1]) || /^0+$/.test(match[2])) return undefined;
  return value;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function stringMap(value: unknown): value is Record<string, string> {
  return isRecord(value) && Object.values(value).every((entry) => typeof entry === "string");
}

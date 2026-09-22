import { afterEach, describe, expect, it, vi } from "vitest";
import { codexTelemetryConfig, providerTelemetryEnv, readAgentTelemetry, type AgentTelemetry } from "../src/telemetry.js";
import { ClaudeRunner } from "../src/runners/claude.js";
import { OpenCodeRunner } from "../src/runners/opencode.js";
import { runnerOptions, withTempSession } from "./helpers.js";

const telemetry: AgentTelemetry = {
  endpoint: "http://collector:4318/prefix", headers: { authorization: "Bearer secret+token" },
  captureContent: false, attributes: { "agent_compose.sandbox.id": "sandbox-1" },
};

const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01";
const tracestate = "vendor=value,other=1";

afterEach(() => { vi.unstubAllEnvs(); vi.restoreAllMocks(); });

describe("agent telemetry", () => {
  it("reads per-invocation attribution without mutating the input", () => {
    const env = { AGENT_COMPOSE_TELEMETRY: JSON.stringify(telemetry), AGENT_COMPOSE_RUN_ID: "run-1", AGENT_COMPOSE_PROJECT_ID: "project-1" };
    const result = readAgentTelemetry("codex", env)!;
    expect(result.attributes).toEqual({ ...telemetry.attributes, "agent_compose.provider": "codex", "agent_compose.run.id": "run-1", "agent_compose.project.id": "project-1" });
    result.headers.authorization = "changed";
    expect(env.AGENT_COMPOSE_TELEMETRY).toBe(JSON.stringify(telemetry));
    expect(readAgentTelemetry("claude", { ...env, AGENT_COMPOSE_RUN_ID: "run-2" })?.attributes["agent_compose.run.id"]).toBe("run-2");
  });

  it("surfaces a valid inbound W3C trace context and forwards it only where a parent is honoured", () => {
    const result = readAgentTelemetry("codex", { AGENT_COMPOSE_TELEMETRY: JSON.stringify({ ...telemetry, traceparent, tracestate }) })!;
    expect(result.traceparent).toBe(traceparent);
    expect(result.tracestate).toBe(tracestate);
    // Codex parents its exec root span and Claude Code its interaction span
    // from the TRACEPARENT/TRACESTATE env vars; no OTEL_* key carries a parent.
    for (const provider of ["codex", "claude"] as const) {
      const env = providerTelemetryEnv(provider, result, {});
      expect(env.TRACEPARENT).toBe(traceparent);
      expect(env.TRACESTATE).toBe(tracestate);
    }
    for (const provider of ["opencode", "dsh", "pi", "gemini"] as const) {
      const env = providerTelemetryEnv(provider, result, {});
      expect(env.TRACEPARENT).toBeUndefined();
      expect(env.TRACESTATE).toBeUndefined();
    }
  });

  it("carries the trace context into the Claude SDK child environment", async () => {
    await withTempSession(async (root) => {
      const opts = new ClaudeRunner({ ...runnerOptions(root, "", "claude"), telemetry: { ...telemetry, traceparent, tracestate } }).queryOptions(null);
      const env = opts.env as NodeJS.ProcessEnv;
      expect(env.TRACEPARENT).toBe(traceparent);
      expect(env.TRACESTATE).toBe(tracestate);
    });
  });

  it.each([
    "not-a-traceparent",
    "",
    "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7",
    "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
    "00-4bf92f3577b34da6a3ce929d0e0e473-00f067aa0ba902b7-01",
    "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b-01",
    "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
    "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
    "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01",
    "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra",
    42,
  ])("ignores a malformed traceparent without throwing: %s", (value) => {
    const result = readAgentTelemetry("codex", { AGENT_COMPOSE_TELEMETRY: JSON.stringify({ ...telemetry, traceparent: value, tracestate }) });
    expect(result).toBeDefined();
    expect(result?.traceparent).toBeUndefined();
    // tracestate is only meaningful with a traceparent, so it is dropped too.
    expect(result?.tracestate).toBeUndefined();
    const env = providerTelemetryEnv("codex", result, {});
    expect(env.TRACEPARENT).toBeUndefined();
    expect(env.TRACESTATE).toBeUndefined();
  });

  it.each([
    "bad\u0000state",
    "bad\nstate",
    "a".repeat(513),
    42,
    "",
    "   ",
  ])("drops a malformed tracestate without dropping the traceparent: %s", (value) => {
    const result = readAgentTelemetry("codex", { AGENT_COMPOSE_TELEMETRY: JSON.stringify({ ...telemetry, traceparent, tracestate: value }) })!;
    expect(result.traceparent).toBe(traceparent);
    expect(result.tracestate).toBeUndefined();
    const env = providerTelemetryEnv("codex", result, {});
    expect(env.TRACEPARENT).toBe(traceparent);
    expect(env.TRACESTATE).toBeUndefined();
  });

  it("trims a valid tracestate before forwarding it", () => {
    const result = readAgentTelemetry("codex", { AGENT_COMPOSE_TELEMETRY: JSON.stringify({ ...telemetry, traceparent, tracestate: `  ${tracestate}  ` }) })!;
    expect(result.tracestate).toBe(tracestate);
    expect(providerTelemetryEnv("codex", result, {}).TRACESTATE).toBe(tracestate);
  });

  it("keeps the encoded payload and provider env unchanged when trace context is absent", () => {
    const result = readAgentTelemetry("codex", { AGENT_COMPOSE_TELEMETRY: JSON.stringify(telemetry) })!;
    expect(result).not.toHaveProperty("traceparent");
    expect(result).not.toHaveProperty("tracestate");
    // The DSH config is forwarded verbatim, so its encoding must stay byte-identical.
    expect(providerTelemetryEnv("dsh", telemetry, {}).AGENT_COMPOSE_DSH_TELEMETRY).toBe(JSON.stringify(telemetry));
    for (const provider of ["codex", "claude", "opencode", "dsh"] as const) {
      const env = providerTelemetryEnv(provider, result, {});
      expect(env.TRACEPARENT).toBeUndefined();
      expect(env.TRACESTATE).toBeUndefined();
    }
  });

  it.each(["not json", "null", "[]", '{"endpoint":"ftp://host"}', '{"endpoint":"http://secret@host"}', '{"endpoint":"http://host","headers":{"x":1}}'])
  ("rejects malformed runtime settings without reflecting secrets: %s", (raw) => {
    expect(() => readAgentTelemetry("codex", { AGENT_COMPOSE_TELEMETRY: raw })).toThrow(/^Invalid agent telemetry/);
  });

  it("keeps unmanaged native settings and strips the daemon envelope", () => {
    const source = { OTEL_EXPORTER_OTLP_ENDPOINT: "http://manual", AGENT_COMPOSE_TELEMETRY: "secret" };
    expect(readAgentTelemetry("codex", {})).toBeUndefined();
    expect(providerTelemetryEnv("claude", undefined, source)).toEqual({ OTEL_EXPORTER_OTLP_ENDPOINT: "http://manual" });
    expect(source.AGENT_COMPOSE_TELEMETRY).toBe("secret");
  });

  it.each(["pi", "gemini"] as const)("does not claim or pass managed telemetry to %s", (provider) => {
    const warning = vi.spyOn(process.stderr, "write").mockReturnValue(true);
    const source = { AGENT_COMPOSE_TELEMETRY: JSON.stringify(telemetry) };
    const config = readAgentTelemetry(provider, source);
    expect(config).toBeUndefined();
    expect(providerTelemetryEnv(provider, config, source)).toEqual({});
    expect(warning).toHaveBeenCalledWith(expect.stringContaining("unavailable"));
    expect(warning.mock.calls.flat().join("")).not.toContain("secret");
  });

  it("configures three Codex signals and leaves resource keys out of SDK dotted overrides", () => {
    const config = codexTelemetryConfig(telemetry) as any;
    expect(config.otel.exporter["otlp-http"]).toEqual({ endpoint: telemetry.endpoint + "/v1/logs", headers: telemetry.headers, protocol: "json" });
    expect(config.otel.trace_exporter["otlp-http"].endpoint).toBe(telemetry.endpoint + "/v1/traces");
    expect(config.otel.metrics_exporter["otlp-http"].endpoint).toBe(telemetry.endpoint + "/v1/metrics");
    expect(config.otel.log_user_prompt).toBe(false);
    expect(config.otel.span_attributes).toBeUndefined();
    expect(providerTelemetryEnv("codex", telemetry, {}).OTEL_RESOURCE_ATTRIBUTES).toContain("agent_compose.sandbox.id=sandbox-1");
    expect(codexTelemetryConfig()).toEqual({});
    expect(codexTelemetryConfig({ ...telemetry, captureContent: true })).toHaveProperty("otel.log_user_prompt", true);
  });

  it("Claude SDK gets managed exporters, redaction and destination precedence", async () => {
    vi.stubEnv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "http://wrong");
    vi.stubEnv("OTEL_EXPORTER_OTLP_LOGS_HEADERS", "authorization=wrong");
    vi.stubEnv("BETA_TRACING_ENDPOINT", "http://wrong");
    vi.stubEnv("AGENT_COMPOSE_TELEMETRY", "secret");
    await withTempSession(async (root) => {
      const opts = new ClaudeRunner({ ...runnerOptions(root, "", "claude"), telemetry }).queryOptions(null);
      const env = opts.env as NodeJS.ProcessEnv;
      expect(env.OTEL_EXPORTER_OTLP_ENDPOINT).toBe(telemetry.endpoint);
      expect(env.OTEL_EXPORTER_OTLP_HEADERS).toBe("authorization=Bearer%20secret%2Btoken");
      expect(env.OTEL_METRICS_EXPORTER).toBe("otlp");
      expect(env.OTEL_LOGS_EXPORTER).toBe("otlp");
      expect(env.OTEL_TRACES_EXPORTER).toBe("none");
      expect(env.OTEL_LOG_USER_PROMPTS).toBe("0");
      for (const key of ["OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_HEADERS", "BETA_TRACING_ENDPOINT", "AGENT_COMPOSE_TELEMETRY"]) expect(env[key]).toBeUndefined();
    });
  });

  it("OpenCode merges inline config without mutating shared config or process state", async () => {
    const original = JSON.stringify({ model: "custom/model", experimental: { other: true, openTelemetry: true } });
    vi.stubEnv("OPENCODE_CONFIG_CONTENT", original);
    await withTempSession(async (root) => {
      const runner = new OpenCodeRunner({ ...runnerOptions(root, "", "opencode"), telemetry });
      const env = await runner.environment();
      expect(JSON.parse(env.OPENCODE_CONFIG_CONTENT!)).toEqual({ model: "custom/model", experimental: { other: true, openTelemetry: false } });
      expect(env.OTEL_EXPORTER_OTLP_HEADERS).toBe("authorization=Bearer secret+token");
      expect(process.env.OPENCODE_CONFIG_CONTENT).toBe(original);
      const optedIn = providerTelemetryEnv("opencode", { ...telemetry, captureContent: true }, {});
      expect(JSON.parse(optedIn.OPENCODE_CONFIG_CONTENT!).experimental.openTelemetry).toBe(true);
    });
  });

  it("passes DSH settings only to its version-aware profile adapter", () => {
    const env = providerTelemetryEnv("dsh", telemetry, { DSH_TELEMETRY_DISABLED: "1", OTEL_EXPORTER_OTLP_HEADERS: "stale" });
    expect(JSON.parse(env.AGENT_COMPOSE_DSH_TELEMETRY!)).toEqual(telemetry);
    expect(env.DSH_TELEMETRY_MODE).toBe("DISABLED");
    expect(env.DSH_TELEMETRY_DISABLED).toBeUndefined();
    expect(env.OTEL_EXPORTER_OTLP_HEADERS).toBeUndefined();
  });
});

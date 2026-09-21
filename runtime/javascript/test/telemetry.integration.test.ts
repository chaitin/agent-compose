import { afterEach, describe, expect, it, vi } from "vitest";
import fs from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { CodexRunner } from "../src/runners/codex.js";
import { createInteractiveSession } from "../src/interactive.js";
import { runnerOptions, withTempSession } from "./helpers.js";

const capture = vi.hoisted(() => ({ options: [] as any[] }));
vi.mock("@openai/codex-sdk", () => ({ Codex: class {
  constructor(options: any) { capture.options.push(options); }
  startThread() { return { id: "thread-1", runStreamed: async () => ({ events: (async function* () { yield { type: "turn.completed", usage: {} }; })() }) }; }
  resumeThread() { return this.startThread(); }
} }));
afterEach(() => { vi.unstubAllEnvs(); vi.restoreAllMocks(); capture.options.length = 0; });

const telemetry = { endpoint: "http://collector:4318", headers: { authorization: "Bearer secret" }, captureContent: true, attributes: { "agent_compose.run.id": "run-1" } };

describe("telemetry runner integration", () => {
  it("configures Codex on prompt and interactive resume without overwriting developer instructions", async () => {
    await withTempSession(async (root) => {
      await new CodexRunner({ ...runnerOptions(root, "instructions"), telemetry }).runPrompt("hello");
      vi.stubEnv("AGENT_COMPOSE_TELEMETRY", JSON.stringify(telemetry));
      const session = await createInteractiveSession({ provider: "codex", stateRoot: path.join(root, "state"), home: path.join(root, "home"), workspace: root }, () => {});
      await session.runHumanMessage("next turn");
      expect(capture.options).toHaveLength(2);
      expect(capture.options[0].config.developer_instructions).toBe("instructions");
      for (const options of capture.options) {
        expect(options.config.otel.exporter["otlp-http"].endpoint).toBe("http://collector:4318/v1/logs");
        expect(options.env.OTEL_RESOURCE_ATTRIBUTES).toContain("agent_compose.run.id=run-1");
        expect(options.env.AGENT_COMPOSE_TELEMETRY).toBeUndefined();
      }
    });
  });

  it.each([true, false])("adapts the actual DSH backend capability FULL=%s without breaking execution", async (full) => {
    await withTempSession(async (root) => {
      const source = await fs.readFile(new URL("../../../assets/.dsh/profiles/agent-compose/telemetry.js", import.meta.url), "utf8");
      await fs.writeFile(path.join(root, "backend.mjs"), `export const SessionTelemetryMode = ${JSON.stringify(full ? { FULL: "FULL" } : { FEEDBACK_ONLY: "FEEDBACK_ONLY" })};`);
      await fs.writeFile(path.join(root, "telemetry.mjs"), source.replace("'@deepseek-ai/dsh-session-telemetry-otel'", "'./backend.mjs'"));
      const { apply } = await import(/* @vite-ignore */ pathToFileURL(path.join(root, "telemetry.mjs")).href);
      const ctx = { plugin: vi.fn().mockResolvedValue(undefined), on: vi.fn(), logger: { warn: vi.fn() } };
      vi.stubEnv("AGENT_COMPOSE_DSH_TELEMETRY", JSON.stringify(telemetry));
      await apply(ctx, {});
      if (full) {
        expect(ctx.plugin).toHaveBeenCalledWith(expect.anything(), expect.objectContaining({ mode: "FULL", exporter: { url: "http://collector:4318/v1/logs", headers: telemetry.headers, timeoutMillis: 3000 } }));
        const transform = ctx.on.mock.calls[0][1];
        const record = { attributes: { "session.id": "native-session" }, body: { type: "tool/call" } };
        expect(transform(record, () => record).attributes).toEqual({ ...record.attributes, ...telemetry.attributes });
        expect(record.attributes).toEqual({ "session.id": "native-session" });
      } else {
        expect(ctx.plugin).not.toHaveBeenCalled();
        expect(ctx.logger.warn).toHaveBeenCalledWith(expect.stringContaining("no FULL mode"));
      }
      ctx.plugin.mockClear();
      vi.stubEnv("AGENT_COMPOSE_DSH_TELEMETRY", JSON.stringify({ ...telemetry, captureContent: false }));
      await apply(ctx, {});
      expect(ctx.plugin).not.toHaveBeenCalled();
      vi.stubEnv("AGENT_COMPOSE_DSH_TELEMETRY", "");
      await apply(ctx, { mode: "DISABLED" });
      expect(ctx.plugin).toHaveBeenCalledWith(expect.anything(), { mode: "DISABLED" });
    });
  });

  it("runs without the optional DSH backend when telemetry is disabled", async () => {
    await withTempSession(async (root) => {
      const source = await fs.readFile(new URL("../../../assets/.dsh/profiles/agent-compose/telemetry.js", import.meta.url), "utf8");
      await fs.writeFile(path.join(root, "telemetry.mjs"), source.replace("'@deepseek-ai/dsh-session-telemetry-otel'", "'./missing-backend.mjs'"));
      const { apply } = await import(/* @vite-ignore */ pathToFileURL(path.join(root, "telemetry.mjs")).href);
      const ctx = { plugin: vi.fn().mockResolvedValue(undefined), on: vi.fn(), logger: { warn: vi.fn() } };
      vi.stubEnv("AGENT_COMPOSE_DSH_TELEMETRY", "");
      await expect(apply(ctx, { mode: "DISABLED" })).resolves.toBeUndefined();
      expect(ctx.plugin).not.toHaveBeenCalled();
      expect(ctx.logger.warn).not.toHaveBeenCalled();
    });
  });
});

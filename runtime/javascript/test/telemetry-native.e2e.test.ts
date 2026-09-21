import { createServer } from "node:http";
import fs from "node:fs/promises";
import path from "node:path";
import { once } from "node:events";
import { expect, it } from "vitest";
import { Codex } from "@openai/codex-sdk";
import { codexTelemetryConfig, providerTelemetryEnv } from "../src/telemetry.js";
import { stringEnv } from "../src/env.js";
import { withTempSession } from "./helpers.js";

// Opt-in native provider test. Both the model endpoint and OTLP receiver are
// loopback fixtures; no model credentials or public network service is used.
it.skipIf(!process.env.AGENT_COMPOSE_TEST_CODEX_BINARY)("native Codex flushes authenticated OTLP on an API failure", async () => {
  await withTempSession(async (root) => {
    const received: { path: string; authorization?: string; body: string }[] = [];
    const server = createServer(async (req, res) => {
      const chunks: Buffer[] = [];
      for await (const chunk of req) chunks.push(Buffer.from(chunk));
      received.push({ path: req.url!, authorization: req.headers.authorization, body: Buffer.concat(chunks).toString() });
      res.setHeader("content-type", "application/json");
      if (req.url?.startsWith("/model")) {
        res.writeHead(401);
        res.end(JSON.stringify({ error: { message: "fixture rejection", type: "authentication_error" } }));
      } else res.end("{}");
    });
    server.listen(0, "127.0.0.1");
    await once(server, "listening");
    const address = server.address() as { port: number };
    const endpoint = `http://127.0.0.1:${address.port}`;
    await fs.mkdir(path.join(root, "home"));
    const telemetry = { endpoint, headers: { authorization: "Bearer collector-fixture" }, captureContent: false, attributes: { "agent_compose.run.id": "fixture-run" } };
    const codex = new Codex({
      codexPathOverride: process.env.AGENT_COMPOSE_TEST_CODEX_BINARY,
      env: stringEnv(providerTelemetryEnv("codex", telemetry, {
        PATH: process.env.PATH, HOME: path.join(root, "home"), CODEX_HOME: path.join(root, "home"),
        FIXTURE_API_KEY: "fixture-model-key",
      })),
      config: {
        ...codexTelemetryConfig(telemetry),
        model_provider: "fixture", model: "fixture-model", check_for_update_on_startup: false,
        features: { plugins: false, plugin_hooks: false, remote_plugin: false, plugin_sharing: false },
        model_providers: { fixture: { name: "fixture", base_url: `${endpoint}/model`, env_key: "FIXTURE_API_KEY", wire_api: "responses", request_max_retries: 0 } },
      },
    });
    try {
      const thread = codex.startThread({ workingDirectory: root, skipGitRepoCheck: true, approvalPolicy: "never", sandboxMode: "read-only" });
      await expect(thread.run("fixture prompt", { signal: AbortSignal.timeout(20000) })).rejects.toThrow();
      expect(received.some((request) => request.path.startsWith("/model"))).toBe(true);
      const logs = received.filter((request) => request.path === "/v1/logs");
      expect(logs.length).toBeGreaterThan(0);
      expect(logs.every((request) => request.authorization === "Bearer collector-fixture")).toBe(true);
      const resources = logs.flatMap((request) => JSON.parse(request.body).resourceLogs);
      expect(JSON.stringify(resources)).toContain("agent_compose.run.id");
      expect(JSON.stringify(resources)).toContain("fixture-run");
    } finally {
      server.closeAllConnections();
      await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
    }
  });
}, 30000);

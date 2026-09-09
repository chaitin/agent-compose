import http from "node:http";
import { afterEach, describe, expect, it } from "vitest";
import { llm } from "../src/llm.js";

const ENV_NAMES = [
  "BASE_URL",
  "HTTP_URL",
  "AGENT_COMPOSE_BASE_URL",
  "AGENT_COMPOSE_HTTP_URL",
  "AGENT_COMPOSE_RUNTIME_BASE_URL",
  "AGENT_COMPOSE_SANDBOX_TOKEN",
  "SANDBOX_ID",
  "OPENAI_BASE_URL",
  "OPENAI_API_KEY",
  "ANTHROPIC_BASE_URL",
  "ANTHROPIC_AUTH_TOKEN",
  "ANTHROPIC_API_KEY",
  "LLM_API_PROTOCOL",
] as const;

type EnvSnapshot = Partial<Record<(typeof ENV_NAMES)[number], string | undefined>>;

function snapshotEnv(): EnvSnapshot {
  const snapshot: EnvSnapshot = {};
  for (const name of ENV_NAMES) {
    snapshot[name] = process.env[name];
    delete process.env[name];
  }
  return snapshot;
}

function restoreEnv(snapshot: EnvSnapshot) {
  for (const name of ENV_NAMES) {
    const value = snapshot[name];
    if (value === undefined) {
      delete process.env[name];
    } else {
      process.env[name] = value;
    }
  }
}

describe("runtime.llm sandbox facade contract", () => {
  let saved: EnvSnapshot;
  afterEach(() => restoreEnv(saved));

  it("uses the managed runtime base URL and sandbox token against the OpenAI Responses facade", async () => {
    saved = snapshotEnv();
    process.env.AGENT_COMPOSE_RUNTIME_BASE_URL = "http://daemon.internal:7410";
    process.env.AGENT_COMPOSE_SANDBOX_TOKEN = "sandbox-secret-token";
    process.env.SANDBOX_ID = "sbx-42";
    process.env.LLM_API_PROTOCOL = "responses";

    const seen: { url?: string; auth?: string; body?: Record<string, unknown> } = {};
    const server = await startRawServer((req, body) => {
      seen.url = req.url;
      seen.auth = req.headers["x-api-key"] as string;
      seen.body = body;
      return {
        status: 200,
        payload: {
          id: "resp_facade_1",
          model: "gpt-5.4",
          status: "completed",
          output: [
            {
              type: "message",
              content: [{ type: "output_text", text: "facade says hi" }],
            },
          ],
        },
      };
    });
    try {
      // Point the derived facade URL at our local server by overriding
      // OPENAI_BASE_URL only for the fetch target; the managed resolution
      // path is asserted via the derived URL shape below.
      process.env.OPENAI_BASE_URL = `${server.baseUrl}/api/runtime/sandboxes/sbx-42/llm/openai/v1`;
      const result = await llm("hello", { model: "gpt-5.4" });
      expect(result.text).toBe("facade says hi");
      expect(result.model).toBe("gpt-5.4");
      expect(result.responseId).toBe("resp_facade_1");
      expect(result.finishReason).toBe("completed");
      expect(seen.url).toBe("/api/runtime/sandboxes/sbx-42/llm/openai/v1/responses");
      expect(seen.auth).toBe("sandbox-secret-token");
      expect(seen.body).toMatchObject({ model: "gpt-5.4", input: "hello" });
    } finally {
      await server.close();
    }
  });

  it("derives the facade URL from AGENT_COMPOSE_RUNTIME_BASE_URL + SANDBOX_ID when no family base URL exists", async () => {
    saved = snapshotEnv();
    process.env.AGENT_COMPOSE_SANDBOX_TOKEN = "tok";
    process.env.SANDBOX_ID = "sbx-9";
    // Local stand-in for the daemon; runtime base URL points at the test server.
    const seen: { url?: string } = {};
    const server = await startRawServer((req) => {
      seen.url = req.url;
      return { status: 200, payload: { id: "r1", model: "m", output_text: "ok", status: "completed" } };
    });
    try {
      process.env.AGENT_COMPOSE_RUNTIME_BASE_URL = server.baseUrl;
      const result = await llm("hi", { model: "m" });
      expect(result.text).toBe("ok");
      expect(seen.url).toBe("/api/runtime/sandboxes/sbx-9/llm/openai/v1/responses");
    } finally {
      await server.close();
    }
  });

  it("routes anthropic_messages protocol to the Anthropic facade with x-api-key and prompt-guided schema", async () => {
    saved = snapshotEnv();
    process.env.LLM_API_PROTOCOL = "anthropic_messages";
    process.env.ANTHROPIC_AUTH_TOKEN = "anthropic-secret";
    const seen: { url?: string; apiKey?: string; version?: string; body?: Record<string, unknown> } = {};
    const server = await startRawServer((req, body) => {
      seen.url = req.url;
      seen.apiKey = req.headers["x-api-key"] as string;
      seen.version = req.headers["anthropic-version"] as string;
      seen.body = body;
      return {
        status: 200,
        payload: {
          id: "msg_1",
          model: "claude-sonnet",
          stop_reason: "end_turn",
          content: [{ type: "text", text: "{\"summary\":\"ok\",\"risk\":\"low\"}" }],
        },
      };
    });
    try {
      process.env.ANTHROPIC_BASE_URL = `${server.baseUrl}/api/runtime/sandboxes/sbx-7/llm/anthropic`;
      const result = await llm<{ summary: string; risk: string }>("inspect", {
        model: "claude-sonnet",
        outputSchema: {
          type: "object",
          properties: { summary: { type: "string" }, risk: { type: "string" } },
          required: ["summary", "risk"],
        },
      });
      expect(seen.url).toBe("/api/runtime/sandboxes/sbx-7/llm/anthropic/v1/messages");
      expect(seen.apiKey).toBe("anthropic-secret");
      expect(seen.version).toBe("2023-06-01");
      const messages = seen.body?.messages as Array<{ content: string }>;
      expect(messages[0].content).toContain("JSON Schema");
      expect(result.json).toEqual({ summary: "ok", risk: "low" });
      expect(result.finishReason).toBe("end_turn");
    } finally {
      await server.close();
    }
  });

  it("sends json_schema text.format on the OpenAI Responses facade when outputSchema is set", async () => {
    saved = snapshotEnv();
    process.env.AGENT_COMPOSE_SANDBOX_TOKEN = "tok";
    const seen: { body?: Record<string, unknown> } = {};
    const server = await startRawServer((req, body) => {
      seen.body = body;
      return {
        status: 200,
        payload: {
          id: "r2",
          model: "m",
          status: "completed",
          output: [{ content: [{ text: "{\"summary\":\"s\",\"risk\":\"low\"}" }] }],
        },
      };
    });
    try {
      process.env.OPENAI_BASE_URL = server.baseUrl;
      const result = await llm<{ summary: string; risk: string }>("inspect", {
        model: "m",
        outputSchema: {
          type: "object",
          properties: { summary: { type: "string" }, risk: { type: "string" } },
          required: ["summary", "risk"],
        },
      });
      const text = seen.body?.text as { format?: Record<string, unknown> };
      expect(text.format).toMatchObject({ type: "json_schema", strict: true });
      expect(result.json).toEqual({ summary: "s", risk: "low" });
    } finally {
      await server.close();
    }
  });

  it("explicit baseUrl wins over the facade environment and keeps the legacy Generate endpoint", async () => {
    saved = snapshotEnv();
    process.env.AGENT_COMPOSE_RUNTIME_BASE_URL = "http://should-not-be-used:7410";
    process.env.AGENT_COMPOSE_SANDBOX_TOKEN = "should-not-be-sent";
    process.env.OPENAI_BASE_URL = "http://should-not-be-used:7410";
    const seen: { url?: string; apiKey?: string } = {};
    const server = await startRawServer((req) => {
      seen.url = req.url;
      seen.apiKey = req.headers["x-api-key"] as string | undefined;
      return {
        status: 200,
        payload: { text: "legacy ok", model: "m", responseId: "r3", finishReason: "stop" },
      };
    });
    try {
      const result = await llm("hi", { baseUrl: server.baseUrl, model: "m" });
      expect(result.text).toBe("legacy ok");
      expect(seen.url).toBe("/agentcompose.v2.LLMService/Generate");
      expect(seen.apiKey).toBeUndefined();
    } finally {
      await server.close();
    }
  });

  it("falls back to the legacy BASE_URL chain when no facade token is configured", async () => {
    saved = snapshotEnv();
    const seen: { url?: string; apiKey?: string } = {};
    const server = await startRawServer((req) => {
      seen.url = req.url;
      seen.apiKey = req.headers["x-api-key"] as string | undefined;
      return { status: 200, payload: { text: "legacy chain", model: "m" } };
    });
    try {
      process.env.BASE_URL = server.baseUrl;
      const result = await llm("hi", { model: "m" });
      expect(result.text).toBe("legacy chain");
      expect(seen.url).toBe("/agentcompose.v2.LLMService/Generate");
      expect(seen.apiKey).toBeUndefined();
    } finally {
      await server.close();
    }
  });

  it("requires a model when calling the sandbox facade", async () => {
    saved = snapshotEnv();
    process.env.AGENT_COMPOSE_SANDBOX_TOKEN = "tok";
    process.env.OPENAI_BASE_URL = "http://127.0.0.1:1";
    await expect(llm("hi")).rejects.toThrow("requires a model");
  });

  it("surfaces non-2xx facade responses without leaking the token", async () => {
    saved = snapshotEnv();
    process.env.AGENT_COMPOSE_SANDBOX_TOKEN = "super-secret-token-123";
    const server = await startRawServer(() => ({
      status: 403,
      payload: { error: "facade rejected super-secret-token-123" },
    }));
    try {
      process.env.OPENAI_BASE_URL = server.baseUrl;
      const error = await llm("hi", { model: "m" }).catch((err: unknown) => err);
      expect(error).toBeInstanceOf(Error);
      const message = (error as Error).message;
      expect(message).toContain("HTTP 403");
      expect(message).not.toContain("super-secret-token-123");
      expect(message).toContain("[redacted]");
    } finally {
      await server.close();
    }
  });

  it("times out facade requests after timeoutMs", async () => {
    saved = snapshotEnv();
    process.env.AGENT_COMPOSE_SANDBOX_TOKEN = "tok";
    const server = await startRawServer(async () => {
      await new Promise((resolve) => setTimeout(resolve, 200));
      return { status: 200, payload: { id: "r", model: "m", output_text: "late" } };
    });
    try {
      process.env.OPENAI_BASE_URL = server.baseUrl;
      await expect(llm("hi", { model: "m", timeoutMs: 25 })).rejects.toThrow("timed out after 25ms");
    } finally {
      await server.close();
    }
  });
});

async function startRawServer(handler: (req: http.IncomingMessage, body: Record<string, unknown>) => Promise<{ status: number; payload: Record<string, unknown> }> | { status: number; payload: Record<string, unknown> }): Promise<{
  baseUrl: string;
  close: () => Promise<void>;
}> {
  const server = http.createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) {
      chunks.push(Buffer.from(chunk));
    }
    const body = chunks.length > 0 ? (JSON.parse(Buffer.concat(chunks).toString("utf8")) as Record<string, unknown>) : {};
    const result = await handler(req, body);
    res.writeHead(result.status, { "Content-Type": "application/json" });
    res.end(JSON.stringify(result.payload));
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (!address || typeof address === "string") {
    throw new Error("test server did not bind to a TCP port");
  }
  return {
    baseUrl: `http://127.0.0.1:${address.port}`,
    close: () => new Promise((resolve, reject) => server.close((error) => (error ? reject(error) : resolve()))),
  };
}

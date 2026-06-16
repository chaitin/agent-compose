import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { captureStdio, runnerOptions, withTempSession } from "./helpers.js";

const fetchState = vi.hoisted(() => ({
  calls: [] as Array<{ url: string; init: RequestInit }>,
  response: {
    ok: true,
    status: 200,
    body: null as ReadableStream<Uint8Array> | null,
    text: async () => "",
  },
}));

vi.stubGlobal("fetch", vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
  fetchState.calls.push({ url: String(url), init: init || {} });
  return fetchState.response;
}));

function sseBody(chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  let index = 0;
  return new ReadableStream({
    pull(controller) {
      if (index >= chunks.length) {
        controller.close();
        return;
      }
      controller.enqueue(encoder.encode(chunks[index]));
      index += 1;
    },
  });
}

describe("DeepSeekRunner", () => {
  beforeEach(() => {
    fetchState.calls = [];
    fetchState.response = {
      ok: true,
      status: 200,
      body: sseBody([
        'data: {"id":"chat-1","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}\n\n',
        'data: {"id":"chat-1","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}\n\n',
        "data: [DONE]\n\n",
      ]),
      text: async () => "",
    };
    vi.stubEnv("DEEPSEEK_API_KEY", "test-key");
    vi.stubEnv("DEEPSEEK_API_BASE", undefined);
    vi.stubEnv("DEEPSEEK_MODEL", undefined);
  });

  afterEach(() => {
    vi.unstubAllEnvs();
    vi.clearAllMocks();
  });

  it("streams chat completions and returns transcript", async () => {
    const { DeepSeekRunner } = await import("../src/runners/deepseek.js");
    await withTempSession(async (root) => {
      const stdio = captureStdio();
      try {
        const result = await new DeepSeekRunner(runnerOptions(root, "mpi context", "deepseek")).runPrompt("prompt");

        expect(result).toMatchObject({
          provider: "deepseek",
          sessionId: "chat-1",
          finalText: "hello world",
          stopReason: "completed",
        });
        expect(result.transcript).toBe("hello world");
        expect(fetchState.calls.at(-1)?.url).toBe("https://api.deepseek.com/chat/completions");
        const body = JSON.parse(String(fetchState.calls.at(-1)?.init.body));
        expect(body.model).toBe("deepseek-v4-flash");
        expect(body.messages).toEqual([
          { role: "system", content: "mpi context" },
          { role: "user", content: "prompt" },
        ]);
      } finally {
        stdio.restore();
      }
    });
  });

  it("requires DEEPSEEK_API_KEY", async () => {
    vi.stubEnv("DEEPSEEK_API_KEY", undefined);
    const { DeepSeekRunner } = await import("../src/runners/deepseek.js");
    await withTempSession(async (root) => {
      await expect(new DeepSeekRunner(runnerOptions(root, "", "deepseek")).runPrompt("prompt")).rejects.toThrow(
        "Missing environment variable: DEEPSEEK_API_KEY",
      );
    });
  });

  it("surfaces API errors", async () => {
    fetchState.response = {
      ok: false,
      status: 401,
      body: null,
      text: async () => "invalid key",
    };
    const { DeepSeekRunner } = await import("../src/runners/deepseek.js");
    await withTempSession(async (root) => {
      await expect(new DeepSeekRunner(runnerOptions(root, "", "deepseek")).runPrompt("prompt")).rejects.toThrow(
        "deepseek API error 401: invalid key",
      );
    });
  });
});

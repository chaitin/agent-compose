import http from "node:http";
import { describe, expect, it } from "vitest";
import { z } from "zod";
import { runtime } from "../src/index.js";

describe("runtime.llm", () => {
  it("calls the LLM service with an output schema and parses structured JSON output", async () => {
    const server = await startLLMServer(async (body) => {
      expect(body.prompt).toBe("summarize");
      expect(body.model).toBe("model-a");
      expect(JSON.parse(body.outputSchema)).toMatchObject({
        type: "object",
        required: ["summary", "risk"],
      });
      return {
        text: JSON.stringify({ summary: "ok", risk: "low" }),
        model: "model-a",
        responseId: "resp-1",
        finishReason: "stop",
        json: JSON.stringify({ summary: "ok", risk: "low" }),
      };
    });
    try {
      const result = await runtime.llm<{ summary: string; risk: string }>("summarize", {
        baseUrl: server.baseUrl,
        model: "model-a",
        outputSchema: {
          type: "object",
          properties: {
            summary: { type: "string" },
            risk: { type: "string" },
          },
          required: ["summary", "risk"],
        },
      });

      expect(result.text).toBe("{\"summary\":\"ok\",\"risk\":\"low\"}");
      expect(result.json).toEqual({ summary: "ok", risk: "low" });
      expect(result.responseId).toBe("resp-1");
      expect(result.finishReason).toBe("stop");
    } finally {
      await server.close();
    }
  });

  it("accepts Zod schemas, converts them to JSON Schema, and validates parsed output", async () => {
    const server = await startLLMServer(async (body) => {
      const schema = JSON.parse(body.outputSchema);
      expect(schema).toMatchObject({
        type: "object",
        properties: {
          summary: { type: "string" },
          risk: { type: "string", enum: ["low", "high"] },
        },
        additionalProperties: false,
      });
      return {
        text: JSON.stringify({ summary: "ok", risk: "high" }),
        model: "model-a",
      };
    });
    try {
      const result = await runtime.llm("summarize", {
        baseUrl: server.baseUrl,
        outputSchema: z.object({
          summary: z.string(),
          risk: z.enum(["low", "high"]),
        }),
      });

      expect(result.json?.risk).toBe("high");
    } finally {
      await server.close();
    }
  });

  it("throws when Zod schema validation rejects parsed JSON output", async () => {
    const server = await startLLMServer(async () => ({
      text: JSON.stringify({ summary: "ok", risk: "medium" }),
    }));
    try {
      await expect(runtime.llm("summarize", {
        baseUrl: server.baseUrl,
        outputSchema: z.object({
          summary: z.string(),
          risk: z.enum(["low", "high"]),
        }),
      })).rejects.toThrow("llm JSON output does not match outputSchema");
    } finally {
      await server.close();
    }
  });

  it("throws when structured LLM output is not valid JSON", async () => {
    const server = await startLLMServer(async () => ({ text: "not json" }));
    try {
      await expect(runtime.llm("summarize", {
        baseUrl: server.baseUrl,
        outputSchema: { type: "object" },
      })).rejects.toThrow("llm text is not valid JSON for outputSchema");
    } finally {
      await server.close();
    }
  });
});

async function startLLMServer(handler: (body: Record<string, string>) => Promise<Record<string, unknown>> | Record<string, unknown>): Promise<{
  baseUrl: string;
  close: () => Promise<void>;
}> {
  const server = http.createServer(async (req, res) => {
    if (req.method !== "POST" || req.url !== "/agentcompose.v2.LLMService/Generate") {
      res.writeHead(404);
      res.end();
      return;
    }
    const chunks: Buffer[] = [];
    for await (const chunk of req) {
      chunks.push(Buffer.from(chunk));
    }
    const body = JSON.parse(Buffer.concat(chunks).toString("utf8")) as Record<string, string>;
    const payload = await handler(body);
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify(payload));
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (!address || typeof address === "string") {
    throw new Error("test server did not bind to a TCP port");
  }
  return {
    baseUrl: `http://127.0.0.1:${address.port}`,
    close: () => new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve())),
  };
}

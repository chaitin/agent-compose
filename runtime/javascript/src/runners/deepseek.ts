import process from "node:process";
import { TranscriptWriter } from "../transcript.js";
import type { AgentResult, RunnerOptions } from "../types.js";

function deepseekChatCompletionsURL(base: string): string {
  const trimmed = base.replace(/\/$/, "");
  if (trimmed.endsWith("/chat/completions")) {
    return trimmed;
  }
  if (trimmed.endsWith("/v1")) {
    return `${trimmed}/chat/completions`;
  }
  return `${trimmed}/chat/completions`;
}

export class DeepSeekRunner {
  private readonly writer = new TranscriptWriter();

  constructor(private readonly options: RunnerOptions) {}

  async runPrompt(promptText: string): Promise<AgentResult> {
    const apiKey = process.env.DEEPSEEK_API_KEY?.trim();
    if (!apiKey) {
      throw new Error("Missing environment variable: DEEPSEEK_API_KEY");
    }

    const baseUrl = process.env.DEEPSEEK_API_BASE?.trim() || "https://api.deepseek.com";
    const model = process.env.DEEPSEEK_MODEL?.trim() || "deepseek-v4-flash";
    const messages: Array<{ role: "system" | "user"; content: string }> = [];
    const mpiContext = this.options.mpiContext.trim();
    if (mpiContext) {
      messages.push({ role: "system", content: mpiContext });
    }
    if (this.options.outputSchema) {
      messages.push({
        role: "system",
        content: "Respond with a single JSON object that matches the requested schema.",
      });
    }
    messages.push({ role: "user", content: promptText });

    const body: Record<string, unknown> = {
      model,
      messages,
      stream: true,
    };
    if (this.options.outputSchema) {
      body.response_format = { type: "json_object" };
    }

    const response = await fetch(deepseekChatCompletionsURL(baseUrl), {
      method: "POST",
      headers: {
        Authorization: `Bearer ${apiKey}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify(body),
    });
    if (!response.ok) {
      const text = await response.text();
      throw new Error(`deepseek API error ${response.status}: ${text}`);
    }
    if (!response.body) {
      throw new Error("deepseek response has no body");
    }

    const result: AgentResult = {
      provider: "deepseek",
      sessionId: "",
      stopReason: "completed",
      finalText: "",
      transcript: "",
      stderr: "",
    };

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    let content = "";

    while (true) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }
      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split("\n");
      buffer = lines.pop() || "";
      for (const line of lines) {
        if (!line.startsWith("data: ")) {
          continue;
        }
        const data = line.slice(6).trim();
        if (!data || data === "[DONE]") {
          continue;
        }
        let event: Record<string, unknown>;
        try {
          event = JSON.parse(data) as Record<string, unknown>;
        } catch {
          continue;
        }
        if (typeof event.id === "string" && event.id) {
          result.sessionId = event.id;
        }
        const choices = Array.isArray(event.choices) ? event.choices : [];
        const choice = choices[0] as Record<string, unknown> | undefined;
        const delta = choice?.delta as Record<string, unknown> | undefined;
        const deltaContent = delta?.content;
        if (typeof deltaContent === "string" && deltaContent) {
          content += deltaContent;
          this.writer.write(deltaContent);
        }
        const finishReason = choice?.finish_reason;
        if (finishReason === "stop" || finishReason === "length") {
          result.stopReason = "completed";
        } else if (finishReason) {
          result.stopReason = "error";
        }
      }
    }

    result.finalText = content;
    result.transcript = this.writer.transcript();
    return result;
  }
}

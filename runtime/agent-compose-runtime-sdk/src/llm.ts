import { type output as ZodOutput, type ZodType } from "zod";
import { optionalEnvValue } from "./env.js";
import { normalizeOptionalOutputSchema, parseJsonOutput, type RuntimeJsonSchema, type RuntimeOutputSchema } from "./schema.js";

const DEFAULT_BASE_URL = "http://127.0.0.1:7410";

export type RuntimeLLMOutputSchema = RuntimeOutputSchema;

export interface RuntimeLLMOptions<S extends RuntimeLLMOutputSchema = RuntimeLLMOutputSchema> {
  model?: string;
  baseUrl?: string;
  timeoutMs?: number;
  outputSchema?: S;
}

export interface RuntimeLLMResult<T = unknown> {
  text: string;
  model: string;
  responseId: string;
  finishReason: string;
  json: T | null;
}

export async function llm<S extends ZodType>(prompt: string, options: RuntimeLLMOptions<S> & { outputSchema: S }): Promise<RuntimeLLMResult<ZodOutput<S>>>;
export async function llm<T = unknown>(prompt: string, options?: RuntimeLLMOptions<RuntimeJsonSchema>): Promise<RuntimeLLMResult<T>>;
export async function llm<T = unknown>(prompt: string, options: RuntimeLLMOptions = {}): Promise<RuntimeLLMResult<T>> {
  const trimmedPrompt = prompt.trim();
  if (!trimmedPrompt) {
    throw new Error("runtime.llm requires a non-empty prompt");
  }
  const { schema, validator } = normalizeOptionalOutputSchema(options.outputSchema, "llm");
  const controller = new AbortController();
  let timeout: NodeJS.Timeout | undefined;
  if (options.timeoutMs && options.timeoutMs > 0) {
    timeout = setTimeout(() => controller.abort(), options.timeoutMs);
  }
  try {
    const response = await fetch(connectProcedureURL(options.baseUrl, "/agentcompose.v2.LLMService/Generate"), {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        prompt: trimmedPrompt,
        ...(options.model ? { model: options.model } : {}),
        ...(schema ? { outputSchema: JSON.stringify(schema) } : {}),
      }),
      signal: controller.signal,
    });
    const responseText = await response.text();
    if (!response.ok) {
      throw new Error(`runtime.llm request failed with HTTP ${response.status}: ${responseText}`);
    }
    const payload = JSON.parse(responseText) as {
      text?: string;
      model?: string;
      responseId?: string;
      response_id?: string;
      finishReason?: string;
      finish_reason?: string;
      json?: string;
    };
    const text = payload.text ?? "";
    return {
      text,
      model: payload.model ?? options.model ?? "",
      responseId: payload.responseId ?? payload.response_id ?? "",
      finishReason: payload.finishReason ?? payload.finish_reason ?? "",
      json: schema ? parseJsonOutput<T>(payload.json || text, validator, "llm text") : null,
    };
  } catch (error) {
    if (error instanceof Error && error.name === "AbortError") {
      throw new Error(`runtime.llm timed out after ${options.timeoutMs}ms`, { cause: error });
    }
    throw error;
  } finally {
    if (timeout) {
      clearTimeout(timeout);
    }
  }
}

function connectProcedureURL(baseUrl: string | undefined, procedure: string): string {
  const base = (
    baseUrl ??
    optionalEnvValue("BASE_URL") ??
    optionalEnvValue("HTTP_URL") ??
    optionalEnvValue("AGENT_COMPOSE_BASE_URL") ??
    optionalEnvValue("AGENT_COMPOSE_HTTP_URL") ??
    DEFAULT_BASE_URL
  ).trim();
  if (!base) {
    return procedure;
  }
  return base.replace(/\/+$/, "") + procedure;
}

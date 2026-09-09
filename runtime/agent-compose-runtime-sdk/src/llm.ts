import { type output as ZodOutput, type ZodType } from "zod";
import { optionalEnvValue } from "./env.js";
import { normalizeOptionalOutputSchema, parseJsonOutput, type RuntimeJsonSchema, type RuntimeOutputSchema } from "./schema.js";

const DEFAULT_BASE_URL = "http://127.0.0.1:7410";
const LEGACY_GENERATE_PROCEDURE = "/agentcompose.v2.LLMService/Generate";
const ANTHROPIC_VERSION = "2023-06-01";
const ANTHROPIC_DEFAULT_MAX_TOKENS = 8192;

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
  const { schema = null, validator } = normalizeOptionalOutputSchema(options.outputSchema, "llm");
  const endpoint = resolveLLMEndpoint(options);
  if (endpoint.protocol !== "connect-generate" && !(options.model ?? "").trim()) {
    throw new Error("runtime.llm requires a model when calling the sandbox LLM facade");
  }
  const controller = new AbortController();
  let timeout: NodeJS.Timeout | undefined;
  if (options.timeoutMs && options.timeoutMs > 0) {
    timeout = setTimeout(() => controller.abort(), options.timeoutMs);
  }
  try {
    const response = await postLLMRequest(trimmedPrompt, options, schema, endpoint, controller.signal);
    const responseText = await response.text();
    if (!response.ok) {
      throw new Error(`runtime.llm request failed with HTTP ${response.status}: ${redactSecrets(responseText)}`);
    }
    const payload = JSON.parse(responseText) as Record<string, unknown>;
    const decoded = decodeLLMResponse(endpoint.protocol, payload, options.model);
    return {
      text: decoded.text,
      model: decoded.model,
      responseId: decoded.responseId,
      finishReason: decoded.finishReason,
      json: schema ? parseJsonOutput<T>(decoded.text, validator, "llm text") : null,
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

type FacadeWireProtocol = "openai-responses" | "anthropic-messages";
type LLMProtocol = "connect-generate" | FacadeWireProtocol;

interface ResolvedLLMEndpoint {
  url: string;
  protocol: LLMProtocol;
  token?: string;
}

interface DecodedLLMResponse {
  text: string;
  model: string;
  responseId: string;
  finishReason: string;
}

async function postLLMRequest(prompt: string, options: RuntimeLLMOptions, schema: RuntimeJsonSchema | null, endpoint: ResolvedLLMEndpoint, signal: AbortSignal): Promise<Response> {
  if (endpoint.protocol === "openai-responses") {
    return fetch(endpoint.url, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "x-api-key": endpoint.token ?? "",
      },
      body: JSON.stringify(buildResponsesRequest(prompt, options, schema)),
      signal,
    });
  }
  if (endpoint.protocol === "anthropic-messages") {
    return fetch(endpoint.url, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "anthropic-version": ANTHROPIC_VERSION,
        "x-api-key": endpoint.token ?? "",
      },
      body: JSON.stringify(buildAnthropicMessagesRequest(prompt, options, schema)),
      signal,
    });
  }
  return fetch(endpoint.url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      prompt,
      ...(options.model ? { model: options.model } : {}),
      ...(schema ? { outputSchema: JSON.stringify(schema) } : {}),
    }),
    signal,
  });
}

// resolveLLMEndpoint implements the sandbox runtime facade contract:
//
//  1. An explicit baseUrl option always wins and selects the legacy public
//     LLMService Generate endpoint (backwards compatible, no token).
//  2. The managed sandbox facade environment wins next: the facade base URL
//     (provider-specific OPENAI_BASE_URL / ANTHROPIC_BASE_URL, or derived from
//     AGENT_COMPOSE_RUNTIME_BASE_URL + SANDBOX_ID) plus the facade token
//     (AGENT_COMPOSE_SANDBOX_TOKEN, then the family-specific
//     OPENAI_API_KEY / ANTHROPIC_AUTH_TOKEN / ANTHROPIC_API_KEY). The wire
//     protocol comes from LLM_API_PROTOCOL and defaults to OpenAI Responses.
//  3. When no facade environment exists, fall back to the legacy base URL
//     chain and the public LLMService endpoint without a token.
//
// The token is used only to build the x-api-key header and is never included
// in thrown errors, logs, or results.
function resolveLLMEndpoint(options: RuntimeLLMOptions): ResolvedLLMEndpoint {
  if (options.baseUrl && options.baseUrl.trim() !== "") {
    return {
      url: joinURL(options.baseUrl, LEGACY_GENERATE_PROCEDURE),
      protocol: "connect-generate",
    };
  }

  const sandboxToken = optionalEnvValue("AGENT_COMPOSE_SANDBOX_TOKEN");
  const openAIBase = optionalEnvValue("OPENAI_BASE_URL");
  const anthropicBase = optionalEnvValue("ANTHROPIC_BASE_URL");
  const runtimeBase = optionalEnvValue("AGENT_COMPOSE_RUNTIME_BASE_URL");
  const protocol = normalizeWireProtocol(optionalEnvValue("LLM_API_PROTOCOL"));
  const facadeBase = pickFacadeBase(protocol, openAIBase, anthropicBase);
  const facadeToken = pickFacadeToken(protocol, sandboxToken, optionalEnvValue("OPENAI_API_KEY"), optionalEnvValue("ANTHROPIC_AUTH_TOKEN"), optionalEnvValue("ANTHROPIC_API_KEY"));
  if (facadeToken !== undefined && (facadeBase !== undefined || runtimeBase !== undefined)) {
    const base = facadeBase ?? defaultFacadeBaseURL(runtimeBase as string, protocol);
    return {
      url: joinURL(base, protocol === "anthropic-messages" ? "/v1/messages" : "/responses"),
      protocol,
      token: facadeToken,
    };
  }

  const legacyBase =
    optionalEnvValue("BASE_URL") ??
    optionalEnvValue("HTTP_URL") ??
    optionalEnvValue("AGENT_COMPOSE_BASE_URL") ??
    optionalEnvValue("AGENT_COMPOSE_HTTP_URL") ??
    DEFAULT_BASE_URL;
  return {
    url: joinURL(legacyBase, LEGACY_GENERATE_PROCEDURE),
    protocol: "connect-generate",
  };
}

function normalizeWireProtocol(value: string | undefined): FacadeWireProtocol {
  const normalized = (value ?? "").trim().toLowerCase();
  if (normalized === "anthropic_messages" || normalized === "messages") {
    return "anthropic-messages";
  }
  // "responses" (the managed default) and "chat_completions" (the facade
  // serves both OpenAI families on the same base URL) both resolve to the
  // OpenAI Responses endpoint; unknown values fall back to it as well.
  return "openai-responses";
}

function pickFacadeBase(protocol: FacadeWireProtocol, openAIBase: string | undefined, anthropicBase: string | undefined): string | undefined {
  if (protocol === "anthropic-messages") {
    return anthropicBase ?? openAIBase;
  }
  return openAIBase ?? anthropicBase;
}

function pickFacadeToken(protocol: FacadeWireProtocol, sandboxToken: string | undefined, openAIKey: string | undefined, anthropicAuthToken: string | undefined, anthropicAPIKey: string | undefined): string | undefined {
  if (sandboxToken) {
    return sandboxToken;
  }
  if (protocol === "anthropic-messages") {
    return anthropicAuthToken ?? anthropicAPIKey ?? openAIKey;
  }
  return openAIKey ?? anthropicAuthToken ?? anthropicAPIKey;
}

function defaultFacadeBaseURL(runtimeBase: string, protocol: FacadeWireProtocol): string {
  const sandboxID = optionalEnvValue("SANDBOX_ID") ?? "";
  const family = protocol === "anthropic-messages" ? "anthropic" : "openai/v1";
  return `${runtimeBase}/api/runtime/sandboxes/${sandboxID}/llm/${family}`;
}

function buildResponsesRequest(prompt: string, options: RuntimeLLMOptions, schema: RuntimeJsonSchema | null): Record<string, unknown> {
  const body: Record<string, unknown> = {
    model: (options.model ?? "").trim(),
    input: prompt,
  };
  if (schema) {
    body.text = {
      format: {
        type: "json_schema",
        name: "runtime_llm_output",
        schema,
        strict: true,
      },
    };
  }
  return body;
}

function buildAnthropicMessagesRequest(prompt: string, options: RuntimeLLMOptions, schema: RuntimeJsonSchema | null): Record<string, unknown> {
  let content = prompt;
  if (schema) {
    content = `${prompt}\n\nRespond with a JSON object that matches this JSON Schema. Output only the JSON object, without markdown fences or commentary.\n${JSON.stringify(schema)}`;
  }
  return {
    model: (options.model ?? "").trim(),
    max_tokens: ANTHROPIC_DEFAULT_MAX_TOKENS,
    messages: [{ role: "user", content }],
  };
}

function decodeLLMResponse(protocol: LLMProtocol, payload: Record<string, unknown>, requestedModel: string | undefined): DecodedLLMResponse {
  if (protocol === "openai-responses") {
    return decodeOpenAIResponsesPayload(payload, requestedModel);
  }
  if (protocol === "anthropic-messages") {
    return decodeAnthropicMessagesPayload(payload, requestedModel);
  }
  return decodeConnectGeneratePayload(payload, requestedModel);
}

function decodeConnectGeneratePayload(payload: Record<string, unknown>, requestedModel: string | undefined): DecodedLLMResponse {
  return {
    text: stringField(payload, "text"),
    model: stringField(payload, "model") || (requestedModel ?? ""),
    responseId: stringField(payload, "responseId") || stringField(payload, "response_id"),
    finishReason: stringField(payload, "finishReason") || stringField(payload, "finish_reason"),
  };
}

function decodeOpenAIResponsesPayload(payload: Record<string, unknown>, requestedModel: string | undefined): DecodedLLMResponse {
  let text = stringField(payload, "output_text");
  let finishReason = "";
  if (!text) {
    const parts: string[] = [];
    for (const item of arrayField(payload, "output")) {
      if (!finishReason) {
        finishReason = stringField(item, "finish_reason") || stringField(item, "stop_reason");
      }
      for (const content of arrayField(item, "content")) {
        const part = stringField(content, "text");
        if (part) {
          parts.push(part);
        }
      }
    }
    text = parts.join("\n");
  }
  if (!finishReason) {
    const incomplete = payload.incomplete_details as Record<string, unknown> | undefined;
    finishReason = (incomplete ? stringField(incomplete, "reason") : "") || stringField(payload, "status");
  }
  return {
    text,
    model: stringField(payload, "model") || (requestedModel ?? ""),
    responseId: stringField(payload, "id"),
    finishReason,
  };
}

function decodeAnthropicMessagesPayload(payload: Record<string, unknown>, requestedModel: string | undefined): DecodedLLMResponse {
  const parts: string[] = [];
  for (const block of arrayField(payload, "content")) {
    if (stringField(block, "type") === "text") {
      const part = stringField(block, "text");
      if (part) {
        parts.push(part);
      }
    }
  }
  return {
    text: parts.join("\n"),
    model: stringField(payload, "model") || (requestedModel ?? ""),
    responseId: stringField(payload, "id"),
    finishReason: stringField(payload, "stop_reason"),
  };
}

function stringField(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  return typeof value === "string" ? value : "";
}

function arrayField(source: Record<string, unknown>, key: string): Record<string, unknown>[] {
  const value = source[key];
  return Array.isArray(value) ? (value as Record<string, unknown>[]) : [];
}

function joinURL(base: string, path: string): string {
  const trimmed = base.trim().replace(/\/+$/, "");
  if (!trimmed) {
    return path;
  }
  return trimmed + path;
}

function redactSecrets(message: string): string {
  let result = message;
  for (const name of ["AGENT_COMPOSE_SANDBOX_TOKEN", "OPENAI_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "LLM_API_KEY"]) {
    const value = process.env[name];
    if (value && value.trim() !== "") {
      result = result.split(value).join("[redacted]");
    }
  }
  return result;
}

import { callRuntimeBridge, type RuntimeBridgeFetch } from "./bridge.js";
import { RuntimeUnsupportedError } from "./errors.js";

export interface RuntimeCapabilityCallOptions {
  endpoint?: string;
  fetch?: RuntimeBridgeFetch;
  headers?: Record<string, string>;
  signal?: AbortSignal;
}

export interface RuntimeCapabilityRequest<TInput = unknown> {
  method: string;
  input: TInput;
}

export const capability = {
  async call<TInput = unknown, TOutput = unknown>(
    method: string,
    input: TInput,
    options: RuntimeCapabilityCallOptions = {},
  ): Promise<TOutput> {
    const normalizedMethod = method.trim();
    if (!normalizedMethod) {
      throw new Error("capability method is required");
    }

    const endpoint = options.endpoint ?? process.env.AGENT_COMPOSE_CAPABILITY_ENDPOINT;
    if (!endpoint) {
      throw new RuntimeUnsupportedError("runtime capability bridge is not configured");
    }

    return await callRuntimeBridge<TOutput>({
      endpoint,
      fetch: options.fetch,
      headers: options.headers,
      signal: options.signal,
      bridgeName: "capability",
      request: { method: normalizedMethod, input } satisfies RuntimeCapabilityRequest<TInput>,
    });
  },
};

import { RuntimeUnsupportedError } from "./errors.js";
import { callRuntimeBridge, type RuntimeBridgeFetch } from "./bridge.js";

export interface RuntimeServiceInvokeOptions {
  endpoint?: string;
  fetch?: RuntimeBridgeFetch;
  headers?: Record<string, string>;
  signal?: AbortSignal;
}

export interface RuntimeServiceRequest<TInput = unknown> {
  service: string;
  method: string;
  input: TInput;
}

export async function invokeService<TInput = unknown, TOutput = unknown>(
  serviceName: string,
  method: string,
  input: TInput,
  options: RuntimeServiceInvokeOptions = {},
): Promise<TOutput> {
  const normalizedServiceName = serviceName.trim();
  const normalizedMethod = method.trim();
  if (!normalizedServiceName) {
    throw new Error("service name is required");
  }
  if (!normalizedMethod) {
    throw new Error("service method is required");
  }

  const endpoint = options.endpoint ?? process.env.AGENT_COMPOSE_SERVICE_BRIDGE_ENDPOINT;
  if (!endpoint) {
    throw new RuntimeUnsupportedError("runtime service daemon bridge is not configured");
  }

  return await callRuntimeBridge<TOutput>({
    endpoint,
    fetch: options.fetch,
    headers: options.headers,
    signal: options.signal,
    bridgeName: "service",
    request: {
      service: normalizedServiceName,
      method: normalizedMethod,
      input,
    } satisfies RuntimeServiceRequest<TInput>,
  });
}

export const service = {
  invoke: invokeService,
};

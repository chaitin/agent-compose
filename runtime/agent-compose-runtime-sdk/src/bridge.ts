import { RuntimeBridgeError } from "./errors.js";

export type RuntimeBridgeFetch = typeof fetch;

export interface RuntimeBridgeCallOptions {
  endpoint: string;
  fetch?: RuntimeBridgeFetch;
  headers?: Record<string, string>;
  signal?: AbortSignal;
  request: unknown;
  bridgeName: string;
}

export async function callRuntimeBridge<TOutput = unknown>({
  endpoint,
  fetch: fetchImpl = fetch,
  headers,
  signal,
  request,
  bridgeName,
}: RuntimeBridgeCallOptions): Promise<TOutput> {
  const response = await fetchImpl(endpoint, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      ...headers,
    },
    body: JSON.stringify(request),
    signal,
  });
  const responseText = await response.text();

  if (!response.ok) {
    const suffix = responseText.trim() ? `: ${responseText}` : "";
    throw new RuntimeBridgeError(`runtime ${bridgeName} bridge returned HTTP ${response.status}${suffix}`);
  }

  if (!responseText.trim()) {
    return undefined as TOutput;
  }
  try {
    return JSON.parse(responseText) as TOutput;
  } catch (error) {
    throw new RuntimeBridgeError(`runtime ${bridgeName} bridge returned invalid JSON`, { cause: error });
  }
}

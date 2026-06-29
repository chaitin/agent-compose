import process from "node:process";

export interface RuntimeCapabilityScope {
  capsetIds: string[];
  metadata: Record<string, string>;
}

export interface RuntimeContext {
  source?: string;
  clientRequestId?: string;
  traceId?: string;
  externalRunId?: string;
  metadata: Record<string, string>;
  env: Record<string, string>;
  identityContext: Record<string, string>;
  capabilityScope: RuntimeCapabilityScope;
}

export function readContext(): RuntimeContext {
  const parsed = parseRuntimeContext(process.env.AGENT_COMPOSE_RUNTIME_CONTEXT_JSON);
  return {
    source: stringValue(parsed.source) ?? envString("AGENT_COMPOSE_SOURCE"),
    clientRequestId: stringValue(parsed.clientRequestId ?? parsed.client_request_id) ?? envString("AGENT_COMPOSE_CLIENT_REQUEST_ID"),
    traceId: stringValue(parsed.traceId ?? parsed.trace_id) ?? envString("AGENT_COMPOSE_TRACE_ID"),
    externalRunId: stringValue(parsed.externalRunId ?? parsed.external_run_id) ?? envString("AGENT_COMPOSE_EXTERNAL_RUN_ID"),
    metadata: recordValue(parsed.metadata),
    env: recordValue(parsed.env),
    identityContext: recordValue(parsed.identityContext ?? parsed.identity_context),
    capabilityScope: capabilityScopeValue(parsed.capabilityScope ?? parsed.capability_scope),
  };
}

function parseRuntimeContext(raw: string | undefined): Record<string, unknown> {
  if (!raw || raw.trim() === "") {
    return {};
  }
  const parsed = JSON.parse(raw) as unknown;
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("AGENT_COMPOSE_RUNTIME_CONTEXT_JSON must contain an object");
  }
  return parsed as Record<string, unknown>;
}

function capabilityScopeValue(value: unknown): RuntimeCapabilityScope {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return { capsetIds: [], metadata: {} };
  }
  const scope = value as Record<string, unknown>;
  return {
    capsetIds: stringListValue(scope.capsetIds ?? scope.capset_ids),
    metadata: recordValue(scope.metadata),
  };
}

function recordValue(value: unknown): Record<string, string> {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return {};
  }
  const out: Record<string, string> = {};
  for (const [key, item] of Object.entries(value as Record<string, unknown>)) {
    if (typeof item === "string") {
      out[key] = item;
    }
  }
  return out;
}

function stringListValue(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.filter((item): item is string => typeof item === "string" && item.trim() !== "");
}

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() !== "" ? value : undefined;
}

function envString(name: string): string | undefined {
  const value = process.env[name];
  return value && value.trim() !== "" ? value : undefined;
}

export const context = {
  read: readContext,
};

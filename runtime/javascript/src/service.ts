import fsp from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

export interface RuntimeServiceRequest {
  protocolVersion?: string;
  serviceName?: string;
  entry: string;
  inputJson?: string;
  contextJson?: string;
  outputSchema?: string;
  inputSchema?: string;
  artifactDirName?: string;
  artifactDir?: string;
  stateRoot?: string;
  runtimeRoot?: string;
  workspace?: string;
}

export interface RuntimeServiceResult {
  protocolVersion: string;
  serviceName: string;
  outputJson: string;
  success: boolean;
  artifacts: Record<string, string>;
  metrics: Record<string, string>;
}

export async function runServiceCommand(options: {
  requestFile?: string;
  requestJson?: string;
  stateRoot?: string;
  workspace?: string;
}): Promise<RuntimeServiceResult> {
  const request = await readServiceRequest(options);
  const normalized = normalizeServiceRequest(request, options);
  await fsp.mkdir(normalized.artifactDir, { recursive: true });
  await fsp.writeFile(path.join(normalized.artifactDir, "service-request.json"), JSON.stringify(normalized, null, 2) + "\n", "utf8");

  const input = parseJSON(normalized.inputJson || "{}", "inputJson");
  if (normalized.inputSchema) {
    validateJsonSchema(input, parseJSON(normalized.inputSchema, "inputSchema"), "input");
  }
  const context = parseJSON(normalized.contextJson || "{}", "contextJson");
  const moduleURL = pathToFileURL(path.resolve(normalized.workspace, normalized.entry)).href;
  const imported = await import(moduleURL);
  const handler = imported.default ?? imported.handler ?? imported.run;
  if (typeof handler !== "function") {
    throw new Error("service entry must export default, handler, or run function");
  }
  const started = Date.now();
  const output = await handler(input, {
    context,
    serviceName: normalized.serviceName,
    workspace: normalized.workspace,
    artifactDir: normalized.artifactDir,
    stateRoot: normalized.stateRoot,
    runtimeRoot: normalized.runtimeRoot,
  });
  if (normalized.outputSchema) {
    validateJsonSchema(output ?? null, parseJSON(normalized.outputSchema, "outputSchema"), "output");
  }
  const outputJson = JSON.stringify(output ?? null);
  const result: RuntimeServiceResult = {
    protocolVersion: normalized.protocolVersion,
    serviceName: normalized.serviceName,
    outputJson,
    success: true,
    artifacts: {
      request: path.join(normalized.artifactDir, "service-request.json"),
      result: path.join(normalized.artifactDir, "service-result.json"),
    },
    metrics: {
      durationMs: String(Date.now() - started),
    },
  };
  await fsp.writeFile(result.artifacts.result, JSON.stringify(result, null, 2) + "\n", "utf8");
  return result;
}

async function readServiceRequest(options: { requestFile?: string; requestJson?: string }): Promise<RuntimeServiceRequest> {
  if (options.requestJson) {
    return parseJSON(options.requestJson, "requestJson") as RuntimeServiceRequest;
  }
  if (!options.requestFile) {
    throw new Error("service request file or request JSON is required");
  }
  return parseJSON(await fsp.readFile(options.requestFile, "utf8"), "requestFile") as RuntimeServiceRequest;
}

function normalizeServiceRequest(
  request: RuntimeServiceRequest,
  defaults: { stateRoot?: string; workspace?: string },
): Required<RuntimeServiceRequest> {
  if (!request || typeof request !== "object" || Array.isArray(request)) {
    throw new Error("service request must be an object");
  }
  if (!request.entry || typeof request.entry !== "string") {
    throw new Error("service entry is required");
  }
  const protocolVersion = request.protocolVersion || "service.v1";
  if (protocolVersion !== "service.v1") {
    throw new Error(`unsupported service protocol version: ${protocolVersion}`);
  }
  const stateRoot = request.stateRoot || defaults.stateRoot || process.env.STATE_ROOT || process.env.AGENT_COMPOSE_STATE_ROOT || "/data/state";
  const runtimeRoot = request.runtimeRoot || process.env.RUNTIME_ROOT || process.env.AGENT_COMPOSE_RUNTIME_ROOT || path.join(path.dirname(stateRoot), "runtime");
  const workspace = request.workspace || defaults.workspace || process.env.WORKSPACE || process.env.AGENT_COMPOSE_WORKSPACE || "/workspace";
  const serviceName = request.serviceName || path.basename(request.entry);
  const artifactDir = request.artifactDir || path.join(stateRoot, "artifacts", request.artifactDirName || serviceName);
  return {
    protocolVersion: request.protocolVersion || "service.v1",
    serviceName,
    entry: request.entry,
    inputJson: request.inputJson || "{}",
    contextJson: request.contextJson || "{}",
    outputSchema: request.outputSchema || "",
    inputSchema: request.inputSchema || "",
    artifactDirName: request.artifactDirName || serviceName,
    artifactDir,
    stateRoot,
    runtimeRoot,
    workspace,
  };
}

function validateJsonSchema(value: unknown, schema: unknown, path: string): void {
  if (!schema || typeof schema !== "object" || Array.isArray(schema)) {
    throw new Error(`${path} schema must be a JSON object`);
  }
  const rule = schema as Record<string, unknown>;
  const type = rule.type;
  if (typeof type === "string" && !matchesJsonType(value, type)) {
    throw new Error(`${path} must be ${type}`);
  }
  if (Array.isArray(rule.enum) && !rule.enum.some((item) => JSON.stringify(item) === JSON.stringify(value))) {
    throw new Error(`${path} must match one of the allowed enum values`);
  }
  if (type === "object" || (type === undefined && isPlainObject(value))) {
    if (!isPlainObject(value)) {
      throw new Error(`${path} must be object`);
    }
    const objectValue = value as Record<string, unknown>;
    const required = Array.isArray(rule.required) ? rule.required.filter((item): item is string => typeof item === "string") : [];
    for (const key of required) {
      if (!(key in objectValue)) {
        throw new Error(`${path}.${key} is required`);
      }
    }
    const properties = isPlainObject(rule.properties) ? rule.properties as Record<string, unknown> : {};
    for (const [key, childSchema] of Object.entries(properties)) {
      if (key in objectValue) {
        validateJsonSchema(objectValue[key], childSchema, `${path}.${key}`);
      }
    }
    if (rule.additionalProperties === false) {
      for (const key of Object.keys(objectValue)) {
        if (!(key in properties)) {
          throw new Error(`${path}.${key} is not allowed`);
        }
      }
    }
  }
  if (type === "array" || (type === undefined && Array.isArray(value))) {
    if (!Array.isArray(value)) {
      throw new Error(`${path} must be array`);
    }
    if (rule.items !== undefined) {
      value.forEach((item, index) => validateJsonSchema(item, rule.items, `${path}[${index}]`));
    }
  }
}

function matchesJsonType(value: unknown, type: string): boolean {
  switch (type) {
    case "object":
      return isPlainObject(value);
    case "array":
      return Array.isArray(value);
    case "string":
      return typeof value === "string";
    case "number":
      return typeof value === "number";
    case "integer":
      return typeof value === "number" && Number.isInteger(value);
    case "boolean":
      return typeof value === "boolean";
    case "null":
      return value === null;
    default:
      return true;
  }
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function parseJSON(raw: string, field: string): unknown {
  try {
    return JSON.parse(raw);
  } catch (error) {
    throw new Error(`${field} must be valid JSON: ${error instanceof Error ? error.message : String(error)}`);
  }
}

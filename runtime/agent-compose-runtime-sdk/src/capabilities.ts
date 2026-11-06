import fs from "node:fs/promises";
import path from "node:path";
import os from "node:os";
import { z } from "zod";
import { paths } from "./env.js";
import { capabilityGRPC, type CapabilityCallOptions } from "./capability-grpc.js";

const metadataValue = z.string().min(1).regex(/^[A-Za-z0-9_.:/-]+$/);
const typeName = z.string().regex(/^[A-Za-z_][A-Za-z0-9_.]*$/);
const toolSchema = z.object({
  id: z.string().regex(/^[a-f0-9]{64}$/),
  capset: metadataValue,
  instance: metadataValue,
  method: z.string().regex(/^[A-Za-z_][A-Za-z0-9_.]*\/[A-Za-z_][A-Za-z0-9_]*$/),
  request_type: typeName,
  response_type: typeName,
});
const catalogSchema = z.object({
  version: z.literal(1), sandbox_id: z.string().min(1),
  tools: z.array(toolSchema), unavailable_capsets: z.array(z.string()),
});

export type RuntimeCapability = z.infer<typeof toolSchema>;
export type { CapabilityCallOptions } from "./capability-grpc.js";

async function list(): Promise<RuntimeCapability[]> {
  const file = await fs.open(path.join(paths.runtimeRoot, "mpi", "capabilities.json"), "r");
  let raw: Buffer;
  try {
    raw = Buffer.alloc(1024 * 1024 + 1);
    const { bytesRead } = await file.read(raw, 0, raw.length, 0);
    if (bytesRead > 1024 * 1024) throw new Error("capability catalog exceeds limit");
    raw = raw.subarray(0, bytesRead);
  } finally {
    await file.close();
  }
  let document: unknown;
  try { document = JSON.parse(raw.toString("utf8")); }
  catch { throw new Error("invalid capability catalog JSON"); }
  const parsed = catalogSchema.safeParse(document);
  if (!parsed.success) throw new Error("invalid capability catalog");
  const catalog = parsed.data;
  if (!process.env.SANDBOX_ID || catalog.sandbox_id !== process.env.SANDBOX_ID) throw new Error("capability catalog sandbox mismatch");
  if (catalog.unavailable_capsets.length) throw new Error("capability catalog is incomplete; recreate the sandbox when the gateway is available");
  if (new Set(catalog.tools.map((tool) => tool.id)).size !== catalog.tools.length) throw new Error("duplicate capability identity");
  return catalog.tools;
}

async function resolveTool(id: string): Promise<RuntimeCapability> {
  const tool = (await list()).find((candidate) => candidate.id === id);
  if (!tool) throw new Error("capability is not in the sandbox catalog");
  return tool;
}

function headers(tool: RuntimeCapability): string[] {
  return ["-H", `x-octobus-capset: ${tool.capset}`, "-H", `x-octobus-instance: ${tool.instance}`];
}

async function describeMethod(tool: RuntimeCapability, options: CapabilityCallOptions, descriptorFile?: string): Promise<string> {
  const result = await capabilityGRPC([...headers(tool), ...(descriptorFile ? ["-protoset-out", descriptorFile] : []), "describe", tool.method.replace("/", ".")], undefined, options);
  if (/\(\s*stream\b|returns\s*\(\s*stream\b/.test(result)) throw new Error("streaming capabilities are not supported");
  return result;
}

async function describe(id: string, options: CapabilityCallOptions = {}) {
  const tool = await resolveTool(id);
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ac-capability-"));
  try {
    const filePath = path.join(root, "definition.pb");
    const method = await describeMethod(tool, options, filePath);
    const request = await capabilityGRPC([...headers(tool), "describe", tool.request_type], undefined, options);
    const response = await capabilityGRPC([...headers(tool), "describe", tool.response_type], undefined, options);
    const file = await fs.open(filePath, "r");
    try {
      const limit = options.maxOutputBytes ?? 256 * 1024;
      const raw = Buffer.alloc(limit + 1);
      const { bytesRead } = await file.read(raw, 0, raw.length, 0);
      if (!bytesRead || bytesRead > limit) throw new Error("capability descriptor is empty or exceeds limit");
      return { tool, method, request, response, descriptorSetBase64: raw.subarray(0, bytesRead).toString("base64") };
    } finally { await file.close(); }
  } finally { await fs.rm(root, { recursive: true, force: true }); }
}

async function call<T = unknown>(id: string, params: unknown, options: CapabilityCallOptions = {}): Promise<T> {
  // Well-known Protobuf messages also use scalar, list and null JSON forms.
  const input = JSON.stringify(params, (_key, value: unknown) => {
    if (["undefined", "function", "symbol", "bigint"].includes(typeof value) || (typeof value === "number" && !Number.isFinite(value))) {
      throw new Error("capability params must be JSON-compatible");
    }
    return value;
  });
  if (input === undefined) throw new Error("capability params must be JSON-compatible");
  if (Buffer.byteLength(input, "utf8") > 256 * 1024) throw new Error("capability input exceeds limit");
  const tool = await resolveTool(id);
  await describeMethod(tool, options);
  const result = await capabilityGRPC([...headers(tool), "-d", "@", tool.method], input, options);
  try { return JSON.parse(result) as T; }
  catch { throw new Error("invalid capability response JSON"); }
}

/** Discovery is a snapshot; capproxy and OB enforce authorization on every call. */
export const capabilities = { list, describe, call };

import crypto from "node:crypto";
import fsp from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { type output as ZodOutput, type ZodType } from "zod";
import { paths } from "./env.js";
import { DEFAULT_MAX_OUTPUT_BYTES, runProcess } from "./exec.js";
import {
  isPlainJsonObject,
  normalizeOutputSchema,
  parseJsonOutput,
  type RuntimeJsonSchema,
  type RuntimeOutputSchema,
} from "./schema.js";

const AGENT_RESULT_PREFIX = "__AGENT_RESULT__";

export type RuntimeAgentOutputSchema = RuntimeOutputSchema;

export interface RuntimeAgentOptions<S extends RuntimeAgentOutputSchema = RuntimeAgentOutputSchema> {
  provider?: "codex" | "claude" | "gemini" | "opencode" | "pi";
  stateRoot?: string;
  workspace?: string;
  home?: string;
  timeoutMs?: number;
  outputSchema?: S;
}

export interface RuntimeAgentResult<T = unknown> {
  provider: string;
  threadId: string;
  stopReason: string;
  finalText: string;
  json: T | null;
  transcript: string;
  stderr: string;
}

export async function agent<S extends ZodType>(prompt: string, options: RuntimeAgentOptions<S> & { outputSchema: S }): Promise<RuntimeAgentResult<ZodOutput<S>>>;
export async function agent<T = unknown>(prompt: string, options?: RuntimeAgentOptions<RuntimeJsonSchema>): Promise<RuntimeAgentResult<T>>;
export async function agent<T = unknown>(prompt: string, options: RuntimeAgentOptions = {}): Promise<RuntimeAgentResult<T>> {
  const { messageFile, schemaFile, tempDir, validator } = await writeAgentRequestFiles(prompt, options.outputSchema);
  const provider = options.provider ?? "codex";
  const args = [
    "prompt",
    "--provider",
    provider,
    "--message-file",
    messageFile,
    "--state-root",
    options.stateRoot ?? paths.stateRoot,
    "--workspace",
    options.workspace ?? paths.workspace,
    "--home",
    options.home ?? paths.home,
  ];
  if (schemaFile) {
    args.push("--output-schema-file");
    args.push(schemaFile);
  }

  try {
    const childResult = await runProcess("agent-compose-runtime", args, {
      cwd: options.workspace ?? paths.workspace,
      timeoutMs: options.timeoutMs,
      maxOutputBytes: DEFAULT_MAX_OUTPUT_BYTES,
      forwardStderr: true,
    });
    const stdout = childResult.stdout.text;
    const resultLine = stdout.split(/\r?\n/).find((line) => line.startsWith(AGENT_RESULT_PREFIX));
    if (!resultLine) {
      throw new Error("agent-compose-runtime did not emit an agent result payload");
    }
    const parsed = JSON.parse(resultLine.slice(AGENT_RESULT_PREFIX.length)) as RuntimeAgentResult;
    const finalText = parsed.finalText ?? "";
    return {
      provider: parsed.provider ?? provider,
      threadId: parsed.threadId ?? "",
      stopReason: parsed.stopReason ?? "",
      finalText,
      json: schemaFile ? parseJsonOutput<T>(finalText, validator, "agent finalText") : null,
      transcript: parsed.transcript ?? "",
      stderr: parsed.stderr ?? childResult.stderr.text,
    };
  } finally {
    await fsp.rm(tempDir, { recursive: true, force: true });
  }
}

async function writeAgentRequestFiles(
  prompt: string,
  outputSchema?: RuntimeAgentOutputSchema,
): Promise<{
  messageFile: string;
  schemaFile?: string;
  tempDir: string;
  validator?: (value: unknown) => unknown;
}> {
  const dir = await fsp.mkdtemp(path.join(os.tmpdir(), "agent-compose-runtime-sdk-agent-"));
  const messageFile = path.join(dir, `message-${crypto.randomUUID()}.txt`);
  await fsp.writeFile(messageFile, prompt, "utf8");
  if (outputSchema === undefined) {
    return { messageFile, tempDir: dir };
  }
  const normalized = normalizeOutputSchema(outputSchema, "agent");
  if (!isPlainJsonObject(normalized.schema)) {
    await fsp.rm(dir, { recursive: true, force: true });
    throw new Error("agent outputSchema must be a plain JSON object");
  }
  const schemaFile = path.join(dir, `schema-${crypto.randomUUID()}.json`);
  await fsp.writeFile(schemaFile, JSON.stringify(normalized.schema), "utf8");
  return { messageFile, schemaFile, tempDir: dir, validator: normalized.validator };
}

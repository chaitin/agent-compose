import path from "node:path";
import process from "node:process";
import { SANDBOX_ROOT } from "./constants.js";
import { readText } from "./fs.js";
import { readMpiContext } from "./mpi.js";
import { normalizeProvider } from "./provider.js";
import { ClaudeRunner } from "./runners/claude.js";
import { CodexRunner } from "./runners/codex.js";
import { GeminiRunner } from "./runners/gemini.js";
import { OpenCodeRunner } from "./runners/opencode.js";
import { agentSystemPromptPath, buildSystemContext, readSystemPromptFile } from "./system-context.js";
import type { AgentResult, RuntimeJsonSchema } from "./types.js";

export interface PromptCommandOptions {
  provider?: string;
  messageFile?: string;
  stateRoot?: string;
  workspace?: string;
  home?: string;
  model?: string;
  outputSchemaFile?: string;
}

export async function runPromptCommand(commandOptions: PromptCommandOptions): Promise<AgentResult> {
  const provider = normalizeProvider(commandOptions.provider);
  const messageFile = commandOptions.messageFile;
  const stateRoot = path.resolve(commandOptions.stateRoot || path.join(SANDBOX_ROOT, "state"));
  const workspace = path.resolve(
    commandOptions.workspace || process.env.WORKSPACE || process.env.AGENT_COMPOSE_WORKSPACE || path.join(SANDBOX_ROOT, "workspace"),
  );
  const home = path.resolve(commandOptions.home || process.env.HOME || path.join(SANDBOX_ROOT, "home"));

  if (!messageFile) {
    throw new Error("--message-file is required");
  }

  const promptText = await readText(path.resolve(messageFile));
  const outputSchema = commandOptions.outputSchemaFile
    ? parseOutputSchema(await readText(path.resolve(commandOptions.outputSchemaFile)))
    : undefined;
  const systemPrompt = await readSystemPromptFile(agentSystemPromptPath(stateRoot));
  const mpi = await readMpiContext(stateRoot);
  const options = {
    provider,
    model: commandOptions.model,
    stateRoot,
    workspace,
    home,
    runtimeRoot: mpi.runtimeRoot,
    systemContext: buildSystemContext(systemPrompt, mpi.context),
    outputSchema,
  };
  if (provider === "codex") {
    return await new CodexRunner(options).runPrompt(promptText);
  }
  if (provider === "claude") {
    return await new ClaudeRunner(options).runPrompt(promptText);
  }
  if (provider === "opencode") {
    return await new OpenCodeRunner(options).runPrompt(promptText);
  }
  return await new GeminiRunner(options).runPrompt(promptText);
}

function parseOutputSchema(raw: string): RuntimeJsonSchema {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (error) {
    throw new Error("--output-schema-file must contain valid JSON", { cause: error });
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    throw new Error("--output-schema-file must contain a JSON object");
  }
  return parsed as RuntimeJsonSchema;
}

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
import { readMCPConfig } from "./mcp-config.js";
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
  skills?: string[];
}

export async function buildPromptRuntimeOptions(commandOptions: Omit<PromptCommandOptions, "messageFile">) {
  const provider = normalizeProvider(commandOptions.provider);
  const stateRoot = path.resolve(commandOptions.stateRoot || path.join(SANDBOX_ROOT, "state"));
  const workspace = path.resolve(
    commandOptions.workspace || process.env.WORKSPACE || process.env.AGENT_COMPOSE_WORKSPACE || path.join(SANDBOX_ROOT, "workspace"),
  );
  const home = path.resolve(commandOptions.home || process.env.HOME || path.join(SANDBOX_ROOT, "home"));
  const outputSchema = commandOptions.outputSchemaFile
    ? parseOutputSchema(await readText(path.resolve(commandOptions.outputSchemaFile)))
    : undefined;
  const systemPrompt = await readSystemPromptFile(agentSystemPromptPath(stateRoot));
  const mpi = await readMpiContext(stateRoot);
  const mcpConfig = await readMCPConfig(stateRoot);
  const skills = normalizeSkills(commandOptions.skills);
  const baseSystemContext = buildSystemContext(systemPrompt, mpi.context);
  return {
    provider,
    model: commandOptions.model,
    stateRoot,
    workspace,
    home,
    runtimeRoot: mpi.runtimeRoot,
    systemContext: provider === "gemini" || provider === "codex"
      ? await appendSkillCatalogContext(baseSystemContext, home, skills)
      : baseSystemContext,
    mcpConfig: mcpConfig.mcp_servers,
    skills,
    outputSchema,
  };
}

export async function runPromptCommand(commandOptions: PromptCommandOptions): Promise<AgentResult> {
  const options = await buildPromptRuntimeOptions(commandOptions);
  const messageFile = commandOptions.messageFile;

  if (!messageFile) {
    throw new Error("--message-file is required");
  }

  const promptText = await readText(path.resolve(messageFile));
  const provider = options.provider;
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

function normalizeSkills(skills: string[] | undefined): string[] {
  const seen = new Set<string>();
  const normalized: string[] = [];
  for (const skill of skills || []) {
    const name = String(skill || "").trim();
    if (!name || seen.has(name)) {
      continue;
    }
    seen.add(name);
    normalized.push(name);
  }
  return normalized;
}

async function appendSkillCatalogContext(systemContext: string, home: string, skills: string[]): Promise<string> {
  if (skills.length === 0) {
    return systemContext;
  }
  const lines = ["## Agent Skills", ""];
  for (const skill of skills) {
    const skillDir = path.join(home, ".agents", "skills", skill);
    const meta = await readSkillMetadata(path.join(skillDir, "SKILL.md"));
    if (!meta) {
      process.stderr.write(`[agent-compose-runtime] warning: skill metadata missing for ${skill}\n`);
      continue;
    }
    lines.push(`- ${meta.name}: ${truncate(meta.description, 200)} (${skillDir})`);
  }
  if (lines.length <= 2) {
    return systemContext;
  }
  const skillsContext = lines.join("\n");
  return systemContext ? `${systemContext}\n\n${skillsContext}` : skillsContext;
}

async function readSkillMetadata(skillPath: string): Promise<{ name: string; description: string } | null> {
  try {
    const raw = await readText(skillPath);
    const frontmatter = raw.trimStart().match(/^---\r?\n([\s\S]*?)\r?\n---/);
    if (!frontmatter) {
      return null;
    }
    let name = "";
    let description = "";
    for (const line of frontmatter[1].split(/\r?\n/)) {
      const index = line.indexOf(":");
      if (index < 0) {
        continue;
      }
      const key = line.slice(0, index).trim();
      const value = line.slice(index + 1).trim().replace(/^['"]|['"]$/g, "");
      if (key === "name") {
        name = value;
      }
      if (key === "description") {
        description = value;
      }
    }
    return name && description ? { name, description } : null;
  } catch {
    return null;
  }
}

function truncate(value: string, max: number): string {
  return value.length <= max ? value : `${value.slice(0, max)}...`;
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

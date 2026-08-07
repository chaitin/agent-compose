#!/usr/bin/env node
import { Command } from "commander";
import { realpathSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";
import { runExecCommand } from "./command.js";
import { COMMAND_RESULT_PREFIX, RESULT_PREFIX, WORKFLOW_RESULT_PREFIX } from "./constants.js";
import { formatError } from "./errors.js";
import { runPromptCommand } from "./prompt.js";
import { runStreamCommand } from "./stream.js";
import { RuntimeShutdownController } from "./shutdown.js";
import { runWorkflowCommand } from "./workflow/command.js";

function collectRepeated(value: string, previous: string[] = []): string[] {
  return [...previous, value];
}

export function createProgram(options: { exitOverride?: boolean } = {}): Command {
  const program = new Command();
  program
    .name("agent-compose-runtime")
    .description("agent-compose JavaScript agent runtime");
  if (options.exitOverride) {
    program.exitOverride();
  }

  program
    .command("prompt")
    .requiredOption("--provider <provider>", "agent provider: codex, claude, gemini, opencode, or pi")
    .requiredOption("--message-file <path>", "prompt file path")
    .option("--state-root <path>", "agent-compose runtime state root")
    .option("--workspace <path>", "agent working directory")
    .option("--home <path>", "agent HOME directory")
    .option("--model <model>", "agent model")
    .option("--output-schema-file <path>", "JSON schema file for structured output")
    .option("--skill <name>", "enabled agent skill name", collectRepeated, [])
    .action(async (options: {
      provider: string;
      messageFile: string;
      stateRoot?: string;
      workspace?: string;
      home?: string;
      model?: string;
      outputSchemaFile?: string;
      skill?: string[];
    }) => {
      const { skill, ...promptOptions } = options;
      const shutdown = new RuntimeShutdownController();
      try {
        const result = await runPromptCommand({ ...promptOptions, skills: skill, abortController: shutdown.abortController });
        process.stdout.write(`${RESULT_PREFIX}${JSON.stringify(result)}\n`);
      } finally {
        shutdown.dispose();
      }
    });

  program
    .command("workflow")
    .requiredOption("--script-file <path>", "workflow JavaScript file path")
    .option("--args-file <path>", "workflow arguments JSON file path")
    .option("--state-root <path>", "agent-compose runtime state root")
    .option("--workspace <path>", "workflow working directory")
    .option("--home <path>", "workflow HOME directory")
    .option("--provider <provider>", "default agent provider", "codex")
    .option("--model <model>", "default agent model")
    .option("--concurrency <n>", "maximum concurrent agents", parseNumberOption)
    .option("--token-budget <n>", "estimated workflow result token budget", parseNumberOption)
    .option("--run-id <id>", "new workflow run ID")
    .option("--resume-run-id <id>", "read-only workflow run to resume from")
    .action(async (commandOptions: {
      scriptFile: string;
      argsFile?: string;
      stateRoot?: string;
      workspace?: string;
      home?: string;
      provider?: string;
      model?: string;
      concurrency?: number;
      tokenBudget?: number;
      runId?: string;
      resumeRunId?: string;
    }) => {
      const shutdown = new RuntimeShutdownController();
      try {
        const result = await runWorkflowCommand({ ...commandOptions, abortController: shutdown.abortController });
        process.stdout.write(`${WORKFLOW_RESULT_PREFIX}${JSON.stringify(result)}\n`);
      } finally {
        shutdown.dispose();
      }
    });

  program
    .command("exec")
    .requiredOption("--request-file <path>", "command request JSON file path")
    .option("--state-root <path>", "agent-compose runtime state root")
    .option("--workspace <path>", "command working directory")
    .option("--home <path>", "command HOME directory")
    .action(async (options: {
      requestFile: string;
      stateRoot?: string;
      workspace?: string;
      home?: string;
    }) => {
      const shutdown = new RuntimeShutdownController();
      try {
        const result = await runExecCommand({ ...options, signal: shutdown.abortController.signal });
        process.stdout.write(`${COMMAND_RESULT_PREFIX}${JSON.stringify(result)}\n`);
      } finally {
        shutdown.dispose();
      }
    });

  program
    .command("stream")
    .description("run the runtime NDJSON stream protocol on stdin/stdout")
    .action(async () => {
      const shutdown = new RuntimeShutdownController();
      try {
        await runStreamCommand({ abortController: shutdown.abortController });
      } finally {
        shutdown.dispose();
      }
    });

  return program;
}

function parseNumberOption(value: string): number {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) {
    throw new Error(`expected a finite number, received: ${value}`);
  }
  return parsed;
}

export async function main(argv = process.argv): Promise<void> {
  const program = createProgram();
  await program.parseAsync(argv);
}

function executableURL(filePath: string): string {
  const resolved = path.resolve(filePath);
  try {
    return pathToFileURL(realpathSync(resolved)).href;
  } catch {
    return pathToFileURL(resolved).href;
  }
}

export function isMainModule(metaURL: string, argvPath = process.argv[1]): boolean {
  return Boolean(argvPath) && metaURL === executableURL(argvPath);
}

if (isMainModule(import.meta.url)) {
  main().catch((error) => {
    process.stderr.write(`${formatError(error)}\n`);
    process.exit(1);
  });
}

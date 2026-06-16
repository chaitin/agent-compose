#!/usr/bin/env node
import { Command } from "commander";
import { realpathSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";
import { runExecCommand } from "./command.js";
import { COMMAND_RESULT_PREFIX, RESULT_PREFIX } from "./constants.js";
import { formatError } from "./errors.js";
import { runPromptCommand } from "./prompt.js";

export function createProgram(options: { exitOverride?: boolean } = {}): Command {
  const program = new Command();
  program
    .name("agent-compose-runtime-js")
    .description("agent-compose JavaScript agent runtime");
  if (options.exitOverride) {
    program.exitOverride();
  }

  program
    .command("prompt")
    .requiredOption("--provider <provider>", "agent provider: codex, claude, gemini, or deepseek")
    .requiredOption("--message-file <path>", "prompt file path")
    .option("--state-root <path>", "agent-compose runtime state root")
    .option("--workspace <path>", "agent working directory")
    .option("--home <path>", "agent HOME directory")
    .option("--output-schema-file <path>", "JSON schema file for structured output")
    .action(async (options: {
      provider: string;
      messageFile: string;
      stateRoot?: string;
      workspace?: string;
      home?: string;
      outputSchemaFile?: string;
    }) => {
      const result = await runPromptCommand(options);
      process.stdout.write(`${RESULT_PREFIX}${JSON.stringify(result)}\n`);
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
      const result = await runExecCommand(options);
      process.stdout.write(`${COMMAND_RESULT_PREFIX}${JSON.stringify(result)}\n`);
    });

  return program;
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

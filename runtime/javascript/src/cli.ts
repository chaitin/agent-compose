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
import { runStreamCommand } from "./stream.js";

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
    .requiredOption("--provider <provider>", "agent provider: codex, claude, gemini, or opencode")
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
      const result = await runPromptCommand({ ...promptOptions, skills: skill });
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

  program
    .command("stream")
    .description("run the runtime NDJSON stream protocol on stdin/stdout")
    .action(async () => {
      await runStreamCommand();
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

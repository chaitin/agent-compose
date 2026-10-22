#!/usr/bin/env node
import { Command } from "commander";
import { realpathSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";
import { runExecCommand } from "./command.js";
import { COMMAND_RESULT_PREFIX, RESULT_PREFIX, WORKFLOW_RESULT_PREFIX } from "./constants.js";
import { formatError } from "./errors.js";
import { attachExecution, capabilities, cleanupDetached, resolveCliPath, startDetached, stopExecution } from "./detach/command.js";
import { runSupervisor } from "./detach/supervisor.js";
import { GUEST_ABI_MANIFEST_PATH, readGuestAbiReport, type GuestAbiReport } from "./guest-abi.js";
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
    .requiredOption("--provider <provider>", "agent provider: codex, claude, opencode, pi, or dsh")
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
    .command("abi")
    .description("describe the guest ABI capabilities baked into this image")
    .option("--json", "print the report as a single line of JSON")
    .option("--manifest <path>", "guest ABI manifest path", GUEST_ABI_MANIFEST_PATH)
    .action(async (options: { json?: boolean; manifest: string }) => {
      const report = await readGuestAbiReport({ manifestPath: options.manifest });
      if (options.json) {
        process.stdout.write(`${JSON.stringify(report)}\n`);
        return;
      }
      process.stdout.write(formatGuestAbiSummary(report));
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

  program
    .command("capabilities")
    .description("describe the detached-execution capabilities of this runtime")
    .option("--json", "print the capabilities as a single line of JSON")
    .action(async (options: { json?: boolean }) => {
      const report = capabilities();
      if (options.json) {
        process.stdout.write(`${JSON.stringify(report)}\n`);
        return;
      }
      process.stdout.write(`detach: ${report.detach}\n`);
    });

  program
    .command("start")
    .requiredOption("--detach", "run the task detached from this exec")
    .requiredOption("--execution-id <id>", "detached execution id")
    .option("--state-root <path>", "agent-compose runtime state root")
    .option("--task-timeout-ms <ms>", "stop the task with reason timeout after this long", parseNumberOption)
    .option("--stdin", "give the task a stdin that attach --stdin can feed")
    .option("--raw-command", "run the task args as a command line instead of a runtime subcommand")
    .allowExcessArguments(true)
    .action(async function start(this: Command, options: { executionId: string; stateRoot?: string; taskTimeoutMs?: number; stdin?: boolean; rawCommand?: boolean }) {
      await startDetached({
        stateRoot: options.stateRoot ?? defaultStateRoot(),
        executionId: options.executionId,
        taskArgs: this.args ?? [],
        cliPath: resolveCliPath(),
        taskTimeoutMs: options.taskTimeoutMs,
        ...(options.stdin ? { stdin: true } : {}),
        ...(options.rawCommand ? { rawCommand: true } : {}),
      });
    });

  program
    .command("attach")
    .requiredOption("--execution-id <id>", "detached execution id")
    .requiredOption("--from <offset>", "journal byte offset to resume from", parseNumberOption)
    .option("--state-root <path>", "agent-compose runtime state root")
    .option("--stdin", "forward stdin frames to the task")
    .action(async (options: { executionId: string; from: number; stateRoot?: string; stdin?: boolean }) => {
      const exitCode = await attachExecution({
        stateRoot: options.stateRoot ?? defaultStateRoot(),
        executionId: options.executionId,
        from: options.from,
        ...(options.stdin ? { stdin: process.stdin } : {}),
      });
      process.exitCode = exitCode;
    });

  program
    .command("stop")
    .requiredOption("--execution-id <id>", "detached execution id")
    .requiredOption("--reason <reason>", "stop reason: user or timeout")
    .option("--state-root <path>", "agent-compose runtime state root")
    .action(async (options: { executionId: string; reason: string; stateRoot?: string }) => {
      if (options.reason !== "user" && options.reason !== "timeout") {
        throw new Error("--reason must be user or timeout");
      }
      await stopExecution({
        stateRoot: options.stateRoot ?? defaultStateRoot(),
        executionId: options.executionId,
        reason: options.reason,
      });
    });

  program
    .command("cleanup")
    .description("remove a finished detached execution's state")
    .requiredOption("--execution-id <id>", "detached execution id")
    .option("--state-root <path>", "agent-compose runtime state root")
    .action(async (options: { executionId: string; stateRoot?: string }) => {
      await cleanupDetached({
        stateRoot: options.stateRoot ?? defaultStateRoot(),
        executionId: options.executionId,
      });
    });

  // Internal: launched detached by `start`; owns the task across the exec.
  program
    .command("supervisor")
    .requiredOption("--execution-id <id>", "detached execution id")
    .requiredOption("--state-root <path>", "agent-compose runtime state root")
    .requiredOption("--nonce <nonce>", "per-execution nonce")
    .option("--task-timeout-ms <ms>", "stop the task with reason timeout after this long", parseNumberOption)
    .option("--stdin", "feed the task's stdin from the execution's stdin socket")
    .option("--raw-command", "run the task args as a command line instead of a runtime subcommand")
    .allowExcessArguments(true)
    .action(async function supervisor(this: Command, options: { executionId: string; stateRoot: string; nonce: string; taskTimeoutMs?: number; stdin?: boolean; rawCommand?: boolean }) {
      const taskArgs = this.args ?? [];
      if (options.rawCommand) {
        if (taskArgs.length === 0) {
          throw new Error("--raw-command needs a command after --");
        }
        await runSupervisor({
          stateRoot: options.stateRoot,
          executionId: options.executionId,
          nonce: options.nonce,
          taskTimeoutMs: options.taskTimeoutMs,
          ...(options.stdin ? { stdin: true } : {}),
          taskCommand: taskArgs[0],
          taskArgs: taskArgs.slice(1),
        });
        return;
      }
      const taskCommand = process.execPath;
      await runSupervisor({
        stateRoot: options.stateRoot,
        executionId: options.executionId,
        nonce: options.nonce,
        taskTimeoutMs: options.taskTimeoutMs,
        ...(options.stdin ? { stdin: true } : {}),
        taskCommand,
        taskArgs: [resolveCliPath(), ...taskArgs],
      });
    });

  return program;
}

function defaultStateRoot(): string {
  return process.env.STATE_ROOT || process.env.AGENT_COMPOSE_STATE_ROOT || "/data/state";
}

function parseNumberOption(value: string): number {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) {
    throw new Error(`expected a finite number, received: ${value}`);
  }
  return parsed;
}

function formatGuestAbiSummary(report: GuestAbiReport): string {
  const lines = [
    `guest ABI: ${report.guestAbi}`,
    `manifest: ${report.manifestPath}`,
    `runtime version: ${report.runtimeVersion}`,
  ];
  const providers = Object.entries(report.providers).sort(([left], [right]) => left.localeCompare(right));
  if (providers.length === 0) {
    lines.push("providers: none");
  } else {
    lines.push("providers:");
    for (const [name, provider] of providers) {
      lines.push(`  ${name} ${provider.version} (${provider.features.length} features)`);
    }
  }
  return `${lines.join("\n")}\n`;
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

import process from "node:process";
import { AGENT_OUTPUT_ENV, RESULT_PREFIX } from "./constants.js";
import { formatError } from "./errors.js";
import { createFrameEmitter } from "./frame.js";
import { runPromptCommand, type PromptCommandOptions } from "./prompt.js";

/** Preserve prompt's public stdout contract unless the caller requests frames. */
export async function runPromptWithOutput(
  options: PromptCommandOptions,
  env: NodeJS.ProcessEnv = process.env,
  write: (line: string) => void = (line) => { process.stdout.write(line); },
): Promise<void> {
  const format = env[AGENT_OUTPUT_ENV];
  if (format && format !== "frames") {
    throw new Error(`unsupported ${AGENT_OUTPUT_ENV} value: ${format}`);
  }
  if (format !== "frames") {
    const result = await runPromptCommand(options);
    write(`${RESULT_PREFIX}${JSON.stringify(result)}\n`);
    return;
  }

  const emit = createFrameEmitter(write);
  emit("started", { provider: options.provider });
  try {
    const result = await runPromptCommand({
      ...options,
      onEvent: (event) => emit("agent_event", { event }),
    });
    emit("agent_turn_completed", result);
    emit("result", result);
  } catch (error) {
    emit("error", { code: "prompt_failed", message: formatError(error) });
    throw error;
  }
}

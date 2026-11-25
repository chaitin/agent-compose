import process from "node:process";
import { AGENT_OUTPUT_ENV, RESULT_PREFIX } from "./constants.js";
import { formatError } from "./errors.js";
import { createFrameEmitter } from "./frame.js";
import { runPromptCommand, type PromptCommandOptions } from "./prompt.js";
import { normalizeProvider } from "./provider.js";
import type { Provider } from "./types.js";

/** Preserve prompt's public stdout contract unless the caller requests frames. */
export async function runPromptWithOutput(
  options: PromptCommandOptions,
  env: NodeJS.ProcessEnv = process.env,
  write: (line: string) => void = (line) => { process.stdout.write(line); },
  flush: () => Promise<void> = flushStdout,
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
  emit("started", startedFields(options.provider));
  try {
    const result = await runPromptCommand({
      ...options,
      onEvent: (event) => emit("agent_event", { event }),
    });
    // Same fields as stream's agent_turn_completed; the full result, with its
    // transcript, is carried once by the result frame.
    emit("agent_turn_completed", {
      provider: result.provider,
      threadId: result.threadId,
      finalText: result.finalText,
      finalTextSource: result.finalTextSource,
      stopReason: result.stopReason,
    });
    emit("result", result);
  } catch (error) {
    emit("error", { code: "prompt_failed", message: formatError(error) });
    // The CLI exits with process.exit on failure, which does not wait for
    // pending stdout writes; on a pipe they can be asynchronous.
    await flush();
    throw error;
  }
}

/**
 * Reports the provider as the run will resolve it. An invalid or missing
 * provider is left out here; runPromptCommand rejects it and the error frame
 * reports why.
 */
function startedFields(raw: string | undefined): { provider?: Provider } {
  try {
    return { provider: normalizeProvider(raw) };
  } catch {
    return {};
  }
}

/** Resolves once every write queued on stdout so far has been handed off. */
function flushStdout(): Promise<void> {
  return new Promise((resolve) => {
    process.stdout.write("", () => resolve());
  });
}

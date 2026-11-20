import process from "node:process";
import type { AgentEventSink } from "./agent-event.js";
import { AGENT_EVENT_PREFIX, AGENT_EVENTS_ENV } from "./constants.js";

/**
 * Sink that writes each neutral agent event as one AGENT_EVENT_PREFIX line,
 * or undefined when the daemon did not opt in. The lines share stdout with
 * the final RESULT_PREFIX line, which is always written last, so a reader
 * that scans for the result is unaffected by events ahead of it.
 */
export function agentEventLineSink(
  env: NodeJS.ProcessEnv = process.env,
  write: (line: string) => void = (line) => {
    process.stdout.write(line);
  },
): AgentEventSink | undefined {
  if (env[AGENT_EVENTS_ENV] !== "1") {
    return undefined;
  }
  return (event) => {
    write(`${AGENT_EVENT_PREFIX}${JSON.stringify(event)}\n`);
  };
}

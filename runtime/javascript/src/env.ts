import process from "node:process";
import { AGENT_OUTPUT_ENV } from "./constants.js";

/** Protocol selection belongs to this invocation, not nested SDK/tool calls. */
export function providerEnv(source: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const env = { ...source };
  delete env[AGENT_OUTPUT_ENV];
  return env;
}

export function stringEnv(source: NodeJS.ProcessEnv = process.env): Record<string, string> {
  return Object.fromEntries(
    Object.entries(source).filter((entry): entry is [string, string] => typeof entry[1] === "string"),
  );
}

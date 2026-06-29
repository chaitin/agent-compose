export { agent } from "./agent.js";
export type { RuntimeAgentOptions, RuntimeAgentOutputSchema, RuntimeAgentResult } from "./agent.js";
export { artifact } from "./artifact.js";
export type { RuntimeArtifactRecord, RuntimeArtifactWriteOptions } from "./artifact.js";
export { context, readContext } from "./context.js";
export type { RuntimeCapabilityScope, RuntimeContext } from "./context.js";
export { env, paths } from "./env.js";
export type { RuntimePaths } from "./env.js";
export { event } from "./event.js";
export type { RuntimeEventRecord } from "./event.js";
export { CommandError, exec, shell } from "./exec.js";
export type { RuntimeCommandResult, RuntimeExecOptions } from "./exec.js";
export { llm } from "./llm.js";
export type { RuntimeLLMOptions, RuntimeLLMOutputSchema, RuntimeLLMResult } from "./llm.js";
export { log } from "./log.js";
export { report } from "./report.js";
export type { RuntimeReportWriteOptions } from "./report.js";
export type { RuntimeJsonSchema, RuntimeOutputSchema } from "./schema.js";
export { ssh } from "./ssh.js";
export type { RuntimeSshConfig, RuntimeSshPrepareOptions } from "./ssh.js";
export { state, stateStore } from "./state.js";
export type { RuntimeStateStore } from "./state.js";

import { agent } from "./agent.js";
import { artifact } from "./artifact.js";
import { context } from "./context.js";
import { env, paths } from "./env.js";
import { event } from "./event.js";
import { exec, shell } from "./exec.js";
import { llm } from "./llm.js";
import { log } from "./log.js";
import { report } from "./report.js";
import { ssh } from "./ssh.js";
import { state, stateStore } from "./state.js";

export const runtime = {
  exec,
  shell,
  agent,
  artifact,
  context,
  llm,
  env,
  event,
  paths,
  log,
  report,
  ssh,
  state,
  stateStore,
};

export default runtime;

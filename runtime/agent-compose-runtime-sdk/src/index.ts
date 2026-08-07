export { agent } from "./agent.js";
export type { RuntimeAgentOptions, RuntimeAgentOutputSchema, RuntimeAgentResult } from "./agent.js";
export { env, paths } from "./env.js";
export type { RuntimePaths } from "./env.js";
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
export {
  RuntimeWorkflowError,
  RuntimeWorkflowProtocolError,
  RuntimeWorkflowTimeoutError,
  workflow,
  workflowFile,
} from "./workflow.js";
export type {
  RuntimeWorkflowAborted,
  RuntimeWorkflowAgent,
  RuntimeWorkflowCompleted,
  RuntimeWorkflowEvent,
  RuntimeWorkflowFailed,
  RuntimeWorkflowMeta,
  RuntimeWorkflowOptions,
  RuntimeWorkflowOutcome,
  RuntimeWorkflowResult,
} from "./workflow.js";

import { agent } from "./agent.js";
import { env, paths } from "./env.js";
import { exec, shell } from "./exec.js";
import { llm } from "./llm.js";
import { log } from "./log.js";
import { report } from "./report.js";
import { ssh } from "./ssh.js";
import { workflow, workflowFile } from "./workflow.js";

export const runtime = {
  exec,
  shell,
  agent,
  llm,
  env,
  paths,
  log,
  report,
  ssh,
  workflow,
  workflowFile,
};

export default runtime;

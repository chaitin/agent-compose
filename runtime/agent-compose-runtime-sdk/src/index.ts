export { agent } from "./agent.js";
export type { RuntimeAgentOptions, RuntimeAgentOutputSchema, RuntimeAgentResult } from "./agent.js";
export { artifact } from "./artifact.js";
export type { RuntimeArtifactRecord, RuntimeArtifactWriteOptions } from "./artifact.js";
export { capability } from "./capability.js";
export type { RuntimeCapabilityCallOptions, RuntimeCapabilityRequest } from "./capability.js";
export { context, readContext } from "./context.js";
export type { RuntimeCapabilityScope, RuntimeContext } from "./context.js";
export { env, paths } from "./env.js";
export { RuntimeBridgeError, RuntimeUnsupportedError } from "./errors.js";
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
export { secret } from "./secret.js";
export { invokeService, service } from "./service.js";
export type { RuntimeServiceInvokeOptions, RuntimeServiceRequest } from "./service.js";
export { ssh } from "./ssh.js";
export type { RuntimeSshConfig, RuntimeSshPrepareOptions } from "./ssh.js";
export { state, stateStore } from "./state.js";
export type { RuntimeStateStore } from "./state.js";
export { mockRuntime };
export type { RuntimeMock, RuntimeMockHandler, RuntimeMockRegistry } from "./test.js";

import { agent } from "./agent.js";
import { artifact } from "./artifact.js";
import { capability } from "./capability.js";
import { context } from "./context.js";
import { env, paths } from "./env.js";
import { event } from "./event.js";
import { exec, shell } from "./exec.js";
import { llm } from "./llm.js";
import { log } from "./log.js";
import { report } from "./report.js";
import { secret } from "./secret.js";
import { invokeService, service } from "./service.js";
import { ssh } from "./ssh.js";
import { state, stateStore } from "./state.js";
import { mockRuntime } from "./test.js";

export const runtime = {
  exec,
  shell,
  agent,
  artifact,
  capability,
  context,
  llm,
  env,
  event,
  invokeService,
  paths,
  log,
  report,
  secret,
  service,
  ssh,
  state,
  stateStore,
  test: {
    mockRuntime,
  },
};

export default runtime;

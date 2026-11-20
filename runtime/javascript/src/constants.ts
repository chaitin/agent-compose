export const SANDBOX_ROOT = "/srv/agent-compose/sandbox";
export const RESULT_PREFIX = "__AGENT_RESULT__";
export const COMMAND_RESULT_PREFIX = "__COMMAND_RESULT__";
export const WORKFLOW_RESULT_PREFIX = "__WORKFLOW_RESULT__";
export const WORKFLOW_EVENT_PREFIX = "__WORKFLOW_EVENT__";
export const AGENT_EVENT_PREFIX = "__AGENT_EVENT__";
/**
 * Opt-in for AGENT_EVENT_PREFIX lines on the non-interactive `prompt` path.
 * The daemon sets it only when it can parse them, so an older daemon never
 * sees event lines mixed into a prompt's stdout.
 */
export const AGENT_EVENTS_ENV = "AGENT_COMPOSE_RUNTIME_EVENTS";

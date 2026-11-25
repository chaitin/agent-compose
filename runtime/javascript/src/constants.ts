export const SANDBOX_ROOT = "/srv/agent-compose/sandbox";
export const RESULT_PREFIX = "__AGENT_RESULT__";
export const COMMAND_RESULT_PREFIX = "__COMMAND_RESULT__";
export const WORKFLOW_RESULT_PREFIX = "__WORKFLOW_RESULT__";
export const WORKFLOW_EVENT_PREFIX = "__WORKFLOW_EVENT__";
// Explicit caller opt-in to frame output; unset preserves the published prompt CLI contract.
export const AGENT_OUTPUT_ENV = "AGENT_COMPOSE_RUNTIME_OUTPUT";

export function codexModelConfig(env: NodeJS.ProcessEnv): Record<string, number> {
  const value = env.AGENT_COMPOSE_CODEX_CONTEXT_WINDOW;
  if (!value || !/^[0-9]+$/.test(value)) return {};
  const window = Number(value);
  if (window <= 0 || window > 2_147_483_647) return {};
  // Keep headroom for prompt assembly and override a static project limit.
  return {
    model_context_window: window,
    model_auto_compact_token_limit: Math.max(1, Math.floor(window * 0.75)),
  };
}

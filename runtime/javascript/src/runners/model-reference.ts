/**
 * The daemon resolves the guest model, including any CLI provider namespace.
 * Never interpret slashes in that value: they can belong to the model ID.
 *
 * Compatibility only: daemons predating AGENT_COMPOSE_RESOLVED_MODEL pass the
 * declared connection/model argument. Keep the legacy Pi/DSH conversion here
 * until those daemon versions are no longer supported. New routing must use
 * the resolved value instead of extending these fallback rules.
 */
export function resolveFacadeModel(
  agent: "pi" | "dsh",
  requestedModel: string | undefined,
  resolvedModel: string | undefined,
): string {
  const resolved = resolvedModel?.trim();
  if (resolved) return resolved;

  const legacy = requestedModel?.trim() || "";
  if (!legacy) return "";
  if (agent === "pi" && legacy.startsWith("agent-compose/")) return legacy;
  const separator = legacy.indexOf("/");
  const model = separator >= 0 ? legacy.slice(separator + 1) : legacy;
  return agent === "pi" ? `agent-compose/${model}` : model;
}

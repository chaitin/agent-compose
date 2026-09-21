/** Adapt the installed native DSH backend, including versions without FULL mode. */

export const inject = ['sessions'];

export async function apply(ctx, inheritedConfig) {
  const raw = process.env.AGENT_COMPOSE_DSH_TELEMETRY;
  if (!raw) {
    // The telemetry backend is optional. Do not resolve it for ordinary DSH
    // executions, so an image without that package remains fully usable.
    return;
  }
  const telemetry = JSON.parse(raw);
  if (!telemetry.captureContent) {
    ctx.logger.warn('agent-compose: DSH native telemetry requires AGENT_TELEMETRY_CAPTURE_CONTENT=true; export disabled');
    return;
  }
  let backend;
  try {
    backend = await import('@deepseek-ai/dsh-session-telemetry-otel');
  } catch {
    ctx.logger.warn('agent-compose: DSH telemetry backend is unavailable; export disabled');
    return;
  }
  if (backend.SessionTelemetryMode?.FULL !== 'FULL') {
    ctx.logger.warn('agent-compose: installed DSH telemetry backend has no FULL mode; export disabled');
    return;
  }
  // DSH exposes a record waterfall rather than custom Resource attributes.
  ctx.on('session-telemetry/record', (_record, next) => {
    const record = next();
    return { ...record, attributes: { ...record.attributes, ...telemetry.attributes } };
  });
  await ctx.plugin(backend, {
    mode: backend.SessionTelemetryMode.FULL,
    exporter: { url: `${telemetry.endpoint}/v1/logs`, headers: telemetry.headers, timeoutMillis: 3000 },
    processor: { scheduledDelayMillis: 1000, exportTimeoutMillis: 3000 },
    shutdownTimeoutMillis: 4000,
  });
}

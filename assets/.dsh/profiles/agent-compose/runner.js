/**
 * agent-compose's DSH runner plugin: creates or resumes one Agent, drives one
 * followup turn, streams every session/event to stdout as a JSON line
 * (`{"type":"session_event","sessionId":...,"event":...}`), flushes the
 * session, and requests a bounded process exit.
 *
 * Modeled on @deepseek-ai/dsh-headless's one-shot driver (create, followup,
 * whenIdle, flush, exit) and dsh-TUI's dsh-cc-tui plugin (create-vs-resume,
 * ctx.on('session/event', ...) subscription, agentDefaultModel selection).
 * Neither is reused directly: headless has no resume/no event stream, and
 * cc-tui is interactive. See docs/design/dsh_agent_provider_design.md §3.3/§3.4.
 */

import { randomUUID } from 'node:crypto';
import fs from 'node:fs/promises';
import { createUserMessage } from '@deepseek-ai/dsh-llm';
import * as dshMcpClient from '@deepseek-ai/dsh-mcp-client';
import { SessionId } from '@deepseek-ai/dsh-session';
import { PERSONA_PREFIX_SECTION } from '@deepseek-ai/dsh-system-prompt';
import z from '@deepseek-ai/schemastery';

export const name = 'agent-compose-runner';
export const inject = ['agents', 'sessions', 'agentDefaultModel'];

export const Config = z.object({
  promptFile: z.string().required(),
  sessionId: z.string(),
  systemContextFile: z.string(),
});

function writeSessionEventLine(sessionId, event) {
  process.stdout.write(`${JSON.stringify({ type: 'session_event', sessionId, event })}\n`);
}

// The `cordis.patch.yml` overlay is static (baked into the guest image), but
// MCP servers are a dynamic 0..N list only known at run time — they can't be
// expressed as static plugin rows. runner.js already does the analogous
// thing for session create/resume, so it registers one dsh-mcp-client
// instance per server here instead. `ctx.plugin()`'s returned Fiber settles
// once that server's plugin has finished loading (and its tools are
// registered on ctx.tools), so awaiting all of them guarantees every
// configured server's tools are visible before the agent's first turn.
// See docs/design/dsh_agent_provider_design.md §6.
async function registerMcpServers(ctx) {
  const raw = process.env.DSH_MCP_SERVERS;
  if (!raw) return;
  const servers = JSON.parse(raw);
  await Promise.all(servers.map((server) => ctx.plugin(dshMcpClient, server)));
}

// DSH 0.1.5 renamed the deployment persona slot to the *prefix* section and
// moved its placement behind SystemPrompt.getSectionOrder(); 0.2 removed the
// old PERSONA_SECTION/PERSONA_ORDER exports outright, which made this module
// fail to import and left every dsh run hanging. The prefix slot is still the
// agent-scoped override this runner needs: a scoped contribution shadows the
// deployment's own section of the same name ("an agent preset shadows the
// deployment's persona with its own" — dsh-system-prompt's doc comment), and
// agent.ctx is agent-scoped, so this replaces the empty global default the
// cordis.patch.yml `system-prompt` row leaves in place instead of duplicating
// it. See docs/design/dsh_agent_provider_design.md §7.
async function injectPersona(agent, config) {
  if (!config.systemContextFile) return;
  const text = await fs.readFile(config.systemContextFile, 'utf8');
  const prompt = agent.ctx.systemPrompt;
  prompt.section({
    name: PERSONA_PREFIX_SECTION,
    order: prompt.getSectionOrder('DEPLOYMENT_PERSONA_PREFIX'),
    text,
  });
}

/** Request a bounded process exit once the tree disposes (mirrors dsh-headless / dsh-cc-tui). */
async function exitTree(ctx, code) {
  const exit = ctx.get('appExit');
  if (typeof exit === 'function') {
    exit(code);
    return;
  }
  const timer = setTimeout(() => process.exit(code), 5000);
  timer.unref();
  try {
    await ctx.root.fiber.dispose();
  } finally {
    clearTimeout(timer);
    process.exit(code);
  }
}

async function run(ctx, config) {
  await ctx.get('loader')?.await();
  const agents = ctx.get('agents');
  const sessions = ctx.get('sessions');
  const defaultModel = ctx.get('agentDefaultModel');
  if (agents === undefined || sessions === undefined || defaultModel === undefined) {
    // inject: is reactive, not a one-time blocking wait — Cordis can invoke
    // apply() before the injected services are mounted and will call it
    // again once they are (confirmed by booting this profile directly: the
    // first invocation lands here). Mirror dsh-headless's own driver and
    // no-op instead of failing the premature call.
    return;
  }

  const promptText = await fs.readFile(config.promptFile, 'utf8');
  const resume = process.env.DSH_RESUME === '1';
  const requestedSessionId = config.sessionId ? SessionId(config.sessionId) : undefined;
  const selection = defaultModel.currentSelection();
  const agentOptions = { provider: selection.provider, model: selection.model };

  await registerMcpServers(ctx);

  let agent;
  if (resume) {
    if (requestedSessionId === undefined) {
      throw new Error('agent-compose-runner: DSH_RESUME=1 was set without a session id');
    }
    // Deliberately not caught: a resume miss must fail loud, not silently
    // fall back to create() and drop the caller's history (see §3.4).
    const resumed = await agents.resume({ resumeSessionId: requestedSessionId, agentOptions });
    agent = resumed.agent;
  } else {
    const sessionId = requestedSessionId ?? SessionId(`session-${randomUUID()}`);
    const created = await agents.create({
      sessionId,
      meta: { cwd: process.cwd() },
      agentOptions,
    });
    agent = created.agent;
  }

  await injectPersona(agent, config);

  const sessionId = agent.session.id;
  // DSH 0.2 removed the Session.events getter (the snapshot readers are
  // deprecated in favor of an explicitly owned interval), so retain the last
  // event as the subscription delivers it instead of reading the log back.
  let lastEvent;
  const unsubscribe = ctx.on('session/event', (session, event) => {
    if (session.id !== sessionId) return;
    lastEvent = event;
    writeSessionEventLine(sessionId, event);
  });

  let exitCode = 0;
  try {
    await agent.whenIdle();
    agent.followup(createUserMessage({
      content: [{ type: 'text', text: promptText }],
      source: { kind: 'user' },
    }));
    await agent.whenIdle();
    await sessions.flush(agent.session);
    if (lastEvent?.type === 'turn/end' && lastEvent.data.reason.kind !== 'completed') {
      exitCode = 1;
    }
  } finally {
    unsubscribe?.();
  }

  await exitTree(ctx, exitCode);
}

export function apply(ctx, config) {
  void run(ctx, config).catch(async (error) => {
    process.stderr.write(`agent-compose-runner: ${error instanceof Error ? error.stack || error.message : String(error)}\n`);
    await exitTree(ctx, 1);
  });
}

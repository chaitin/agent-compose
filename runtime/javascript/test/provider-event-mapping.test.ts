import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import type { AgentEvent } from "../src/agent-event.js";
import { ClaudeRunner } from "../src/runners/claude.js";
import { CodexRunner } from "../src/runners/codex.js";
import { DshRunner } from "../src/runners/dsh.js";
import { GeminiRunner } from "../src/runners/gemini.js";
import { OpenCodeRunner } from "../src/runners/opencode.js";
import { PiRunner } from "../src/runners/pi.js";
import type { AgentResult, Provider, RunnerOptions } from "../src/types.js";

// Fixtures are real recorded runs of one prompt ("write hello.txt, read it
// back") against each provider. They exist to catch provider field drift —
// codex, for instance, sends a cache_write_input_tokens that its own SDK type
// does not declare.
const fixturesDir = path.join(import.meta.dirname, "fixtures", "providers");

function readFixture(provider: Provider): Record<string, unknown>[] {
  return readFileSync(path.join(fixturesDir, `${provider}.jsonl`), "utf8")
    .split("\n")
    .filter((line) => line.trim() !== "")
    .map((line) => JSON.parse(line) as Record<string, unknown>);
}

function options(provider: Provider, onEvent: (event: AgentEvent) => void): RunnerOptions {
  return {
    provider,
    stateRoot: "/state",
    sessionRoot: "/state",
    workspace: "/workspace",
    home: "/home",
    runtimeRoot: "/runtime",
    systemContext: "",
    onEvent,
  };
}

const silentWriter = { write() {}, line() {}, transcript: () => "" };

function blankResult(provider: Provider): AgentResult {
  return { provider, threadId: "", stopReason: "", finalText: "", finalTextSource: "none", transcript: "", stderr: "" };
}

function replay(provider: Provider): AgentEvent[] {
  const events: AgentEvent[] = [];
  const push = (event: AgentEvent) => events.push(event);
  const result = blankResult(provider);
  const lines = readFixture(provider);
  if (provider === "claude") {
    const runner = new ClaudeRunner(options(provider, push), silentWriter);
    for (const message of lines) {
      runner.emitTopLevel(message);
      if (message.type === "stream_event") {
        runner.handleStreamEvent(message);
      }
    }
    return events;
  }
  if (provider === "gemini") {
    const runner = new GeminiRunner(options(provider, push));
    for (const event of lines) {
      runner.handleEvent(event, result);
    }
    return events;
  }
  if (provider === "dsh") {
    const runner = new DshRunner(options(provider, push), silentWriter);
    for (const line of lines) {
      if (line.type !== "session_event" || typeof line.event !== "object" || line.event === null) {
        continue;
      }
      runner.handleEvent(line.event as Record<string, unknown>, result);
    }
    return events;
  }
  const runner = provider === "codex"
    ? new CodexRunner(options(provider, push), silentWriter)
    : provider === "opencode"
      ? new OpenCodeRunner(options(provider, push), silentWriter)
      : new PiRunner(options(provider, push), silentWriter);
  for (const event of lines) {
    runner.handleEvent(event, result);
  }
  return events;
}

const providers: Provider[] = ["codex", "claude", "gemini", "opencode", "pi", "dsh"];

describe("provider event mapping", () => {
  // Per-provider coverage without a snapshot: a serialised dump would be
  // 12KB a reviewer cannot judge, and updating it after an intentional mapper
  // change hides a regression in the diff. These assert what the mapping is
  // actually for.
  const knownKinds = new Set([
    "step_start", "step_end", "text_delta", "reasoning_delta", "tool_call",
    "tool_result", "todo", "usage", "retry", "compaction", "error",
  ]);

  for (const provider of providers) {
    it(`maps the recorded ${provider} run onto well-formed neutral events`, () => {
      const events = replay(provider);
      expect(events.length).toBeGreaterThan(0);
      for (const event of events) {
        expect(knownKinds.has(event.kind), `${provider}: ${event.kind}`).toBe(true);
        // An empty delta is noise the mapper should have dropped, not passed on.
        if (event.kind === "text_delta" || event.kind === "reasoning_delta") {
          expect(event.text, provider).not.toBe("");
        }
        if (event.kind === "tool_call") {
          expect(event.id, provider).not.toBe("");
          expect(event.name, provider).not.toBe("");
        }
      }
      // Every provider answered the prompt, so text must have reached the client.
      expect(events.some((event) => event.kind === "text_delta"), provider).toBe(true);
    });
  }

  it("emits a tool_call and a correlated tool_result for every provider", () => {
    for (const provider of providers) {
      const events = replay(provider);
      const calls = events.filter((event) => event.kind === "tool_call");
      const results = events.filter((event) => event.kind === "tool_result");
      expect(calls.length, provider).toBeGreaterThan(0);
      expect(results.length, provider).toBeGreaterThan(0);
      // A result must reference a call that was announced, or consumers cannot
      // pair a tool's output with its input.
      const callIds = new Set(calls.map((event) => event.id));
      for (const result of results) {
        expect(callIds.has(result.id), `${provider}: ${result.id}`).toBe(true);
      }
    }
  });

  it("reports usage without double counting and never as an all-zero record", () => {
    // DSH publishes identical usage twice (assistant/chunk and
    // assistant/message); only one may be mapped or every count doubles.
    const expected: Record<Provider, number> = {
      codex: 1, claude: 3, gemini: 1, opencode: 2, pi: 3, dsh: 3,
    };
    for (const provider of providers) {
      const usage = replay(provider).filter((event) => event.kind === "usage");
      expect(usage.length, provider).toBe(expected[provider]);
      for (const record of usage) {
        expect(record.inputTokens + record.outputTokens, provider).toBeGreaterThan(0);
      }
    }
  });

  it("keeps inputTokens exclusive of cached tokens", () => {
    // codex and gemini report an inclusive prompt count upstream; the mappers
    // subtract so the field means the same thing everywhere.
    const codexUsage = replay("codex").find((event) => event.kind === "usage");
    expect(codexUsage).toMatchObject({ scope: "turn", inputTokens: 25788, cachedTokens: 2816 });
    const geminiUsage = replay("gemini").find((event) => event.kind === "usage");
    expect(geminiUsage).toMatchObject({ scope: "run", inputTokens: 5216, cachedTokens: 1024 });
  });

  it("omits kinds the provider cannot produce rather than emitting empty ones", () => {
    // No provider reported reasoning in these runs (design doc §7): the kind
    // must be absent, not present with an empty payload.
    for (const provider of providers) {
      expect(replay(provider).some((event) => event.kind === "reasoning_delta"), provider).toBe(false);
    }
    // codex and gemini expose no per-model-call boundary at all.
    for (const provider of ["codex", "gemini"] as Provider[]) {
      expect(replay(provider).some((event) => event.kind === "step_start"), provider).toBe(false);
    }
  });
});

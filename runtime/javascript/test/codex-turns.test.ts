import { describe, expect, it } from "vitest";
import type { AgentEvent } from "../src/agent-event.js";
import { CodexRunner } from "../src/runners/codex.js";
import type { AgentResult, RunnerOptions } from "../src/types.js";

// Each SDK call launches a new CLI. The item IDs below deliberately repeat
// across calls, while started/updated/completed updates also repeat within one.
describe("Codex turn identity", () => {
  it("keeps repeated item IDs in later turns without duplicating updates", async () => {
    const events: AgentEvent[] = [];
    const chunks: string[] = [];
    const options: RunnerOptions = {
      provider: "codex", stateRoot: "/state", sessionRoot: "/state", workspace: "/workspace",
      home: "/home", runtimeRoot: "/runtime", systemContext: "", onEvent: (event) => events.push(event),
    };
    const writer = {
      write: (text: string) => { chunks.push(text); },
      line: (text = "") => { chunks.push(`${text}\n`); },
      transcript: () => chunks.join(""),
    };
    const runner = new CodexRunner(options, writer);
    const thread = {
      async runStreamed() {
        return {
          events: (async function* () {
            yield { type: "item.updated", item: { id: "item_0", type: "agent_message", text: "hello" } };
            yield { type: "item.completed", item: { id: "item_0", type: "agent_message", text: "hello" } };
            yield { type: "item.completed", item: {
              id: "item_1", type: "command_execution", command: "ls", status: "completed",
              exit_code: 0, aggregated_output: "file.txt\n",
            } };
          })(),
        };
      },
    };
    for (const prompt of ["first", "second"]) {
      const result: AgentResult = {
        provider: "codex", threadId: "thread", stopReason: "", finalText: "",
        finalTextSource: "none", transcript: "", stderr: "",
      };
      await runner.runTurn(thread, prompt, result);
      expect(result.finalText).toBe("hello");
    }
    expect(events.filter((event) => event.kind === "text_delta")).toEqual([
      { kind: "text_delta", text: "hello" }, { kind: "text_delta", text: "hello" },
    ]);
    expect(writer.transcript().match(/file\.txt/g)).toHaveLength(2);
    expect(writer.transcript().match(/hello/g)).toHaveLength(2);
  });
});

#!/usr/bin/env node
// A deterministic provider subprocess; the SDK, guest runtime, daemon and driver
// remain real. Files gate progress so a host restart never depends on a sleep.
const fs = require("node:fs");
const { setTimeout: delay } = require("node:timers/promises");

async function main() {
  let input = "";
  for await (const chunk of process.stdin) input += chunk;
  const match = /runlog:([a-z0-9-]+):(gated|immediate)/.exec(input);
  if (!match) throw new Error("missing runlog fixture instruction");
  const [, token, mode] = match;
  const emit = event => process.stdout.write(JSON.stringify(event) + "\n");
  const wait = async phase => {
    const deadline = Date.now() + 300000;
    while (!fs.existsSync(`/workspace/${token}.${phase}`)) {
      if (Date.now() > deadline) throw new Error(`fixture timed out: ${token}.${phase}`);
      await delay(50);
    }
  };
  fs.appendFileSync("/data/state/runlog-fixture.calls", token + "\n");
  emit({ type: "thread.started", thread_id: `runlog-thread-${token}` });
  emit({ type: "item.started", item: { id: "tool-1", type: "command_execution", command: "printf fixture-tool", status: "in_progress" } });
  let text = `runlog-answer-${token}:before:`;
  emit({ type: "item.updated", item: { id: "answer", type: "agent_message", text } });
  if (mode === "gated") await wait("phase1");
  text += "middle:";
  emit({ type: "item.updated", item: { id: "answer", type: "agent_message", text } });
  // The answer is now mid-paragraph, held by the daemon and not yet in its log.
  if (mode === "gated") fs.writeFileSync(`/workspace/${token}.middle-emitted`, "");
  if (mode === "gated") await wait("finish");
  emit({ type: "item.completed", item: { id: "tool-1", type: "command_execution", command: "printf fixture-tool", aggregated_output: "fixture-tool", exit_code: 0, status: "completed" } });
  text += "after";
  emit({ type: "item.completed", item: { id: "answer", type: "agent_message", text } });
  emit({ type: "turn.completed", usage: { input_tokens: 11, cached_input_tokens: 3, output_tokens: 7 } });
}

main().catch(error => { console.error(error); process.exitCode = 1; });

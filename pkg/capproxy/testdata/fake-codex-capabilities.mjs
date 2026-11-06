#!/usr/bin/env node
// Exercises the existing Codex-compatible process protocol without calling a model.
import fs from "node:fs";
import { capabilities } from "../../../runtime/agent-compose-runtime-sdk/dist/esm/index.js";

const emit = (event) => process.stdout.write(`${JSON.stringify(event)}\n`);
emit({ type: "thread.started", thread_id: "capability-probe" });
try {
  const input = JSON.parse(fs.readFileSync(0, "utf8"));
  const tools = await capabilities.list();
  const method = input.method ?? "grpc.health.v1.Health/Check";
  const tool = tools.find((item) => item.instance === input.instance && item.method === method);
  if (!tool) throw new Error("expected capability missing");
  const description = await capabilities.describe(tool.id);
  if (!description.request.includes(input.expectedField ?? "service")) throw new Error("request descriptor missing");
  const result = await capabilities.call(tool.id, input.params ?? { service: input.instance });
  emit({ type: "item.completed", item: { id: "reply", type: "agent_message", text: JSON.stringify({ instance: input.instance, result }) } });
  emit({ type: "turn.completed" });
} catch (error) {
  emit({ type: "turn.failed", error: { message: error.message } });
  process.exitCode = 1;
}

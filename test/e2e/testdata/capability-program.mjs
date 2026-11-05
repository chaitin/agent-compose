#!/usr/bin/env node
// The same application runs as a plain command or a Codex-compatible subprocess.
import fs from "node:fs";
import { spawnSync } from "node:child_process";
import { runtime } from "@chaitin-ai/agent-compose-runtime-sdk";

const emit = (event) => process.stdout.write(`${JSON.stringify(event)}\n`);
emit({ type: "thread.started", thread_id: "generic-capability-program" });
try {
  const input = JSON.parse(process.argv[2]?.startsWith("{") ? process.argv[2] : fs.readFileSync(0, "utf8"));
  const tools = await runtime.capabilities.list();
  let result = { count: tools.length };
  if (input.method) {
    const tool = tools.find((item) => item.instance === input.instance && item.method === input.method);
    if (!tool) throw new Error("requested tool not discovered");
    const definition = await runtime.capabilities.describe(tool.id);
    if (!definition.request.includes(input.field)) throw new Error("request definition unavailable");
    result = { tool, descriptor: definition.descriptorSetBase64, response: await runtime.capabilities.call(tool.id, input.params) };
  }
  if (input.deniedCapset) {
    // Bypass SDK discovery to prove that the daemon enforces the boundary too.
    const probe = spawnSync("grpcurl", ["-plaintext", "-expand-headers",
      "-H", "x-capability-sandbox-token: ${CAP_TOKEN}",
      "-H", `x-octobus-capset: ${input.deniedCapset}`,
      "-H", `x-octobus-instance: ${input.instance}`,
      "-d", "{}", process.env.CAP_GRPC_TARGET, input.deniedMethod,
    ], { encoding: "utf8", timeout: 10_000 });
    if (probe.error || probe.status === 0 || !probe.stderr.includes("PermissionDenied")) throw new Error("authorization boundary not verified");
    result.denied = true;
  }
  emit({ type: "item.completed", item: { id: "answer", type: "agent_message", text: JSON.stringify(result) } });
  emit({ type: "turn.completed", usage: { input_tokens: 0, cached_input_tokens: 0, output_tokens: 0 } });
} catch (error) {
  emit({ type: "turn.failed", error: { message: error.message } });
  process.exitCode = 1;
}

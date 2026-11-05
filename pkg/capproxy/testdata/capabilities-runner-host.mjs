import path from "node:path";
import { CodexRunner } from "../../../runtime/javascript/dist/runners/codex.js";

const root = process.env.AC_TEST_STATE;
const events = [];
const chunks = [];
const runner = new CodexRunner({
  provider: "codex", stateRoot: root, sessionRoot: root, workspace: root,
  home: root, runtimeRoot: process.env.RUNTIME_ROOT, systemContext: "capability integration probe",
  onEvent: (event) => events.push(event),
}, {
  write: (text) => chunks.push(text),
  line: (text = "") => chunks.push(`${text}\n`),
  transcript: () => chunks.join(""),
});
process.env.AGENT_COMPOSE_CODEX_BIN = path.resolve(process.env.AC_TEST_RUNNER ?? "testdata/fake-codex-capabilities.mjs");
try {
  const input = process.argv[3] ? JSON.parse(process.argv[3]) : { instance: process.argv[2] };
  const result = await runner.runPrompt(process.env.AC_TEST_QUESTION ?? JSON.stringify(input));
  process.stdout.write(JSON.stringify({ result, events }));
} catch (error) {
  process.stderr.write(JSON.stringify({ error: error.message, events }));
  process.exitCode = 1;
}

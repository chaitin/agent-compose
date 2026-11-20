import { describe, expect, it } from "vitest";
import { agentEventLineSink } from "../src/agent-event-lines.js";
import { AGENT_EVENT_PREFIX, AGENT_EVENTS_ENV } from "../src/constants.js";

describe("agentEventLineSink", () => {
  it("stays off unless the daemon opted in", () => {
    expect(agentEventLineSink({})).toBeUndefined();
    expect(agentEventLineSink({ [AGENT_EVENTS_ENV]: "0" })).toBeUndefined();
  });

  it("writes one prefixed JSON line per event", () => {
    const lines: string[] = [];
    const sink = agentEventLineSink({ [AGENT_EVENTS_ENV]: "1" }, (line) => lines.push(line));
    expect(sink).toBeDefined();
    sink?.({ kind: "text_delta", text: "hi\nthere" });
    sink?.({ kind: "tool_call", id: "t1", name: "Bash", toolKind: "execute", status: "in_progress", command: "ls" });
    expect(lines).toEqual([
      `${AGENT_EVENT_PREFIX}{"kind":"text_delta","text":"hi\\nthere"}\n`,
      `${AGENT_EVENT_PREFIX}{"kind":"tool_call","id":"t1","name":"Bash","toolKind":"execute","status":"in_progress","command":"ls"}\n`,
    ]);
  });
});

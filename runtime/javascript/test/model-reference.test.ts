import { describe, expect, it } from "vitest";
import { resolveFacadeModel } from "../src/runners/model-reference.js";

describe("facade model compatibility", () => {
  it.each(["pi", "dsh"] as const)("%s uses the resolved value without interpreting its namespace", (agent) => {
    expect(resolveFacadeModel(agent, "gateway/other", "agent-compose/org/model"))
      .toBe("agent-compose/org/model");
    expect(resolveFacadeModel(agent, undefined, "org/model")).toBe("org/model");
  });

  it.each([
    ["pi", "gateway/org/model", "agent-compose/org/model"],
    ["pi", "agent-compose/agent-compose/model", "agent-compose/agent-compose/model"],
    ["pi", "model", "agent-compose/model"],
    ["dsh", "gateway/org/model", "org/model"],
    ["dsh", "agent-compose/agent-compose/model", "agent-compose/model"],
    ["dsh", "model", "model"],
  ] as const)("%s accepts legacy argument %s", (agent, argument, expected) => {
    expect(resolveFacadeModel(agent, argument, undefined)).toBe(expected);
    expect(resolveFacadeModel(agent, argument, " ")).toBe(expected);
  });

  it.each(["pi", "dsh"] as const)("%s leaves an absent model unset", (agent) => {
    expect(resolveFacadeModel(agent, undefined, undefined)).toBe("");
    expect(resolveFacadeModel(agent, " ", " ")).toBe("");
  });
});

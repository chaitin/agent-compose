import { describe, expect, it } from "vitest";
import { parseWorkflowScript } from "../src/workflow/parser.js";

describe("workflow parser", () => {
  it("extracts static metadata and removes its export from the executable body", () => {
    const parsed = parseWorkflowScript(`export const meta = {
      name: "inspect",
      description: \`Inspect repository\`,
      phases: [{ title: "Scan", detail: "Find modules", model: "gpt" }],
    }
    return { ok: true }
    `);

    expect(parsed.meta).toEqual({
      name: "inspect",
      description: "Inspect repository",
      phases: [{ title: "Scan", detail: "Find modules", model: "gpt" }],
    });
    expect(parsed.body).not.toContain("export const meta");
    expect(parsed.scriptHash).toMatch(/^sha256:[a-f0-9]{64}$/);
  });

  it.each([
    ["missing first meta", "const value = 1; export const meta = { name: 'x', description: 'y' }"],
    ["non-const meta", "export let meta = { name: 'x', description: 'y' }"],
    ["computed property", "export const meta = { ['name']: 'x', description: 'y' }"],
    ["reserved property", "export const meta = { name: 'x', description: 'y', __proto__: {} }"],
    ["spread", "export const meta = { name: 'x', description: 'y', ...other }"],
    ["sparse array", "export const meta = { name: 'x', description: 'y', phases: [,] }"],
  ])("rejects %s", (_name, source) => {
    expect(() => parseWorkflowScript(source)).toThrow();
  });

  it.each([
    "Date.now()",
    "Date['now']()",
    "Math.random()",
    "Math['ran' + 'dom']()",
    "new Date()",
    "require('x')",
    "import('x')",
  ])("rejects nondeterministic or external expression %s", (expression) => {
    expect(() => parseWorkflowScript(`export const meta = { name: 'x', description: 'y' }; ${expression}`)).toThrow();
  });

  it("allows forbidden API names inside prompt strings", () => {
    expect(() => parseWorkflowScript(`export const meta = { name: 'x', description: 'y' }; return "Date.now() Math.random()"`)).not.toThrow();
  });
});

import fs from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { runServiceCommand } from "../src/service.js";
import { withTempSession } from "./helpers.js";

describe("service runtime", () => {
  it("invokes a JavaScript service entry with input and context", async () => {
    await withTempSession(async (root) => {
      const workspace = path.join(root, "workspace");
      const stateRoot = path.join(root, "state");
      await fs.mkdir(workspace, { recursive: true });
      await fs.writeFile(
        path.join(workspace, "echo.mjs"),
        "export async function handler(input, runtime) { return { message: input.message, source: runtime.context.source }; }\n",
        "utf8",
      );

      const result = await runServiceCommand({
        requestJson: JSON.stringify({
          serviceName: "echo",
          entry: "echo.mjs",
          inputJson: JSON.stringify({ message: "hello" }),
          contextJson: JSON.stringify({ source: "test" }),
        }),
        stateRoot,
        workspace,
      });

      expect(result.success).toBe(true);
      expect(result.protocolVersion).toBe("service.v1");
      expect(JSON.parse(result.outputJson)).toEqual({ message: "hello", source: "test" });
      await expect(fs.readFile(result.artifacts.result, "utf8")).resolves.toContain("\"serviceName\": \"echo\"");
    });
  });

  it("rejects input that does not match the declared schema", async () => {
    await withTempSession(async (root) => {
      const workspace = path.join(root, "workspace");
      await fs.mkdir(workspace, { recursive: true });
      await fs.writeFile(path.join(workspace, "echo.mjs"), "export function handler(input) { return input; }\n", "utf8");

      await expect(runServiceCommand({
        requestJson: JSON.stringify({
          serviceName: "echo",
          entry: "echo.mjs",
          inputJson: JSON.stringify({ extra: true }),
          inputSchema: JSON.stringify({
            type: "object",
            required: ["message"],
            properties: { message: { type: "string" } },
            additionalProperties: false,
          }),
        }),
        stateRoot: path.join(root, "state"),
        workspace,
      })).rejects.toThrow("input.message is required");
    });
  });

  it("rejects output that does not match the declared schema", async () => {
    await withTempSession(async (root) => {
      const workspace = path.join(root, "workspace");
      await fs.mkdir(workspace, { recursive: true });
      await fs.writeFile(path.join(workspace, "echo.mjs"), "export function handler() { return { reply: 1 }; }\n", "utf8");

      await expect(runServiceCommand({
        requestJson: JSON.stringify({
          serviceName: "echo",
          entry: "echo.mjs",
          outputSchema: JSON.stringify({
            type: "object",
            required: ["reply"],
            properties: { reply: { type: "string" } },
          }),
        }),
        stateRoot: path.join(root, "state"),
        workspace,
      })).rejects.toThrow("output.reply must be string");
    });
  });

  it("rejects unsupported service protocol versions", async () => {
    await withTempSession(async (root) => {
      const workspace = path.join(root, "workspace");
      await fs.mkdir(workspace, { recursive: true });
      await fs.writeFile(path.join(workspace, "echo.mjs"), "export function handler(input) { return input; }\n", "utf8");

      await expect(runServiceCommand({
        requestJson: JSON.stringify({
          protocolVersion: "service.v99",
          serviceName: "echo",
          entry: "echo.mjs",
        }),
        stateRoot: path.join(root, "state"),
        workspace,
      })).rejects.toThrow("unsupported service protocol version: service.v99");
    });
  });
});

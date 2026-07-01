import fs from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { runExecCommand } from "../src/command.js";
import { normalizeProvider } from "../src/provider.js";
import { CodexRunner } from "../src/runners/codex.js";
import { ClaudeRunner } from "../src/runners/claude.js";
import { runServiceCommand } from "../src/service.js";
import { TranscriptWriter } from "../src/transcript.js";
import { runnerOptions, withTempSession } from "./helpers.js";

describe("runtime shape E2E workflows", () => {
  it("runs a command and prepares agent runner configuration", async () => {
    await withTempSession(async (root) => {
      const workspace = path.join(root, "workspace");
      const requestFile = path.join(root, "request.json");
      await fs.mkdir(workspace, { recursive: true });
      await fs.writeFile(path.join(workspace, "input.txt"), "shape-e2e");
      await fs.writeFile(requestFile, JSON.stringify({
        mode: "shell",
        script: "cat input.txt",
        artifactDir: path.join(root, "artifacts"),
      }));

      const result = await runExecCommand({ requestFile, workspace, home: path.join(root, "home") });
      expect(result.success).toBe(true);
      expect(result.stdout).toBe("shape-e2e");

      expect(normalizeProvider("gemini-cli")).toBe("gemini");
      const writer = new TranscriptWriter();
      writer.write("agent ");
      writer.line("output");
      expect(writer.transcript()).toBe("agent output");

      const codex = new CodexRunner(runnerOptions(root, "mpi context"));
      expect(codex.threadOptions().additionalDirectories).toEqual([
        path.join(root, "state"),
        path.join(root, "home"),
        path.join(root, "runtime"),
      ]);

      const claude = new ClaudeRunner(runnerOptions(root, "", "claude"));
      expect(claude.queryOptions(null)).toMatchObject({
        cwd: path.join(root, "workspace"),
        permissionMode: "bypassPermissions",
      });

      await fs.writeFile(
        path.join(workspace, "service.mjs"),
        "export function handler(input, runtime) { return { message: input.message, source: runtime.context.source, service: runtime.serviceName }; }\n",
        "utf8",
      );
      const serviceResult = await runServiceCommand({
        requestJson: JSON.stringify({
          serviceName: "shape",
          entry: "service.mjs",
          inputJson: JSON.stringify({ message: "service-e2e" }),
          contextJson: JSON.stringify({ source: "shape-e2e" }),
          outputSchema: JSON.stringify({
            type: "object",
            required: ["message", "source", "service"],
            properties: {
              message: { type: "string" },
              source: { type: "string" },
              service: { type: "string" },
            },
          }),
        }),
        stateRoot: path.join(root, "state"),
        workspace,
      });
      expect(JSON.parse(serviceResult.outputJson)).toEqual({
        message: "service-e2e",
        source: "shape-e2e",
        service: "shape",
      });
      await expect(fs.readFile(serviceResult.artifacts.result, "utf8")).resolves.toContain("\"success\": true");
    });
  });
});

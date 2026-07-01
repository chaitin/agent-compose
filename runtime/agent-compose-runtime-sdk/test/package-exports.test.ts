import fs from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { withTempDir } from "./helpers.js";

describe("package exports", () => {
  it("supports CommonJS require from built package", async () => {
    await withTempDir(async (root) => {
      await linkPackage(root);
      const script = path.join(root, "require.cjs");
      await fs.writeFile(script, [
        "const sdk = require('@chaitin-ai/agent-compose-runtime-sdk');",
        "console.log(typeof sdk.runtime.shell);",
        "console.log(typeof sdk.runtime.secret.get);",
        "console.log(typeof sdk.runtime.capability.call);",
        "console.log(typeof sdk.runtime.service.invoke);",
        "console.log(typeof sdk.invokeService);",
        "console.log(typeof sdk.default.shell);",
        "console.log(Object.prototype.hasOwnProperty.call(sdk, 'a' + 'dp'));",
      ].join("\n"), "utf8");

      const { execFile } = await import("node:child_process");
      const { promisify } = await import("node:util");
      const execFileAsync = promisify(execFile);
      const result = await execFileAsync("node", [script], { cwd: process.cwd() });

      expect(result.stdout).toBe("function\nfunction\nfunction\nfunction\nfunction\nfunction\nfalse\n");
    });
  });

  it("supports ESM named import from built package", async () => {
    await withTempDir(async (root) => {
      await linkPackage(root);
      const script = path.join(root, "import.mjs");
      await fs.writeFile(script, [
        "import runtimeDefault, { runtime } from '@chaitin-ai/agent-compose-runtime-sdk';",
        "import * as sdk from '@chaitin-ai/agent-compose-runtime-sdk';",
        "console.log(typeof runtime.exec);",
        "console.log(typeof runtime.secret.get);",
        "console.log(typeof runtime.capability.call);",
        "console.log(typeof runtime.service.invoke);",
        "console.log(typeof sdk.invokeService);",
        "console.log(typeof runtimeDefault.exec);",
        "console.log(Object.prototype.hasOwnProperty.call(sdk, 'a' + 'dp'));",
      ].join("\n"), "utf8");

      const { execFile } = await import("node:child_process");
      const { promisify } = await import("node:util");
      const execFileAsync = promisify(execFile);
      const result = await execFileAsync("node", [script], { cwd: process.cwd() });

      expect(result.stdout).toBe("function\nfunction\nfunction\nfunction\nfunction\nfunction\nfalse\n");
    });
  });
});

async function linkPackage(root: string): Promise<void> {
  const packageDir = path.join(root, "node_modules", "@chaitin-ai");
  await fs.mkdir(packageDir, { recursive: true });
  await fs.symlink(process.cwd(), path.join(packageDir, "agent-compose-runtime-sdk"));
}

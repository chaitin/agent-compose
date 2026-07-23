import { execFile } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";

import { parseNPMPackEntries } from "./npm-pack-result.mjs";

const execFileAsync = promisify(execFile);
const root = process.cwd();
const tempRoot = await fs.mkdtemp(path.join(os.tmpdir(), "agent-compose-runtime-sdk-packaging-"));

try {
  const dryRun = await execFileAsync("npm", ["pack", "--dry-run", "--json"], { cwd: root });
  const packEntries = parseNPMPackEntries(dryRun.stdout);
  const files = packEntries[0]?.files?.map((entry) => entry.path).sort() ?? [];
  const allowedRootFiles = new Set(["LICENSE", "README.md", "package.json"]);
  const invalid = files.filter((file) => {
    return !allowedRootFiles.has(file) && !file.startsWith("dist/");
  });
  if (invalid.length > 0) {
    throw new Error(`npm pack contains unexpected files: ${invalid.join(", ")}`);
  }
  const publishBundled = packEntries[0]?.bundled ?? [];
  if (publishBundled.includes("zod")) {
    throw new Error("npm publish tarball must not bundle zod");
  }

  const optDir = path.join(tempRoot, "opt", "agent-compose", "npm");
  const workspace = path.join(tempRoot, "workspace");
  const npmCache = path.join(tempRoot, "empty-npm-cache");
  await fs.mkdir(optDir, { recursive: true });
  await fs.mkdir(workspace, { recursive: true });
  await fs.mkdir(npmCache, { recursive: true });

  const runtimeTarball = path.join(optDir, "agent-compose-runtime-sdk.tgz");
  await execFileAsync("npm", ["run", "pack:runtime-bundle", "--", "--pack-destination", optDir, "--filename", "agent-compose-runtime-sdk.tgz"], { cwd: root });
  const tarList = await execFileAsync("tar", ["-tzf", runtimeTarball]);
  const tarEntries = tarList.stdout.split(/\r?\n/);
  if (!tarEntries.includes("package/node_modules/zod/package.json")) {
    throw new Error("runtime bundle tarball must include node_modules/zod for offline guest installs");
  }

  await execFileAsync("npm", ["install", "--offline", "--cache", npmCache, runtimeTarball], { cwd: workspace });
  const requireResult = await execFileAsync("node", ["-e", "const { runtime } = require('@chaitin-ai/agent-compose-runtime-sdk'); console.log(typeof runtime.shell)"], { cwd: workspace });
  if (requireResult.stdout.trim() !== "function") {
    throw new Error(`CommonJS offline install smoke test failed: ${requireResult.stdout}`);
  }
  const importResult = await execFileAsync("node", ["--input-type=module", "-e", "import { runtime } from '@chaitin-ai/agent-compose-runtime-sdk'; console.log(typeof runtime.exec)"], { cwd: workspace });
  if (importResult.stdout.trim() !== "function") {
    throw new Error(`ESM offline install smoke test failed: ${importResult.stdout}`);
  }

  console.log(`verified offline install from ${runtimeTarball}`);
} finally {
  await fs.rm(tempRoot, { recursive: true, force: true });
}

import { execFile } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

import { parseNPMPackEntries } from "./npm-pack-result.mjs";

const execFileAsync = promisify(execFile);
const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(scriptDir, "..");
const args = parseArgs(process.argv.slice(2));
const tempRoot = await fs.mkdtemp(path.join(os.tmpdir(), "agent-compose-runtime-sdk-bundle-"));

try {
  const stage = path.join(tempRoot, "package");
  await copyPackageToStage(root, stage);
  const packageJSONPath = path.join(stage, "package.json");
  const packageJSON = JSON.parse(await fs.readFile(packageJSONPath, "utf8"));
  packageJSON.bundledDependencies = ["zod"];
  await fs.writeFile(packageJSONPath, JSON.stringify(packageJSON, null, 2) + "\n", "utf8");

  await execFileAsync("npm", ["ci"], { cwd: stage });
  const packArgs = ["pack", "--json"];
  if (args.packDestination) {
    await fs.mkdir(args.packDestination, { recursive: true });
    packArgs.push("--pack-destination", args.packDestination);
  }
  const pack = await execFileAsync("npm", packArgs, { cwd: stage });
  const packEntries = parseNPMPackEntries(pack.stdout);
  const packedFilename = packEntries[0]?.filename;
  if (!packedFilename) {
    throw new Error("npm pack did not return a filename");
  }
  const packedPath = path.join(args.packDestination ?? stage, packedFilename);
  if (args.filename) {
    const target = path.join(args.packDestination ?? stage, args.filename);
    await fs.rename(packedPath, target);
    process.stdout.write(target + "\n");
  } else {
    process.stdout.write(packedPath + "\n");
  }
} finally {
  await fs.rm(tempRoot, { recursive: true, force: true });
}

async function copyPackageToStage(source, target) {
  await fs.mkdir(target, { recursive: true });
  for (const entry of [
    "package.json",
    "package-lock.json",
    "README.md",
    "tsconfig.base.json",
    "tsconfig.cjs.json",
    "tsconfig.esm.json",
    "tsconfig.types.json",
    "src",
    "scripts",
  ]) {
    await fs.cp(path.join(source, entry), path.join(target, entry), { recursive: true });
  }
}

function parseArgs(rawArgs) {
  const parsed = {
    packDestination: "",
    filename: "",
  };
  for (let index = 0; index < rawArgs.length; index += 1) {
    const arg = rawArgs[index];
    switch (arg) {
      case "--pack-destination":
        parsed.packDestination = path.resolve(requiredValue(rawArgs, index, arg));
        index += 1;
        break;
      case "--filename":
        parsed.filename = path.basename(requiredValue(rawArgs, index, arg));
        index += 1;
        break;
      default:
        throw new Error(`unknown argument: ${arg}`);
    }
  }
  return parsed;
}

function requiredValue(args, index, name) {
  const value = args[index + 1];
  if (!value || value.startsWith("--")) {
    throw new Error(`${name} requires a value`);
  }
  return value;
}

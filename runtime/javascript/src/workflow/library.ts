import fs from "node:fs/promises";
import path from "node:path";
import { validateWorkflowID } from "./state.js";

export async function resolveNestedWorkflow(
  reference: unknown,
  currentScriptFile: string,
  workspace: string,
  stateRoot: string,
): Promise<{ path: string; source: string }> {
  if (typeof reference === "object" && reference !== null) {
    const object = reference as { script?: unknown; scriptPath?: unknown };
    if (typeof object.script === "string") {
      return { path: `${currentScriptFile}#nested`, source: object.script };
    }
    if (typeof object.scriptPath === "string") {
      return await readNestedPath(object.scriptPath, currentScriptFile, workspace);
    }
  }
  if (typeof reference !== "string" || reference.trim() === "") {
    throw new Error("workflow() requires a name, path, or WorkflowRef");
  }
  if (reference.includes("/") || reference.includes("\\") || reference.endsWith(".js")) {
    return await readNestedPath(reference, currentScriptFile, workspace);
  }
  validateWorkflowID(reference, "workflow name");
  const candidates = [
    path.join(workspace, ".agent-compose", "workflows", `${reference}.js`),
    path.join(stateRoot, "workflows", "library", `${reference}.js`),
  ];
  return await readFirstExisting(candidates, reference);
}

async function readNestedPath(reference: string, currentScriptFile: string, workspace: string) {
  const candidates = path.isAbsolute(reference)
    ? [reference]
    : [path.resolve(path.dirname(currentScriptFile), reference), path.resolve(workspace, reference)];
  return await readFirstExisting([...new Set(candidates)], reference);
}

async function readFirstExisting(candidates: string[], reference: string) {
  for (const candidate of candidates) {
    try {
      return { path: candidate, source: await fs.readFile(candidate, "utf8") };
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") {
        throw error;
      }
    }
  }
  throw new Error(`nested workflow not found: ${reference}`);
}

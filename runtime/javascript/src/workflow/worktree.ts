import { execFile } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

export interface ManagedWorktree {
  path: string;
  head: string;
}

export async function createManagedWorktree(
  workspace: string,
  stateRoot: string,
  runId: string,
  agentId: string,
): Promise<ManagedWorktree> {
  let repositoryRoot: string;
  try {
    repositoryRoot = (await git(["-C", workspace, "rev-parse", "--show-toplevel"])).trim();
  } catch {
    throw new Error("workflow worktree isolation requires a git workspace");
  }
  const target = path.join(stateRoot, "workflows", "worktrees", runId, agentId);
  await fs.mkdir(path.dirname(target), { recursive: true });
  await git(["-C", repositoryRoot, "worktree", "add", "--detach", target, "HEAD"]);
  const head = (await git(["-C", target, "rev-parse", "HEAD"])).trim();
  return { path: target, head };
}

export async function worktreeStatus(worktreePath: string): Promise<string> {
  return (await git(["-C", worktreePath, "status", "--short"])).trimEnd();
}

export async function worktreeHead(worktreePath: string): Promise<string> {
  return (await git(["-C", worktreePath, "rev-parse", "HEAD"])).trim();
}

export async function isLinkedWorktree(worktreePath: string): Promise<boolean> {
  const resolved = path.resolve(worktreePath);
  const gitDir = resolveGitPath(resolved, (await git(["-C", resolved, "rev-parse", "--git-dir"])).trim());
  const commonDir = resolveGitPath(resolved, (await git(["-C", resolved, "rev-parse", "--git-common-dir"])).trim());
  if (gitDir === commonDir) {
    return false;
  }
  const output = await git(["-C", resolved, "worktree", "list", "--porcelain", "-z"]);
  return output.split("\0").some((field) =>
    field.startsWith("worktree ") && path.resolve(field.slice("worktree ".length)) === resolved);
}

function resolveGitPath(worktreePath: string, gitPath: string): string {
  return path.resolve(worktreePath, gitPath);
}

export function isManagedWorktreePath(stateRoot: string, worktreePath: string): boolean {
  const managedRoot = path.resolve(stateRoot, "workflows", "worktrees");
  const relative = path.relative(managedRoot, path.resolve(worktreePath));
  return relative !== "" && !relative.startsWith("..") && !path.isAbsolute(relative);
}

export async function removeManagedWorktree(workspace: string, worktreePath: string): Promise<void> {
  await git(["-C", workspace, "worktree", "remove", worktreePath]);
}

async function git(args: string[]): Promise<string> {
  const result = await execFileAsync("git", args, { encoding: "utf8", maxBuffer: 1024 * 1024 });
  return result.stdout;
}

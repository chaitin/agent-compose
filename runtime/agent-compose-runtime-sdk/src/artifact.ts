import fsp from "node:fs/promises";
import path from "node:path";
import { paths } from "./env.js";

export interface RuntimeArtifactWriteOptions {
  dir?: string;
  contentType?: string;
  metadata?: Record<string, string>;
}

export interface RuntimeArtifactRecord {
  name: string;
  path: string;
  contentType?: string;
  metadata: Record<string, string>;
}

export const artifact = {
  async write(name: string, content: string | Uint8Array, options: RuntimeArtifactWriteOptions = {}): Promise<RuntimeArtifactRecord> {
    const safeName = safeRelativePath(name);
    const dir = options.dir ?? process.env.ARTIFACT_DIR ?? path.join(paths.stateRoot, "artifacts");
    const artifactPath = path.join(dir, safeName);
    await fsp.mkdir(path.dirname(artifactPath), { recursive: true });
    await fsp.writeFile(artifactPath, content);
    return {
      name: safeName,
      path: artifactPath,
      contentType: options.contentType,
      metadata: options.metadata ?? {},
    };
  },

  async read(name: string, options: { dir?: string; encoding?: BufferEncoding } = {}): Promise<string | Buffer> {
    const safeName = safeRelativePath(name);
    const dir = options.dir ?? process.env.ARTIFACT_DIR ?? path.join(paths.stateRoot, "artifacts");
    const artifactPath = path.join(dir, safeName);
    if (options.encoding) {
      return fsp.readFile(artifactPath, options.encoding);
    }
    return fsp.readFile(artifactPath);
  },

  async list(options: { dir?: string } = {}): Promise<string[]> {
    const dir = options.dir ?? process.env.ARTIFACT_DIR ?? path.join(paths.stateRoot, "artifacts");
    return listFiles(dir, dir);
  },
};

async function listFiles(root: string, dir: string): Promise<string[]> {
  let entries;
  try {
    entries = await fsp.readdir(dir, { withFileTypes: true });
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") {
      return [];
    }
    throw error;
  }
  const out: string[] = [];
  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      out.push(...await listFiles(root, fullPath));
    } else if (entry.isFile()) {
      out.push(path.relative(root, fullPath));
    }
  }
  out.sort();
  return out;
}

function safeRelativePath(name: string): string {
  const normalized = path.normalize(name).replace(/^([/\\])+/, "");
  if (!normalized || normalized === "." || normalized === ".." || normalized.startsWith(".." + path.sep)) {
    throw new Error("artifact name must be a relative path inside artifact dir");
  }
  return normalized;
}

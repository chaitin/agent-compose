import fsp from "node:fs/promises";
import path from "node:path";
import { paths } from "./env.js";

export interface RuntimeStateStore {
  get<T = unknown>(key: string): Promise<T | undefined>;
  set<T = unknown>(key: string, value: T): Promise<void>;
  delete(key: string): Promise<void>;
}

export function stateStore(namespace = "default", options: { dir?: string } = {}): RuntimeStateStore {
  const root = path.join(options.dir ?? path.join(paths.stateRoot, "sdk-state"), safeSegment(namespace));
  return {
    async get<T = unknown>(key: string): Promise<T | undefined> {
      const file = pathForKey(root, key);
      try {
        return JSON.parse(await fsp.readFile(file, "utf8")) as T;
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code === "ENOENT") {
          return undefined;
        }
        throw error;
      }
    },

    async set<T = unknown>(key: string, value: T): Promise<void> {
      const file = pathForKey(root, key);
      await fsp.mkdir(path.dirname(file), { recursive: true });
      await fsp.writeFile(file, JSON.stringify(value, null, 2), "utf8");
    },

    async delete(key: string): Promise<void> {
      await fsp.rm(pathForKey(root, key), { force: true });
    },
  };
}

export const state = stateStore();

function pathForKey(root: string, key: string): string {
  return path.join(root, safeSegment(key) + ".json");
}

function safeSegment(value: string): string {
  const trimmed = value.trim();
  if (!trimmed || trimmed.includes("/") || trimmed.includes("\\") || trimmed === "." || trimmed === "..") {
    throw new Error("state key must be a non-empty path segment");
  }
  return trimmed;
}

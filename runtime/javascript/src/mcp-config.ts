import fs from "node:fs/promises";
import path from "node:path";

export type RuntimeMCPEnvVar = {
  value: string;
  secret?: boolean;
};

export type RuntimeMCPServer = {
  type: "local" | "remote";
  transport?: "sse" | "http";
  command?: string;
  args?: string[];
  env?: Record<string, RuntimeMCPEnvVar>;
  url?: string;
  headers?: Record<string, RuntimeMCPEnvVar>;
};

export type RuntimeMCPConfig = {
  mcps?: Record<string, RuntimeMCPServer>;
};

const mcpConfigRelativePath = path.join("agents", "mcp", "config.json");

export function agentMCPConfigPath(stateRoot: string): string {
  return path.join(stateRoot, mcpConfigRelativePath);
}

export async function readMCPConfig(stateRoot: string): Promise<RuntimeMCPConfig> {
  const configPath = agentMCPConfigPath(stateRoot);
  try {
    const raw = await fs.readFile(configPath, "utf-8");
    const parsed = JSON.parse(raw) as RuntimeMCPConfig;
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch (error) {
    if ((error as NodeJS.ErrnoException)?.code === "ENOENT") {
      return {};
    }
    throw error;
  }
}

export function flattenEnvMap(values?: Record<string, RuntimeMCPEnvVar>): Record<string, string> | undefined {
  if (!values || typeof values !== "object") {
    return undefined;
  }
  const entries = Object.entries(values)
    .filter(([key]) => key.trim() !== "")
    .map(([key, value]) => [key, String(value?.value ?? "")]);
  if (entries.length === 0) {
    return undefined;
  }
  return Object.fromEntries(entries);
}

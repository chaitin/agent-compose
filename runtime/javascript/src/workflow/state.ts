import fs from "node:fs/promises";
import path from "node:path";
import type { NestedWorkflowSnapshot, WorkflowAgentRecord, WorkflowRunSnapshot } from "./types.js";

const safeIDPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

export class WorkflowStateStore {
  readonly runRoot: string;
  readonly agentsRoot: string;
  readonly eventsPath: string;

  private constructor(
    readonly stateRoot: string,
    readonly runId: string,
  ) {
    this.runRoot = path.join(stateRoot, "workflows", "runs", runId);
    this.agentsRoot = path.join(this.runRoot, "agents");
    this.eventsPath = path.join(this.runRoot, "events.jsonl");
  }

  static async create(stateRoot: string, runId: string): Promise<WorkflowStateStore> {
    validateWorkflowID(runId, "runId");
    const store = new WorkflowStateStore(path.resolve(stateRoot), runId);
    await fs.mkdir(path.dirname(store.runRoot), { recursive: true });
    try {
      await fs.mkdir(store.runRoot);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === "EEXIST") {
        throw new Error(`workflow run already exists: ${runId}`);
      }
      throw error;
    }
    await fs.mkdir(store.agentsRoot);
    await fs.writeFile(store.eventsPath, "", { encoding: "utf8", mode: 0o600, flag: "wx" });
    return store;
  }

  static open(stateRoot: string, runId: string): WorkflowStateStore {
    validateWorkflowID(runId, "resumeRunId");
    return new WorkflowStateStore(path.resolve(stateRoot), runId);
  }

  agentRoot(agentId: string): string {
    validateWorkflowID(agentId, "agentId");
    return path.join(this.agentsRoot, agentId);
  }

  agentSessionRoot(agentId: string): string {
    return path.join(this.agentRoot(agentId), "state");
  }

  async writeRun(snapshot: WorkflowRunSnapshot): Promise<void> {
    await atomicWriteJSON(path.join(this.runRoot, "run.json"), snapshot);
  }

  async writeAgent(record: WorkflowAgentRecord): Promise<void> {
    const root = this.agentRoot(record.agentId);
    await fs.mkdir(root, { recursive: true });
    await atomicWriteJSON(path.join(root, "record.json"), record);
  }

  async writeNested(snapshot: NestedWorkflowSnapshot): Promise<void> {
    validateWorkflowID(snapshot.nestedId, "nestedId");
    const root = path.join(this.runRoot, "nested");
    await fs.mkdir(root, { recursive: true });
    await atomicWriteJSON(path.join(root, `${snapshot.nestedId}.json`), snapshot);
  }

  async readRun(): Promise<WorkflowRunSnapshot> {
    const snapshot = await readJSON<WorkflowRunSnapshot>(path.join(this.runRoot, "run.json"));
    if (snapshot.schemaVersion !== 1 || snapshot.runId !== this.runId) {
      throw new Error(`workflow run state is incompatible or corrupt: ${this.runId}`);
    }
    return snapshot.status === "running" ? { ...snapshot, status: "interrupted" } : snapshot;
  }

  async readAgents(): Promise<WorkflowAgentRecord[]> {
    let entries: Array<{ name: string; isDirectory(): boolean }>;
    try {
      entries = await fs.readdir(this.agentsRoot, { withFileTypes: true });
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === "ENOENT") {
        return [];
      }
      throw error;
    }
    const records = await Promise.all(entries
      .filter((entry) => entry.isDirectory())
      .map((entry) => readJSON<WorkflowAgentRecord>(path.join(this.agentsRoot, entry.name, "record.json"))));
    const keys = new Set<string>();
    for (const record of records) {
      if (record.schemaVersion !== 1 || !record.invocationKey || keys.has(record.invocationKey)) {
        throw new Error(`workflow resume state contains invalid or duplicate invocationKey: ${record.invocationKey || "<empty>"}`);
      }
      keys.add(record.invocationKey);
      if ((record.status === "done" || record.status === "cached") && !("result" in record)) {
        throw new Error(`workflow resume state has reusable agent without result: ${record.agentId}`);
      }
    }
    return records.map((record) => record.status === "running" ? { ...record, status: "interrupted" } : record);
  }
}

export function validateWorkflowID(value: string, field: string): void {
  if (!safeIDPattern.test(value) || value === "." || value === "..") {
    throw new Error(`${field} must match ${safeIDPattern.source}`);
  }
}

async function atomicWriteJSON(target: string, value: unknown): Promise<void> {
  const temp = path.join(path.dirname(target), `.${path.basename(target)}.${process.pid}.${Date.now()}.tmp`);
  await fs.writeFile(temp, `${JSON.stringify(value, null, 2)}\n`, { encoding: "utf8", mode: 0o600, flag: "wx" });
  try {
    await fs.rename(temp, target);
  } catch (error) {
    await fs.rm(temp, { force: true });
    throw error;
  }
}

async function readJSON<T>(target: string): Promise<T> {
  return JSON.parse(await fs.readFile(target, "utf8")) as T;
}

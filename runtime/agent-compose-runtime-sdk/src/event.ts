import process from "node:process";

export interface RuntimeEventRecord {
  topic: string;
  payload: unknown;
  metadata: Record<string, string>;
  createdAt: string;
}

export const event = {
  publish(topic: string, payload: unknown, metadata: Record<string, string> = {}): RuntimeEventRecord {
    const normalizedTopic = topic.trim();
    if (!normalizedTopic) {
      throw new Error("event topic is required");
    }
    const record: RuntimeEventRecord = {
      topic: normalizedTopic,
      payload,
      metadata,
      createdAt: new Date().toISOString(),
    };
    process.stdout.write(JSON.stringify({ type: "agent-compose.runtime.event", ...record }) + "\n");
    return record;
  },
};

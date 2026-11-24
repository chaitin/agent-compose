export const FRAME_VERSION = 1;

export type InputFrameType = "start" | "human_message" | "cancel" | "eof" | "command";
export type OutputFrameType = "started" | "agent_event" | "agent_turn_completed" | "stdout" | "stderr" | "output" | "result" | "error" | "command";

export interface StreamFrame {
  v: number;
  seq: number;
  type: string;
  [key: string]: unknown;
}

export interface BinaryField {
  encoding: "base64";
  data: string;
}

export class FrameCodecError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "FrameCodecError";
  }
}

export function encodeBinary(bytes: Uint8Array): BinaryField {
  return {
    encoding: "base64",
    data: Buffer.from(bytes).toString("base64"),
  };
}

export function decodeBinary(field: unknown): Uint8Array {
  if (!isRecord(field) || field.encoding !== "base64" || typeof field.data !== "string") {
    throw new FrameCodecError("binary field must use base64 encoding");
  }
  return Uint8Array.from(Buffer.from(field.data, "base64"));
}

export function encodeFrame(frame: StreamFrame): string {
  validateFrame(frame);
  return `${JSON.stringify(frame)}\n`;
}

/** Each runtime invocation owns its frame sequence, independent of transport. */
export function createFrameEmitter(write: (line: string) => void) {
  let seq = 0;
  return (type: string, fields: object = {}) => {
    write(encodeFrame({ ...fields, v: FRAME_VERSION, seq: seq++, type }));
  };
}

export function decodeFrame(line: string): StreamFrame {
  let parsed: unknown;
  try {
    parsed = JSON.parse(line);
  } catch (error) {
    throw new FrameCodecError("frame must be valid JSON");
  }
  validateFrame(parsed);
  return parsed;
}

function validateFrame(frame: unknown): asserts frame is StreamFrame {
  if (!isRecord(frame)) {
    throw new FrameCodecError("frame must be a JSON object");
  }
  if (frame.v !== FRAME_VERSION) {
    throw new FrameCodecError(`frame version must be ${FRAME_VERSION}`);
  }
  if (!Number.isInteger(frame.seq) || (frame.seq as number) < 0) {
    throw new FrameCodecError("frame seq must be a non-negative integer");
  }
  if (typeof frame.type !== "string" || frame.type.length === 0) {
    throw new FrameCodecError("frame type must be a non-empty string");
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

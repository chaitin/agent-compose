import { createInterface } from "node:readline/promises";
import type { Readable, Writable } from "node:stream";
import { runRuntimeCommand, type RuntimeCommandRequest } from "./command.js";
import { decodeFrame, encodeFrame, FRAME_VERSION, type StreamFrame } from "./frame.js";
import { createInteractiveSession, UnsupportedProviderError, type InteractiveSession } from "./interactive.js";

export interface RunStreamOptions {
  stdin?: Readable;
  stdout?: Writable;
  stderr?: Writable;
}

export async function runStreamCommand(options: RunStreamOptions = {}): Promise<void> {
  const stdin = options.stdin || process.stdin;
  const stdout = options.stdout || process.stdout;
  const stderr = options.stderr || process.stderr;
  let outputSeq = 0;
  let session: InteractiveSession | undefined;
  let finished = false;

  const emit = (type: string, fields: object = {}) => {
    stdout.write(encodeFrame({ v: FRAME_VERSION, seq: outputSeq++, type, ...fields }));
  };
  const emitError = (error: unknown, inputSeq?: number) => {
    emit("error", structuredError(error, inputSeq));
  };

  const lines = createInterface({ input: stdin, crlfDelay: Infinity });
  for await (const line of lines) {
    if (!line.trim()) {
      continue;
    }
    let frame: StreamFrame;
    try {
      frame = decodeFrame(line);
    } catch (error) {
      emitError(error);
      continue;
    }

    try {
      switch (frame.type) {
        case "start":
          if (session) {
            throw new Error("stream has already been started");
          }
          if (frame.mode === "command") {
            emit("started", { mode: "command" });
            emit("result", await runCommandFrame(frame, emit));
            finished = true;
            lines.close();
            break;
          }
          session = await createInteractiveSession({
            provider: stringField(frame, "provider"),
            stateRoot: stringField(frame, "stateRoot"),
            workspace: stringField(frame, "workspace"),
            home: stringField(frame, "home"),
            model: stringField(frame, "model"),
            outputSchemaFile: stringField(frame, "outputSchemaFile"),
          }, emit);
          break;
        case "human_message":
          if (!session) {
            throw new Error("stream has not been started");
          }
          await session.runHumanMessage(messageText(frame));
          break;
        case "command":
          if (session) {
            throw new Error("command frames are not supported after interactive start");
          }
          emit("started", { mode: "command" });
          emit("result", await runCommandFrame(frame, emit));
          finished = true;
          lines.close();
          break;
        case "cancel":
        case "eof": {
          if (!session) {
            emit("result", { stopReason: frame.type === "cancel" ? "cancelled" : "eof" });
          } else {
            emit("result", await session.finish(frame.type === "cancel" ? "cancelled" : "eof"));
          }
          finished = true;
          lines.close();
          break;
        }
        default:
          throw new Error(`unsupported input frame type ${frame.type}`);
      }
    } catch (error) {
      emitError(error, frame.seq);
      if (error instanceof UnsupportedProviderError) {
        finished = true;
        lines.close();
      }
    }
  }

  if (!finished && session) {
    try {
      emit("result", await session.finish("eof"));
    } catch (error) {
      stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
      emitError(error);
    }
  }
}

async function runCommandFrame(
  frame: StreamFrame,
  emit: (type: string, fields?: object) => void,
) {
  const request = commandRequest(frame);
  return runRuntimeCommand({
    request,
    artifactDir: request.artifactDir || stringField(frame, "artifactDir"),
    stateRoot: stringField(frame, "stateRoot"),
    workspace: stringField(frame, "workspace"),
    home: stringField(frame, "home"),
    onStdout(chunk) {
      emitTextFrame(emit, "stdout", chunk);
      emitOutputFrame(emit, "stdout", chunk);
    },
    onStderr(chunk) {
      emitTextFrame(emit, "stderr", chunk);
      emitOutputFrame(emit, "stderr", chunk);
    },
  });
}

function commandRequest(frame: StreamFrame): RuntimeCommandRequest {
  if (isRecord(frame.request)) {
    return frame.request as unknown as RuntimeCommandRequest;
  }
  throw new Error(`${frame.type} frame in command mode requires request object`);
}

function emitTextFrame(
  emit: (type: string, fields?: object) => void,
  type: "stdout" | "stderr",
  chunk: Buffer,
) {
  emit(type, { text: chunk.toString("utf8") });
}

function emitOutputFrame(
  emit: (type: string, fields?: object) => void,
  source: "stdout" | "stderr",
  chunk: Buffer,
) {
  emit("output", { source, text: chunk.toString("utf8") });
}

function stringField(frame: StreamFrame, field: string): string | undefined {
  const value = frame[field];
  return typeof value === "string" ? value : undefined;
}

function messageText(frame: StreamFrame): string {
  if (typeof frame.message === "string") {
    return frame.message;
  }
  if (typeof frame.text === "string") {
    return frame.text;
  }
  throw new Error("human_message frame requires message");
}

function structuredError(error: unknown, inputSeq?: number): Record<string, unknown> {
  const record: Record<string, unknown> = {
    code: "runtime_stream_error",
    message: error instanceof Error ? error.message : String(error),
  };
  if (inputSeq !== undefined) {
    record.inputSeq = inputSeq;
  }
  if (error instanceof UnsupportedProviderError) {
    record.code = error.code;
    record.provider = error.provider;
  }
  return record;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

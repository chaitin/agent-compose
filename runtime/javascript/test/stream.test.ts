import fs from "node:fs/promises";
import path from "node:path";
import { Readable, Writable } from "node:stream";
import { afterEach, describe, expect, it, vi } from "vitest";
import { decodeBinary, decodeFrame, encodeBinary, encodeFrame, FRAME_VERSION, type StreamFrame } from "../src/frame.js";
import { runStreamCommand } from "../src/stream.js";
import { withTempSession } from "./helpers.js";

const runInputs: string[] = [];
const thread = {
  id: "thread-1",
  async runStreamed(input: string) {
    runInputs.push(input);
    return {
      events: asyncGenerator([
        { type: "thread.started", thread_id: "thread-1" },
        { type: "item.completed", item: { id: `msg-${runInputs.length}`, type: "agent_message", text: `answer ${runInputs.length}` } },
      ]),
    };
  },
};

const startThread = vi.fn(() => thread);
const resumeThread = vi.fn(() => thread);

const claudeState = vi.hoisted(() => ({
  queryCalls: [] as Array<Record<string, unknown>>,
}));

vi.mock("@openai/codex-sdk", () => ({
  Codex: vi.fn(function CodexMock(this: object) {
    Object.assign(this, {
      startThread,
      resumeThread,
    });
  }),
}));

vi.mock("@anthropic-ai/claude-agent-sdk", () => ({
  query: vi.fn((request: Record<string, unknown>) => {
    claudeState.queryCalls.push(request);
    const turn = claudeState.queryCalls.length;
    return {
      close: vi.fn(),
      [Symbol.asyncIterator]: async function* iterator() {
        yield {
          type: "stream_event",
          session_id: "claude-session-1",
          event: {
            type: "content_block_delta",
            delta: { type: "text_delta", text: `claude answer ${turn}` },
          },
        };
        yield {
          type: "result",
          subtype: "success",
          session_id: "claude-session-1",
          stop_reason: "end_turn",
          result: `claude answer ${turn}`,
        };
      },
    };
  }),
}));

afterEach(() => {
  runInputs.length = 0;
  startThread.mockClear();
  resumeThread.mockClear();
  claudeState.queryCalls = [];
  vi.restoreAllMocks();
});

describe("runtime stream frames", () => {
  it("encodes and decodes newline-delimited frames", () => {
    const encoded = encodeFrame({ v: FRAME_VERSION, seq: 7, type: "human_message", message: "hello" });

    expect(encoded.endsWith("\n")).toBe(true);
    expect(decodeFrame(encoded)).toMatchObject({
      v: FRAME_VERSION,
      seq: 7,
      type: "human_message",
      message: "hello",
    });
  });

  it("encodes binary fields as base64", () => {
    const encoded = encodeBinary(Buffer.from("abc"));

    expect(encoded).toEqual({ encoding: "base64", data: "YWJj" });
    expect(Buffer.from(decodeBinary(encoded)).toString("utf8")).toBe("abc");
  });

  it("rejects malformed frames", () => {
    expect(() => decodeFrame("not json")).toThrow(/valid JSON/);
    expect(() => encodeFrame({ v: 2, seq: 0, type: "start" })).toThrow(/version/);
    expect(() => encodeFrame({ v: FRAME_VERSION, seq: -1, type: "start" })).toThrow(/seq/);
  });
});

describe("runStreamCommand", () => {
  it("runs multiple Codex human messages on the same thread and writes only NDJSON frames to stdout", async () => {
    await withTempSession(async (root) => {
      const stdout = new MemoryWritable();
      const stderr = new MemoryWritable();

      await runStreamCommand({
        stdin: Readable.from([
          frame({ seq: 0, type: "start", provider: "codex", stateRoot: `${root}/state`, workspace: `${root}/workspace`, home: `${root}/home` }),
          frame({ seq: 1, type: "human_message", message: "first" }),
          frame({ seq: 2, type: "human_message", message: "second" }),
          frame({ seq: 3, type: "eof" }),
        ]),
        stdout,
        stderr,
      });

      const frames = parseOutput(stdout.text);
      expect(frames.map((entry) => entry.type)).toEqual([
        "started",
        "agent_event",
        "agent_event",
        "agent_turn_completed",
        "agent_event",
        "agent_event",
        "agent_turn_completed",
        "result",
      ]);
      expect(frames.every((entry, index) => entry.seq === index)).toBe(true);
      expect(runInputs).toEqual(["first", "second"]);
      expect(startThread).toHaveBeenCalledTimes(1);
      expect(resumeThread).not.toHaveBeenCalled();
      expect(frames.at(-1)).toMatchObject({
        type: "result",
        provider: "codex",
        threadId: "thread-1",
        stopReason: "eof",
        finalText: "answer 2",
        transcript: "answer 1\nanswer 2",
      });
      expect(parseOutput(stdout.text)).toHaveLength(8);
    });
  });

  it("runs multiple Claude human messages by resuming the stored provider session", async () => {
    await withTempSession(async (root) => {
      const stdout = new MemoryWritable();
      const stderr = new MemoryWritable();

      await runStreamCommand({
        stdin: Readable.from([
          frame({ seq: 0, type: "start", provider: "claude", stateRoot: `${root}/state`, workspace: `${root}/workspace`, home: `${root}/home` }),
          frame({ seq: 1, type: "human_message", message: "first" }),
          frame({ seq: 2, type: "human_message", message: "second" }),
          frame({ seq: 3, type: "eof" }),
        ]),
        stdout,
        stderr,
      });

      const frames = parseOutput(stdout.text);
      expect(frames.map((entry) => entry.type)).toEqual([
        "started",
        "agent_event",
        "agent_turn_completed",
        "agent_event",
        "agent_turn_completed",
        "result",
      ]);
      expect(frames.every((entry, index) => entry.seq === index)).toBe(true);
      expect(frames.filter((entry) => entry.type === "agent_event")).toEqual([
        expect.objectContaining({ event: expect.objectContaining({ provider: "claude", text: "claude answer 1" }) }),
        expect.objectContaining({ event: expect.objectContaining({ provider: "claude", text: "claude answer 2" }) }),
      ]);
      expect(claudeState.queryCalls.map((call) => call.prompt)).toEqual(["first", "second"]);
      expect(claudeState.queryCalls[0]?.options).not.toMatchObject({ resume: expect.anything() });
      expect(claudeState.queryCalls[1]?.options).toMatchObject({ resume: "claude-session-1" });
      expect(frames.at(-1)).toMatchObject({
        type: "result",
        provider: "claude",
        threadId: "claude-session-1",
        stopReason: "eof",
        finalText: "claude answer 2",
        transcript: "claude answer 1\nclaude answer 2",
      });
      expect(stderr.text).toBe("");
    });
  });

  it("emits a structured error for unsupported interactive providers", async () => {
    await withTempSession(async (root) => {
      const stdout = new MemoryWritable();

      await runStreamCommand({
        stdin: Readable.from([
          frame({ seq: 0, type: "start", provider: "opencode", stateRoot: `${root}/state` }),
        ]),
        stdout,
      });

      expect(parseOutput(stdout.text)).toEqual([
        {
          v: FRAME_VERSION,
          seq: 0,
          type: "error",
          code: "unsupported_provider",
          message: "interactive stream is not supported for provider opencode",
          inputSeq: 0,
          provider: "opencode",
        },
      ]);
    });
  });

  it("emits parser errors without writing diagnostics to stdout", async () => {
    const stdout = new MemoryWritable();
    const stderr = new MemoryWritable();

    await runStreamCommand({
      stdin: Readable.from(["{}\n"]),
      stdout,
      stderr,
    });

    expect(parseOutput(stdout.text)).toEqual([
      {
        v: FRAME_VERSION,
        seq: 0,
        type: "error",
        code: "runtime_stream_error",
        message: `frame version must be ${FRAME_VERSION}`,
      },
    ]);
    expect(stderr.text).toBe("");
  });

  it("runs a non-TTY command from a start command-mode request and emits NDJSON stream frames", async () => {
    await withTempSession(async (root) => {
      const stdout = new MemoryWritable();
      const stderr = new MemoryWritable();
      const artifactDir = path.join(root, "artifacts");

      await runStreamCommand({
        stdin: Readable.from([
          frame({
            seq: 0,
            type: "start",
            mode: "command",
            workspace: root,
            request: {
              mode: "exec",
              command: "node",
              args: ["-e", "process.stdout.write('out-1\\n'); process.stderr.write('err-1\\n')"],
              artifactDir,
            },
          }),
        ]),
        stdout,
        stderr,
      });

      const frames = parseOutput(stdout.text);
      expect(frames.map((entry) => entry.type)).toEqual([
        "started",
        "stdout",
        "output",
        "stderr",
        "output",
        "result",
      ]);
      expect(frames.every((entry, index) => entry.seq === index)).toBe(true);
      expect(frames.find((entry) => entry.type === "stdout")).toMatchObject({ text: "out-1\n" });
      expect(frames.find((entry) => entry.type === "stderr")).toMatchObject({ text: "err-1\n" });
      expect(frames.filter((entry) => entry.type === "output")).toEqual([
        expect.objectContaining({ source: "stdout", text: "out-1\n" }),
        expect.objectContaining({ source: "stderr", text: "err-1\n" }),
      ]);
      expect(stderr.text).toBe("");

      const result = frames.at(-1);
      expect(result).toMatchObject({
        type: "result",
        stdout: "out-1\n",
        stderr: "err-1\n",
        output: expect.stringContaining("out-1\n"),
        exitCode: 0,
        success: true,
      });
      expect(result?.output).toContain("err-1\n");

      expect(await fs.readFile(path.join(artifactDir, "stdout.txt"), "utf8")).toBe("out-1\n");
      expect(await fs.readFile(path.join(artifactDir, "stderr.txt"), "utf8")).toBe("err-1\n");
      expect(await fs.readFile(path.join(artifactDir, "output.txt"), "utf8")).toContain("out-1\n");
      expect(await fs.readFile(path.join(artifactDir, "output.txt"), "utf8")).toContain("err-1\n");
      const savedRequest = JSON.parse(await fs.readFile(path.join(artifactDir, "command-request.json"), "utf8"));
      const savedResult = JSON.parse(await fs.readFile(path.join(artifactDir, "command-result.json"), "utf8"));
      expect(savedRequest).toMatchObject({ mode: "exec", command: "node", cwd: root });
      expect(savedResult).toMatchObject({
        stdout: "out-1\n",
        stderr: "err-1\n",
        exitCode: 0,
        success: true,
      });
      expect(result).toMatchObject(savedResult);
    });
  });
});

function frame(fields: Omit<StreamFrame, "v">): string {
  return encodeFrame({ v: FRAME_VERSION, ...fields });
}

function parseOutput(output: string): StreamFrame[] {
  return output.trimEnd().split("\n").filter(Boolean).map((line) => JSON.parse(line) as StreamFrame);
}

async function* asyncGenerator(events: unknown[]): AsyncIterable<unknown> {
  for (const event of events) {
    yield event;
  }
}

class MemoryWritable extends Writable {
  private readonly chunks: Buffer[] = [];

  override _write(chunk: Buffer | string, _encoding: BufferEncoding, callback: (error?: Error | null) => void): void {
    this.chunks.push(Buffer.from(chunk));
    callback();
  }

  get text(): string {
    return Buffer.concat(this.chunks).toString("utf8");
  }
}

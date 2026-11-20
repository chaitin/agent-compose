import { describe, expect, it } from "vitest";
import { appendDelta, TranscriptWriter } from "../src/transcript.js";
import { extractText, jsonString, normalizeNewlines } from "../src/text.js";
import { captureStdio } from "./helpers.js";

describe("transcript helpers", () => {
  it("normalizes CRLF text and records stderr transcript", () => {
    const stdio = captureStdio();
    try {
      const writer = new TranscriptWriter();
      writer.write("a\r\n");
      writer.line("b");

      expect(writer.transcript()).toBe("a\nb");
      expect(stdio.stderr).toBe("a\nb\n");
    } finally {
      stdio.restore();
    }
  });

  it("keeps the transcript without mirroring it when mirror is off", () => {
    const stdio = captureStdio();
    try {
      const writer = new TranscriptWriter({ mirror: false });
      writer.write("quiet");
      expect(writer.transcript()).toBe("quiet");
      expect(stdio.stderr).toBe("");
    } finally {
      stdio.restore();
    }
  });

  it("appends only new deltas for repeated item text", () => {
    const writes: string[] = [];
    const writer = { write: (text: string) => writes.push(text), line: (text = "") => writes.push(`${text}\n`) };
    const cache = new Map<string, string>();

    appendDelta(writer, cache, "item", "hello");
    appendDelta(writer, cache, "item", "hello world");
    appendDelta(writer, cache, "item", "hello world");
    appendDelta(writer, cache, "item", "replacement");

    expect(writes).toEqual(["hello", " world", "replacement"]);
  });

  it("extracts text from provider-shaped payloads", () => {
    expect(extractText("x")).toBe("x");
    expect(extractText([{ text: "a" }, { content: [{ text: "b" }] }])).toBe("ab");
    expect(extractText({ response: "r" })).toBe("r");
    expect(extractText({ result: "done" })).toBe("done");
    expect(extractText({ nope: true })).toBe("");
  });

  it("normalizes newlines and stringifies json", () => {
    expect(normalizeNewlines("a\r\nb")).toBe("a\nb");
    expect(jsonString({ ok: true })).toContain("\"ok\": true");
  });
});

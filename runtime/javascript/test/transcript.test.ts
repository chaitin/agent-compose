import { describe, expect, it } from "vitest";
import { appendDelta, lineDiagnostic, TranscriptWriter, writeDiagnostic } from "../src/transcript.js";
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

  it("sends diagnostics to stderr even when the transcript is not mirrored", () => {
    const stdio = captureStdio();
    try {
      const writer = new TranscriptWriter({ mirror: false });
      writer.write("answer");
      lineDiagnostic(writer, "auth failed");
      writeDiagnostic(writer, "provider stderr\n");
      expect(stdio.stderr).toBe("auth failed\nprovider stderr\n");
      expect(writer.transcript()).toBe("answerauth failed\nprovider stderr");
    } finally {
      stdio.restore();
    }
  });

  it("records diagnostics as text on writers without a diagnostic channel", () => {
    const writes: string[] = [];
    writeDiagnostic({ write: (text: string) => writes.push(text), line: () => {} }, "note");
    expect(writes).toEqual(["note"]);
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

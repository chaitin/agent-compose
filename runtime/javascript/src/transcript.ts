import process from "node:process";
import { normalizeNewlines } from "./text.js";

export interface TextWriter {
  write(text: string): void;
  line(text?: string): void;
  /**
   * Records provider diagnostics that have no agent event of their own, such
   * as a provider's stderr or an authentication status. They belong to the
   * transcript like any text, but must reach stderr even when the transcript
   * is not mirrored there. Writers without it record them as text.
   */
  diagnostic?(text: string): void;
}

/** Writes a provider diagnostic; see TextWriter.diagnostic. */
export function writeDiagnostic(writer: TextWriter, text: string): void {
  if (writer.diagnostic) {
    writer.diagnostic(text);
  } else {
    writer.write(text);
  }
}

/** writeDiagnostic, ending the text with a newline as TextWriter.line does. */
export function lineDiagnostic(writer: TextWriter, text = ""): void {
  writeDiagnostic(writer, text.endsWith("\n") ? text : `${text}\n`);
}

export interface TranscriptTextWriter extends TextWriter {
  transcript(): string;
}

export function appendDelta(
  writer: TextWriter,
  cache: Map<string, string>,
  key: string,
  nextText: string,
): void {
  const previous = cache.get(key) || "";
  if (nextText === previous) {
    return;
  }
  let delta = nextText;
  if (typeof nextText === "string" && nextText.startsWith(previous)) {
    delta = nextText.slice(previous.length);
  }
  cache.set(key, nextText);
  if (delta) {
    writer.write(delta);
  }
}

export class TranscriptWriter implements TranscriptTextWriter {
  private readonly chunks: string[] = [];

  /**
   * `mirror` copies the transcript to stderr as it is written. It is turned
   * off when the caller consumes structured agent events instead, so the
   * same answer is not delivered twice.
   */
  constructor(private readonly options: { mirror?: boolean } = {}) {}

  write(text: string): void {
    this.record(text, this.options.mirror !== false);
  }

  diagnostic(text: string): void {
    this.record(text, true);
  }

  private record(text: string, mirror: boolean): void {
    if (!text) {
      return;
    }
    const normalized = normalizeNewlines(text);
    this.chunks.push(normalized);
    if (mirror) {
      process.stderr.write(normalized);
    }
  }

  line(text = ""): void {
    this.write(text.endsWith("\n") ? text : `${text}\n`);
  }

  transcript(): string {
    return this.chunks.join("").trimEnd();
  }
}

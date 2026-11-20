import process from "node:process";
import { normalizeNewlines } from "./text.js";

export interface TextWriter {
  write(text: string): void;
  line(text?: string): void;
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
   * off when the daemon consumes structured agent events instead, so the
   * same answer does not reach the run log twice.
   */
  constructor(private readonly options: { mirror?: boolean } = {}) {}

  write(text: string): void {
    if (!text) {
      return;
    }
    const normalized = normalizeNewlines(text);
    this.chunks.push(normalized);
    if (this.options.mirror !== false) {
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

import process from "node:process";
import { normalizeNewlines } from "./text.js";

export interface TextWriter {
  write(text: string): void;
  line(text?: string): void;
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

/**
 * A TextWriter that accumulates text and optionally streams it to a sink.
 *
 * By default, the sink writes to process.stderr (the original behavior).
 * Pass a custom sink to redirect output, or `() => {}` to suppress streaming
 * while still accumulating for transcript retrieval.
 */
export class TranscriptWriter implements TextWriter {
  private readonly chunks: string[] = [];
  private readonly sink: (text: string) => void;

  constructor(sink?: (text: string) => void) {
    this.sink = sink ?? ((text: string) => process.stderr.write(text));
  }

  write(text: string): void {
    if (!text) {
      return;
    }
    const normalized = normalizeNewlines(text);
    this.chunks.push(normalized);
    this.sink(normalized);
  }

  line(text = ""): void {
    this.write(text.endsWith("\n") ? text : `${text}\n`);
  }

  transcript(): string {
    return this.chunks.join("").trimEnd();
  }
}

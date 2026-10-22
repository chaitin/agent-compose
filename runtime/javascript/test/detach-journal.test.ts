import fs from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { JournalWriter, journalRecordsBeyond, parseJournalLine, readJournalFrom } from "../src/detach/journal.js";
import { withTempSession } from "./helpers.js";

async function readRaw(file: string): Promise<string> {
  try {
    return await fs.readFile(file, "utf8");
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") {
      return "";
    }
    throw error;
  }
}

describe("JournalWriter", () => {
  it("appends output records with monotonic byte offsets", async () => {
    await withTempSession(async (root) => {
      const file = path.join(root, "journal");
      const writer = new JournalWriter(file, 1 << 20);
      await writer.append("stdout", "hello\n");
      await writer.append("stderr", "warn\n");
      await writer.append("stdout", "world");
      await writer.flush();

      const records = journalRecordsBeyond(await readRaw(file), 0);
      expect(records).toEqual([
        { kind: "out", offset: 0, stream: "stdout", text: "hello\n" },
        { kind: "out", offset: 6, stream: "stderr", text: "warn\n" },
        { kind: "out", offset: 11, stream: "stdout", text: "world" },
      ]);
    });
  });

  it("reports the next offset without appending", async () => {
    await withTempSession(async (root) => {
      const file = path.join(root, "journal");
      const writer = new JournalWriter(file, 1 << 20);
      expect(writer.nextOffset()).toBe(0);
      await writer.append("stdout", "ab");
      expect(writer.nextOffset()).toBe(2);
    });
  });

  it("drops oldest records and records a gap when over the cap", async () => {
    await withTempSession(async (root) => {
      const file = path.join(root, "journal");
      // Cap small enough that several records trigger compaction.
      const writer = new JournalWriter(file, 200);
      await writer.append("stdout", "first\n");
      await writer.append("stdout", "second\n");
      await writer.append("stdout", "third\n");
      await writer.append("stdout", "fourth\n");
      await writer.flush();

      const raw = await readRaw(file);
      const records = journalRecordsBeyond(raw, 0);
      const gap = records.find((r) => r.kind === "gap");
      expect(gap).toBeDefined();
      // The tail must still be readable from its original offset, and the gap
      // must cover exactly the dropped byte range [0, firstKeptOffset).
      const outs = records.filter((r) => r.kind === "out");
      expect(outs.length).toBeGreaterThan(0);
      const firstOut = outs[0];
      if (firstOut.kind === "out") {
        expect(gap).toMatchObject({ kind: "gap", from: 0, to: firstOut.offset });
      }
    });
  });

  it("readJournalFrom skips records fully before the cursor and trims partial overlap", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const executionId = "ex1";
      const writer = new JournalWriter(path.join(stateRoot, "executions", executionId, "journal"), 1 << 20);
      await writer.append("stdout", "0123456789");
      await writer.append("stderr", "ABCDE");
      await writer.flush();

      const from5 = await readJournalFrom(stateRoot, executionId, 5);
      expect(from5).toEqual([
        { kind: "out", offset: 5, stream: "stdout", text: "56789" },
        { kind: "out", offset: 10, stream: "stderr", text: "ABCDE" },
      ]);

      const fromEnd = await readJournalFrom(stateRoot, executionId, 15);
      expect(fromEnd).toEqual([]);
    });
  });

  it("parseJournalLine returns null for malformed lines", () => {
    expect(parseJournalLine("not json")).toBeNull();
    expect(parseJournalLine('{"kind":"out","offset":0,"stream":"bogus","text":"x"}')).toBeNull();
    expect(parseJournalLine('{"kind":"unknown"}')).toBeNull();
    expect(parseJournalLine('{"kind":"gap","from":0,"to":5}')).toEqual({ kind: "gap", from: 0, to: 5 });
  });

  it("trims an out record at a byte boundary inside a multi-byte text", () => {
    // "中文\n" is 7 UTF-8 bytes (中 = 3, 文 = 3, \n = 1). A cursor at byte 3 must
    // resume at 文, not slice by UTF-16 code units (which would return "").
    const raw = `${JSON.stringify({ kind: "out", offset: 0, stream: "stdout", text: "中文\n" })}\n`;
    expect(journalRecordsBeyond(raw, 3)).toEqual([
      { kind: "out", offset: 3, stream: "stdout", text: "文\n" },
    ]);
  });

  it("compaction keeps the gap aligned with kept records when a torn line is present", async () => {
    await withTempSession(async (root) => {
      const file = path.join(root, "journal");
      await fs.mkdir(path.dirname(file), { recursive: true });
      // A torn line (invalid JSON) at the front, as a crash mid-append would
      // leave. Compaction must skip it without desynchronizing the gap from the
      // kept records (which would otherwise overlap).
      await fs.appendFile(file, "garbage\n");

      const writer = new JournalWriter(file, 220);
      await writer.append("stdout", "first\n");
      await writer.append("stdout", "second\n");
      await writer.append("stdout", "third\n");
      await writer.append("stdout", "fourth\n");
      await writer.append("stdout", "fifth\n");
      await writer.flush();

      const raw = await readRaw(file);
      const records = journalRecordsBeyond(raw, 0);
      const gap = records.find((r) => r.kind === "gap");
      const outs = records.filter((r): r is { kind: "out"; offset: number } => r.kind === "out");
      expect(outs.length).toBeGreaterThan(0);
      if (gap && gap.kind === "gap") {
        expect(Number.isFinite(gap.from)).toBe(true);
        for (const out of outs) {
          expect(out.offset).toBeGreaterThanOrEqual(gap.to);
        }
      }
    });
  });
});

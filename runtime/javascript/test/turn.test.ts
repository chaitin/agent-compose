import { createHash } from "node:crypto";
import { mkdir, mkdtemp, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { mediaParts, parseTurnManifest, renderTurnText, textTurn, verifyTurnMedia } from "../src/turn.js";
import type { MediaPart, TurnContent } from "../src/turn.js";

const image: MediaPart = {
  type: "media",
  mediaType: "image/png",
  filename: "before.png",
  path: "/sandbox/state/agents/attachments/run-1/turn-0/part-1",
};

const pdf: MediaPart = {
  type: "media",
  mediaType: "application/pdf",
  path: "/sandbox/workspace/reports/q3.pdf",
  workspacePath: "reports/q3.pdf",
};

describe("parseTurnManifest", () => {
  it("parses the ordered parts the daemon emits", () => {
    const turn = parseTurnManifest(JSON.stringify({
      version: 1,
      parts: [
        { text: "compare these" },
        {
          media: {
            mediaType: "image/png",
            filename: "before.png",
            path: image.path,
            sizeBytes: 184320,
            sha256: "a".repeat(64),
          },
        },
        { text: "and these" },
        { media: { mediaType: "application/pdf", path: pdf.path, workspacePath: pdf.workspacePath } },
      ],
    }));

    expect(turn).toEqual({
      parts: [
        { type: "text", text: "compare these" },
        {
          type: "media",
          mediaType: "image/png",
          filename: "before.png",
          path: image.path,
          sizeBytes: 184320,
          sha256: "a".repeat(64),
        },
        { type: "text", text: "and these" },
        { type: "media", mediaType: "application/pdf", path: pdf.path, workspacePath: pdf.workspacePath },
      ],
    });
  });

  it("omits optional media fields that the manifest did not set", () => {
    const turn = parseTurnManifest(JSON.stringify({
      version: 1,
      parts: [{ media: { mediaType: "image/gif", path: "/sandbox/tmp/a.gif" } }],
    }));

    expect(turn.parts).toEqual([{ type: "media", mediaType: "image/gif", path: "/sandbox/tmp/a.gif" }]);
  });

  it.each([
    ["invalid JSON", "{", /must contain valid JSON/],
    ["a JSON array", "[]", /must contain a JSON object/],
    ["a missing version", JSON.stringify({ parts: [{ text: "hi" }] }), /manifest version must be 1/],
    ["an unsupported version", JSON.stringify({ version: 2, parts: [{ text: "hi" }] }), /manifest version must be 1/],
    ["missing parts", JSON.stringify({ version: 1 }), /parts must be an array/],
    ["no parts", JSON.stringify({ version: 1, parts: [] }), /at least one part/],
    ["a non-object part", JSON.stringify({ version: 1, parts: ["text"] }), /part 0 must be an object/],
    ["a part with both text and media", JSON.stringify({
      version: 1,
      parts: [{ text: "hi", media: { mediaType: "image/png", path: "/sandbox/a.png" } }],
    }), /must not set both text and media/],
    ["a non-string text", JSON.stringify({ version: 1, parts: [{ text: 1 }] }), /text must be a string/],
    ["an empty part", JSON.stringify({ version: 1, parts: [{}] }), /must set text or media/],
    ["a non-object media", JSON.stringify({ version: 1, parts: [{ media: "image/png" }] }), /media must be an object/],
    ["a missing media type", JSON.stringify({ version: 1, parts: [{ media: { path: "/sandbox/a.png" } }] }), /media\.mediaType/],
    ["a missing path", JSON.stringify({ version: 1, parts: [{ media: { mediaType: "image/png" } }] }), /media\.path/],
    ["a relative path", JSON.stringify({ version: 1, parts: [{ media: { mediaType: "image/png", path: "a.png" } }] }), /absolute guest path/],
    ["a non-string filename", JSON.stringify({
      version: 1,
      parts: [{ media: { mediaType: "image/png", path: "/sandbox/a.png", filename: 7 } }],
    }), /media\.filename must be a string/],
    ["a non-string digest", JSON.stringify({
      version: 1,
      parts: [{ media: { mediaType: "image/png", path: "/sandbox/a.png", sha256: false } }],
    }), /media\.sha256 must be a string/],
    ["a negative size", JSON.stringify({
      version: 1,
      parts: [{ media: { mediaType: "image/png", path: "/sandbox/a.png", sizeBytes: -1 } }],
    }), /media\.sizeBytes must be a non-negative number/],
  ])("rejects %s", (_label, manifest, expected) => {
    expect(() => parseTurnManifest(manifest)).toThrow(expected);
  });
});

describe("renderTurnText", () => {
  it("joins text parts with a blank line in order, across media", () => {
    const turn: TurnContent = {
      parts: [
        { type: "text", text: "compare these" },
        image,
        { type: "text", text: "and these" },
        pdf,
      ],
    };

    expect(renderTurnText(turn)).toBe("compare these\n\nand these");
  });

  it("keeps a single text part unchanged", () => {
    expect(renderTurnText(textTurn("just text"))).toBe("just text");
  });

  it("skips whitespace-only text parts like the daemon projection", () => {
    expect(renderTurnText({
      parts: [
        { type: "text", text: "first" },
        { type: "text", text: "   " },
        { type: "text", text: "second" },
      ],
    })).toBe("first\n\nsecond");
  });

  it("returns an empty projection for a media-only turn", () => {
    expect(renderTurnText({ parts: [image, pdf] })).toBe("");
  });
});

describe("mediaParts", () => {
  it("returns media parts in manifest order and ignores text", () => {
    const turn: TurnContent = {
      parts: [
        { type: "text", text: "before" },
        image,
        { type: "text", text: "between" },
        pdf,
      ],
    };

    expect(mediaParts(turn)).toEqual([image, pdf]);
  });

  it("returns no media for a text-only turn", () => {
    expect(mediaParts(textTurn("hello"))).toEqual([]);
  });
});

describe("verifyTurnMedia", () => {
  async function tempDir(): Promise<string> {
    return mkdtemp(path.join(tmpdir(), "turn-media-"));
  }

  it("accepts a regular file", async () => {
    const dir = await tempDir();
    const file = path.join(dir, "part-1.png");
    await writeFile(file, "png");
    await expect(verifyTurnMedia({ parts: [{ type: "media", mediaType: "image/png", path: file }] }, dir)).resolves.toBeUndefined();
  });

  it("rejects a missing attachment", async () => {
    const dir = await tempDir();
    await expect(verifyTurnMedia({ parts: [{ type: "media", mediaType: "image/png", path: path.join(dir, "missing.png") }] }, dir))
      .rejects.toThrow(/is not readable/);
  });

  it("rejects a directory", async () => {
    const dir = await tempDir();
    await expect(verifyTurnMedia({ parts: [{ type: "media", mediaType: "image/png", path: dir }] }, dir))
      .rejects.toThrow(/not a regular file/);
  });

  it("enforces the per-part limit on the actual file size", async () => {
    const dir = await tempDir();
    const file = path.join(dir, "part-1.png");
    await writeFile(file, "12345");
    await expect(verifyTurnMedia({
      parts: [{ type: "media", mediaType: "image/png", path: file }],
      limits: { maxPartBytes: 2, maxTurnBytes: 100 },
    }, dir)).rejects.toThrow(/limit is 2/);
  });

  it("enforces the turn total across attachments", async () => {
    const dir = await tempDir();
    const first = path.join(dir, "a.png");
    const second = path.join(dir, "b.png");
    await writeFile(first, "123");
    await writeFile(second, "456");
    await expect(verifyTurnMedia({
      parts: [
        { type: "media", mediaType: "image/png", path: first },
        { type: "media", mediaType: "image/png", path: second },
      ],
      limits: { maxPartBytes: 10, maxTurnBytes: 4 },
    }, dir)).rejects.toThrow(/turn attachments/);
  });

  it("verifies a declared digest", async () => {
    const dir = await tempDir();
    const file = path.join(dir, "part-1.png");
    await writeFile(file, "png");
    const digest = createHash("sha256").update("different").digest("hex");
    await expect(verifyTurnMedia({
      parts: [{ type: "media", mediaType: "image/png", path: file, sha256: digest }],
    }, dir)).rejects.toThrow(/sha256 does not match/);
  });

  it("rejects a symlinked path component", async () => {
    const dir = await tempDir();
    await mkdir(path.join(dir, "real"));
    await writeFile(path.join(dir, "real", "secret.txt"), "x");
    await symlink(path.join(dir, "real"), path.join(dir, "alias"));
    await expect(verifyTurnMedia({
      parts: [{ type: "media", mediaType: "text/plain", path: path.join(dir, "alias", "secret.txt") }],
    }, dir)).rejects.toThrow(/symlink/);
  });
});

describe("reference media", () => {
  const reference: MediaPart = {
    type: "media",
    mediaType: "text/csv",
    path: "/sandbox/state/agents/attachments/run-1/turn-0/part-1.csv",
    reference: true,
  };

  it("renders a path reference into the text projection", () => {
    const turn: TurnContent = { parts: [{ type: "text", text: "read" }, reference] };
    expect(renderTurnText(turn)).toContain("Attached file: /sandbox/state/agents/attachments/run-1/turn-0/part-1.csv (text/csv)");
  });

  it("keeps reference media out of the native attachment list", () => {
    const turn: TurnContent = { parts: [reference] };
    expect(mediaParts(turn)).toEqual([]);
  });

  it("still verifies a reference file", async () => {
    const dir = await mkdtemp(path.join(tmpdir(), "turn-ref-"));
    const file = path.join(dir, "data.csv");
    await writeFile(file, "a,b\n");
    await expect(verifyTurnMedia({
      parts: [{ type: "media", mediaType: "text/csv", path: file, reference: true }],
    }, dir)).resolves.toBeUndefined();
  });
});

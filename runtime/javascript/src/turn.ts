import path from "node:path";
import { stat } from "node:fs/promises";

/**
 * One ordered piece of a turn's input.
 *
 * The guest receives a manifest because the daemon has already resolved every
 * media source to an absolute guest path — the runner only reads `path` and
 * never reconstructs a workspace location from `workspacePath`.
 */
export interface MediaPart {
  type: "media";
  mediaType: string;
  filename?: string;
  path: string;
  workspacePath?: string;
  sizeBytes?: number;
  sha256?: string;
}

export interface TextPart {
  type: "text";
  text: string;
}

export type TurnPart = TextPart | MediaPart;

export interface TurnContent {
  parts: TurnPart[];
}

/** Manifest schema version understood by this runtime. */
const manifestVersion = 1;

/**
 * The turn's text projection: text parts joined with a blank line, in order.
 *
 * This mirrors the daemon's projection (`agentinput.Text`) so the model and the
 * run history agree, including skipping whitespace-only parts.
 */
export function renderTurnText(turn: TurnContent): string {
  return turn.parts
    .filter((part): part is TextPart => part.type === "text" && part.text.trim() !== "")
    .map((part) => part.text)
    .join("\n\n");
}

/** The turn's media parts in manifest order. */
export function mediaParts(turn: TurnContent): MediaPart[] {
  return turn.parts.filter((part): part is MediaPart => part.type === "media");
}

/**
 * Verify every media path is a readable regular file before the turn starts.
 *
 * The daemon cannot do this for a workspace path when it does not share the
 * sandbox filesystem, and a harness that silently substitutes placeholder text
 * for a missing image would let the run "succeed" with the model answering
 * about nothing. Failing here names the missing attachment instead.
 */
export async function verifyTurnMedia(turn: TurnContent): Promise<void> {
  for (const media of mediaParts(turn)) {
    let info;
    try {
      info = await stat(media.path);
    } catch (error) {
      throw new Error(`attachment ${media.path} is not readable`, { cause: error });
    }
    if (!info.isFile()) {
      throw new Error(`attachment ${media.path} is not a regular file`);
    }
  }
}

/** Wrap plain text as a turn, preserving the pre-multimodal prompt path. */
export function textTurn(text: string): TurnContent {
  return { parts: [{ type: "text", text }] };
}

/** Parse and validate the JSON turn manifest passed through `--content-file`. */
export function parseTurnManifest(raw: string): TurnContent {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (error) {
    throw new Error("--content-file must contain valid JSON", { cause: error });
  }
  if (!isRecord(parsed)) {
    throw new Error("--content-file must contain a JSON object");
  }
  if (parsed.version !== manifestVersion) {
    throw new Error(
      `--content-file manifest version must be ${manifestVersion}, got ${JSON.stringify(parsed.version)}`,
    );
  }
  if (!Array.isArray(parsed.parts)) {
    throw new Error("--content-file manifest parts must be an array");
  }
  if (parsed.parts.length === 0) {
    throw new Error("--content-file manifest must contain at least one part");
  }
  return { parts: parsed.parts.map((part, index) => parsePart(part, index)) };
}

function parsePart(value: unknown, index: number): TurnPart {
  if (!isRecord(value)) {
    throw manifestPartError(index, "must be an object");
  }
  const hasText = value.text !== undefined;
  const hasMedia = value.media !== undefined;
  if (hasText && hasMedia) {
    throw manifestPartError(index, "must not set both text and media");
  }
  if (hasText) {
    if (typeof value.text !== "string") {
      throw manifestPartError(index, "text must be a string");
    }
    return { type: "text", text: value.text };
  }
  if (hasMedia) {
    return parseMedia(value.media, index);
  }
  throw manifestPartError(index, "must set text or media");
}

function parseMedia(value: unknown, index: number): MediaPart {
  if (!isRecord(value)) {
    throw manifestPartError(index, "media must be an object");
  }
  const mediaType = value.mediaType;
  if (typeof mediaType !== "string" || mediaType === "") {
    throw manifestPartError(index, "media.mediaType must be a non-empty string");
  }
  const mediaPath = value.path;
  if (typeof mediaPath !== "string" || mediaPath === "") {
    throw manifestPartError(index, "media.path must be a non-empty string");
  }
  if (!path.isAbsolute(mediaPath)) {
    throw manifestPartError(index, `media.path must be an absolute guest path, got ${JSON.stringify(mediaPath)}`);
  }
  const media: MediaPart = { type: "media", mediaType, path: mediaPath };
  const filename = optionalString(value.filename, index, "media.filename");
  if (filename) {
    media.filename = filename;
  }
  const workspacePath = optionalString(value.workspacePath, index, "media.workspacePath");
  if (workspacePath) {
    media.workspacePath = workspacePath;
  }
  const sha256 = optionalString(value.sha256, index, "media.sha256");
  if (sha256) {
    media.sha256 = sha256;
  }
  if (value.sizeBytes !== undefined) {
    if (typeof value.sizeBytes !== "number" || !Number.isFinite(value.sizeBytes) || value.sizeBytes < 0) {
      throw manifestPartError(index, "media.sizeBytes must be a non-negative number");
    }
    media.sizeBytes = value.sizeBytes;
  }
  return media;
}

function optionalString(value: unknown, index: number, field: string): string | undefined {
  if (value === undefined) {
    return undefined;
  }
  if (typeof value !== "string") {
    throw manifestPartError(index, `${field} must be a string`);
  }
  return value || undefined;
}

function manifestPartError(index: number, detail: string): Error {
  return new Error(`--content-file manifest part ${index} ${detail}`);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

import { createHash } from "node:crypto";
import path from "node:path";
import { lstat, readFile } from "node:fs/promises";

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
  /** Non-native type: verify the file but reference its path in the prompt. */
  reference?: boolean;
}

export interface TextPart {
  type: "text";
  text: string;
}

export type TurnPart = TextPart | MediaPart;

export interface TurnLimits {
  maxPartBytes: number;
  maxTurnBytes: number;
}

export interface TurnContent {
  parts: TurnPart[];
  limits?: TurnLimits;
}

/** Fallbacks used when an older manifest carries no limits. */
const defaultMaxPartBytes = 10 << 20;
const defaultMaxTurnBytes = 32 << 20;

/** Manifest schema version understood by this runtime. */
const manifestVersion = 1;

/**
 * The turn's text projection: text parts joined with a blank line, in order.
 *
 * This mirrors the daemon's projection (`agentinput.Text`) so the model and the
 * run history agree, including skipping whitespace-only parts.
 */
export function renderTurnText(turn: TurnContent): string {
  const texts: string[] = [];
  for (const part of turn.parts) {
    if (part.type === "text") {
      if (part.text.trim() !== "") {
        texts.push(part.text);
      }
      continue;
    }
    if (part.reference) {
      texts.push(referenceText(part));
    }
  }
  return texts.join("\n\n");
}

/**
 * The text a non-native attachment becomes. The agent already has filesystem
 * tools; naming the materialized path is what lets TXT, CSV, code, Office
 * documents, and archives use the same input entry point.
 */
export function referenceText(media: MediaPart): string {
  return `Attached file: ${media.path} (${media.mediaType}). Read it with your file tools.`;
}

/** The turn's native media parts in manifest order. */
export function mediaParts(turn: TurnContent): MediaPart[] {
  return turn.parts.filter((part): part is MediaPart => part.type === "media" && !part.reference);
}

/** Every media part, including path references, for pre-turn verification. */
function allMediaParts(turn: TurnContent): MediaPart[] {
  return turn.parts.filter((part): part is MediaPart => part.type === "media");
}

/**
 * Verify every media path is a readable, non-symlinked regular file within the
 * turn limits before the turn starts.
 *
 * The daemon cannot stat a workspace path when it does not share the sandbox
 * filesystem, so the guest enforces the same 10 MiB part / 32 MiB turn caps on
 * the actual files. A missing file, a symlinked component, or an oversized file
 * fails the run instead of being read into memory or silently replaced with
 * placeholder text.
 */
export async function verifyTurnMedia(turn: TurnContent, workspace: string): Promise<void> {
  const limits = turn.limits ?? { maxPartBytes: defaultMaxPartBytes, maxTurnBytes: defaultMaxTurnBytes };
  const root = path.resolve(workspace);
  let total = 0;
  for (const media of allMediaParts(turn)) {
    const target = path.resolve(media.path);
    await assertNoSymlinkPath(root, target, media.path);
    let info;
    try {
      info = await lstat(target);
    } catch (error) {
      throw new Error(`attachment ${media.path} is not readable`, { cause: error });
    }
    if (info.isSymbolicLink()) {
      throw new Error(`attachment ${media.path} is a symlink`);
    }
    if (!info.isFile()) {
      throw new Error(`attachment ${media.path} is not a regular file`);
    }
    if (info.size > limits.maxPartBytes) {
      throw new Error(`attachment ${media.path} is ${info.size} bytes, limit is ${limits.maxPartBytes}`);
    }
    total += info.size;
    if (total > limits.maxTurnBytes) {
      throw new Error(`turn attachments are ${total} bytes, limit is ${limits.maxTurnBytes}`);
    }
    if (media.sha256) {
      const digest = createHash("sha256").update(await readFile(target)).digest("hex");
      if (digest !== media.sha256) {
        throw new Error(`attachment ${media.path} sha256 does not match its bytes`);
      }
    }
  }
}

/**
 * Reject a path whose components under the workspace root are symlinks. The
 * daemon's lexical check cannot see a symlink an agent or a workspace source
 * created, and following one could read daemon-owned state or another
 * sandbox's files.
 */
async function assertNoSymlinkPath(root: string, target: string, displayPath: string): Promise<void> {
  const relative = path.relative(root, target);
  if (relative === "" || relative.startsWith("..") || path.isAbsolute(relative)) {
    return;
  }
  let current = root;
  for (const segment of relative.split(path.sep).filter(Boolean)) {
    current = path.join(current, segment);
    const info = await lstat(current).catch(() => undefined);
    if (info?.isSymbolicLink()) {
      throw new Error(`attachment ${displayPath} has a symlinked path component`);
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
  const turn: TurnContent = { parts: parsed.parts.map((part, index) => parsePart(part, index)) };
  const limits = parseLimits(parsed.limits);
  if (limits) {
    turn.limits = limits;
  }
  return turn;
}

function parseLimits(value: unknown): TurnLimits | undefined {
  if (value === undefined) {
    return undefined;
  }
  if (!isRecord(value)) {
    throw new Error("--content-file manifest limits must be an object");
  }
  const maxPartBytes = value.maxPartBytes;
  const maxTurnBytes = value.maxTurnBytes;
  if (typeof maxPartBytes !== "number" || !Number.isFinite(maxPartBytes) || maxPartBytes <= 0) {
    throw new Error("--content-file manifest limits.maxPartBytes must be a positive number");
  }
  if (typeof maxTurnBytes !== "number" || !Number.isFinite(maxTurnBytes) || maxTurnBytes <= 0) {
    throw new Error("--content-file manifest limits.maxTurnBytes must be a positive number");
  }
  return { maxPartBytes, maxTurnBytes };
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
  if (value.reference !== undefined) {
    if (typeof value.reference !== "boolean") {
      throw manifestPartError(index, "media.reference must be a boolean");
    }
    media.reference = value.reference;
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

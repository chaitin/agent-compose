/**
 * Framing for a detached task's stdin. The daemon writes frames into the
 * `attach --stdin` exec, attach forwards the bytes unchanged to the
 * supervisor's socket, and the supervisor decodes them.
 *
 * Each frame is a one-byte type, a 4-byte big-endian payload length, then the
 * payload:
 *
 * - `D` data: bytes for the task's stdin.
 * - `E` end of input: close the task's stdin.
 * - `S` signal: the payload is a signal name to deliver to the task group.
 *
 * End of input is a frame rather than the connection closing because a
 * connection also closes when the daemon (and with it the attach exec) dies,
 * and that must leave the task's stdin open for the next daemon.
 */

export type StdinFrame =
  | { type: "data"; data: Buffer }
  | { type: "eof" }
  | { type: "signal"; signal: string };

const HEADER_BYTES = 5;
/** Upper bound on one frame's payload, so a corrupt length cannot stall the decoder. */
export const MAX_STDIN_FRAME_BYTES = 16 * 1024 * 1024;

export function encodeStdinFrame(frame: StdinFrame): Buffer {
  const payload = frame.type === "data" ? frame.data : frame.type === "signal" ? Buffer.from(frame.signal, "utf8") : Buffer.alloc(0);
  const header = Buffer.alloc(HEADER_BYTES);
  header.write(frame.type === "data" ? "D" : frame.type === "eof" ? "E" : "S", 0, "latin1");
  header.writeUInt32BE(payload.length, 1);
  return Buffer.concat([header, payload]);
}

/** Incremental decoder: push bytes as they arrive, get whole frames back. */
export class StdinFrameDecoder {
  private pending: Buffer = Buffer.alloc(0);

  push(chunk: Buffer): StdinFrame[] {
    this.pending = this.pending.length === 0 ? chunk : Buffer.concat([this.pending, chunk]);
    const frames: StdinFrame[] = [];
    while (this.pending.length >= HEADER_BYTES) {
      const type = String.fromCharCode(this.pending[0]);
      const length = this.pending.readUInt32BE(1);
      if (length > MAX_STDIN_FRAME_BYTES || (type !== "D" && type !== "E" && type !== "S")) {
        throw new Error(`invalid stdin frame (type ${JSON.stringify(type)}, length ${length})`);
      }
      if (this.pending.length < HEADER_BYTES + length) {
        break;
      }
      const payload = this.pending.subarray(HEADER_BYTES, HEADER_BYTES + length);
      this.pending = this.pending.subarray(HEADER_BYTES + length);
      if (type === "D") {
        frames.push({ type: "data", data: Buffer.from(payload) });
      } else if (type === "E") {
        frames.push({ type: "eof" });
      } else {
        frames.push({ type: "signal", signal: payload.toString("utf8") });
      }
    }
    return frames;
  }
}

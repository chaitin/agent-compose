import { spawn } from "node:child_process";

export interface CapabilityCallOptions {
  timeoutMs?: number;
  maxOutputBytes?: number;
  signal?: AbortSignal;
}

// Keep credentials out of argv and never forward grpcurl's diagnostics to stdout.
export async function capabilityGRPC(args: string[], input: string | undefined, options: CapabilityCallOptions): Promise<string> {
  const token = process.env.CAP_TOKEN?.trim();
  const target = process.env.CAP_GRPC_TARGET?.trim();
  if (!token || !target) throw new Error("CAP_TOKEN and CAP_GRPC_TARGET are required");
  let url: URL;
  try { url = new URL(target.includes("://") ? target : `http://${target}`); }
  catch { throw new Error("CAP_GRPC_TARGET must be an origin address"); }
  if (!["http:", "https:"].includes(url.protocol) || url.username || url.password || url.pathname !== "/" || url.search || url.hash) {
    throw new Error("CAP_GRPC_TARGET must be an origin address");
  }
  const timeout = options.timeoutMs ?? 30_000;
  const limit = options.maxOutputBytes ?? 256 * 1024;
  if (!Number.isSafeInteger(timeout) || timeout <= 0 || !Number.isSafeInteger(limit) || limit <= 0 || limit > 1024 * 1024) {
    throw new Error("invalid capability timeout or output limit");
  }
  options.signal?.throwIfAborted();
  const positionals = args.at(-2) === "describe" ? 2 : 1;
  const child = spawn("grpcurl", [
    ...(url.protocol === "http:" ? ["-plaintext"] : []),
    "-expand-headers", "-H", "x-capability-sandbox-token: ${CAP_TOKEN}",
    "-max-time", String(timeout / 1000), "-max-msg-sz", String(limit),
    ...args.slice(0, -positionals), url.host, ...args.slice(-positionals),
  ], { shell: false, stdio: ["pipe", "pipe", "pipe"] });
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    let bytes = 0;
    let failed: Error | undefined;
    let killTimer: NodeJS.Timeout | undefined;
    const stop = (message: string) => {
      failed ??= new Error(message);
      child.kill("SIGTERM");
      killTimer ??= setTimeout(() => child.kill("SIGKILL"), 1000);
      killTimer.unref();
    };
    const abort = () => stop("capability request cancelled");
    const timer = setTimeout(() => stop("capability request timed out"), timeout);
    options.signal?.addEventListener("abort", abort, { once: true });
    if (options.signal?.aborted) abort();
    child.stdout.on("data", (chunk: Buffer) => {
      bytes += chunk.length;
      if (bytes > limit) stop("capability output exceeds limit");
      else chunks.push(chunk);
    });
    // Do not retain upstream error text: it can contain credentials or business data.
    child.stderr.resume();
    child.stdin.on("error", () => { /* Early server rejection can close stdin. */ });
    child.on("error", () => { failed ??= new Error("unable to start grpcurl; use an AC guest image with grpcurl installed"); });
    child.on("close", (code) => {
      clearTimeout(timer);
      if (killTimer) clearTimeout(killTimer);
      options.signal?.removeEventListener("abort", abort);
      if (failed) reject(failed);
      else if (code !== 0) reject(new Error(`capability request failed (grpcurl exit ${code ?? "signal"})`));
      else resolve(Buffer.concat(chunks).toString("utf8"));
    });
    child.stdin.end(input);
  });
}

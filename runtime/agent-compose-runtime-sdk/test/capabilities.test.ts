import fs from "node:fs/promises";
import path from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { capabilities } from "../src/capabilities.js";
import { capabilityGRPC } from "../src/capability-grpc.js";
import { paths } from "../src/env.js";
import { withTempDir } from "./helpers.js";

const originalRuntime = paths.runtimeRoot;
afterEach(() => { vi.unstubAllEnvs(); paths.runtimeRoot = originalRuntime; });

const tool = {
  id: "a".repeat(64), capset: "internal/dev", instance: "first",
  method: "sample.Service/Echo", request_type: "sample.Request", response_type: "sample.Response",
};

async function setup(root: string) {
  paths.runtimeRoot = root;
  vi.stubEnv("SANDBOX_ID", "sandbox");
  vi.stubEnv("CAP_TOKEN", "private-sandbox-token");
  vi.stubEnv("CAP_GRPC_TARGET", "http://127.0.0.1:12345");
  vi.stubEnv("PATH", `${root}${path.delimiter}${process.env.PATH}`);
  vi.stubEnv("AC_TEST_CAPTURE", path.join(root, "capture.json"));
  await fs.mkdir(path.join(root, "mpi"));
  await catalog(root, {});
  await fs.writeFile(path.join(root, "grpcurl"), `#!${process.execPath}
import fs from 'node:fs';
const args = process.argv.slice(2);
const input = fs.readFileSync(0, 'utf8');
fs.writeFileSync(process.env.AC_TEST_CAPTURE, JSON.stringify({args, input}));
if (args.includes('-protoset-out')) {
  const file = args[args.indexOf('-protoset-out')+1];
  fs.writeFileSync(file, process.env.AC_TEST_MODE === 'descriptor-large' ? 'x'.repeat(2048) : 'descriptor-with-imports');
  fs.writeFileSync(process.env.AC_TEST_CAPTURE+'.descriptor', file);
}
if (process.env.AC_TEST_MODE === 'wait') setInterval(() => {}, 1000);
else if (process.env.AC_TEST_MODE === 'fail') { console.error(process.env.CAP_TOKEN); process.exitCode = 7; }
else if (process.env.AC_TEST_MODE === 'large') console.log('x'.repeat(2048));
else if (args.includes('describe')) console.log(process.env.AC_TEST_MODE === 'stream' ? 'rpc Echo (stream Request) returns (Response);' : 'rpc Echo (Request) returns (Response);');
else if (process.env.AC_TEST_MODE === 'echo-json') console.log(input);
else console.log(JSON.stringify({message: JSON.parse(input).message}));
`, { mode: 0o755 });
}

async function catalog(root: string, overrides: Record<string, unknown>) {
  await fs.writeFile(path.join(root, "mpi", "capabilities.json"), JSON.stringify({
    version: 1, sandbox_id: "sandbox", tools: [tool], unavailable_capsets: [], ...overrides,
  }));
}

describe("runtime.capabilities", () => {
  it.each(["text", 42, true, null, ["first", 2]])("accepts well-known Protobuf JSON input %j", async (params) => {
    await withTempDir(async (root) => {
      await setup(root);
      vi.stubEnv("AC_TEST_MODE", "echo-json");
      expect(await capabilities.call(tool.id, params)).toEqual(params);
    });
  });

  it.each([undefined, NaN, Infinity, { value: undefined }, [undefined], { value: 1n }])("rejects non-JSON input before starting grpcurl", async (params) => {
    await withTempDir(async (root) => {
      await setup(root);
      await expect(capabilities.call(tool.id, params)).rejects.toThrow("JSON-compatible");
      await expect(fs.stat(path.join(root, "capture.json"))).rejects.toThrow();
    });
  });

  it("calls an exact binding through grpcurl without putting credentials or params in argv", async () => {
    await withTempDir(async (root) => {
      await setup(root);
      expect(await capabilities.list()).toEqual([tool]);
      expect(await capabilities.call(tool.id, { message: "business-input" })).toEqual({ message: "business-input" });
      const capture = JSON.parse(await fs.readFile(path.join(root, "capture.json"), "utf8"));
      expect(capture.args.slice(-2)).toEqual(["127.0.0.1:12345", tool.method]);
      expect(capture.args).toContain("x-octobus-capset: internal/dev");
      expect(capture.args).toContain("x-octobus-instance: first");
      expect(capture.args).toContain("x-capability-sandbox-token: ${CAP_TOKEN}");
      expect(JSON.stringify(capture.args)).not.toContain("private-sandbox-token");
      expect(JSON.stringify(capture.args)).not.toContain("business-input");
      expect(JSON.parse(capture.input)).toEqual({ message: "business-input" });
      const description = await capabilities.describe(tool.id);
      expect(description.request).toContain("rpc Echo");
      expect(Buffer.from(description.descriptorSetBase64, "base64").toString()).toBe("descriptor-with-imports");
      await expect(fs.stat(await fs.readFile(path.join(root, "capture.json.descriptor"), "utf8"))).rejects.toThrow();
      const described = JSON.parse(await fs.readFile(path.join(root, "capture.json"), "utf8"));
      expect(described.args.slice(-3)).toEqual(["127.0.0.1:12345", "describe", tool.response_type]);
    });
  });

  it.each([
    [{ sandbox_id: "other" }, "sandbox mismatch"],
    [{ unavailable_capsets: ["offline"] }, "incomplete"],
    [{ tools: [tool, tool] }, "duplicate"],
    [{ version: 2 }, "invalid"],
    [{ tools: [{ ...tool, capset: "${CAP_TOKEN}" }] }, "invalid"],
  ])("rejects invalid or incomplete discovery %j", async (overrides, expected) => {
    await withTempDir(async (root) => {
      await setup(root);
      await catalog(root, overrides);
      await expect(capabilities.list()).rejects.toThrow(expected);
    });
  });

  it("rejects unknown tools and streaming methods before business invocation", async () => {
    await withTempDir(async (root) => {
      await setup(root);
      await expect(capabilities.call("unknown", {})).rejects.toThrow("not in");
      await expect(fs.stat(path.join(root, "capture.json"))).rejects.toThrow();
      vi.stubEnv("AC_TEST_MODE", "stream");
      await expect(capabilities.call(tool.id, {})).rejects.toThrow("streaming");
      const capture = JSON.parse(await fs.readFile(path.join(root, "capture.json"), "utf8"));
      expect(capture.args).toContain("describe");
      expect(capture.args).not.toContain("-d");
    });
  });

  it("does not expose upstream stderr containing credentials", async () => {
    await withTempDir(async (root) => {
      await setup(root);
      vi.stubEnv("AC_TEST_MODE", "fail");
      await expect(capabilities.call(tool.id, {})).rejects.toThrow("grpcurl exit 7");
      await expect(capabilities.call(tool.id, {})).rejects.not.toThrow("private-sandbox-token");
    });
  });

  it("bounds output and terminates calls on timeout and cancellation", async () => {
    await withTempDir(async (root) => {
      await setup(root);
      vi.stubEnv("AC_TEST_MODE", "large");
      await expect(capabilityGRPC([tool.method], "{}", { maxOutputBytes: 1024 })).rejects.toThrow("exceeds");
      vi.stubEnv("AC_TEST_MODE", "wait");
      await expect(capabilityGRPC([tool.method], "{}", { timeoutMs: 250 })).rejects.toThrow("timed out");
      const signal = AbortSignal.timeout(100);
      await expect(capabilityGRPC([tool.method], "{}", { signal })).rejects.toThrow();
    });
  });

  it("rejects missing credentials and arbitrary target paths", async () => {
    await withTempDir(async (root) => {
      await setup(root);
      vi.stubEnv("CAP_TOKEN", "");
      await expect(capabilityGRPC([tool.method], "{}", {})).rejects.toThrow("required");
      vi.stubEnv("CAP_TOKEN", "private-sandbox-token");
      vi.stubEnv("CAP_GRPC_TARGET", "http://127.0.0.1:12345/private");
      await expect(capabilityGRPC([tool.method], "{}", {})).rejects.toThrow("origin");
    });
  });

  it("bounds complete descriptors and cleans temporary files on failure", async () => {
    await withTempDir(async (root) => {
      await setup(root);
      vi.stubEnv("AC_TEST_MODE", "descriptor-large");
      await expect(capabilities.describe(tool.id, { maxOutputBytes: 1024 })).rejects.toThrow("descriptor");
      const file = await fs.readFile(path.join(root, "capture.json.descriptor"), "utf8");
      await expect(fs.stat(file)).rejects.toThrow();
    });
  });
});

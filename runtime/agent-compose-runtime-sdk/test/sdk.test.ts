import fs from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { runtime, CommandError } from "../src/index.js";
import { captureStdio, withTempDir } from "./helpers.js";

describe("@chaitin-ai/agent-compose-runtime-sdk", () => {
  it("runs exec commands without shell expansion", async () => {
    await withTempDir(async (root) => {
      const result = await runtime.exec("node", ["-e", "console.log(process.argv[1])", "$HOME && echo injected"], { cwd: root });

      expect(result.success).toBe(true);
      expect(result.stdout).toBe("$HOME && echo injected\n");
      expect(result.stderr).toBe("");
      expect(result.exitCode).toBe(0);
    });
  });

  it("mirrors exec stdout and stderr to parent streams", async () => {
    await withTempDir(async (root) => {
      const stdio = captureStdio();
      let result;
      try {
        result = await runtime.exec("node", [
          "-e",
          "process.stdout.write('out'); process.stderr.write('err')",
        ], { cwd: root });
      } finally {
        stdio.restore();
      }

      expect(result?.stdout).toBe("out");
      expect(result?.stderr).toBe("err");
      expect(stdio.stdout).toContain("out");
      expect(stdio.stderr).toContain("err");
    });
  });

  it("runs shell scripts through bash -lc", async () => {
    await withTempDir(async (root) => {
      const result = await runtime.shell("echo shell:$AGENT_COMPOSE_TEST_VALUE", {
        cwd: root,
        env: { AGENT_COMPOSE_TEST_VALUE: "works" },
      });

      expect(result.success).toBe(true);
      expect(result.stdout).toBe("shell:works\n");
    });
  });

  it("injects simplified runtime path environment into child processes", async () => {
    await withTempDir(async (root) => {
      const previousEnv = {
        HOME: process.env.HOME,
        SESSION_WORKSPACE: process.env.SESSION_WORKSPACE,
        ARTIFACT_DIR: process.env.ARTIFACT_DIR,
      };
      process.env.HOME = "/native/home";
      delete process.env.SESSION_WORKSPACE;
      delete process.env.ARTIFACT_DIR;
      try {
        const result = await runtime.exec("node", [
          "-e",
          [
            "const keys = ['WORKSPACE', 'SESSION_WORKSPACE', 'STATE_ROOT', 'RUNTIME_ROOT', 'ARTIFACT_DIR', 'HOME'];",
            "process.stdout.write(JSON.stringify(Object.fromEntries(keys.map((key) => [key, process.env[key]]))));",
          ].join(" "),
        ], {
          cwd: root,
          streamOutput: false,
        });
        const env = JSON.parse(result.stdout);

        expect(env.WORKSPACE).toBe(runtime.paths.workspace);
        expect(env.SESSION_WORKSPACE).toBeUndefined();
        expect(env.STATE_ROOT).toBe(runtime.paths.stateRoot);
        expect(env.RUNTIME_ROOT).toBe(runtime.paths.runtimeRoot);
        expect(env.ARTIFACT_DIR).toBeUndefined();
        expect(env.HOME).toBe("/native/home");
      } finally {
        for (const [key, value] of Object.entries(previousEnv)) {
          if (value === undefined) {
            delete process.env[key];
          } else {
            process.env[key] = value;
          }
        }
      }
    });
  });

  it("uses simplified runtime paths without legacy home or session root fields", () => {
    expect(runtime.paths.workspace).toBe(process.env.WORKSPACE || "/workspace");
    expect(runtime.paths.home).toBe(process.env.HOME || "/root");
    expect("sessionRoot" in runtime.paths).toBe(false);
    expect("artifactDir" in runtime.paths).toBe(false);
  });

  it("streams command output to parent stdio by default", async () => {
    await withTempDir(async (root) => {
      const stdio = captureStdio();
      try {
        const result = await runtime.exec("node", [
          "-e",
          "process.stdout.write('out-1\\n'); setTimeout(() => process.stderr.write('err-1\\n'), 5);",
        ], { cwd: root });

        expect(result.stdout).toBe("out-1\n");
        expect(result.stderr).toBe("err-1\n");
        expect(stdio.stdout).toContain("out-1\n");
        expect(stdio.stderr).toContain("err-1\n");
      } finally {
        stdio.restore();
      }
    });
  });

  it("can disable output streaming to parent stdio", async () => {
    await withTempDir(async (root) => {
      const stdio = captureStdio();
      try {
        const result = await runtime.shell("echo hidden && echo hidden-err >&2", {
          cwd: root,
          streamOutput: false,
        });

        expect(result.stdout).toBe("hidden\n");
        expect(result.stderr).toBe("hidden-err\n");
        expect(stdio.stdout).toBe("");
        expect(stdio.stderr).toBe("");
      } finally {
        stdio.restore();
      }
    });
  });

  it("returns non-zero exit codes and optionally rejects on failure", async () => {
    await withTempDir(async (root) => {
      const result = await runtime.exec("node", ["-e", "process.exit(7)"], { cwd: root });

      expect(result.success).toBe(false);
      expect(result.exitCode).toBe(7);

      await expect(runtime.exec("node", ["-e", "process.exit(8)"], { cwd: root, rejectOnFailure: true }))
        .rejects.toBeInstanceOf(CommandError);
    });
  });

  it("terminates commands that exceed timeout", async () => {
    await withTempDir(async (root) => {
      await expect(runtime.exec("node", ["-e", "setTimeout(() => {}, 10000)"], {
        cwd: root,
        timeoutMs: 25,
      })).rejects.toThrow("command timed out");
    });
  });

  it("overrides env and truncates returned output", async () => {
    await withTempDir(async (root) => {
      const result = await runtime.exec("node", [
        "-e",
        "process.stdout.write(process.env.AGENT_COMPOSE_TEST_VALUE.repeat(12)); process.stderr.write('b'.repeat(9))",
      ], {
        cwd: root,
        env: { AGENT_COMPOSE_TEST_VALUE: "a" },
        maxOutputBytes: 5,
      });

      expect(result.stdout).toBe("aaaaa");
      expect(result.stderr).toBe("bbbbb");
      expect(result.output.length).toBe(5);
      expect(result.stdoutTruncated).toBe(true);
      expect(result.stderrTruncated).toBe(true);
      expect(result.outputTruncated).toBe(true);
    });
  });

  it("reads env values and throws for missing required env", () => {
    process.env.AGENT_COMPOSE_RUNTIME_SDK_TEST_ENV = "configured";
    try {
      expect(runtime.env.get("AGENT_COMPOSE_RUNTIME_SDK_TEST_ENV")).toBe("configured");
      expect(runtime.env.require("AGENT_COMPOSE_RUNTIME_SDK_TEST_ENV")).toBe("configured");
      expect(runtime.env.all().AGENT_COMPOSE_RUNTIME_SDK_TEST_ENV).toBe("configured");
      expect(() => runtime.env.require("AGENT_COMPOSE_RUNTIME_SDK_TEST_MISSING")).toThrow("required environment variable");
    } finally {
      delete process.env.AGENT_COMPOSE_RUNTIME_SDK_TEST_ENV;
    }
  });

  it("reads runtime context from json env", () => {
    const previous = process.env.AGENT_COMPOSE_RUNTIME_CONTEXT_JSON;
    process.env.AGENT_COMPOSE_RUNTIME_CONTEXT_JSON = JSON.stringify({
      source: "api",
      client_request_id: "request-1",
      trace_id: "trace-1",
      external_run_id: "external-1",
      metadata: { project: "demo" },
      env: { MODE: "test" },
      identity_context: { user: "user-1" },
      capability_scope: { capset_ids: ["capset-a"], metadata: { corp: "x" } },
    });
    try {
      const context = runtime.context.read();
      expect(context.source).toBe("api");
      expect(context.clientRequestId).toBe("request-1");
      expect(context.traceId).toBe("trace-1");
      expect(context.externalRunId).toBe("external-1");
      expect(context.metadata.project).toBe("demo");
      expect(context.env.MODE).toBe("test");
      expect(context.identityContext.user).toBe("user-1");
      expect(context.capabilityScope.capsetIds).toEqual(["capset-a"]);
      expect(context.capabilityScope.metadata.corp).toBe("x");
    } finally {
      if (previous === undefined) {
        delete process.env.AGENT_COMPOSE_RUNTIME_CONTEXT_JSON;
      } else {
        process.env.AGENT_COMPOSE_RUNTIME_CONTEXT_JSON = previous;
      }
    }
  });

  it("writes, reads, and lists artifacts", async () => {
    await withTempDir(async (root) => {
      const record = await runtime.artifact.write("reports/summary.txt", "hello", {
        dir: root,
        contentType: "text/plain",
        metadata: { kind: "summary" },
      });

      expect(record.name).toBe("reports/summary.txt");
      expect(record.contentType).toBe("text/plain");
      expect(record.metadata.kind).toBe("summary");
      expect(await runtime.artifact.read("reports/summary.txt", { dir: root, encoding: "utf8" })).toBe("hello");
      expect(await runtime.artifact.list({ dir: root })).toEqual(["reports/summary.txt"]);
    });
  });

  it("stores namespaced state as json", async () => {
    await withTempDir(async (root) => {
      const store = runtime.stateStore("service", { dir: root });
      await store.set("cursor", { value: 42 });
      expect(await store.get("cursor")).toEqual({ value: 42 });
      await store.delete("cursor");
      expect(await store.get("cursor")).toBeUndefined();
    });
  });

  it("publishes structured runtime events to stdout", () => {
    const stdio = captureStdio();
    try {
      const event = runtime.event.publish("demo.created", { ok: true }, { source: "test" });
      expect(event.topic).toBe("demo.created");
    } finally {
      stdio.restore();
    }

    const published = JSON.parse(stdio.stdout);
    expect(published.type).toBe("agent-compose.runtime.event");
    expect(published.topic).toBe("demo.created");
    expect(published.payload).toEqual({ ok: true });
    expect(published.metadata).toEqual({ source: "test" });
  });

  it("writes structured logs to stdout", () => {
    const stdio = captureStdio();
    try {
      runtime.log("hello", { ok: true });
    } finally {
      stdio.restore();
    }

    const log = JSON.parse(stdio.stdout);
    expect(log.type).toBe("agent-compose.runtime.log");
    expect(log.message).toBe("hello");
    expect(log.payload).toEqual({ ok: true });
    expect(log.createdAt).toEqual(expect.any(String));
  });

  it("writes markdown reports to artifact dir or workspace fallback", async () => {
    await withTempDir(async (root) => {
      const artifactDir = path.join(root, "artifacts");
      const reportPath = await runtime.report.writeMarkdown("report.md", "artifact report", { dir: artifactDir });
      expect(reportPath).toBe(path.join(artifactDir, "report.md"));
      expect(await fs.readFile(reportPath, "utf8")).toBe("artifact report");

      const fallbackPath = await runtime.report.writeMarkdown("fallback.md", "workspace report", { dir: root });
      expect(fallbackPath).toBe(path.join(root, "fallback.md"));
      expect(await fs.readFile(fallbackPath, "utf8")).toBe("workspace report");
    });
  });
});

import fs from "node:fs/promises";
import path from "node:path";
import { EventEmitter } from "node:events";
import { Readable, Writable } from "node:stream";
import { describe, expect, it, vi } from "vitest";
import { createProgram } from "../src/cli.js";
import { runExecCommand } from "../src/command.js";
import { COMMAND_RESULT_PREFIX, RESULT_PREFIX } from "../src/constants.js";
import { runStreamCommand } from "../src/stream.js";
import { captureStdio, withTempSession } from "./helpers.js";

const codexMockState = vi.hoisted(() => ({
  constructorOptions: null as unknown,
  threadOptions: null as unknown,
}));

const claudeMockState = vi.hoisted(() => ({
	lastArgs: null as unknown,
}));

vi.mock("@openai/codex-sdk", () => ({
  Codex: vi.fn().mockImplementation(function Codex(options: unknown) {
    codexMockState.constructorOptions = options;
    return {
      startThread: vi.fn((threadOptions: unknown) => {
        codexMockState.threadOptions = threadOptions;
        return {
          id: "e2e-codex-thread",
          runStreamed: vi.fn(async (input: string, options: unknown) => ({
            events: (async function* events() {
              yield {
                type: "item.completed",
                item: {
                  id: "answer",
                  type: "agent_message",
                  text: JSON.stringify({ input, options }),
                },
              };
            })(),
          })),
        };
      }),
    };
  }),
}));

vi.mock("@anthropic-ai/claude-agent-sdk", () => ({
  query: vi.fn((args: unknown) => {
		claudeMockState.lastArgs = args;
		return {
			close: vi.fn(),
			[Symbol.asyncIterator]: async function* iterator() {
				yield {
					type: "stream_event",
					session_id: "e2e-claude-session",
					event: {
						type: "content_block_start",
						content_block: {
							name: "Read",
							input: { file_path: "README.md" },
						},
					},
				};
				yield {
					type: "stream_event",
					event: {
						type: "content_block_delta",
						delta: { type: "text_delta", text: "claude partial" },
					},
				};
				yield {
					type: "auth_status",
					output: ["auth ok"],
					error: "auth warning",
				};
				yield {
					type: "system",
					subtype: "local_command_output",
					content: "local command output",
				};
				yield {
					type: "result",
					subtype: "success",
					stop_reason: "end_turn",
					structured_output: { answer: "claude final" },
				};
			},
		};
	}),
}));

vi.mock("node:child_process", async (importOriginal) => {
  const original = await importOriginal<typeof import("node:child_process")>();
  return {
    ...original,
    spawn: vi.fn((command: string, args: string[], options: Record<string, unknown>) => {
      if (command !== "gemini" && command !== "opencode") {
        return original.spawn(command, args, options);
      }
      const child = new EventEmitter() as EventEmitter & { stdout: Readable; stderr: EventEmitter };
      child.stdout = Readable.from(command === "gemini"
		? [
			JSON.stringify({ type: "init", sessionId: "e2e-gemini-session" }) + "\n",
			JSON.stringify({ type: "message", content: "gemini says ok" }) + "\n",
			JSON.stringify({ type: "result", result: "gemini final" }) + "\n",
		]
		: [
			JSON.stringify({ type: "result", result: "opencode final", sessionId: "e2e-opencode-session" }) + "\n",
		]);
      child.stderr = new EventEmitter();
      const originalOnce = child.once.bind(child);
      child.once = ((eventName: string | symbol, listener: (...args: unknown[]) => void) => {
        if (eventName === "exit") {
          queueMicrotask(() => listener(0));
          return child;
        }
        return originalOnce(eventName, listener);
      }) as typeof child.once;
      return child;
    }),
  };
});

describe("runtime JavaScript E2E", () => {
  it("preserves turn boundaries across interactive Codex messages", async () => {
    await withTempSession(async (root) => {
      let output = "";
      const stdout = new Writable({
        write(chunk, _encoding, callback) {
          output += chunk.toString();
          callback();
        },
      });
      const frame = (seq: number, type: string, fields: Record<string, unknown> = {}) =>
        JSON.stringify({ v: 1, seq, type, ...fields }) + "\n";

      await runStreamCommand({
        stdin: Readable.from([
          frame(0, "start", { provider: "codex", stateRoot: path.join(root, "state") }),
          frame(1, "human_message", { message: "first" }),
          frame(2, "human_message", { message: "second" }),
          frame(3, "eof"),
        ]),
        stdout,
      });

      const frames = output.trim().split("\n").map((line) => JSON.parse(line) as Record<string, unknown>);
      expect(frames.at(-1)).toMatchObject({
        type: "result",
        threadId: "e2e-codex-thread",
        stopReason: "eof",
        transcript: '{"input":"first"}\n{"input":"second"}',
      });
    });
  });

  it("runs a production-like command workflow through request and artifact files", async () => {
    await withTempSession(async (root) => {
      const workspace = path.join(root, "workspace");
      const home = path.join(root, "home");
      const artifactDir = path.join(root, "runtime", "command");
      const requestFile = path.join(root, "runtime", "command-request.json");
      await fs.mkdir(workspace, { recursive: true });
      await fs.mkdir(home, { recursive: true });
      await fs.mkdir(path.dirname(requestFile), { recursive: true });
      await fs.writeFile(path.join(workspace, "input.txt"), "e2e-ok");
      await fs.writeFile(requestFile, JSON.stringify({
        mode: "shell",
        script: "cat input.txt > output.txt && cat output.txt",
        artifactDir
      }));

      const result = await runExecCommand({ requestFile, workspace, home });

      expect(result.success).toBe(true);
      expect(result.stdout).toBe("e2e-ok");
      await expect(fs.readFile(path.join(workspace, "output.txt"), "utf8")).resolves.toBe("e2e-ok");
      const artifactResult = JSON.parse(await fs.readFile(result.artifacts.result, "utf8")) as { success: boolean };
      expect(artifactResult.success).toBe(true);
    });
  });

  it("runs the CLI exec path with environment, stderr, and truncation artifacts", async () => {
    await withTempSession(async (root) => {
      const workspace = path.join(root, "workspace");
      const requestFile = path.join(root, "runtime", "command-request.json");
      const artifactDir = path.join(root, "runtime", "artifacts");
      await fs.mkdir(workspace, { recursive: true });
      await fs.mkdir(path.dirname(requestFile), { recursive: true });
      await fs.writeFile(requestFile, JSON.stringify({
        mode: "exec",
        command: "node",
        args: ["-e", "console.log(process.env.E2E_FLAG); console.error('warn'); console.log('abcdef')"],
        env: { E2E_FLAG: "from-env" },
        maxOutputBytes: 8,
        artifactDir,
      }));

      const stdio = captureStdio();
      try {
        await createProgram({ exitOverride: true }).parseAsync([
          "node",
          "cli",
          "exec",
          "--request-file",
          requestFile,
          "--state-root",
          path.join(root, "state"),
          "--workspace",
          workspace,
          "--home",
          path.join(root, "home"),
        ]);
      } finally {
        stdio.restore();
      }

      expect(stdio.stdout).toContain("from-env");
      expect(stdio.stdout).toContain(`${COMMAND_RESULT_PREFIX}{`);
      expect(stdio.stderr).toContain("warn");
      const result = JSON.parse(await fs.readFile(path.join(artifactDir, "command-result.json"), "utf8")) as {
        stdoutTruncated: boolean;
        outputTruncated: boolean;
      };
      expect(result.stdoutTruncated).toBe(true);
      expect(result.outputTruncated).toBe(true);
    });
  });

  it("runs the CLI prompt path with MPI catalog and output schema files", async () => {
    await withTempSession(async (root) => {
      const stateRoot = path.join(root, "state");
      const runtimeRoot = path.join(root, "runtime");
      const messageFile = path.join(root, "message.txt");
      const schemaFile = path.join(root, "schema.json");
      await fs.mkdir(path.join(runtimeRoot, "mpi", "resources"), { recursive: true });
      await fs.writeFile(path.join(runtimeRoot, "mpi", "catalog.md"), "# Tooling\nRead resource-a.md", "utf8");
      await fs.writeFile(path.join(runtimeRoot, "mpi", "resources", "resource-a.md"), "details", "utf8");
      await fs.writeFile(messageFile, "hello e2e", "utf8");
      await fs.writeFile(schemaFile, JSON.stringify({ type: "object" }), "utf8");

      const stdio = captureStdio();
      try {
        await createProgram({ exitOverride: true }).parseAsync([
          "node",
          "cli",
          "prompt",
          "--provider",
          "codex",
          "--message-file",
          messageFile,
          "--state-root",
          stateRoot,
          "--workspace",
          path.join(root, "workspace"),
          "--home",
          path.join(root, "home"),
          "--output-schema-file",
          schemaFile,
        ]);
      } finally {
        stdio.restore();
      }

      expect(stdio.stdout).toContain(`${RESULT_PREFIX}{`);
      expect(stdio.stdout).toContain("hello e2e");
      expect(stdio.stdout).toContain("outputSchema");
      expect(codexMockState.constructorOptions).toMatchObject({
        config: {
          developer_instructions: expect.stringContaining("MPI Catalog"),
        },
      });
      expect(codexMockState.threadOptions).not.toHaveProperty("config");
    });
  });

  it("surfaces prompt schema parse errors through the CLI program", async () => {
    await withTempSession(async (root) => {
      const messageFile = path.join(root, "message.txt");
      const schemaFile = path.join(root, "schema.json");
      await fs.writeFile(messageFile, "hello", "utf8");
      await fs.writeFile(schemaFile, "{", "utf8");

      await expect(createProgram({ exitOverride: true }).parseAsync([
        "node",
        "cli",
        "prompt",
        "--provider",
        "codex",
        "--message-file",
        messageFile,
        "--output-schema-file",
        schemaFile,
      ])).rejects.toThrow("--output-schema-file must contain valid JSON");
    });
  });

  it("runs the Gemini prompt path through stream-json protocol output", async () => {
    await withTempSession(async (root) => {
      const messageFile = path.join(root, "message.txt");
      const stateRoot = path.join(root, "state");
      await fs.writeFile(messageFile, "gemini prompt", "utf8");
      await fs.mkdir(path.join(stateRoot, "agents", "mcp"), { recursive: true });
      await fs.writeFile(path.join(stateRoot, "agents", "mcp", "config.json"), JSON.stringify({
		mcp_servers: {
			docs: {
				type: "remote",
				transport: "http",
				url: "https://docs.example/mcp",
				headers: { Authorization: { value: "Bearer token" } },
			},
		},
	  }), "utf8");

      const stdio = captureStdio();
      try {
        await createProgram({ exitOverride: true }).parseAsync([
          "node",
          "cli",
          "prompt",
          "--provider",
          "gemini",
          "--message-file",
          messageFile,
          "--state-root",
          stateRoot,
          "--workspace",
          path.join(root, "workspace"),
          "--home",
          path.join(root, "home"),
        ]);
      } finally {
        stdio.restore();
      }

      expect(stdio.stdout).toContain(`${RESULT_PREFIX}{`);
      expect(stdio.stdout).toContain("e2e-gemini-session");
      expect(stdio.stdout).toContain("gemini final");
      expect(stdio.stderr).toContain("gemini says ok");
      const settings = JSON.parse(await fs.readFile(path.join(root, "home", ".gemini", "settings.json"), "utf8")) as Record<string, unknown>;
      expect(settings.mcpServers).toMatchObject({
		docs: { httpUrl: "https://docs.example/mcp", headers: { Authorization: "Bearer token" } },
	  });
    });
  });

  it("runs the Claude prompt path with structured output and transcript events", async () => {
    await withTempSession(async (root) => {
      const messageFile = path.join(root, "message.txt");
      const schemaFile = path.join(root, "schema.json");
      const stateRoot = path.join(root, "state");
      await fs.writeFile(messageFile, "claude prompt", "utf8");
      await fs.writeFile(schemaFile, JSON.stringify({
        type: "object",
        properties: { answer: { type: "string" } },
      }), "utf8");
      await fs.mkdir(path.join(stateRoot, "agents", "mcp"), { recursive: true });
      await fs.writeFile(path.join(stateRoot, "agents", "mcp", "config.json"), JSON.stringify({
		mcp_servers: {
			filesystem: { type: "local", command: "npx", args: ["-y", "server"] },
			docs: {
				type: "remote",
				transport: "sse",
				url: "https://docs.example/sse",
				headers: { Authorization: { value: "Bearer token" } },
			},
		},
	  }), "utf8");

      const stdio = captureStdio();
      try {
        await createProgram({ exitOverride: true }).parseAsync([
          "node",
          "cli",
          "prompt",
          "--provider",
          "claude",
          "--message-file",
          messageFile,
          "--state-root",
          stateRoot,
          "--workspace",
          path.join(root, "workspace"),
          "--home",
          path.join(root, "home"),
          "--output-schema-file",
          schemaFile,
        ]);
      } finally {
        stdio.restore();
      }

      expect(stdio.stdout).toContain(`${RESULT_PREFIX}{`);
      expect(stdio.stdout).toContain("e2e-claude-session");
      expect(stdio.stdout).toContain("claude final");
      expect(stdio.stderr).toContain("[tool:Read]");
      expect(stdio.stderr).toContain("claude partial");
      expect(stdio.stderr).toContain("auth warning");
      expect(stdio.stderr).toContain("local command output");
	  expect(claudeMockState.lastArgs).toMatchObject({
		options: {
			strictMcpConfig: true,
			mcpServers: {
				filesystem: { type: "stdio", command: "npx", args: ["-y", "server"] },
				docs: { type: "sse", url: "https://docs.example/sse", headers: { Authorization: "Bearer token" } },
			},
		},
	  });
    });
  });

	it("runs the OpenCode prompt path and writes MCP config from state", async () => {
		await withTempSession(async (root) => {
			const messageFile = path.join(root, "message.txt");
			const stateRoot = path.join(root, "state");
			await fs.writeFile(messageFile, "opencode prompt", "utf8");
			await fs.mkdir(path.join(stateRoot, "agents", "mcp"), { recursive: true });
			await fs.writeFile(path.join(stateRoot, "agents", "mcp", "config.json"), JSON.stringify({
				mcp_servers: {
					filesystem: {
						type: "local",
						command: "npx",
						args: ["-y", "server"],
						env: { TOKEN: { value: "secret" } },
					},
				},
			}), "utf8");

			const stdio = captureStdio();
			try {
				await createProgram({ exitOverride: true }).parseAsync([
					"node",
					"cli",
					"prompt",
					"--provider",
					"opencode",
					"--message-file",
					messageFile,
					"--state-root",
					stateRoot,
					"--workspace",
					path.join(root, "workspace"),
					"--home",
					path.join(root, "home"),
				]);
			} finally {
				stdio.restore();
			}

			expect(stdio.stdout).toContain("e2e-opencode-session");
			expect(stdio.stdout).toContain("opencode final");
			const config = JSON.parse(await fs.readFile(path.join(root, "home", ".config", "opencode", "opencode.json"), "utf8")) as Record<string, unknown>;
			expect(config.mcp).toMatchObject({
				filesystem: { type: "local", command: ["npx", "-y", "server"], environment: { TOKEN: "secret" } },
			});
		});
	});
});
